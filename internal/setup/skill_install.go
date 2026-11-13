package setup

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/skills"
)

// handleInstallSkill installs a skill from skills.sh, clawhub.ai, or a
// specific GitHub repo. Body:
//
//	{
//	  "source": "skillssh" | "clawhub" | "github" | "" (auto),
//	  "name":   "<skill slug / folder name>",
//	  "repo":   "owner/repo"  (github only),
//	  "agent":  "<agent-id>"  (optional; if set, install into the agent's own
//	                           skills dir and hot-reload it)
//	  "scope":  "user"        (optional; install into the caller's own skills,
//	                           available in all their conversations)
//	}
//
// With neither, the install is global — admin only.
//
// Source precedence when source is empty: skills.sh → clawhub.
// Global installs (no `agent`) require the local/admin user — cloud users
// cannot modify shared skills through this endpoint.
func (s *Server) handleInstallSkill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source string `json:"source"`
		Name   string `json:"name"`
		Skill  string `json:"skill"` // legacy alias for "name"
		Repo   string `json:"repo"`
		Agent  string `json:"agent"`
		Scope  string `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	if req.Name == "" {
		req.Name = req.Skill
	}
	if req.Name == "" && req.Repo == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "name or repo required"})
		return
	}

	target, ok := s.skillInstallTarget(w, r, req.Agent, req.Scope == "user")
	if !ok {
		return
	}
	targetDir, err := target.dir(r)
	if err != nil {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	result, err := runInstall(req.Source, req.Name, req.Repo, targetDir)
	if err != nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	// Mirror the installed skill bundle to the shared object store so
	// other pods can hydrate it on their next reload. Without this step
	// the skill only exists on this pod's emptyDir, and a chat request
	// balanced to another pod wouldn't see it.
	if s.workspaceStore != nil && result != nil && result.Name != "" {
		owner := target.storeOwner()
		if uerr := skills.SyncSkillUp(r.Context(), s.workspaceStore, owner, result.Name, targetDir); uerr != nil {
			slog.Warn("failed to mirror skill to object store",
				"owner", owner, "skill", result.Name, "error", uerr)
		}
	}

	if req.Agent != "" {
		if ag := s.resolveAgent(r, req.Agent); ag != nil {
			ag.ReloadWorkspaceFiles()
		}
	}

	slog.Info("skill installed",
		"source", result.Source, "name", result.Name,
		"version", result.Version, "path", result.InstalledAt, "agent", req.Agent, "user", target.UserID)
	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":          true,
		"source":      result.Source,
		"name":        result.Name,
		"version":     result.Version,
		"installedAt": result.InstalledAt,
		"files":       result.FilesWritten,
	})
}

// authorizeSkillInstallTarget enforces the mutation and target-scope rules
// shared by registry installs and zip uploads.
func (s *Server) authorizeSkillInstallTarget(w http.ResponseWriter, r *http.Request, agentID string) bool {
	if !s.requireWritable(w, r) {
		return false
	}
	if agentID != "" {
		// Owner-only — Identity.CanAccessAgent is a deferred-true for
		// session callers, so without an explicit owner check anyone
		// could push a skill into anyone else's agent home dir.
		return s.requireAgentOwner(w, r, agentID) != nil
	}
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unauthorized"})
		return false
	}
	if !ident.CanAdminPlatform() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "platform admin required"})
		return false
	}
	return true
}

// resolveInstallTarget picks the target directory for an install. Authorization
// happens before this helper is called: agent installs are owner-only; global
// installs are platform-admin-only.
func resolveInstallTarget(r *http.Request, agentID string) (string, error) {
	if agentID != "" {
		// agents.id is globally unique, so the home dir doesn't need a
		// user namespace — owner check happens upstream of this call.
		homePath, err := config.AgentHomeDir(agentID)
		if err != nil {
			return "", fmt.Errorf("resolve agent home: %w", err)
		}
		dir := filepath.Join(homePath, "skills")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create agent skills dir: %w", err)
		}
		return dir, nil
	}
	home, err := config.HomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// runInstall dispatches to the right skills backend. The dispatch itself
// lives in skills.Install so `fastclaw skill install` accepts exactly the
// same sources as this endpoint.
func runInstall(source, name, repo, targetDir string) (*skills.Result, error) {
	return skills.Install(source, name, repo, targetDir)
}

// handleUploadSkill installs a skill from a user-supplied .zip file.
// Multipart POST: field `file` is the zip; optional `name` form field
// overrides the inferred skill folder name. Optional `?agent=<id>` query
// param scopes the install to one agent's home, `?scope=user` to the
// caller's own skills (same auth + target rules as handleInstallSkill).
//
// Layout assumptions:
//   - Zip with a single common top-level directory (e.g. `my-skill/...`):
//     that dir becomes the skill folder name and its contents land
//     directly inside <target>/my-skill/.
//   - Zip without a common top-level (files at root): we wrap them in a
//     <target>/<name>/ folder, where <name> defaults to the upload's
//     filename minus extension and can be overridden by the `name` form
//     field.
//
// Zip-slip protection: every extracted file path is validated to stay
// under the chosen skill dir. Symlinks are skipped — Go's archive/zip
// doesn't auto-follow them but we also refuse to recreate them on disk.
func (s *Server) handleUploadSkill(w http.ResponseWriter, r *http.Request) {
	const maxUploadSize = 64 << 20 // 64 MiB
	agentID := r.URL.Query().Get("agent")
	// An end user's own skill: checked and capped (see handleUploadMySkill).
	if agentID == "" && r.URL.Query().Get("scope") == "user" && isEndUserCaller(r) {
		s.handleUploadMySkill(w, r)
		return
	}
	target, ok := s.skillInstallTarget(w, r, agentID, r.URL.Query().Get("scope") == "user")
	if !ok {
		return
	}

	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "file field required"})
		return
	}
	defer file.Close()

	if hdr.Size > maxUploadSize {
		jsonResponse(w, http.StatusRequestEntityTooLarge, map[string]any{"ok": false, "error": "zip too large"})
		return
	}

	targetDir, err := target.dir(r)
	if err != nil {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	data, err := io.ReadAll(io.LimitReader(file, maxUploadSize+1))
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if int64(len(data)) > maxUploadSize {
		jsonResponse(w, http.StatusRequestEntityTooLarge, map[string]any{"ok": false, "error": "zip too large"})
		return
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "not a valid zip: " + err.Error()})
		return
	}

	commonTop := detectCommonTopDir(zr.File)
	skillName := strings.TrimSpace(r.FormValue("name"))
	if skillName == "" {
		skillName = commonTop
	}
	if skillName == "" {
		base := filepath.Base(hdr.Filename)
		skillName = strings.TrimSuffix(base, filepath.Ext(base))
	}
	skillName = sanitizeSkillName(skillName)
	if skillName == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "could not determine skill name"})
		return
	}

	stripPrefix := ""
	if commonTop != "" {
		// Zip already has a wrapping dir — strip it on extraction so
		// we don't end up with skill/skill/SKILL.md (works whether the
		// wrapper matches skillName or the user renamed via form field).
		stripPrefix = commonTop + "/"
	}

	// Validation: a valid skill MUST have a SKILL.md at its root. We
	// check this BEFORE creating any directories so a bad upload (e.g.
	// a zip of a session log, a random folder) doesn't pollute the
	// agent's skills dir. Match SKILL.md exactly (case-sensitive — the
	// runtime in agent/skills.go reads "SKILL.md").
	hasSkillMD := false
	for _, entry := range zr.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		name := entry.Name
		if stripPrefix != "" && strings.HasPrefix(name, stripPrefix) {
			name = strings.TrimPrefix(name, stripPrefix)
		}
		if name == "SKILL.md" {
			hasSkillMD = true
			break
		}
	}
	if !hasSkillMD {
		jsonResponse(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "zip is not a valid skill: SKILL.md not found at the skill root",
		})
		return
	}

	skillDir := filepath.Join(targetDir, skillName)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	skillDirAbs, err := filepath.Abs(filepath.Clean(skillDir))
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	files := make([]string, 0, len(zr.File))

	for _, entry := range zr.File {
		name := entry.Name
		if stripPrefix != "" {
			if name == strings.TrimSuffix(stripPrefix, "/") || name == stripPrefix {
				continue
			}
			if !strings.HasPrefix(name, stripPrefix) {
				continue
			}
			name = strings.TrimPrefix(name, stripPrefix)
		}
		if name == "" {
			continue
		}
		// Reject any entry whose cleaned name escapes the skill dir.
		clean := filepath.Clean(name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			slog.Warn("skipping unsafe zip entry", "name", entry.Name)
			continue
		}
		dest := filepath.Join(skillDirAbs, clean)
		destAbs, err := filepath.Abs(filepath.Clean(dest))
		if err != nil || (destAbs != skillDirAbs && !strings.HasPrefix(destAbs, skillDirAbs+string(os.PathSeparator))) {
			slog.Warn("skipping zip-slip entry", "name", entry.Name, "dest", destAbs)
			continue
		}
		// Reject symlinks — Go's archive/zip exposes them via mode bits.
		if entry.Mode()&os.ModeSymlink != 0 {
			slog.Warn("skipping symlink in zip", "name", entry.Name)
			continue
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(destAbs, 0o755); err != nil {
				jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destAbs), 0o755); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		rc, err := entry.Open()
		if err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		out, err := os.OpenFile(destAbs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			rc.Close()
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		// Cap per-file copy too — defense in depth against zip bombs
		// after the outer 64MiB cap.
		if _, err := io.Copy(out, io.LimitReader(rc, maxUploadSize)); err != nil {
			rc.Close()
			out.Close()
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		rc.Close()
		out.Close()
		files = append(files, clean)
	}

	if s.workspaceStore != nil {
		owner := target.storeOwner()
		if uerr := skills.SyncSkillUp(r.Context(), s.workspaceStore, owner, skillName, targetDir); uerr != nil {
			slog.Warn("failed to mirror uploaded skill to object store",
				"owner", owner, "skill", skillName, "error", uerr)
		}
	}
	if agentID != "" {
		if ag := s.resolveAgent(r, agentID); ag != nil {
			ag.ReloadWorkspaceFiles()
		}
	}

	slog.Info("skill uploaded",
		"name", skillName, "agent", agentID, "files", len(files), "path", skillDir)
	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":          true,
		"source":      "upload",
		"name":        skillName,
		"installedAt": skillDir,
		"files":       files,
	})
}

// detectCommonTopDir returns the shared first-path-segment across every
// zip entry, or "" if entries diverge or any entry sits at the zip root.
// Used to decide whether the user's upload already wraps its skill in a
// folder we should peel off (and reuse as the skill name).
func detectCommonTopDir(files []*zip.File) string {
	var top string
	for _, f := range files {
		n := f.Name
		if n == "" {
			continue
		}
		// macOS Finder zips include `__MACOSX/` metadata — ignore it
		// so its presence doesn't break common-top detection.
		if strings.HasPrefix(n, "__MACOSX/") {
			continue
		}
		idx := strings.Index(n, "/")
		if idx <= 0 {
			// file lives at zip root → there's no common top dir
			return ""
		}
		seg := n[:idx]
		if top == "" {
			top = seg
		} else if top != seg {
			return ""
		}
	}
	return top
}

// sanitizeSkillName strips path separators and other surprises from a
// user-supplied skill name, leaving a single safe directory component.
func sanitizeSkillName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, "/")
	name = strings.TrimSuffix(name, "\\")
	// Take the last path segment in case the user typed a path.
	name = filepath.Base(name)
	if name == "." || name == ".." || name == "/" || name == "\\" {
		return ""
	}
	// Filter unsafe characters.
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '\x00':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// handleSearchSkills returns search results. source=skillssh (default) hits
// https://skills.sh; source=clawhub proxies clawhub.ai's search endpoint.
// GET /api/skills/search?q=xxx&source=skillssh|clawhub
func (s *Server) handleSearchSkills(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "skillssh"
	}

	switch source {
	case "skillssh", "skills.sh":
		results, err := skills.SearchSkillsSh(query)
		if err != nil {
			jsonResponse(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		jsonResponse(w, http.StatusOK, map[string]any{"source": "skills.sh", "results": results})
	case "clawhub":
		u := fmt.Sprintf("https://clawhub.ai/api/v1/search?q=%s&limit=20", url.QueryEscape(query))
		resp, err := http.Get(u)
		if err != nil {
			jsonResponse(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	default:
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unsupported source"})
	}
}
