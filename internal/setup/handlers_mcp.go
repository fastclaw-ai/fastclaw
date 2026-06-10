package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/buildinfo"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

const maxMCPServersPerScope = 20

var (
	mcpServerNameRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	mcpEnvKeyRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type agentMCPServerOut struct {
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Enabled   bool              `json:"enabled"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	UpdatedAt string            `json:"updatedAt,omitempty"`
}

type writeAgentMCPServerRequest struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Enabled *bool             `json:"enabled,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// MCP server config is stored in the configs table keyed by (kind="mcp",
// user_id, agent_id). The handlers below come in two flavors — per-agent
// (agent_id=Y, owner-gated) and system (agent_id="", super_admin-gated) —
// but share one scope-parameterized core (mcpList/Create/Update/Delete)
// that operates on a plain (userID, agentID) ownership pair. System scope
// is (userID="", agentID="").

func (s *Server) handleListAgentMCPServers(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if s.requireAgentOwner(w, r, agentID) == nil {
		return
	}
	s.mcpList(w, r, "", agentID)
}

func (s *Server) handleCreateAgentMCPServer(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	agentID := r.PathValue("id")
	if s.requireAgentOwner(w, r, agentID) == nil {
		return
	}
	if s.mcpCreate(w, r, "", agentID) {
		s.invalidateAgent(agentID)
	}
}

func (s *Server) handleUpdateAgentMCPServer(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	agentID := r.PathValue("id")
	if s.requireAgentOwner(w, r, agentID) == nil {
		return
	}
	if s.mcpUpdate(w, r, "", agentID, r.PathValue("name")) {
		s.invalidateAgent(agentID)
	}
}

func (s *Server) handleDeleteAgentMCPServer(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	agentID := r.PathValue("id")
	if s.requireAgentOwner(w, r, agentID) == nil {
		return
	}
	if s.mcpDelete(w, r, "", agentID, r.PathValue("name")) {
		s.invalidateAgent(agentID)
	}
}

func (s *Server) handleListSystemMCPServers(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeScope(w, r, scope.System, "", scopeRead) {
		return
	}
	s.mcpList(w, r, "", "")
}

func (s *Server) handleCreateSystemMCPServer(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	if !s.authorizeScope(w, r, scope.System, "", scopeWrite) {
		return
	}
	if s.mcpCreate(w, r, "", "") {
		s.invalidateScope(scope.System, "")
	}
}

func (s *Server) handleUpdateSystemMCPServer(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	if !s.authorizeScope(w, r, scope.System, "", scopeWrite) {
		return
	}
	if s.mcpUpdate(w, r, "", "", r.PathValue("name")) {
		s.invalidateScope(scope.System, "")
	}
}

func (s *Server) handleDeleteSystemMCPServer(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	if !s.authorizeScope(w, r, scope.System, "", scopeWrite) {
		return
	}
	if s.mcpDelete(w, r, "", "", r.PathValue("name")) {
		s.invalidateScope(scope.System, "")
	}
}

// mcpList writes the masked server list for the given ownership.
func (s *Server) mcpList(w http.ResponseWriter, r *http.Request, userID, agentID string) {
	rows, err := s.dataStore.ListConfigs(r.Context(), store.KindMCP, userID, agentID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out := make([]agentMCPServerOut, 0, len(rows))
	for _, rec := range rows {
		server, err := mcpServerOutFromRecord(rec)
		if err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		out = append(out, server)
	}
	jsonResponse(w, http.StatusOK, map[string]any{"servers": out})
}

// mcpCreate validates and persists a new MCP server at the given
// ownership. Returns true when a row was saved (caller invalidates).
func (s *Server) mcpCreate(w http.ResponseWriter, r *http.Request, userID, agentID string) bool {
	var req writeAgentMCPServerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return false
	}
	if err := validateMCPServerName(req.Name); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return false
	}
	cfg, err := validateMCPServerConfig(req)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return false
	}
	if existing, err := s.dataStore.GetConfigByName(r.Context(), store.KindMCP, userID, agentID, req.Name); err == nil && existing != nil {
		jsonResponse(w, http.StatusConflict, map[string]any{"error": "MCP server already exists"})
		return false
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return false
	}
	rows, err := s.dataStore.ListConfigs(r.Context(), store.KindMCP, userID, agentID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return false
	}
	if len(rows) >= maxMCPServersPerScope {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("maximum %d MCP servers per scope", maxMCPServersPerScope)})
		return false
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	rec := &store.ConfigRecord{
		Kind:    store.KindMCP,
		UserID:  userID,
		AgentID: agentID,
		Name:    req.Name,
		Enabled: enabled,
		Data:    mcpConfigToData(cfg),
	}
	if err := s.dataStore.SaveConfig(r.Context(), rec); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return false
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "server": mcpServerOutFromConfig(req.Name, enabled, cfg, rec.UpdatedAt)})
	return true
}

// mcpUpdate validates and persists changes to an existing MCP server.
// Masked secret values are preserved from the stored config. Returns true
// when a row was saved (caller invalidates).
func (s *Server) mcpUpdate(w http.ResponseWriter, r *http.Request, userID, agentID, name string) bool {
	if err := validateMCPServerName(name); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return false
	}
	rec, err := s.dataStore.GetConfigByName(r.Context(), store.KindMCP, userID, agentID, name)
	if err != nil || rec == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return false
	}
	existing, err := mcpConfigFromRecord(*rec)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return false
	}

	var req writeAgentMCPServerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return false
	}
	if req.Name != "" && req.Name != name {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "name cannot be changed"})
		return false
	}
	cfg, err := validateMCPServerConfig(req)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return false
	}
	cfg.Headers = mergeMaskedStringMap(existing.Headers, cfg.Headers)
	cfg.Env = mergeMaskedStringMap(existing.Env, cfg.Env)
	enabled := rec.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	rec.Enabled = enabled
	rec.Data = mcpConfigToData(cfg)
	if err := s.dataStore.SaveConfig(r.Context(), rec); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return false
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "server": mcpServerOutFromConfig(name, enabled, cfg, rec.UpdatedAt)})
	return true
}

// mcpDelete removes an MCP server row. Returns true when a row was
// deleted (caller invalidates).
func (s *Server) mcpDelete(w http.ResponseWriter, r *http.Request, userID, agentID, name string) bool {
	rec, err := s.dataStore.GetConfigByName(r.Context(), store.KindMCP, userID, agentID, name)
	if err != nil || rec == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return false
	}
	if err := s.dataStore.DeleteConfig(r.Context(), rec.ID); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return false
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
	return true
}

func validateMCPServerName(name string) error {
	if name == "" {
		return errors.New("name required")
	}
	if !mcpServerNameRe.MatchString(name) {
		return errors.New("name must match ^[A-Za-z0-9_]{1,64}$")
	}
	return nil
}

func validateMCPServerConfig(req writeAgentMCPServerRequest) (config.MCPServerConfig, error) {
	switch req.Type {
	case "http":
		if req.Command != "" || len(req.Args) > 0 || len(req.Env) > 0 {
			return config.MCPServerConfig{}, errors.New("http MCP server cannot include command, args, or env")
		}
		if strings.TrimSpace(req.URL) == "" {
			return config.MCPServerConfig{}, errors.New("url required")
		}
		u, err := url.Parse(req.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return config.MCPServerConfig{}, errors.New("url must be http or https with a host")
		}
		if buildinfo.IsHostedDeploy() && isBlockedHostedMCPHost(u.Hostname()) {
			return config.MCPServerConfig{}, errors.New("hosted deployments cannot connect MCP servers to localhost, private, or link-local addresses")
		}
		if err := validateMCPStringMap("header", req.Headers, 50, 128, 4096, false); err != nil {
			return config.MCPServerConfig{}, err
		}
		return config.MCPServerConfig{Type: "http", URL: req.URL, Headers: req.Headers}, nil
	case "stdio":
		if buildinfo.IsHostedDeploy() {
			return config.MCPServerConfig{}, errors.New("stdio MCP servers are disabled on hosted deployments")
		}
		if req.URL != "" || len(req.Headers) > 0 {
			return config.MCPServerConfig{}, errors.New("stdio MCP server cannot include url or headers")
		}
		cmd := strings.TrimSpace(req.Command)
		if cmd == "" {
			return config.MCPServerConfig{}, errors.New("command required")
		}
		if len(req.Args) > 100 {
			return config.MCPServerConfig{}, errors.New("args cannot exceed 100 entries")
		}
		for _, arg := range req.Args {
			if len(arg) > 4096 {
				return config.MCPServerConfig{}, errors.New("arg cannot exceed 4096 characters")
			}
		}
		if err := validateMCPStringMap("env", req.Env, 100, 128, 8192, true); err != nil {
			return config.MCPServerConfig{}, err
		}
		return config.MCPServerConfig{Type: "stdio", Command: cmd, Args: req.Args, Env: req.Env}, nil
	default:
		return config.MCPServerConfig{}, errors.New("type must be http or stdio")
	}
}

func validateMCPStringMap(label string, values map[string]string, maxEntries, maxKeyLen, maxValueLen int, envKeys bool) error {
	if len(values) > maxEntries {
		return fmt.Errorf("%s entries cannot exceed %d", label, maxEntries)
	}
	for k, v := range values {
		if k == "" {
			return fmt.Errorf("%s key required", label)
		}
		if len(k) > maxKeyLen {
			return fmt.Errorf("%s key cannot exceed %d characters", label, maxKeyLen)
		}
		if strings.ContainsAny(k, "\r\n") || strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("%s key/value cannot contain newlines", label)
		}
		if isEnvExpansionReference(v) {
			return fmt.Errorf("%s value for %q cannot reference server environment variables", label, k)
		}
		if envKeys && !mcpEnvKeyRe.MatchString(k) {
			return fmt.Errorf("env key %q is invalid", k)
		}
		if len(v) > maxValueLen {
			return fmt.Errorf("%s value cannot exceed %d characters", label, maxValueLen)
		}
	}
	return nil
}

func mcpConfigFromRecord(rec store.ConfigRecord) (config.MCPServerConfig, error) {
	blob, err := json.Marshal(rec.Data)
	if err != nil {
		return config.MCPServerConfig{}, err
	}
	var cfg config.MCPServerConfig
	if err := json.Unmarshal(blob, &cfg); err != nil {
		return config.MCPServerConfig{}, err
	}
	return cfg, nil
}

func mcpConfigToData(cfg config.MCPServerConfig) map[string]interface{} {
	blob, _ := json.Marshal(cfg)
	var data map[string]interface{}
	_ = json.Unmarshal(blob, &data)
	return data
}

func mcpServerOutFromRecord(rec store.ConfigRecord) (agentMCPServerOut, error) {
	cfg, err := mcpConfigFromRecord(rec)
	if err != nil {
		return agentMCPServerOut{}, err
	}
	return mcpServerOutFromConfig(rec.Name, rec.Enabled, cfg, rec.UpdatedAt), nil
}

func mcpServerOutFromConfig(name string, enabled bool, cfg config.MCPServerConfig, updatedAt time.Time) agentMCPServerOut {
	masked := maskMCPConfig(cfg)
	out := agentMCPServerOut{
		Name:    name,
		Type:    masked.Type,
		Enabled: enabled,
		URL:     masked.URL,
		Headers: masked.Headers,
		Command: masked.Command,
		Args:    masked.Args,
		Env:     masked.Env,
	}
	if !updatedAt.IsZero() {
		out.UpdatedAt = updatedAt.Format(time.RFC3339)
	}
	return out
}

func maskMCPConfig(cfg config.MCPServerConfig) config.MCPServerConfig {
	out := cfg
	if len(cfg.Headers) > 0 {
		out.Headers = make(map[string]string, len(cfg.Headers))
		for k, v := range cfg.Headers {
			if mcpKeyLooksSecret(k) {
				out.Headers[k] = maskAPIKey(v)
			} else {
				out.Headers[k] = v
			}
		}
	}
	if len(cfg.Env) > 0 {
		out.Env = make(map[string]string, len(cfg.Env))
		for k, v := range cfg.Env {
			if mcpKeyLooksSecret(k) {
				out.Env[k] = maskAPIKey(v)
			} else {
				out.Env[k] = v
			}
		}
	}
	return out
}

func isEnvExpansionReference(value string) bool {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) < 2 || trimmed[0] != '$' {
		return false
	}
	return mcpEnvKeyRe.MatchString(trimmed[1:])
}

func isBlockedHostedMCPHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

func mergeMaskedStringMap(existing, incoming map[string]string) map[string]string {
	if incoming == nil {
		return nil
	}
	out := make(map[string]string, len(incoming))
	for k, v := range incoming {
		if isMaskedSecret(v) {
			if old, ok := existing[k]; ok {
				out[k] = old
				continue
			}
		}
		out[k] = v
	}
	return out
}

func mcpKeyLooksSecret(name string) bool {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "authorization") || strings.Contains(lower, "cookie") {
		return true
	}
	return looksLikeSecret(name)
}
