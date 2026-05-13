package setup

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/buildinfo"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// agentScopeModel reads the per-agent model override from the configs
// table — the kind=setting, scope=agent row that supersedes the
// system/user defaults when set.
func (s *Server) agentScopeModel(r *http.Request, agentID string) string {
	rec, err := s.dataStore.GetConfigByName(r.Context(), store.KindSetting, "", agentID, "agents.defaults")
	if err != nil || rec == nil {
		return ""
	}
	if v, ok := rec.Data["model"].(string); ok {
		return v
	}
	return ""
}

// saveAgentScopeModel upserts (model="") or deletes (model=="") the
// agent-scope agents.defaults row.
func (s *Server) saveAgentScopeModel(r *http.Request, agentID, model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return scope.SaveSettingByScope(r.Context(), s.dataStore, scope.Agent, agentID, "agents.defaults", nil)
	}
	return scope.SaveSettingByScope(r.Context(), s.dataStore, scope.Agent, agentID, "agents.defaults", map[string]interface{}{"model": model})
}

// effectiveUserID returns the resolved user_id for the request: the
// caller's own id, or — for super_admin in actAs mode — the impersonated
// user's id.
func (s *Server) effectiveUserID(r *http.Request) string {
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		return ""
	}
	return ident.EffectiveUserID()
}

// requireWritable returns true if the caller may mutate, writing a 4xx
// response and false otherwise.
func (s *Server) requireWritable(w http.ResponseWriter, r *http.Request) bool {
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unauthorized"})
		return false
	}
	if ident.ReadOnly() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "read-only"})
		return false
	}
	return true
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	uid := s.effectiveUserID(r)
	if uid == "" {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	// ?all=true is the cross-tenant view (replaces /api/admin/agents).
	// Admin-only — for the platform-wide "Agents" admin page that
	// joins owner usernames in.
	if r.URL.Query().Get("all") == "true" {
		ident, _ := auth.FromContext(r.Context())
		if !ident.CanAdminPlatform() {
			jsonResponse(w, http.StatusForbidden, map[string]any{"error": "all=true requires admin"})
			return
		}
		s.respondAllAgents(w, r)
		return
	}
	owned, err := s.dataStore.ListAgents(r.Context(), uid)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(owned))
	for _, ar := range owned {
		desc, _ := ar.Config["description"].(string)
		out = append(out, map[string]any{
			"id":          ar.ID,
			"name":        ar.Name,
			"description": desc,
			"model":       s.agentScopeModel(r, ar.ID),
			"avatarUrl":   "/api/agents/" + ar.ID + "/files/avatar.png",
			"createdAt":   ar.CreatedAt,
			"userId":      ar.UserID,
			"role":        "owner",
			"isPublic":    ar.IsPublic,
		})
	}
	jsonResponse(w, http.StatusOK, map[string]any{"agents": out})
}

type createAgentRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model,omitempty"`
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	ident, _ := auth.FromContext(r.Context())
	if !ident.CanCreateAgent() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"error": "type=agent api keys cannot create agents"})
		return
	}
	uid := s.effectiveUserID(r)
	var req createAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if req.Name == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "name required"})
		return
	}
	// Enforce per-user agent quota. -1 = unlimited (default), 0 = no
	// self-creation (single-tenant customers — admin provisions for
	// them via POST /api/users/{id}/agents under admin caller),
	// N>0 = max N owned at once. Admin path bypasses this check.
	if u, err := s.dataStore.GetUser(r.Context(), uid); err == nil && u != nil && u.AgentQuota >= 0 {
		owned, err := s.dataStore.ListAgents(r.Context(), uid)
		if err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if int64(len(owned)) >= u.AgentQuota {
			jsonResponse(w, http.StatusForbidden, map[string]any{
				"error": fmt.Sprintf("agent quota reached (%d) — contact your admin to provision more", u.AgentQuota),
			})
			return
		}
	}
	id, err := generateID("agt_")
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	rec := &store.AgentRecord{
		ID:     id,
		UserID: uid,
		Name:   req.Name,
	}
	if req.Description != "" {
		// Description lives in the agents.config JSON blob — keeps the
		// schema stable while still surfacing through GetAgentConfig and
		// the agents.config namespace settings overlay.
		rec.Config = map[string]interface{}{"description": req.Description}
	}
	if err := s.dataStore.SaveAgent(r.Context(), rec); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if req.Model != "" {
		if err := s.saveAgentScopeModel(r, id, req.Model); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	}
	s.invalidateUser(uid)
	jsonResponse(w, http.StatusCreated, map[string]any{
		"agent": map[string]any{
			"id":     rec.ID,
			"userId": rec.UserID,
			"name":   rec.Name,
			"model":  req.Model,
			"config": rec.Config,
		},
	})
}

// requireUserOrAdmin gates the /api/users/{id}/* nested routes:
//   - any caller may operate on themselves (pathUserID == ident.UserID)
//   - super_admin / type=admin apikey may operate on any user
//
// Returns true on success; on failure writes a 401/403 and returns false.
// Callers should still validate that the path user actually exists when
// the operation depends on it.
func (s *Server) requireUserOrAdmin(w http.ResponseWriter, r *http.Request, pathUserID string) bool {
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return false
	}
	if pathUserID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "user id required"})
		return false
	}
	if pathUserID == ident.EffectiveUserID() {
		return true
	}
	if ident.CanAdminPlatform() {
		return true
	}
	jsonResponse(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
	return false
}

// requireAgentOwner returns the agent record if the caller owns it (or is
// super_admin), otherwise writes a 403/404 and returns nil.
func (s *Server) requireAgentOwner(w http.ResponseWriter, r *http.Request, agentID string) *store.AgentRecord {
	uid := s.effectiveUserID(r)
	rec, err := s.dataStore.GetAgent(r.Context(), agentID)
	if err != nil || rec == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return nil
	}
	ident, _ := auth.FromContext(r.Context())
	if rec.UserID != uid && ident.Role != users.RoleSuperAdmin {
		jsonResponse(w, http.StatusForbidden, map[string]any{"error": "not your agent"})
		return nil
	}
	return rec
}

// requireAgentReadable allows access when the caller is the owner, a
// super_admin, holds an apikey-ACL grant (CanAccessAgent), OR the
// agent is marked public and the caller is at least an authenticated
// session. Public agents are link-shared: any signed-in user who hits
// the URL can chat under their own user_id namespace, while the
// agent's identity (SOUL/IDENTITY/skills) is reused from the owner's
// row. This is the same gate /api/chat/history uses, so app_user
// requests proxied through an integration with X-Fastclaw-End-User
// can read artifacts for sessions they own without 403'ing on the
// strict ownership check.
func (s *Server) requireAgentReadable(w http.ResponseWriter, r *http.Request, agentID string) bool {
	rec, err := s.dataStore.GetAgent(r.Context(), agentID)
	if err != nil || rec == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return false
	}
	uid := s.effectiveUserID(r)
	ident, _ := auth.FromContext(r.Context())
	if rec.UserID == uid || ident.Role == users.RoleSuperAdmin {
		return true
	}
	// CanAccessAgent is a hard check for apikeys (ACL) but a deferred
	// "true" for session callers — the comment on Identity.CanAccessAgent
	// spells this out. Only honor it for the apikey path; for session
	// users we must do the explicit owner / public check ourselves,
	// otherwise any signed-in user could GET another user's private
	// agent via /api/agents/{id} and friends.
	if ident.AuthMethod == "apikey" && ident.CanAccessAgent(agentID) {
		return true
	}
	if rec.IsPublic && uid != "" {
		return true
	}
	jsonResponse(w, http.StatusForbidden, map[string]any{"error": "not your agent"})
	return false
}

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	rec := s.requireAgentOwner(w, r, id)
	if rec == nil {
		return
	}
	var req struct {
		Name        string  `json:"name,omitempty"`
		Description *string `json:"description,omitempty"` // ptr so empty-string clears it
		Model       *string `json:"model,omitempty"`       // ptr so empty-string clears the agent-scope override
		IsPublic    *bool   `json:"isPublic,omitempty"`    // ptr so caller can leave it unchanged
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if req.Name != "" {
		rec.Name = req.Name
	}
	if req.Description != nil {
		if rec.Config == nil {
			rec.Config = map[string]interface{}{}
		}
		if *req.Description == "" {
			delete(rec.Config, "description")
		} else {
			rec.Config["description"] = *req.Description
		}
	}
	if req.IsPublic != nil {
		rec.IsPublic = *req.IsPublic
	}
	if err := s.dataStore.SaveAgent(r.Context(), rec); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// Per-agent model override is its own configs row. nil = caller didn't
	// touch it (e.g. a name-only patch); empty string = explicit "clear my
	// override and fall back to user/system defaults" — the Models page's
	// "Clear override" button relies on this. Non-empty saves the row.
	if req.Model != nil {
		if err := s.saveAgentScopeModel(r, rec.ID, *req.Model); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	}
	// invalidateAgent (not invalidateUser) so super_admin / public-link
	// viewers / apikey callers that lazy-attached this agent into their
	// own UserSpace also drop their stale rc.Model — without this they
	// keep firing the previous model until the 30-min idle eviction.
	s.invalidateAgent(rec.ID)
	jsonResponse(w, http.StatusOK, map[string]any{
		"agent": map[string]any{
			"id":       rec.ID,
			"userId":   rec.UserID,
			"name":     rec.Name,
			"model":    s.agentScopeModel(r, rec.ID),
			"config":   rec.Config,
			"isPublic": rec.IsPublic,
		},
	})
}

// handleGetAgent returns the basic AgentRecord (id, name, description,
// userId) for one agent. Used by the chat header / sidebar switcher to
// resolve a display name. Permission is read-level — owner, super_admin,
// or any grantee of a sharing record.
func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	rec, err := s.dataStore.GetAgent(r.Context(), id)
	if err != nil || rec == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	desc, _ := rec.Config["description"].(string)
	uid := s.effectiveUserID(r)
	role := "owner"
	if rec.UserID != uid {
		role = "viewer"
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"agent": map[string]any{
			"id":          rec.ID,
			"name":        rec.Name,
			"description": desc,
			"userId":      rec.UserID,
			"role":        role,
			"model":       s.agentScopeModel(r, rec.ID),
			"avatarUrl":   "/api/agents/" + rec.ID + "/files/avatar.png",
			"createdAt":   rec.CreatedAt,
			"isPublic":    rec.IsPublic,
		},
	})
}

func (s *Server) handleGetAgentConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec := s.requireAgentOwner(w, r, id)
	if rec == nil {
		return
	}
	cfg := config.AgentFileConfig{}
	if len(rec.Config) > 0 {
		blob, _ := json.Marshal(rec.Config)
		_ = json.Unmarshal(blob, &cfg)
	}
	jsonResponse(w, http.StatusOK, cfg)
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	rec := s.requireAgentOwner(w, r, id)
	if rec == nil {
		return
	}
	if err := s.dataStore.DeleteAgent(r.Context(), id); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// Drop the agent from every cached UserSpace, not just the owner's,
	// so foreign callers stop resolving the now-deleted agent through
	// EnsureAgent's lazy-attach path.
	s.invalidateAgent(rec.ID)
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// Agent identity / memory files — all live in agent_files, agent-scoped.
// Two classes:
//
//   - identity files (agentIdentityFiles below) are the canonical "shared
//     template" for the agent. They live under a single row keyed by the
//     agent owner's user_id — so admin provisioning, the owner's edits,
//     and the agent's own BOOTSTRAP-flow write_file calls all converge on
//     the same row. Mirrors handlers_admin.forkAgentFiles and
//     internal/agent/tools.identityFiles; keep these three lists in sync.
//
//   - per-user files (USER.md, MEMORY.md) are state that genuinely
//     differs per chatter. They're keyed by the caller's effective
//     user_id; a non-owner caller can author their own override and the
//     read path falls back to the owner's row when none exists.
//
// Filename allowlist gates which files this endpoint can touch at all;
// agent-runtime tool calls go through the workspace store instead.
var agentSystemFileAllowlist = map[string]bool{
	"SOUL.md": true, "IDENTITY.md": true, "AGENTS.md": true,
	"BOOTSTRAP.md": true, "TOOLS.md": true, "MEMORY.md": true,
	"HEARTBEAT.md": true, "USER.md": true, "agent.json": true,
}

var agentIdentityFiles = map[string]bool{
	"SOUL.md": true, "IDENTITY.md": true, "AGENTS.md": true,
	"BOOTSTRAP.md": true, "TOOLS.md": true, "HEARTBEAT.md": true,
	"agent.json": true,
}

func (s *Server) handleGetAgentSystemFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := r.PathValue("name")
	if !agentSystemFileAllowlist[name] {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "filename not allowed"})
		return
	}
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	rec, err := s.dataStore.GetAgent(r.Context(), id)
	if err != nil || rec == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	caller := s.effectiveUserID(r)

	// Identity files: read the owner's row directly — that's the single
	// source of truth, regardless of who's asking.
	if agentIdentityFiles[name] {
		data, err := s.dataStore.GetAgentFileExact(r.Context(), id, rec.UserID, name)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				jsonResponse(w, http.StatusOK, map[string]any{"content": "", "source": "default"})
				return
			}
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		jsonResponse(w, http.StatusOK, map[string]any{"content": string(data), "source": "owner"})
		return
	}

	// Per-user files: prefer caller's own row, fall back to the owner's.
	// `source: "db"` means the caller has authored an override; "owner"
	// means we're showing the agent owner's row by fallback. The
	// frontend uses this to decide whether to show the "Edited" badge
	// and enable the Revert action.
	if data, err := s.dataStore.GetAgentFileExact(r.Context(), id, caller, name); err == nil {
		baseContent := ""
		if rec.UserID != caller {
			if base, err2 := s.dataStore.GetAgentFileExact(r.Context(), id, rec.UserID, name); err2 == nil {
				baseContent = string(base)
			}
		}
		resp := map[string]any{"content": string(data), "source": "db"}
		if baseContent != "" {
			resp["baseContent"] = baseContent
		}
		jsonResponse(w, http.StatusOK, resp)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if rec.UserID != caller {
		if data, err := s.dataStore.GetAgentFileExact(r.Context(), id, rec.UserID, name); err == nil {
			jsonResponse(w, http.StatusOK, map[string]any{"content": string(data), "source": "owner"})
			return
		} else if !errors.Is(err, store.ErrNotFound) {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"content": "", "source": "default"})
}

func (s *Server) handlePutAgentSystemFile(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	name := r.PathValue("name")
	if !agentSystemFileAllowlist[name] {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "filename not allowed"})
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	target, ok := s.resolveSystemFileTarget(w, r, id, name)
	if !ok {
		return
	}
	if err := s.dataStore.SaveAgentFile(r.Context(), id, target, name, []byte(body.Content)); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.invalidateUser(target)
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteAgentSystemFile(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	name := r.PathValue("name")
	if !agentSystemFileAllowlist[name] {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "filename not allowed"})
		return
	}
	target, ok := s.resolveSystemFileTarget(w, r, id, name)
	if !ok {
		return
	}
	if err := s.dataStore.DeleteAgentFile(r.Context(), id, target, name); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.invalidateUser(target)
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// resolveSystemFileTarget figures out which user_id row a write/delete
// on (agentID, filename) should hit, and gates access:
//
//   - Identity files (SOUL/IDENTITY/AGENTS/BOOTSTRAP/TOOLS/HEARTBEAT/
//     agent.json) always target the agent owner's row — this is the
//     canonical "shared template". Caller must be the owner or hold
//     platform admin (super_admin session, or type=admin apikey).
//   - Per-user files (USER.md, MEMORY.md) target the caller's own row
//     so each chatter has an independent override. Caller just needs
//     read access to the agent.
//
// Writes 4xx and returns ok=false on permission/lookup failures.
func (s *Server) resolveSystemFileTarget(w http.ResponseWriter, r *http.Request, agentID, name string) (string, bool) {
	rec, err := s.dataStore.GetAgent(r.Context(), agentID)
	if err != nil || rec == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return "", false
	}
	caller := s.effectiveUserID(r)
	ident, _ := auth.FromContext(r.Context())
	if agentIdentityFiles[name] {
		if rec.UserID != caller && !ident.CanAdminPlatform() {
			jsonResponse(w, http.StatusForbidden, map[string]any{"error": "not your agent"})
			return "", false
		}
		return rec.UserID, true
	}
	if !s.requireAgentReadable(w, r, agentID) {
		return "", false
	}
	return caller, true
}

// Workspace files — list / get / upload of agent-produced artifacts.
// Backed by the workspace.Store blob backend, whose layout is
//
//   workspaces/<agent_id>/<session_id>/<path>
//
// The HTTP file endpoints below operate at the agent-root level
// (sessionID="") — that's where uploads land and where ListByAgent
// returns objects across every session of that agent. The agent runtime
// passes its own sessionID for in-chat tool calls; those land under the
// session sub-prefix automatically.

// workspaceSessionScope translates the URL `?sessionId=` token into
// the directory name used under workspaces/<agent>/sessions/. The URL
// token is the session_key (so the dashboard can address any session
// uniformly), but workspace artifacts are namespaced by chat_id
// instead — that's what the agent runtime passed at write time. Pre-
// migration legacy keys looked like "web_<chat_id>" and the runtime
// wrote files under "sessions/<chat_id>/", not "sessions/web_<chat_id>/".
//
// Returns the chat_id when the session_key resolves; falls back to the
// raw token when the lookup fails (brand-new sessions whose row hasn't
// been created yet, or sessions saved before this migration).
func (s *Server) workspaceSessionScope(ctx context.Context, agentID, urlToken string) string {
	tok := strings.TrimSpace(urlToken)
	if tok == "" || s.dataStore == nil {
		return tok
	}
	uid := config.UserIDFromContext(ctx)
	if uid == "" {
		return tok
	}
	_, _, chatID, err := s.dataStore.LookupSessionTriple(ctx, uid, agentID, tok)
	if err != nil || chatID == "" {
		return tok
	}
	return chatID
}

func (s *Server) handleAgentFileList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.workspaceStore == nil {
		jsonResponse(w, http.StatusOK, map[string]any{"files": []any{}})
		return
	}
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	// Always List with project + session both empty so returned paths
	// stay agent-relative (e.g. "sessions/<sid>/foo.png" or
	// "projects/<pid>/notes.md") — the download endpoint expects that
	// shape, and filtering here is cheaper than two divergent code paths.
	objects, err := s.workspaceStore.List(r.Context(), id, "", "")
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	scope := s.fileScopeForRequest(r, id)
	files := make([]map[string]any, 0, len(objects))
	for _, o := range objects {
		if !scope.acceptPath(o.Path) {
			continue
		}
		files = append(files, map[string]any{
			"path":    o.Path,
			"size":    o.Size,
			"modTime": o.ModTime.Unix(),
		})
	}
	jsonResponse(w, http.StatusOK, map[string]any{"files": files})
}

// fileScope describes which agent-relative paths to surface for the
// file browser / zip filter. acceptPath returns true for paths the
// scope considers in-bounds:
//
//   loose chat:  paths under sessions/<chat_id>/
//   project chat: paths under projects/<pid>/<chat_id>/ (the chat's
//                 own files), PLUS files directly at projects/<pid>/
//                 (project-root "shared/legacy" files — pre-subdir
//                 layout still lives there, and operators may
//                 deliberately drop shared files at the root). Other
//                 chats' subdirs (projects/<pid>/<other-sid>/...)
//                 are excluded — those belong to that chat's panel.
//   no session:  everything (admin browser).
//
// archiveSuffix returns the human-readable scope id used in the zip
// filename — chat_id for loose chats, "<pid>-<chat_id>" for project
// chats so a download names "agent-pid-sid.zip" instead of
// disambiguating by chat_id alone.
type fileScope struct {
	acceptPath    func(string) bool
	archiveSuffix string
}

// stripScopePrefix removes the deepest known scope prefix from an
// agent-relative path so zip entries read as plain filenames. Order
// matters: project chats are tried before session chats so a
// `projects/<pid>/<sid>/foo.md` collapses to `foo.md` rather than
// `<pid>/<sid>/foo.md`. Top-level project files keep the leading
// `projects/<pid>/` strip so they read as bare filenames too.
func stripScopePrefix(p string) string {
	for _, top := range []string{"projects/", "sessions/"} {
		if !strings.HasPrefix(p, top) {
			continue
		}
		rest := p[len(top):]
		// Cut after the scope id (one path segment).
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			rest = rest[i+1:]
			// Project paths can have a second id segment for the
			// per-chat subdir; collapse that too when present.
			if top == "projects/" {
				if j := strings.IndexByte(rest, '/'); j >= 0 {
					// Only treat the first segment as a chat id when it
					// looks like one (s-... prefix). Otherwise keep
					// rest as-is so legacy "subdir/file.md" structures
					// don't get over-stripped.
					if first := rest[:j]; strings.HasPrefix(first, "s-") {
						rest = rest[j+1:]
					}
				}
			}
			return rest
		}
		return ""
	}
	return p
}

func (s *Server) fileScopeForRequest(r *http.Request, agentID string) fileScope {
	rawSession := r.URL.Query().Get("sessionId")
	rawProject := r.URL.Query().Get("projectId")
	// Project landing page: no specific chat is open, so the panel
	// shows everything under projects/<pid>/ — every chat's subtree
	// plus root-level shared files. The sessionId branch below is
	// the per-chat view; use this branch when the URL is
	// /agents/<aid>/project/<pid> with no chat selected.
	if rawSession == "" && rawProject != "" {
		prefix := "projects/" + rawProject + "/"
		return fileScope{
			acceptPath:    func(p string) bool { return strings.HasPrefix(p, prefix) },
			archiveSuffix: rawProject,
		}
	}
	if rawSession == "" {
		return fileScope{acceptPath: func(string) bool { return true }}
	}
	chatID := s.workspaceSessionScope(r.Context(), agentID, rawSession)
	if chatID == "" {
		return fileScope{acceptPath: func(string) bool { return true }}
	}
	if pid := s.resolveSessionProject(r.Context(), r, agentID, rawSession); pid != "" {
		ownPrefix := "projects/" + pid + "/" + chatID + "/"
		rootPrefix := "projects/" + pid + "/"
		return fileScope{
			acceptPath: func(p string) bool {
				if strings.HasPrefix(p, ownPrefix) {
					return true
				}
				// Top-level file at projects/<pid>/<file> (no further
				// "/" — i.e. not in any sid subdir).
				if strings.HasPrefix(p, rootPrefix) {
					rest := p[len(rootPrefix):]
					return rest != "" && !strings.Contains(rest, "/")
				}
				return false
			},
			archiveSuffix: pid + "-" + chatID,
		}
	}
	prefix := "sessions/" + chatID + "/"
	return fileScope{
		acceptPath:    func(p string) bool { return strings.HasPrefix(p, prefix) },
		archiveSuffix: chatID,
	}
}

// handleAgentFilesZip streams a zip of every workspace file for the agent
// (or just one session when ?sessionId= is set). Files are added with
// their session-relative path so the archive layout matches what the user
// sees in the chat panel — no enclosing wrapper directory.
func (s *Server) handleAgentFilesZip(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.workspaceStore == nil {
		http.Error(w, "no workspace store", http.StatusServiceUnavailable)
		return
	}
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	objects, err := s.workspaceStore.List(r.Context(), id, "", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	scope := s.fileScopeForRequest(r, id)
	archiveName := fmt.Sprintf("%s.zip", id)
	if scope.archiveSuffix != "" {
		archiveName = fmt.Sprintf("%s-%s.zip", id, scope.archiveSuffix)
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, archiveName))

	zw := zip.NewWriter(w)
	defer zw.Close()

	for _, o := range objects {
		if !scope.acceptPath(o.Path) {
			continue
		}
		// Strip the deepest scope prefix from the archive entry name
		// so the user sees clean filenames in the zip rather than
		// nested `projects/<pid>/<sid>/foo.md` paths.
		entryName := stripScopePrefix(o.Path)
		if entryName == "" {
			continue
		}
		hdr := &zip.FileHeader{
			Name:     entryName,
			Method:   zip.Deflate,
			Modified: o.ModTime,
		}
		entry, err := zw.CreateHeader(hdr)
		if err != nil {
			slog.Warn("zip: create entry failed", "agent", id, "path", o.Path, "err", err)
			return
		}
		rc, err := s.workspaceStore.Get(r.Context(), id, "", "", o.Path)
		if err != nil {
			slog.Warn("zip: open object failed", "agent", id, "path", o.Path, "err", err)
			continue
		}
		_, copyErr := io.Copy(entry, rc)
		rc.Close()
		if copyErr != nil {
			slog.Warn("zip: copy failed", "agent", id, "path", o.Path, "err", copyErr)
			return
		}
	}
}

// handleAgentWorkspaceReveal opens the chatter's workspace folder in
// the operator's native file browser (Finder/Explorer/xdg-open).
// Self-hosted only — hosted deployments don't have a meaningful
// concept of "the operator's local filesystem" and the chatter
// doesn't own the daemon, so exposing this would be a privilege
// leak. Reads sessionId / projectId from the query string, mirrors
// fileScopeForRequest's resolution (session_key → chat_id, project
// lookup) so the revealed dir matches what the chat-side Workspace
// panel is showing.
//
// Best-effort: returns 200 with the resolved path on success, 4xx
// on bad scope, 503 when the configured workspace store doesn't
// expose a host path (S3 / R2 deploys), 500 if the OS open command
// fails. Non-blocking — we don't wait for Finder to actually
// surface the window.
func (s *Server) handleAgentWorkspaceReveal(w http.ResponseWriter, r *http.Request) {
	if buildinfo.IsHostedDeploy() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"error": "workspace reveal is disabled on hosted deployments"})
		return
	}
	id := r.PathValue("id")
	if s.workspaceStore == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "no workspace store configured"})
		return
	}
	if !s.requireAgentReadable(w, r, id) {
		return
	}

	scoper, ok := s.workspaceStore.(workspace.LocalScoper)
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "workspace store has no local path (e.g. S3-backed) — open in Finder is unavailable"})
		return
	}

	rawSession := r.URL.Query().Get("sessionId")
	rawProject := r.URL.Query().Get("projectId")

	// Resolve to the same (project, chatID) the chat-side panel is
	// scoped to. Empty rawSession + non-empty projectId means project
	// landing — reveal the project root. Empty both means agent root
	// (admin browser); we still allow it because requireAgentReadable
	// has already gated access.
	chatID := ""
	projectID := rawProject
	if rawSession != "" {
		chatID = s.workspaceSessionScope(r.Context(), id, rawSession)
		if pid := s.resolveSessionProject(r.Context(), r, id, rawSession); pid != "" {
			projectID = pid
		}
	}

	dir, ok := scoper.LocalScopeDir(id, projectID, chatID)
	if !ok || dir == "" {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "workspace store did not return a host path"})
		return
	}

	// Pre-create the dir so `open <missing-path>` doesn't error out
	// on a brand-new chat that hasn't written any files yet — empty
	// folder still feels like progress to the user.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	if err := openInFileBrowser(dir); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "path": dir})
}

// openInFileBrowser shells out to the platform-appropriate "open"
// command. macOS and Linux behave consistently (open the directory
// in the default file manager); Windows uses explorer.exe. We
// deliberately don't wait on the child — Finder in particular
// returns immediately, and there's no useful exit code to surface
// either way.
func openInFileBrowser(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		// `explorer` returns exit code 1 even on success, so we
		// don't check err. The only real failure mode is "binary
		// not on PATH", which Start() reports.
		cmd = exec.Command("explorer", path)
		return cmd.Start()
	default:
		// Linux / *BSD — xdg-open is the freedesktop standard.
		cmd = exec.Command("xdg-open", path)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	// Detach: we don't care about the file manager's lifetime.
	go func() { _ = cmd.Wait() }()
	return nil
}

func (s *Server) handleAgentFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rel := r.PathValue("path")
	if rel == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "path required"})
		return
	}
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	if s.workspaceStore != nil {
		s.serveFileFromWorkspaceStore(w, r, id, rel)
		return
	}
	// Workspace store not configured — fall back to direct FS read.
	// The local FS layout mirrors the workspace store's:
	// ~/.fastclaw/workspaces/<agent_id>/<path>.
	home, err := config.HomeDir()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	root := filepath.Join(home, "workspaces", id)
	abs := filepath.Join(root, filepath.Clean("/"+rel))
	if !strings.HasPrefix(abs, root+string(os.PathSeparator)) && abs != root {
		jsonResponse(w, http.StatusForbidden, map[string]any{"error": "path escape"})
		return
	}
	// ServeFile sets Content-Type from the mime database itself; we just
	// add the CSP sandbox for HTML on top — same rationale as in
	// setFileResponseHeaders above.
	if ext := strings.ToLower(filepath.Ext(rel)); ext == ".html" || ext == ".htm" {
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", attachmentDisposition(rel))
	}
	http.ServeFile(w, r, abs)
}

func (s *Server) serveFileFromWorkspaceStore(w http.ResponseWriter, r *http.Request, agentID, path string) {
	rc, err := s.workspaceStore.Get(r.Context(), agentID, "", "", path)
	if err != nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	defer rc.Close()
	setFileResponseHeaders(w, r, path)
	io.Copy(w, rc)
}

// setFileResponseHeaders picks the right Content-Type for a user-produced
// workspace file and locks down agent-generated HTML so it can't reach the
// app's cookies/storage even if the user opens the URL in a bare tab. The
// Content-Type derived from the extension is what lets iframes render the
// file (octet-stream → about:blank, since iframes don't sniff). The CSP
// `sandbox` header is the same protection the chat preview gets via the
// iframe `sandbox` attribute, but applied at the HTTP layer so it kicks in
// no matter how the file is loaded.
//
// When the request carries `?download=1`, a `Content-Disposition: attachment`
// header is emitted so the browser triggers a real download instead of
// trying to render in-tab. Without it, a click on `<a href>` without a
// `download` attribute can commit a top-level navigation to the API URL
// — and once the address bar leaves the chat route, the SPA fallback
// can resolve back to `/`, which the root page then redirects to
// `/overview/`.
func setFileResponseHeaders(w http.ResponseWriter, r *http.Request, path string) {
	ext := strings.ToLower(filepath.Ext(path))
	ctype := mime.TypeByExtension(ext)
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if ext == ".html" || ext == ".htm" {
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts")
	}
	if r != nil && r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", attachmentDisposition(path))
	}
}

// attachmentDisposition builds an RFC 6266 Content-Disposition value
// that names the file with both an ASCII fallback (for legacy agents)
// and a UTF-8 percent-encoded form so Chinese / spaced filenames don't
// arrive as `_______.zip`.
func attachmentDisposition(path string) string {
	base := filepath.Base(path)
	// ASCII fallback: replace anything outside printable ASCII with `_`
	// and strip quotes so the quoted-string stays well-formed.
	var ascii strings.Builder
	for _, r := range base {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			ascii.WriteByte('_')
			continue
		}
		ascii.WriteRune(r)
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		ascii.String(), url.PathEscape(base))
}

func (s *Server) handleAgentFileUpload(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	if s.workspaceStore == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "no workspace store"})
		return
	}
	if rec := s.requireAgentOwner(w, r, id); rec == nil {
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	// The chat client sends one form field "file" per attachment, so the
	// multipart payload often carries several entries under the same key.
	// r.FormFile only returns the first — iterate over MultipartForm.File
	// so multi-attach uploads land all of their files, not just one.
	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "no file"})
		return
	}
	// sessionId scopes the upload to the sandbox mount the agent actually
	// sees. We resolve the session to find its project_id so uploads in
	// a project chat land in projects/<pid>/ alongside the agent's own
	// writes; loose chats keep the legacy sessions/<chat>/ subdir.
	sessionKey := r.URL.Query().Get("sessionId")
	sessionID := s.workspaceSessionScope(r.Context(), id, sessionKey)
	projectID := s.resolveSessionProject(r.Context(), r, id, sessionKey)
	if projectID != "" {
		// Project sessions don't use the per-chat subdir — clear it so
		// the workspace store routes to projects/<pid>/.
		sessionID = ""
	}
	saved := make([]map[string]any, 0, len(headers))
	for _, h := range headers {
		fh, err := h.Open()
		if err != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		data, err := io.ReadAll(fh)
		fh.Close()
		if err != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if err := s.workspaceStore.Put(r.Context(), id, projectID, sessionID, h.Filename, strings.NewReader(string(data)), int64(len(data)), ""); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		saved = append(saved, map[string]any{"name": h.Filename, "size": len(data)})
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "files": saved})
}

func defaultIfEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// invalidateUser drops the user's lazy-loaded UserSpace so the next
// access reloads it from the DB. The gateway implements InvalidateUser
// behind the api.UserResolver interface.
func (s *Server) invalidateUser(userID string) {
	if userID == "" || s.userResolver == nil {
		return
	}
	if r, ok := s.userResolver.(interface{ InvalidateUser(string) }); ok {
		r.InvalidateUser(userID)
	}
	slog.Debug("invalidated user space", "user", userID)
}

// invalidateAgent drops every cached UserSpace that holds this agent —
// owner plus any foreign caller that lazy-attached via EnsureAgent
// (super_admin chat, public-link viewer, apikey user). Use this after
// writes that mutate the agent's resolved runtime (agents.defaults,
// agent-scope providers); plain user-scope writes can stick with
// invalidateUser.
func (s *Server) invalidateAgent(agentID string) {
	if agentID == "" || s.userResolver == nil {
		return
	}
	if r, ok := s.userResolver.(interface{ InvalidateAgent(string) }); ok {
		r.InvalidateAgent(agentID)
	}
	slog.Debug("invalidated user spaces holding agent", "agent", agentID)
}

// requireOwnerOrSuperAdmin guards endpoints that mutate another user's
// resources.
func (s *Server) requireOwnerOrSuperAdmin(w http.ResponseWriter, r *http.Request, ownerID string) bool {
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return false
	}
	if ident.UserID == ownerID || ident.Role == users.RoleSuperAdmin {
		return true
	}
	jsonResponse(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
	return false
}

var _ workspace.Store = (workspace.Store)(nil)
