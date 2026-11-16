package setup

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/skills"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// --- Creator skills: an agent's own skills, uploaded for its chatters ---
//
// A product lets the person who made an agent add skills to it. They land
// in the agent's layer (~/.fastclaw/agents/<id>/skills, object-store owner
// <id>), so every conversation with that agent gets them, and their
// scripts run in the chatter's sandbox like any other agent skill.
//
// Their keys are the creator's own: set here, stored in the agent's
// skills.entries row, and given only to these skills' scripts. A creator
// skill (agent.CreatorSkillMarker in its folder) never receives the
// deployment's or the product's keys, and may not take the name of a
// product, system or bundled skill.
//
// The product authorizes the creator; these routes are agent-owner only:
//
//   POST /api/agents/{id}/skills/upload       multipart `file` (checked like a personal skill)
//   GET  /api/agents/{id}/skills/{name}/env   → {envSpec, env (secrets masked)}
//   PUT  /api/agents/{id}/skills/{name}/env   {env: {NAME: value}}; masked values keep the stored one
//
// GET /api/agents/{id}/skills lists them (with creator: true), and
// DELETE /api/agents/{id}/skills/{name} removes one and its keys.

const (
	maxCreatorSkills   = 20
	maxCreatorEnvVars  = 20
	maxCreatorEnvValue = 4 << 10
)

var creatorEnvNameRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)

// reservedEnvNames would change how the sandbox or an interpreter runs,
// not configure the skill.
var reservedEnvNames = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "SHELL": true, "PWD": true,
	"IFS": true, "ENV": true, "BASH_ENV": true, "TMPDIR": true,
}

var reservedEnvPrefixes = []string{"LD_", "DYLD_", "PYTHON", "NODE_", "NPM_", "PERL", "RUBY", "BASH_", "FASTCLAW", "E2B_", "GIT_"}

func creatorEnvNameOK(name string) bool {
	if !creatorEnvNameRE.MatchString(name) || reservedEnvNames[name] {
		return false
	}
	for _, p := range reservedEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

// agentSkillsDir returns (and creates) the agent's skills folder, freshly
// hydrated from the object store.
func (s *Server) agentSkillsDir(r *http.Request, agentID string) (string, error) {
	home, err := config.AgentHomeDir(agentID)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if s.workspaceStore != nil {
		if err := skills.HydrateSkillsDown(r.Context(), s.workspaceStore, agentID, dir); err != nil {
			slog.Warn("creator skills: hydrate", "agent", agentID, "error", err)
		}
	}
	return dir, nil
}

// skillNameTaken reports whether name belongs to a skill the creator may
// not shadow: the agent owner's (the product's), the deployment's or a
// bundled one.
func (s *Server) skillNameTaken(r *http.Request, ownerID, name string) bool {
	for _, b := range agent.BundledSkillNames() {
		if b == name {
			return true
		}
	}
	var dirs []string
	if ownerID != "" && agent.SafeSkillOwnerID(ownerID) {
		if d, err := userSkillsDir(ownerID); err == nil {
			if s.workspaceStore != nil {
				_ = skills.HydrateSkillsDown(r.Context(), s.workspaceStore, skills.UserSkillOwner(ownerID), d)
			}
			dirs = append(dirs, d)
		}
	}
	if home, err := config.HomeDir(); err == nil {
		d := filepath.Join(home, "skills")
		if s.workspaceStore != nil {
			_ = skills.HydrateSkillsDown(r.Context(), s.workspaceStore, skills.GlobalSkillOwner, d, agent.BundledSkillNames()...)
		}
		dirs = append(dirs, d)
	}
	if d := agent.ManagedSkillsDir(); d != "" {
		dirs = append(dirs, d)
	}
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(d, name, "SKILL.md")); err == nil {
			return true
		}
	}
	return false
}

func isCreatorSkill(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name, agent.CreatorSkillMarker))
	return err == nil
}

func (s *Server) handleUploadCreatorSkill(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	rec := s.requireAgentOwner(w, r, id)
	if rec == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMySkillUpload)
	if err := r.ParseMultipartForm(maxMySkillUpload); err != nil {
		mySkillError(w, http.StatusBadRequest, "too_large", "")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		mySkillError(w, http.StatusBadRequest, "invalid_file", "")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxMySkillUpload+1))
	if err != nil || len(data) > maxMySkillUpload {
		mySkillError(w, http.StatusBadRequest, "too_large", "")
		return
	}
	up, code, detail := parseMySkillUpload(hdr.Filename, data)
	if code != "" {
		mySkillError(w, http.StatusBadRequest, code, detail)
		return
	}
	dir, err := s.agentSkillsDir(r, id)
	if err != nil {
		mySkillError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	// The agent's own skills that aren't the creator's (installed by the
	// owner) are off limits too.
	_, statErr := os.Stat(filepath.Join(dir, up.name, "SKILL.md"))
	replacing := statErr == nil
	if (replacing && !isCreatorSkill(dir, up.name)) || s.skillNameTaken(r, rec.UserID, up.name) {
		mySkillError(w, http.StatusConflict, "name_taken", up.name)
		return
	}
	if !replacing {
		n := 0
		for _, e := range readMySkills(dir, "agent") {
			if isCreatorSkill(dir, e.Name) {
				n++
			}
		}
		if n >= maxCreatorSkills {
			mySkillError(w, http.StatusBadRequest, "too_many", "")
			return
		}
	}

	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".skill-upload-")
	if err != nil {
		mySkillError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	defer os.RemoveAll(tmp)
	up.files[agent.CreatorSkillMarker] = []byte("uploaded by the agent's creator\n")
	for rel, content := range up.files {
		dest := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			mySkillError(w, http.StatusInternalServerError, "internal", "")
			return
		}
		if err := os.WriteFile(dest, content, 0o644); err != nil {
			mySkillError(w, http.StatusInternalServerError, "internal", "")
			return
		}
	}
	final := filepath.Join(dir, up.name)
	if err := os.RemoveAll(final); err != nil {
		mySkillError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if err := os.Rename(tmp, final); err != nil {
		mySkillError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if s.workspaceStore != nil {
		if replacing {
			if err := skills.DeleteSkillUp(r.Context(), s.workspaceStore, id, up.name); err != nil {
				slog.Warn("creator skills: drop old copy", "agent", id, "skill", up.name, "error", err)
			}
		}
		if err := skills.SyncSkillUp(r.Context(), s.workspaceStore, id, up.name, dir); err != nil {
			slog.Warn("creator skills: mirror", "agent", id, "skill", up.name, "error", err)
		}
	}
	if ag := s.resolveAgent(r, id); ag != nil {
		ag.ReloadWorkspaceFiles()
	}
	var view mySkillView
	for _, v := range readMySkills(dir, "agent") {
		if v.Name == up.name {
			view = v
		}
	}
	out := map[string]any{"skill": view, "replaced": replacing}
	if spec := skillEnvSpec(filepath.Join(final, "SKILL.md")); len(spec) > 0 {
		out["envSpec"] = spec
	}
	jsonResponse(w, http.StatusOK, out)
}

func skillEnvSpec(skillMD string) []agent.SkillEnvSpec {
	data, err := os.ReadFile(skillMD)
	if err != nil {
		return nil
	}
	fm, _ := agent.SplitSkillFrontmatter(data)
	if fm == nil {
		return nil
	}
	if len(fm.Env) > 0 {
		return fm.Env
	}
	if meta := agent.ParseSkillMetadata(&fm.Metadata); meta != nil && meta.Meta() != nil {
		return meta.Meta().Env
	}
	return nil
}

// creatorSkillTarget resolves {id}/{name} to an existing creator skill of
// an agent the caller owns. It writes the error and returns false
// otherwise.
func (s *Server) creatorSkillTarget(w http.ResponseWriter, r *http.Request) (string, string, string, bool) {
	id, name := r.PathValue("id"), r.PathValue("name")
	if s.requireAgentOwner(w, r, id) == nil {
		return "", "", "", false
	}
	if !mySkillNameRE.MatchString(name) {
		mySkillError(w, http.StatusNotFound, "not_found", "")
		return "", "", "", false
	}
	dir, err := s.agentSkillsDir(r, id)
	if err != nil {
		mySkillError(w, http.StatusInternalServerError, "internal", "")
		return "", "", "", false
	}
	if !isCreatorSkill(dir, name) {
		mySkillError(w, http.StatusNotFound, "not_found", "")
		return "", "", "", false
	}
	return id, name, dir, true
}

func (s *Server) handleGetCreatorSkillEnv(w http.ResponseWriter, r *http.Request) {
	id, name, dir, ok := s.creatorSkillTarget(w, r)
	if !ok {
		return
	}
	entries, err := loadAgentSkillEntries(r.Context(), s.dataStore, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		mySkillError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	env := map[string]string{}
	for k, v := range entries[name].Env {
		// Every value a creator sets is a key until shown otherwise.
		env[k] = maskAPIKey(v)
	}
	spec := skillEnvSpec(filepath.Join(dir, name, "SKILL.md"))
	if spec == nil {
		spec = []agent.SkillEnvSpec{}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"envSpec": spec, "env": env})
}

func (s *Server) handlePutCreatorSkillEnv(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id, name, _, ok := s.creatorSkillTarget(w, r)
	if !ok {
		return
	}
	var body struct {
		Env map[string]string `json:"env"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		mySkillError(w, http.StatusBadRequest, "invalid_env", "")
		return
	}
	if len(body.Env) > maxCreatorEnvVars {
		mySkillError(w, http.StatusBadRequest, "invalid_env", "too many")
		return
	}
	for k, v := range body.Env {
		if !creatorEnvNameOK(k) {
			mySkillError(w, http.StatusBadRequest, "invalid_env", k)
			return
		}
		if len(v) > maxCreatorEnvValue || strings.ContainsRune(v, 0) {
			mySkillError(w, http.StatusBadRequest, "invalid_env", k)
			return
		}
	}
	entries, err := loadAgentSkillEntries(r.Context(), s.dataStore, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		mySkillError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	old := entries[name]
	env := map[string]string{}
	for k, v := range body.Env {
		if isMaskedSecret(v) {
			v = old.Env[k]
		}
		if v != "" {
			env[k] = v
		}
	}
	patched := make(map[string]config.SkillEntryCfg, len(entries)+1)
	for k, v := range entries {
		patched[k] = v
	}
	if len(env) == 0 {
		delete(patched, name)
	} else {
		// The whole env is replaced: a name left out is removed. No
		// APIKey: a creator skill gets exactly the env it was given.
		patched[name] = config.SkillEntryCfg{Enabled: old.Enabled, Env: env}
	}
	if err := saveAgentSkillEntries(r.Context(), s.dataStore, id, patched); err != nil {
		mySkillError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	s.invalidateScope(scope.Agent, id)
	masked := map[string]string{}
	for k, v := range env {
		masked[k] = maskAPIKey(v)
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "env": masked})
}

// dropCreatorSkillEnv removes a deleted skill's keys from the agent's row.
func (s *Server) dropCreatorSkillEnv(r *http.Request, agentID, name string) {
	entries, err := loadAgentSkillEntries(r.Context(), s.dataStore, agentID)
	if err != nil || entries == nil {
		return
	}
	if _, ok := entries[name]; !ok {
		return
	}
	delete(entries, name)
	if err := saveAgentSkillEntries(r.Context(), s.dataStore, agentID, entries); err != nil {
		slog.Warn("creator skills: drop env", "agent", agentID, "skill", name, "error", err)
		return
	}
	s.invalidateScope(scope.Agent, agentID)
}
