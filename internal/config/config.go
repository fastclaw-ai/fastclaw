package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultUserID is the user ID used in single-user/local mode.
// In cloud mode, real user IDs come from authenticated requests; in local mode
// everything is scoped under this single default user.
const DefaultUserID = "local"

type userIDKey struct{}

// WithUserID returns a new context carrying the given user ID.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey{}, userID)
}

// UserIDFromContext extracts the user ID from context, falling back to
// DefaultUserID if none is set (local mode).
func UserIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return DefaultUserID
	}
	if v, ok := ctx.Value(userIDKey{}).(string); ok && v != "" {
		return v
	}
	return DefaultUserID
}

// MCPServerConfig holds configuration for a single MCP server.
type MCPServerConfig struct {
	Type    string            `json:"type"`              // "http" or "stdio"
	URL     string            `json:"url,omitempty"`     // for http
	Headers map[string]string `json:"headers,omitempty"` // for http
	Command string            `json:"command,omitempty"` // for stdio
	Args    []string          `json:"args,omitempty"`    // for stdio
	Env     map[string]string `json:"env,omitempty"`     // for stdio
}

// CronJob defines a scheduled job in the configuration.
type CronJob struct {
	Name     string `json:"name"`
	Type     string `json:"type"`     // "exact", "interval", "cron"
	Schedule string `json:"schedule"` // depends on type
	AgentID  string `json:"agentId"`
	Channel  string `json:"channel"`
	ChatID   string `json:"chatId"`
	Message  string `json:"message"`
}

// HeartbeatCfg holds heartbeat configuration.
type HeartbeatCfg struct {
	IntervalMinutes int `json:"intervalMinutes,omitempty"` // default 30
}

// StorageCfg configures the storage backend.
// Default: file-based. For cloud multi-tenant: "postgres" or "sqlite".
type StorageCfg struct {
	Type        string `json:"type,omitempty"`        // "file" (default), "postgres", "sqlite"
	DSN         string `json:"dsn,omitempty"`          // database connection string
	AutoMigrate bool   `json:"autoMigrate,omitempty"` // auto-create tables on startup
}


// WebSearchCfg configures web search.
type WebSearchCfg struct {
	Provider string `json:"provider,omitempty"` // "brave"
	APIKey   string `json:"apiKey,omitempty"`
}

// ExaSearchCfg configures the Exa AI-powered search tool.
type ExaSearchCfg struct {
	APIKey string `json:"apiKey,omitempty"`
}

// HooksCfg configures the webhook ingress server.
type HooksCfg struct {
	Enabled bool   `json:"enabled,omitempty"`
	Token   string `json:"token,omitempty"`
	Path    string `json:"path,omitempty"`  // default "/hooks"
	Port    int    `json:"port,omitempty"`  // default 18954
}

// PluginsCfg configures the plugin system.
type PluginsCfg struct {
	Enabled bool                       `json:"enabled"`
	Paths   []string                   `json:"paths,omitempty"`
	Entries map[string]PluginEntryCfg  `json:"entries,omitempty"`
}

// PluginEntryCfg is per-plugin configuration.
type PluginEntryCfg struct {
	Enabled bool                   `json:"enabled"`
	Config  map[string]interface{} `json:"config,omitempty"`
}

// TaskQueueCfg configures the task queue.
type TaskQueueCfg struct {
	MaxConcurrent  int `json:"maxConcurrent,omitempty"`  // default 10
	TaskTimeoutSec int `json:"taskTimeoutSec,omitempty"` // default 300
}

// SandboxCfg holds sandbox configuration for an agent.
type SandboxCfg struct {
	Enabled  bool   `json:"enabled"`
	Image    string `json:"image,omitempty"`
	Policy   string `json:"policy,omitempty"`  // policy preset name
	Backend  string `json:"backend,omitempty"` // "docker" (default), "e2b"
	E2BKey   string `json:"e2bKey,omitempty"`  // E2B API key (fallback to E2B_API_KEY env)
}

// GatewayAuth holds authentication settings for the gateway API.
type GatewayAuth struct {
	Mode  string `json:"mode,omitempty"`  // "token" (default), "none"
	Token string `json:"token"`
}

// GatewayHTTPEndpoints controls which HTTP endpoints are enabled.
type GatewayHTTPEndpoints struct {
	ChatCompletions GatewayEndpoint `json:"chatCompletions,omitempty"`
	Agents          GatewayEndpoint `json:"agents,omitempty"`
}

// GatewayEndpoint toggles a single HTTP endpoint.
type GatewayEndpoint struct {
	Enabled bool `json:"enabled"`
}

// GatewayHTTP holds HTTP-specific gateway settings.
type GatewayHTTP struct {
	Endpoints GatewayHTTPEndpoints `json:"endpoints,omitempty"`
}

// GatewayCfg holds gateway server configuration.
type GatewayCfg struct {
	Port      int          `json:"port,omitempty"`
	Mode      string       `json:"mode,omitempty"`  // "local" (default), "cloud"
	Bind      string       `json:"bind,omitempty"`  // "loopback" (default), "all"
	Auth      GatewayAuth  `json:"auth,omitempty"`
	HTTP      GatewayHTTP  `json:"http,omitempty"`
	RateLimit RateLimitCfg `json:"rateLimit,omitempty"`
}

// RateLimitCfg controls per-user rate limiting on the HTTP API.
type RateLimitCfg struct {
	RPM int `json:"rpm,omitempty"` // requests per minute per user (0 = unlimited)
}

// MemoryCfg holds memory system configuration.
type MemoryCfg struct {
	AutoPersist AutoPersistCfg `json:"autoPersist,omitempty"`
	FTS         FTSCfg         `json:"fts,omitempty"`
}

// AutoPersistCfg controls automatic memory persistence after agent turns.
type AutoPersistCfg struct {
	Enabled     bool   `json:"enabled"`                  // default true
	EveryNTurns int    `json:"everyNTurns,omitempty"`    // default 5
	Model       string `json:"model,omitempty"`           // override model for extraction
}

// FTSCfg configures full-text search.
type FTSCfg struct {
	Enabled bool   `json:"enabled"`
	DBPath  string `json:"dbPath,omitempty"`
}

// PrivacyCfg holds privacy-related settings.
type PrivacyCfg struct {
	PIIScrubbing PIIScrubCfg `json:"piiScrubbing,omitempty"`
}

// PIIScrubCfg controls PII scrubbing before LLM calls.
type PIIScrubCfg struct {
	Enabled bool `json:"enabled"` // default false — opt-in
}

// SkillsLearnerCfg configures the skills learning loop.
type SkillsLearnerCfg struct {
	Enabled      bool   `json:"enabled"`               // default false — opt-in
	MinToolCalls int    `json:"minToolCalls,omitempty"` // default 3
	Model        string `json:"model,omitempty"`        // override model
}

// Config is the top-level configuration loaded from ~/.fastclaw/fastclaw.json.
type Config struct {
	Providers  map[string]ProviderConfig  `json:"providers"`
	Agents     AgentsConfig               `json:"agents"`
	Channels   map[string]ChannelConfig   `json:"channels"`
	Bindings   []Binding                  `json:"bindings,omitempty"`
	Teams      map[string]TeamEntry       `json:"teams,omitempty"`
	MCPServers map[string]MCPServerConfig `json:"mcpServers,omitempty"`
	CronJobs   []CronJob                  `json:"cronJobs,omitempty"`
	Heartbeat  HeartbeatCfg               `json:"heartbeat,omitempty"`
	Storage    StorageCfg                 `json:"storage,omitempty"`
	Sandbox    SandboxCfg                 `json:"sandbox,omitempty"`
	WebSearch  WebSearchCfg               `json:"webSearch,omitempty"`
	ExaSearch  ExaSearchCfg               `json:"exaSearch,omitempty"`
	Hooks      HooksCfg                   `json:"hooks,omitempty"`
	Plugins    PluginsCfg                 `json:"plugins,omitempty"`
	Gateway    GatewayCfg                 `json:"gateway,omitempty"`
	TaskQueue  TaskQueueCfg               `json:"taskQueue,omitempty"`
	Skills        SkillsCfg                  `json:"skills,omitempty"`
	Memory        MemoryCfg                  `json:"memory,omitempty"`
	Privacy       PrivacyCfg                 `json:"privacy,omitempty"`
	SkillsLearner SkillsLearnerCfg           `json:"skillsLearner,omitempty"`
}

// ModelCost holds pricing info for a model.
type ModelCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// ModelEntry describes a single model within a provider.
type ModelEntry struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Reasoning     bool     `json:"reasoning"`
	Input         []string `json:"input"`
	Cost          ModelCost `json:"cost"`
	ContextWindow int      `json:"contextWindow"`
	MaxTokens     int      `json:"maxTokens"`
}

// ProviderConfig holds API credentials for an LLM provider.
type ProviderConfig struct {
	APIKey   string       `json:"apiKey"`
	APIBase  string       `json:"apiBase"`
	APIType  string       `json:"apiType,omitempty"`
	AuthType string       `json:"authType,omitempty"`
	Models   []ModelEntry `json:"models,omitempty"`
}

// UnmarshalJSON handles backward compatibility: reads "api" as "apiType".
func (pc *ProviderConfig) UnmarshalJSON(data []byte) error {
	type Alias ProviderConfig
	aux := &struct {
		*Alias
		API string `json:"api,omitempty"`
	}{Alias: (*Alias)(pc)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if pc.APIType == "" && aux.API != "" {
		pc.APIType = aux.API
	}
	return nil
}

// AgentsConfig holds agent defaults and the list of agent entries.
type AgentsConfig struct {
	Defaults AgentDefaults `json:"defaults"`
}

// AgentDefaults holds fallback values for all agents.
type AgentDefaults struct {
	Model             string  `json:"model,omitempty"`
	MaxTokens         int     `json:"maxTokens,omitempty"`
	Temperature       float64 `json:"temperature,omitempty"`
	MaxToolIterations int     `json:"maxToolIterations,omitempty"`
	Thinking          string  `json:"thinking,omitempty"`
	PolicyPreset      string  `json:"policy,omitempty"`
}

// AgentEntry is a per-agent entry — populated by merging agent.json with
// config defaults. The `workspace` field here is the agent's working dir
// for user-facing files (overrides the default ~/.fastclaw/workspaces/{id}).
// The agent's home dir (SOUL.md, sessions, memory) is always derived from
// the agent ID via AgentHomeDir.
type AgentEntry struct {
	ID                string                     `json:"id"`
	Workspace         string                     `json:"workspace,omitempty"`
	Model             string                     `json:"model,omitempty"`
	MaxTokens         int                        `json:"maxTokens,omitempty"`
	Temperature       float64                    `json:"temperature,omitempty"`
	MaxToolIterations int                        `json:"maxToolIterations,omitempty"`
	Skills            []string                   `json:"skills,omitempty"`
	Tools             []string                   `json:"tools,omitempty"`
	MCPServers        map[string]MCPServerConfig `json:"mcpServers,omitempty"`
	AlwaysLoadSkills  []string                   `json:"alwaysLoadSkills,omitempty"`
	Thinking          string                     `json:"thinking,omitempty"` // off, low, medium, high, adaptive
	Sandbox           SandboxCfg                 `json:"sandbox,omitempty"`
	PolicyPreset      string                     `json:"policy,omitempty"`  // "permissive", "standard", "restricted"
}

// ChannelConfig holds per-channel configuration with optional accounts.
type ChannelConfig struct {
	Enabled  bool                     `json:"enabled"`
	BotToken string                   `json:"botToken,omitempty"`
	AppToken string                   `json:"appToken,omitempty"` // for Slack Socket Mode
	Accounts map[string]AccountConfig `json:"accounts,omitempty"`
}

// AccountConfig holds account-specific overrides within a channel.
type AccountConfig struct {
	BotToken string `json:"botToken,omitempty"`
}

// Binding maps a match pattern to an agent.
type Binding struct {
	AgentID string `json:"agentId"`
	Match   Match  `json:"match"`
}

// Match defines criteria for routing a message to an agent.
type Match struct {
	Channel   string `json:"channel"`
	AccountID string `json:"accountId,omitempty"`
	Peer      *Peer  `json:"peer,omitempty"`
}

// Peer specifies peer matching criteria.
type Peer struct {
	Kind string `json:"kind,omitempty"` // "group" or "dm"
	ID   string `json:"id,omitempty"`   // specific chat/group ID
}

// AgentFileConfig is the schema for agent.json inside an agent workspace.
type AgentFileConfig struct {
	Model             string                     `json:"model,omitempty"`
	MaxTokens         int                        `json:"maxTokens,omitempty"`
	Temperature       float64                    `json:"temperature,omitempty"`
	MaxToolIterations int                        `json:"maxToolIterations,omitempty"`
	// Workspace overrides the default working directory (where the agent
	// saves user-facing files). Supports `~/` expansion. Leave empty to
	// use the default `~/.fastclaw/workspaces/{id}`.
	Workspace  string                     `json:"workspace,omitempty"`
	Skills     SkillsConfig               `json:"skills,omitempty"`
	MCPServers map[string]MCPServerConfig `json:"mcpServers,omitempty"`
}

// SkillsConfig controls skill loading for an agent.
type SkillsConfig struct {
	Disabled   []string `json:"disabled,omitempty"`
	AlwaysLoad []string `json:"alwaysLoad,omitempty"`
}

// SkillsCfg is the top-level skills configuration (global).
type SkillsCfg struct {
	Install    SkillsInstallCfg         `json:"install,omitempty"`
	Entries    map[string]SkillEntryCfg `json:"entries,omitempty"`
	Load       SkillsLoadCfg            `json:"load,omitempty"`
	AlwaysLoad []string                 `json:"alwaysLoad,omitempty"`
}

// SkillsInstallCfg configures skill installation behavior.
type SkillsInstallCfg struct {
	NodeManager string `json:"nodeManager,omitempty"` // npm, pnpm, bun
}

// SkillEntryCfg is per-skill configuration.
type SkillEntryCfg struct {
	Enabled bool              `json:"enabled"`
	APIKey  string            `json:"apiKey,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// SkillsLoadCfg configures extra skill directories.
type SkillsLoadCfg struct {
	ExtraDirs []string `json:"extraDirs,omitempty"`
}

// ResolvedAgent is the fully merged config for a single agent.
type ResolvedAgent struct {
	ID                string
	Home              string // agent's own directory: SOUL.md, IDENTITY.md, sessions, memory, skills
	Workspace         string // working directory where agent creates user-facing files
	Model             string
	MaxTokens         int
	Temperature       float64
	MaxToolIterations int
	Thinking          string
	Skills            SkillsConfig
	MCPServers        map[string]MCPServerConfig
	Sandbox           SandboxCfg
	PolicyPreset      string
}

// TeamEntry defines a team of agents with group chat behavior settings.
type TeamEntry struct {
	Agents        []string `json:"agents"`
	DefaultAgent  string   `json:"defaultAgent,omitempty"`
	GroupBehavior string   `json:"groupBehavior,omitempty"` // "mention-only" (default) or "default-agent"
}

// TeamConfig is the schema for team.json.
type TeamConfig struct {
	Name    string            `json:"name"`
	Agents  []string          `json:"agents"`
	Routing map[string]string `json:"routing"`
}

// HomeDir returns the FastClaw global root directory (~/.fastclaw).
// This directory holds host-wide resources that are not scoped per-user:
// HomeDir returns ~/.fastclaw — the root for all FastClaw data.
func HomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".fastclaw"), nil
}

// UserDir returns ~/.fastclaw (kept for backward compat, ignores userID).
func UserDir(userID ...string) (string, error) {
	return HomeDir()
}

// EnsureUserDir creates ~/.fastclaw and ~/.fastclaw/agents if needed.
func EnsureUserDir(userID ...string) (string, error) {
	home, err := HomeDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(home, "agents"), 0o755); err != nil {
		return "", err
	}
	return home, nil
}

// AgentHomeDir returns ~/.fastclaw/agents/{agentID}/agent — the agent's own
// directory where its identity files (SOUL.md, IDENTITY.md), sessions, memory
// and skills live. Think of it as the agent's "home".
func AgentHomeDir(agentID string) (string, error) {
	home, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "agents", agentID, "agent"), nil
}

// AgentWorkspaceDir returns ~/.fastclaw/workspaces/{agentID} — the agent's
// working directory for user-facing artifacts (files it creates for or with
// the user). Separate from AgentHomeDir so identity files and user work
// don't share a directory.
func AgentWorkspaceDir(agentID string) (string, error) {
	home, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "workspaces", agentID), nil
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// UserConfigPath returns the full path to the config file for the given user.
func UserConfigPath(userID string) (string, error) {
	dir, err := UserDir(userID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "fastclaw.json"), nil
}

// Load reads and parses the global config from ~/.fastclaw/fastclaw.json.
// Falls back to the legacy per-user path for backwards compatibility.
func Load() (*Config, error) {
	home, err := HomeDir()
	if err != nil {
		return nil, err
	}
	globalPath := filepath.Join(home, "fastclaw.json")
	if _, err := os.Stat(globalPath); err == nil {
		return loadConfigFile(globalPath)
	}
	// Fallback: legacy per-user path
	return LoadForUser(DefaultUserID)
}

func loadConfigFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	applyDefaults(&cfg)
	return &cfg, nil
}

// GlobalConfigPath returns ~/.fastclaw/fastclaw.json.
func GlobalConfigPath() (string, error) {
	home, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "fastclaw.json"), nil
}

// LoadForUser reads and parses ~/.fastclaw/users/{userID}/fastclaw.json.
func LoadForUser(userID string) (*Config, error) {
	if err := MigrateLegacyLayout(); err != nil {
		return nil, fmt.Errorf("migrate legacy layout: %w", err)
	}

	configPath, err := UserConfigPath(userID)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}

	return loadConfigFile(configPath)
}

func applyDefaults(cfg *Config) {
	if cfg.Agents.Defaults.MaxTokens == 0 {
		cfg.Agents.Defaults.MaxTokens = 8192
	}
	if cfg.Agents.Defaults.Temperature == 0 {
		cfg.Agents.Defaults.Temperature = 0.7
	}
	if cfg.Agents.Defaults.MaxToolIterations == 0 {
		cfg.Agents.Defaults.MaxToolIterations = 20
	}
}

// MergedAgentConfig merges defaults with an agent entry and its workspace agent.json
// to produce a fully resolved agent config. Priority: agent.json > entry > defaults.
// Uses DefaultUserID for workspace path resolution; for cloud users call
// MergedAgentConfigForUser instead.
func (cfg *Config) MergedAgentConfig(entry AgentEntry) ResolvedAgent {
	return cfg.MergedAgentConfigForUser(entry, DefaultUserID)
}

// MergedAgentConfigForUser is like MergedAgentConfig but resolves the
// workspace path under the given user's directory.
func (cfg *Config) MergedAgentConfigForUser(entry AgentEntry, userID string) ResolvedAgent {
	home, _ := AgentHomeDir(entry.ID)
	workspace := expandPath(entry.Workspace)
	if workspace == "" {
		workspace, _ = AgentWorkspaceDir(entry.ID)
	}

	resolved := ResolvedAgent{
		ID:                entry.ID,
		Home:              home,
		Workspace:         workspace,
		Model:             cfg.Agents.Defaults.Model,
		MaxTokens:         cfg.Agents.Defaults.MaxTokens,
		Temperature:       cfg.Agents.Defaults.Temperature,
		MaxToolIterations: cfg.Agents.Defaults.MaxToolIterations,
		Thinking:          cfg.Agents.Defaults.Thinking,
		Sandbox:           cfg.Sandbox,
		PolicyPreset:      cfg.Agents.Defaults.PolicyPreset,
	}

	// Layer 2: per-agent entry overrides
	if entry.Model != "" {
		resolved.Model = entry.Model
	}
	if entry.MaxTokens > 0 {
		resolved.MaxTokens = entry.MaxTokens
	}
	if entry.Temperature > 0 {
		resolved.Temperature = entry.Temperature
	}
	if entry.MaxToolIterations > 0 {
		resolved.MaxToolIterations = entry.MaxToolIterations
	}
	if entry.Thinking != "" {
		resolved.Thinking = entry.Thinking
	}
	if entry.Sandbox.Enabled {
		resolved.Sandbox = entry.Sandbox
	}
	if entry.PolicyPreset != "" {
		resolved.PolicyPreset = entry.PolicyPreset
	}

	// Start with global MCP servers
	if len(cfg.MCPServers) > 0 {
		resolved.MCPServers = make(map[string]MCPServerConfig, len(cfg.MCPServers))
		for k, v := range cfg.MCPServers {
			resolved.MCPServers[k] = v
		}
	}

	// Layer 3: agent.json in workspace (highest priority)
	agentJSON := filepath.Join(workspace, "agent.json")
	if data, readErr := os.ReadFile(agentJSON); readErr == nil {
		var fileCfg AgentFileConfig
		if jsonErr := json.Unmarshal(data, &fileCfg); jsonErr == nil {
			if fileCfg.Model != "" {
				resolved.Model = fileCfg.Model
			}
			if fileCfg.MaxTokens > 0 {
				resolved.MaxTokens = fileCfg.MaxTokens
			}
			if fileCfg.Temperature > 0 {
				resolved.Temperature = fileCfg.Temperature
			}
			if fileCfg.MaxToolIterations > 0 {
				resolved.MaxToolIterations = fileCfg.MaxToolIterations
			}
			resolved.Skills = fileCfg.Skills

			// Merge per-agent MCP servers (agent-level overrides global)
			for k, v := range fileCfg.MCPServers {
				if resolved.MCPServers == nil {
					resolved.MCPServers = make(map[string]MCPServerConfig)
				}
				resolved.MCPServers[k] = v
			}
		}
	}

	return resolved
}

// ResolveAgents produces resolved agent configs from config.agents.list.
// If no agents are listed, creates a single "default" agent.
// ResolveAgents discovers agents from the filesystem (~/.fastclaw/agents/)
// and merges each one's config with the global defaults.
func ResolveAgents(cfg *Config) []ResolvedAgent {
	return ResolveAgentsForUser(cfg, "")
}

// ResolveAgentsForUser is like ResolveAgents (userID kept for backward compat, ignored).
func ResolveAgentsForUser(cfg *Config, userID string) []ResolvedAgent {
	// Discover agents from filesystem
	entries := DiscoverAgents()
	if len(entries) == 0 {
		// No agents found — create a default one
		entries = []AgentEntry{{ID: "default"}}
	}

	agents := make([]ResolvedAgent, 0, len(entries))
	for _, entry := range entries {
		agents = append(agents, cfg.MergedAgentConfigForUser(entry, userID))
	}
	return agents
}

// DiscoverAgents scans ~/.fastclaw/agents/ for agent directories.
// Each subdirectory with an agent/ subfolder is treated as an agent.
func DiscoverAgents() []AgentEntry {
	home, err := HomeDir()
	if err != nil {
		return nil
	}
	agentsDir := filepath.Join(home, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil
	}

	var agents []AgentEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		agentID := e.Name()
		wsDir := filepath.Join(agentsDir, agentID, "agent")
		if _, err := os.Stat(wsDir); err != nil {
			continue
		}

		entry := AgentEntry{ID: agentID}

		// Read agent.json if exists for model override etc.
		if data, err := os.ReadFile(filepath.Join(wsDir, "agent.json")); err == nil {
			json.Unmarshal(data, &entry)
			entry.ID = agentID // ensure ID matches directory name
		}

		agents = append(agents, entry)
	}
	return agents
}

// LoadTeam reads a team.json file.
func LoadTeam(path string) (*TeamConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tc TeamConfig
	if err := json.Unmarshal(data, &tc); err != nil {
		return nil, err
	}
	return &tc, nil
}
