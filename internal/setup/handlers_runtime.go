package setup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Project runtime endpoints — the coding-agent "live app" layer. These
// sit on top of the existing per-(user, agent) project (see
// handlers_projects.go): a project owns the source tree, its runtime
// owns the running instance of that tree (a long-lived dev-server
// sandbox + a preview URL). The upstream SaaS shell drives a generated
// project entirely through these:
//
//	GET    /api/agents/{id}/projects/{pid}/runtime        — current state (404 if none)
//	POST   /api/agents/{id}/projects/{pid}/runtime/up     — provision+boot (body: {templateRef})
//	POST   /api/agents/{id}/projects/{pid}/runtime/sleep  — stop container, keep files
//	POST   /api/agents/{id}/projects/{pid}/runtime/wake   — re-boot a sleeping runtime
//	POST   /api/agents/{id}/projects/{pid}/runtime/exec   — one sandboxed coding-tool command
//	DELETE /api/agents/{id}/projects/{pid}/runtime        — tear down + forget (files kept)
//	GET    /api/agents/{id}/projects/{pid}/preview        — {previewUrl, status}
//	GET    /api/agents/{id}/projects/{pid}/runtime/logs   — dev-server log tail
//
// Ownership mirrors projects: a runtime is keyed (user_id, agent_id,
// project_id), so a viewer on a shared agent only ever touches rows under
// their own user_id. Mutating actions require write access; reads need
// readable.

// runtimeReady resolves the caller + guards that a runtime manager is
// wired. Returns (userID, ok). On !ok it has already written the
// response. write=true additionally enforces write permission.
func (s *Server) runtimeReady(w http.ResponseWriter, r *http.Request, agentID string, write bool) (string, bool) {
	if s.runtimeMgr == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{
			"error": "project runtime not enabled on this deployment",
		})
		return "", false
	}
	if write && !s.requireWritable(w, r) {
		return "", false
	}
	if !s.requireAgentReadable(w, r, agentID) {
		return "", false
	}
	uid := s.effectiveUserID(r)
	if uid == "" {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return "", false
	}
	return uid, true
}

// runtimeToJSON shapes a runtime record for the wire. The store record's
// own json tags already hide ownership fields; we add nothing secret.
func runtimeToJSON(rec *store.ProjectRuntimeRecord) map[string]any {
	return map[string]any{
		"projectId":   rec.ProjectID,
		"templateRef": rec.TemplateRef,
		"status":      rec.Status,
		"devPort":     rec.DevPort,
		"hostPort":    rec.HostPort,
		"previewUrl":  rec.PreviewURL,
		"gitRef":      rec.GitRef,
		"lastError":   rec.LastError,
		"createdAt":   rec.CreatedAt,
		"updatedAt":   rec.UpdatedAt,
	}
}

func (s *Server) handleGetRuntime(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pid := r.PathValue("pid")
	uid, ok := s.runtimeReady(w, r, id, false)
	if !ok {
		return
	}
	rec, err := s.runtimeMgr.Get(r.Context(), uid, id, pid, "")
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			jsonResponse(w, http.StatusNotFound, map[string]any{"error": "no runtime for this project"})
			return
		}
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, runtimeToJSON(rec))
}

func (s *Server) handleRuntimeUp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pid := r.PathValue("pid")
	uid, ok := s.runtimeReady(w, r, id, true)
	if !ok {
		return
	}
	// Confirm the project exists before spinning up a container for it —
	// avoids minting a runtime for a typo'd pid.
	if proj, err := s.dataStore.GetProject(r.Context(), uid, id, pid); err != nil || proj == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "project not found"})
		return
	}
	var req struct {
		TemplateRef string `json:"templateRef"`
	}
	// Body is optional on wake-like re-up (stored template ref reused);
	// tolerate an empty/absent body.
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	// Booting can involve scaffold + pnpm install — give it room beyond
	// the default request deadline.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	rec, err := s.runtimeMgr.Up(ctx, uid, id, pid, "", req.TemplateRef)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, runtimeToJSON(rec))
}

func (s *Server) handleRuntimeSleep(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pid := r.PathValue("pid")
	uid, ok := s.runtimeReady(w, r, id, true)
	if !ok {
		return
	}
	if err := s.runtimeMgr.Sleep(r.Context(), uid, id, pid, ""); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "status": "sleeping"})
}

func (s *Server) handleRuntimeWake(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pid := r.PathValue("pid")
	uid, ok := s.runtimeReady(w, r, id, true)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	rec, err := s.runtimeMgr.Wake(ctx, uid, id, pid, "")
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, runtimeToJSON(rec))
}

// handleRuntimeExec is the narrow tool bridge used by an upstream agent loop.
// The command always runs inside this project's live sandbox; it never falls
// back to the FastClaw host. Authentication, effective-user partitioning and
// project ownership are the same as the other mutating runtime endpoints.
func (s *Server) handleRuntimeExec(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pid := r.PathValue("pid")
	uid, ok := s.runtimeReady(w, r, id, true)
	if !ok {
		return
	}
	if proj, err := s.dataStore.GetProject(r.Context(), uid, id, pid); err != nil || proj == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "project not found"})
		return
	}

	const maxCommandBytes = 1 << 20
	const maxOutputBytes = 1 << 20
	var req struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeoutSeconds"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCommandBytes))
	if err := decoder.Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "invalid exec request"})
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "command is required"})
		return
	}
	timeoutSeconds := req.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 30
	}
	if timeoutSeconds > 120 {
		timeoutSeconds = 120
	}
	timeout := time.Duration(timeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), timeout+5*time.Second)
	defer cancel()
	out, err := s.runtimeMgr.Exec(ctx, uid, id, pid, "", req.Command, timeout)
	truncated := len(out) > maxOutputBytes
	if truncated {
		out = out[:maxOutputBytes]
	}
	if err != nil {
		jsonResponse(w, http.StatusConflict, map[string]any{
			"error":     err.Error(),
			"output":    out,
			"truncated": truncated,
		})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"output":    out,
		"truncated": truncated,
	})
}

func (s *Server) handleRuntimeStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pid := r.PathValue("pid")
	uid, ok := s.runtimeReady(w, r, id, true)
	if !ok {
		return
	}
	if err := s.runtimeMgr.Stop(r.Context(), uid, id, pid, ""); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRuntimePreview(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pid := r.PathValue("pid")
	uid, ok := s.runtimeReady(w, r, id, false)
	if !ok {
		return
	}
	rec, err := s.runtimeMgr.Get(r.Context(), uid, id, pid, "")
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			jsonResponse(w, http.StatusNotFound, map[string]any{"error": "no runtime for this project"})
			return
		}
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"previewUrl": rec.PreviewURL,
		"status":     rec.Status,
	})
}

func (s *Server) handleRuntimeLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pid := r.PathValue("pid")
	uid, ok := s.runtimeReady(w, r, id, false)
	if !ok {
		return
	}
	tailLines := 200
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			tailLines = n
		}
	}
	out, err := s.runtimeMgr.Logs(r.Context(), uid, id, pid, "", tailLines)
	if err != nil {
		jsonResponse(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"logs": out})
}

// handleScopePreview returns the live preview for the CURRENT chat scope,
// addressed by query params (sessionId for a loose chat, projectId for a
// project) rather than a path-bound pid. The web UI uses it to surface an
// "open preview" entry next to the workspace files. Always 200 with a
// status so the client can render conditionally — "none" when the runtime
// isn't enabled or no app has been started for this scope yet.
func (s *Server) handleScopePreview(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.runtimeMgr == nil {
		jsonResponse(w, http.StatusOK, map[string]any{"status": "none"})
		return
	}
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	uid := s.effectiveUserID(r)
	if uid == "" {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	rec, err := s.runtimeMgr.Get(r.Context(), uid, id,
		r.URL.Query().Get("projectId"), r.URL.Query().Get("sessionId"))
	if err != nil {
		// ErrNotFound (no app yet) or a bad/empty scope both mean "nothing
		// to preview" from the UI's perspective.
		jsonResponse(w, http.StatusOK, map[string]any{"status": "none"})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"previewUrl": rec.PreviewURL,
		"status":     rec.Status,
	})
}

// handleScopePreviewLogs tails the build/dev log for the CURRENT chat scope
// (sessionId or projectId query, like handleScopePreview). The preview panel
// polls it while the app is scaffolding so the user sees the live
// pnpm-install output instead of an opaque "Building…" spinner. Always 200
// so the client can render conditionally; "logs" is "" when there's nothing
// yet (no runtime, or scaffold hasn't written a line).
func (s *Server) handleScopePreviewLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.runtimeMgr == nil {
		jsonResponse(w, http.StatusOK, map[string]any{"logs": ""})
		return
	}
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	uid := s.effectiveUserID(r)
	if uid == "" {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	tailLines := 400
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			tailLines = n
		}
	}
	out, err := s.runtimeMgr.Logs(r.Context(), uid, id,
		r.URL.Query().Get("projectId"), r.URL.Query().Get("sessionId"), tailLines)
	if err != nil {
		// Not live yet / no scope → nothing to show, not an error to the UI.
		jsonResponse(w, http.StatusOK, map[string]any{"logs": ""})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"logs": out})
}

// handleChangedFiles returns only the files the agent created/modified vs
// the template baseline (git diff inside the running app), so the file
// tree can show just THIS task's output instead of the whole template.
// `available` is false when there's no live runtime / git baseline — the
// UI then falls back to listing all workspace files.
func (s *Server) handleChangedFiles(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.runtimeMgr == nil {
		jsonResponse(w, http.StatusOK, map[string]any{"available": false, "files": []any{}})
		return
	}
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	uid := s.effectiveUserID(r)
	if uid == "" {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	changed, err := s.runtimeMgr.ChangedFiles(r.Context(), uid, id,
		r.URL.Query().Get("projectId"), r.URL.Query().Get("sessionId"))
	if err != nil {
		jsonResponse(w, http.StatusOK, map[string]any{"available": false, "files": []any{}})
		return
	}
	files := make([]map[string]any, 0, len(changed))
	for _, f := range changed {
		files = append(files, map[string]any{"path": f.Path, "size": 0, "modTime": 0})
	}
	jsonResponse(w, http.StatusOK, map[string]any{"available": true, "files": files})
}
