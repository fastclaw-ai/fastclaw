package setup

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// Agent name is a public identifier used in URLs and bot handles, so it must
// be URL-safe and globally unique. 3–32 chars, lowercase letters/digits/hyphens,
// cannot start with a hyphen.
var agentNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,31}$`)

const defaultBootstrap = `This is your first conversation with the user. Your SOUL.md, IDENTITY.md, and USER.md are all empty — you have no name, no personality, and no information about the user yet.

On the user's first message (regardless of what it is), do NOT answer it directly. Instead:

1. Briefly greet the user and let them know you're a fresh agent that needs a bit of setup.
2. Ask 2-3 short questions in a warm, conversational tone (not a form):
   - What should the user call you? (your agent name)
   - How should you address the user? (their preferred name)
   - What role / personality do they want you to have?

When the user answers, use the write_file tool to save each piece of information in exactly one file — do not repeat it across files:

- ` + "`IDENTITY.md`" + ` is about YOU, the agent. Write only your name and role here, e.g.:
  "# Identity\n\nYour name is {agent_name}. You are {role — e.g. a podcast creation assistant}."

- ` + "`SOUL.md`" + ` is about HOW you behave — tone, style, values. Capture the personality in the user's own words when possible.

- ` + "`USER.md`" + ` is about the USER, not you. Write only facts about them, e.g.:
  "# User\n\nPreferred name: {user_name}." (add any other details they share, like role or working language)

After saving all three, overwrite this BOOTSTRAP.md with a single blank line via write_file so the setup instructions stop appearing in future conversations. Then acknowledge the setup and proceed with whatever the user originally asked (if anything actionable).

Keep the exchange short. Don't lecture, don't dump a list of options — ask, listen, write files, move on.
`

// --- Agent Management ---

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadUserConfig(r)
	if err != nil {
		cfg = &config.Config{}
	}
	resolved := config.ResolveAgents(cfg)
	var agents []map[string]any
	for _, ra := range resolved {
		soul := ""
		soulPath := filepath.Join(ra.Home, "SOUL.md")
		if data, readErr := os.ReadFile(soulPath); readErr == nil {
			soul = string(data)
		}
		agents = append(agents, map[string]any{
			"id":                ra.ID,
			"model":             ra.Model,
			"home":              ra.Home,
			"workspace":         ra.Workspace,
			"maxTokens":         ra.MaxTokens,
			"temperature":       ra.Temperature,
			"maxToolIterations": ra.MaxToolIterations,
			"thinking":          ra.Thinking,
			"soul":              soul,
		})
	}
	if agents == nil {
		agents = []map[string]any{}
	}
	jsonResponse(w, http.StatusOK, agents)
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string `json:"id"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	if req.ID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "agent name is required"})
		return
	}
	if !agentNameRE.MatchString(req.ID) {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "agent name must be 3–32 chars, lowercase letters, digits or hyphens, and cannot start with a hyphen"})
		return
	}

	// Check for duplicate by checking if the home dir already exists.
	homePath, _ := config.AgentHomeDir(req.ID)
	if _, err := os.Stat(homePath); err == nil {
		jsonResponse(w, http.StatusConflict, map[string]any{"ok": false, "error": fmt.Sprintf("agent name %q is already taken", req.ID)})
		return
	}

	// Create the agent's home (identity, sessions, memory, skills).
	for _, dir := range []string{homePath, filepath.Join(homePath, "memory"), filepath.Join(homePath, "sessions"), filepath.Join(homePath, "skills")} {
		os.MkdirAll(dir, 0o755)
	}

	// Create the separate workspace dir for agent-generated user content.
	if workPath, err := config.AgentWorkspaceDir(req.ID); err == nil {
		os.MkdirAll(workPath, 0o755)
	}

	// SOUL.md starts empty; populated by the agent during first-run bootstrap.
	os.WriteFile(filepath.Join(homePath, "SOUL.md"), []byte(""), 0o644)

	// BOOTSTRAP.md guides the agent to interview the user on its first run
	// and persist the answers. Once done, the agent clears this file so the
	// instructions stop appearing in its system prompt.
	os.WriteFile(filepath.Join(homePath, "BOOTSTRAP.md"), []byte(defaultBootstrap), 0o644)

	// Write agent.json with model config
	agentCfg := config.AgentFileConfig{Model: req.Model}
	agentData, _ := json.MarshalIndent(agentCfg, "", "  ")
	os.WriteFile(filepath.Join(homePath, "agent.json"), agentData, 0o644)

	// Load the new agent into the running gateway so chat requests see it.
	if s.agentProvider != nil {
		if err := s.agentProvider.ReloadAgents(); err != nil {
			slog.Warn("failed to reload agents after create", "id", req.ID, "error", err)
		}
	}

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Model string `json:"model"`
		Soul  string `json:"soul"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}

	// Update home files directly.
	homePath, _ := config.AgentHomeDir(id)
	if _, err := os.Stat(homePath); err != nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": "agent not found"})
		return
	}

	if req.Soul != "" {
		os.WriteFile(filepath.Join(homePath, "SOUL.md"), []byte(req.Soul), 0o644)
	}
	if req.Model != "" {
		agentCfg := config.AgentFileConfig{Model: req.Model}
		agentData, _ := json.MarshalIndent(agentCfg, "", "  ")
		os.WriteFile(filepath.Join(homePath, "agent.json"), agentData, 0o644)
	}

	if s.agentProvider != nil {
		if err := s.agentProvider.ReloadAgents(); err != nil {
			slog.Warn("failed to reload agents after update", "id", id, "error", err)
		}
	}

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAgentFile serves a file from the agent's workspace directory for
// inline preview or download. The path is sanitized and must resolve inside
// the workspace root — any attempt to escape is rejected with 403.
// Add ?download=1 to force a download (Content-Disposition: attachment).
func (s *Server) handleAgentFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	relPath := r.PathValue("path")
	if relPath == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}

	workRoot, err := config.AgentWorkspaceDir(id)
	if err != nil {
		http.Error(w, "resolve workspace", http.StatusInternalServerError)
		return
	}
	absRoot, err := filepath.Abs(workRoot)
	if err != nil {
		http.Error(w, "invalid workspace", http.StatusInternalServerError)
		return
	}
	absFull, err := filepath.Abs(filepath.Join(absRoot, relPath))
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if absFull != absRoot && !strings.HasPrefix(absFull, absRoot+string(filepath.Separator)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	info, err := os.Stat(absFull)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if info.IsDir() {
		http.Error(w, "is a directory", http.StatusBadRequest)
		return
	}

	if r.URL.Query().Get("download") == "1" {
		filename := filepath.Base(absFull)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	}

	// http.ServeFile handles Range requests, If-Modified-Since, and content type.
	http.ServeFile(w, r, absFull)
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	homePath, _ := config.AgentHomeDir(id)

	// Remove the entire agent home (~/.fastclaw/agents/{id}).
	parent := filepath.Dir(homePath)
	if err := os.RemoveAll(parent); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	// Remove the workspace (user-facing content) too.
	if workPath, err := config.AgentWorkspaceDir(id); err == nil {
		os.RemoveAll(workPath)
	}

	if s.agentProvider != nil {
		if err := s.agentProvider.ReloadAgents(); err != nil {
			slog.Warn("failed to reload agents after delete", "id", id, "error", err)
		}
	}

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}
