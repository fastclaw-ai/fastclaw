package setup

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/skills"
)

// --- Skills ---

func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	homeDir, err := config.HomeDir()
	if err != nil {
		jsonResponse(w, http.StatusOK, []any{})
		return
	}

	skillsDir := filepath.Join(homeDir, "skills")
	// Hydrate from object store first so pods that didn't handle the
	// original install still see the skill bundle. Pass the bundled skill
	// names as the keep-local list so an empty OSS response never causes
	// us to prune builtin skills. No-op when no object store is configured
	// (local mode) or nothing is mirrored yet.
	if s.workspaceStore != nil {
		if err := skills.HydrateSkillsDown(
			r.Context(), s.workspaceStore, skills.GlobalSkillOwner, skillsDir,
			agent.BundledSkillNames()...,
		); err != nil {
			slog.Warn("failed to hydrate global skills from object store", "error", err)
		}
	}
	out := scanSkillsDir(skillsDir)
	if out == nil {
		jsonResponse(w, http.StatusOK, []any{})
		return
	}
	jsonResponse(w, http.StatusOK, out)
}

func (s *Server) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	homeDir, err := config.HomeDir()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	skillPath := filepath.Join(homeDir, "skills", name)
	if err := os.RemoveAll(skillPath); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// Also remove from the object store so other pods drop it on their
	// next hydrate. Failure here shouldn't fail the delete — the local
	// copy is gone already and a stale remote copy will just re-appear
	// next reload (annoying but not dangerous).
	if s.workspaceStore != nil {
		if derr := skills.DeleteSkillUp(r.Context(), s.workspaceStore, skills.GlobalSkillOwner, name); derr != nil {
			slog.Warn("failed to remove global skill from object store", "skill", name, "error", derr)
		}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// handleListAgentSkills lists every skill the agent actually sees at
// runtime — the three layers the skill loader merges, with the innermost
// layer winning when names collide:
//
//   - "agent":  ~/.fastclaw/agents/<id>/agent/skills/  (Layer 1)
//   - "user":   ~/.fastclaw/users/<uid>/skills/         (Layer 1.3)
//   - "global": ~/.fastclaw/skills/                     (Layer 2/3)
//
// Each entry carries a `scope` field so the UI can badge "agent" /
// "user" / "global" and gate destructive actions (only agent-scope is
// deletable from this dialog). Previously this endpoint only walked
// Layer 1, which made user-scoped installs (the path skill-creator
// uses by default, for instance) invisible — the agent loaded them at
// chat time but no UI could see, configure, or remove them.
func (s *Server) handleListAgentSkills(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Skill listing exposes per-skill env spec (which env keys the
	// owner has set). Owner-only — Identity.CanAccessAgent is a
	// deferred-true for session callers and would let any signed-in
	// user enumerate any agent's skills.
	if s.requireAgentOwner(w, r, id) == nil {
		return
	}
	uid := s.effectiveUserID(r)

	// Hydrate agent-scope from object store on demand so replica pods
	// that haven't yet cached the bundle still list it in the UI.
	agentSkillsDir := ""
	if homePath, err := config.AgentHomeDir(id); err == nil {
		agentSkillsDir = filepath.Join(homePath, "skills")
		if s.workspaceStore != nil {
			if err := skills.HydrateSkillsDown(r.Context(), s.workspaceStore, id, agentSkillsDir); err != nil {
				slog.Warn("failed to hydrate agent skills from object store",
					"agent", id, "error", err)
			}
		}
	}

	home, _ := config.HomeDir()
	userSkillsDir := ""
	globalSkillsDir := ""
	if home != "" {
		if uid != "" {
			userSkillsDir = filepath.Join(home, "users", uid, "skills")
		}
		globalSkillsDir = filepath.Join(home, "skills")
	}

	// Merge in agent → user → global order so the innermost layer wins
	// on name collisions (mirrors the runtime loader's precedence). The
	// resulting list is stable per layer for predictable UI ordering.
	seen := map[string]bool{}
	out := []map[string]any{}
	for _, layer := range []struct {
		scope string
		dir   string
	}{
		{"agent", agentSkillsDir},
		{"user", userSkillsDir},
		{"global", globalSkillsDir},
	} {
		if layer.dir == "" {
			continue
		}
		for _, entry := range scanSkillsDir(layer.dir) {
			name, _ := entry["name"].(string)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			entry["scope"] = layer.scope
			out = append(out, entry)
		}
	}
	jsonResponse(w, http.StatusOK, out)
}

// scanSkillsDir reads every SKILL.md under dir and returns the list of
// {name, description, location, type, envSpec?} entries the admin UI
// renders. Shared between the global /api/skills and the per-agent
// /api/agents/{id}/skills paths so frontmatter parsing (description,
// envSpec) stays consistent — earlier the two handlers drifted, with
// the agent-scoped one falling back to "first non-# line" which then
// surfaced the literal `---` frontmatter delimiter as the description.
func scanSkillsDir(dir string) []map[string]any {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		skillPath := filepath.Join(dir, name, "SKILL.md")
		desc := ""
		var envSpec []agent.SkillEnvSpec
		if data, readErr := os.ReadFile(skillPath); readErr == nil {
			fm, body := agent.SplitSkillFrontmatter(data)
			if fm != nil {
				if fm.Description != "" {
					desc = fm.Description
				}
				// Top-level `env:` shortcut wins; fall back to the
				// namespaced metadata.fastclaw|openclaw.env form.
				if len(fm.Env) > 0 {
					envSpec = fm.Env
				} else if meta := agent.ParseSkillMetadata(&fm.Metadata); meta != nil && meta.Meta() != nil {
					envSpec = meta.Meta().Env
				}
			}
			if desc == "" {
				for _, line := range strings.SplitN(body, "\n", 5) {
					line = strings.TrimSpace(line)
					if line != "" && !strings.HasPrefix(line, "#") {
						desc = line
						break
					}
				}
			}
		}
		entryOut := map[string]any{
			"name":        name,
			"description": desc,
			"location":    filepath.Join(dir, name),
			"type":        "skill",
		}
		if len(envSpec) > 0 {
			entryOut["envSpec"] = envSpec
		}
		out = append(out, entryOut)
	}
	return out
}

// handleDeleteAgentSkill removes a skill from an agent's own home dir
// only. Global/shared skills are untouched.
func (s *Server) handleDeleteAgentSkill(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	name := r.PathValue("name")
	// Mutation — owner-only. Identity.CanAccessAgent is a
	// deferred-true for session callers and would let anyone delete
	// skills off any agent.
	if s.requireAgentOwner(w, r, id) == nil {
		return
	}
	homePath, err := config.AgentHomeDir(id)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	skillPath := filepath.Join(homePath, "skills", name)
	if err := os.RemoveAll(skillPath); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// Delete from object store so other pods drop it on their next hydrate.
	if s.workspaceStore != nil {
		if derr := skills.DeleteSkillUp(r.Context(), s.workspaceStore, id, name); derr != nil {
			slog.Warn("failed to remove agent skill from object store",
				"agent", id, "skill", name, "error", derr)
		}
	}
	// Hot-reload the agent so the removed skill drops out of its context.
	if ag := s.resolveAgent(r, id); ag != nil {
		ag.ReloadWorkspaceFiles()
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}
