// Package config holds runtime configuration types and ctx user-id plumbing.
//
// There is no fastclaw.json. Bootstrap settings (port, bind, storage DSN,
// sandbox backend) come from FASTCLAW_* env vars; user-facing config (providers,
// channels, agents, etc.) lives in the database. The Config struct here is
// the in-memory snapshot the gateway assembles at boot from those sources;
// callers never read it from disk.
package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type userIDKey struct{}

// WithUserID stamps a resolved user_id onto ctx. Auth middleware does this
// after validating a session cookie or apikey; nothing else should.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey{}, userID)
}

// UserIDFromContext extracts the resolved user_id, or "" if none.
//
// There is no DefaultUserID fallback. Code paths that reach the store
// without a real user_id are bugs — the auth middleware should have 401'd
// the request, the cron tick should have read the job's owner from the
// row, the channel ingress should have resolved the credential. Catch
// these in development by panicking on store calls with empty user_id.
func UserIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(userIDKey{}).(string); ok {
		return v
	}
	return ""
}

// MustUserIDFromContext returns the resolved user_id or an error. Use this
// at handler boundaries where missing identity is a 500-level bug rather
// than a normal flow.
func MustUserIDFromContext(ctx context.Context) (string, error) {
	uid := UserIDFromContext(ctx)
	if uid == "" {
		return "", errors.New("config: request context has no user_id (auth middleware bug)")
	}
	return uid, nil
}

// MCPServerConfig holds configuration for a single MCP server.
type MCPServerConfig struct {
	Type    string            `json:"type"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// CronJob defines a scheduled job loaded into the gateway's runtime.
type CronJob struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Schedule    string `json:"schedule"`
	OwnerUserID string `json:"ownerUserId,omitempty"`
	AgentID     string `json:"agentId"`
	Channel     string `json:"channel"`
	ChatID      string `json:"chatId"`
	Message     string `json:"message"`
}

// HeartbeatCfg holds heartbeat configuration.
type HeartbeatCfg struct {
	IntervalMinutes int `json:"intervalMinutes,omitempty"`
}

// StorageCfg mirrors the bootstrap storage block so existing code that reads it
// off Config keeps working without an extra parameter plumbed through.
type StorageCfg struct {
	Type        string `json:"type,omitempty"`
	DSN         string `json:"dsn,omitempty"`
	AutoMigrate bool   `json:"autoMigrate,omitempty"`
}

// ObjectStoreCfg controls the object-storage backend.
type ObjectStoreCfg struct {
	Type         string              `json:"type,omitempty"`
	Local        ObjectStoreLocalCfg `json:"local,omitempty"`
	S3           ObjectStoreS3Cfg    `json:"s3,omitempty"`
	AccountID    string              `json:"accountId,omitempty"`
	AliyunIntern bool                `json:"aliyunInternal,omitempty"`
}

type ObjectStoreLocalCfg struct {
	Root string `json:"root,omitempty"`
}

type ObjectStoreS3Cfg struct {
	Endpoint  string `json:"endpoint,omitempty"`
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix,omitempty"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	UseSSL    bool   `json:"useSSL"`
}

// ToolProviderCfg holds credentials/endpoint for one provider (e.g. "exa").
type ToolProviderCfg struct {
	APIKey   string            `json:"apiKey,omitempty"`
	Endpoint string            `json:"endpoint,omitempty"`
	Options  map[string]string `json:"options,omitempty"`
}

// ToolCategoryCfg chooses which provider(s) back a tool category.
type ToolCategoryCfg struct {
	Primary      string   `json:"primary,omitempty"`
	Fallbacks    []string `json:"fallbacks,omitempty"`
	AutoFallback *bool    `json:"autoFallback,omitempty"`
}

func (c ToolCategoryCfg) FallbackEnabled() bool {
	if c.AutoFallback == nil {
		return true
	}
	return *c.AutoFallback
}

func (c ToolCategoryCfg) Chain() []string {
	var out []string
	if c.Primary != "" {
		out = append(out, c.Primary)
	}
	for _, f := range c.Fallbacks {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// HooksCfg configures the webhook ingress server.
type HooksCfg struct {
	Enabled bool   `json:"enabled,omitempty"`
	Token   string `json:"token,omitempty"`
	Path    string `json:"path,omitempty"`
	Port    int    `json:"port,omitempty"`
}

type PluginsCfg struct {
	Enabled bool                      `json:"enabled"`
	Paths   []string                  `json:"paths,omitempty"`
	Entries map[string]PluginEntryCfg `json:"entries,omitempty"`
}

type PluginEntryCfg struct {
	Enabled bool                   `json:"enabled"`
	Config  map[string]interface{} `json:"config,omitempty"`
}

type TaskQueueCfg struct {
	MaxConcurrent  int `json:"maxConcurrent,omitempty"`
	TaskTimeoutSec int `json:"taskTimeoutSec,omitempty"`
}

// PrefsCfg holds runtime preferences that can be set at system, user, or
// agent scope. Timezone is an IANA name such as "Asia/Shanghai".
type PrefsCfg struct {
	Timezone string `json:"timezone,omitempty"`
}

// SandboxCfg holds sandbox configuration for an agent.
//
// Image is the legacy single-slot image/template/snapshot — read-only
// fallback now. The per-backend fields (DockerImage / E2BTemplate /
// BoxliteSnapshot) are authoritative when set, so switching Backend in
// the dashboard preserves each backend's last-entered value instead of
// overwriting the shared slot. Consumers should prefer the per-backend
// field for the active Backend and fall through to Image only when the
// per-backend field is empty (migration path for configs predating the
// split).
type SandboxCfg struct {
	Enabled         bool   `json:"enabled"`
	Image           string `json:"image,omitempty"`
	DockerImage     string `json:"dockerImage,omitempty"`
	E2BTemplate     string `json:"e2bTemplate,omitempty"`
	BoxliteSnapshot string `json:"boxliteSnapshot,omitempty"`
	Policy          string `json:"policy,omitempty"`
	Backend         string `json:"backend,omitempty"`
	E2BKey          string `json:"e2bKey,omitempty"`
	E2BAPIURL       string `json:"e2bApiUrl,omitempty"`
	E2BDomain       string `json:"e2bDomain,omitempty"`
	// Boxlite (https://github.com/boxlite-ai/boxlite) is a hosted sandbox
	// service speaking the REST spec at openapi/rest-sandbox-open-api.yaml.
	// BoxliteURL is the full base URL (default https://api.boxlite.ai/v1);
	// BoxliteKey is the apikey sent as `Authorization: Bearer <key>`
	// directly (no OAuth exchange — that path was removed upstream).
	// ClientID is retained for back-compat with older config rows but
	// no longer wired to anything. Prefix defaults to "default" when
	// empty so the minimum config is just (URL, Key).
	BoxliteURL      string `json:"boxliteUrl,omitempty"`
	BoxliteClientID string `json:"boxliteClientId,omitempty"`
	BoxliteKey      string `json:"boxliteKey,omitempty"`
	BoxlitePrefix   string `json:"boxlitePrefix,omitempty"`
	Network         string `json:"network,omitempty"`
	IdleTTLSec      int    `json:"idleTTLSec,omitempty"`
}

// GatewayAuth is now a thin shell — the authoritative auth state lives in
// the users table (cookie session) and apikeys table (bearer). Token here
// is unused at runtime; kept on the struct so existing JSON serializations
// remain compatible while the field is migrated out of callers.
type GatewayAuth struct {
	Mode  string `json:"mode,omitempty"`
	Token string `json:"token,omitempty"`
}

type GatewayHTTPEndpoints struct {
	ChatCompletions GatewayEndpoint `json:"chatCompletions,omitempty"`
	Agents          GatewayEndpoint `json:"agents,omitempty"`
}

type GatewayEndpoint struct {
	Enabled bool `json:"enabled"`
}

type GatewayHTTP struct {
	Endpoints GatewayHTTPEndpoints `json:"endpoints,omitempty"`
}

// GatewayCfg holds gateway server configuration. The legacy "mode" field
// is gone — multi-user is unconditional.
type GatewayCfg struct {
	Port      int          `json:"port,omitempty"`
	Bind      string       `json:"bind,omitempty"`
	Auth      GatewayAuth  `json:"auth,omitempty"`
	HTTP      GatewayHTTP  `json:"http,omitempty"`
	RateLimit RateLimitCfg `json:"rateLimit,omitempty"`
}

type RateLimitCfg struct {
	RPM int `json:"rpm,omitempty"`
}

// WorkspaceHistoryCfg toggles the opt-in per-session workspace version
// history (git snapshots committed at turn boundary). LocalFS-backed
// workspaces only — S3-backed stores skip history entirely. Known
// limitation: no retention policy yet — every dirty turn adds a commit.
type WorkspaceHistoryCfg struct {
	Enabled bool `json:"enabled"`
}

type MemoryCfg struct {
	AutoPersist AutoPersistCfg `json:"autoPersist,omitempty"`
	FTS         FTSCfg         `json:"fts,omitempty"`
}

type AutoPersistCfg struct {
	Enabled     bool   `json:"enabled"`
	EveryNTurns int    `json:"everyNTurns,omitempty"`
	Model       string `json:"model,omitempty"`
}

type FTSCfg struct {
	Enabled bool   `json:"enabled"`
	DBPath  string `json:"dbPath,omitempty"`
}

type PrivacyCfg struct {
	PIIScrubbing PIIScrubCfg `json:"piiScrubbing,omitempty"`
}

type PIIScrubCfg struct {
	Enabled bool `json:"enabled"`
}

type SkillsLearnerCfg struct {
	Enabled      bool   `json:"enabled"`
	MinToolCalls int    `json:"minToolCalls,omitempty"`
	Model        string `json:"model,omitempty"`
}

// Config is the in-memory runtime snapshot. The gateway assembles this at
// boot by reading FASTCLAW_* env vars + database (system_settings, providers,
// channels, agents). Callers never serialize it back out — DB tables are
// the persistent source of truth.
type Config struct {
	Providers        map[string]ProviderConfig  `json:"providers"`
	Agents           AgentsConfig               `json:"agents"`
	Channels         map[string]ChannelConfig   `json:"channels"`
	Bindings         []Binding                  `json:"bindings,omitempty"`
	Teams            map[string]TeamEntry       `json:"teams,omitempty"`
	MCPServers       map[string]MCPServerConfig `json:"mcpServers,omitempty"`
	CronJobs         []CronJob                  `json:"cronJobs,omitempty"`
	Heartbeat        HeartbeatCfg               `json:"heartbeat,omitempty"`
	Storage          StorageCfg                 `json:"storage,omitempty"`
	Sandbox          SandboxCfg                 `json:"sandbox,omitempty"`
	ToolProviders    map[string]ToolProviderCfg `json:"toolProviders,omitempty"`
	Tools            map[string]ToolCategoryCfg `json:"tools,omitempty"`
	ObjectStore      ObjectStoreCfg             `json:"objectStore,omitempty"`
	Hooks            HooksCfg                   `json:"hooks,omitempty"`
	Plugins          PluginsCfg                 `json:"plugins,omitempty"`
	Gateway          GatewayCfg                 `json:"gateway,omitempty"`
	TaskQueue        TaskQueueCfg               `json:"taskQueue,omitempty"`
	Skills           SkillsCfg                  `json:"skills,omitempty"`
	Memory           MemoryCfg                  `json:"memory,omitempty"`
	WorkspaceHistory WorkspaceHistoryCfg        `json:"workspaceHistory,omitempty"`
	Privacy          PrivacyCfg                 `json:"privacy,omitempty"`
	SkillsLearner    SkillsLearnerCfg           `json:"skillsLearner,omitempty"`
	Prefs            PrefsCfg                   `json:"prefs,omitempty"`
}

// ModelCost holds pricing info for a model.
type ModelCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

type ModelEntry struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Reasoning     bool      `json:"reasoning"`
	Input         []string  `json:"input"`
	Cost          ModelCost `json:"cost"`
	ContextWindow int       `json:"contextWindow"`
	MaxTokens     int       `json:"maxTokens"`
}

// ProviderConfig holds API credentials for an LLM provider — used both as
// the JSON shape inside agents.config and as the resolved per-(scope, name)
// view assembled by the providers resolver.
type ProviderConfig struct {
	APIKey   string       `json:"apiKey"`
	APIBase  string       `json:"apiBase"`
	APIType  string       `json:"apiType,omitempty"`
	AuthType string       `json:"authType,omitempty"`
	Models   []ModelEntry `json:"models,omitempty"`
}

// UnmarshalJSON handles a long-deprecated `api` alias for `apiType`.
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

type AgentsConfig struct {
	Defaults AgentDefaults `json:"defaults"`
}

type AgentDefaults struct {
	Model             string  `json:"model,omitempty"`
	MaxTokens         int     `json:"maxTokens,omitempty"`
	Temperature       float64 `json:"temperature,omitempty"`
	MaxToolIterations int     `json:"maxToolIterations,omitempty"`
	// MaxParallelToolCalls caps how many tool calls a single LLM
	// response is allowed to execute concurrently in one round. The
	// LLM still decides how many tools to emit; we just refuse to
	// run more than this many at once. The overflow gets a synthetic
	// "deferred — re-issue next round" tool_result so the model
	// naturally serializes. 0 = unlimited (no cap, current behavior).
	// Useful when downstream APIs (Brave free tier 1RPS, etc.) can't
	// take a parallel burst.
	MaxParallelToolCalls int    `json:"maxParallelToolCalls,omitempty"`
	Thinking             string `json:"thinking,omitempty"`
	PolicyPreset         string `json:"policy,omitempty"`
	// PromptMode lives here so the agent-scope `agents.defaults`
	// config row (written by CLI and dashboard) round-trips into
	// ResolvedAgent at userspace assembly time — see
	// gateway/userspace.go where agentOverride is applied.
	PromptMode string `json:"promptMode,omitempty"`
	// SplitReplies — per-agent override of WeChatCfg.SplitReplies.
	// Nil at this layer means the agent-scope row has no opinion; the
	// effective value falls back to system-level WeChatCfg.SplitReplies.
	SplitReplies *bool `json:"splitReplies,omitempty"`
	// AutoPersist — per-agent override of MemoryCfg.AutoPersist.Enabled.
	// Pointer-typed for the same reason as SplitReplies: distinguishing
	// "operator hasn't touched it" from "explicitly false". When non-nil,
	// flips ag.memoryCfg.AutoPersist.Enabled at agent build time so the
	// runPostTurn check at loop.go:2286 either fires the background
	// distill-into-USER.md/MEMORY.md pass or skips it. Mainly useful in
	// chatbot mode — that mode's curated tool allowlist has no write_file,
	// so this is the only way for the agent to remember a chatter across
	// sessions.
	AutoPersist *bool `json:"autoPersist,omitempty"`
	// WorkspaceHistory — same pointer semantics as AutoPersist: nil = inherit
	// system workspaceHistory.enabled, non-nil = authoritative for this agent.
	WorkspaceHistory *bool `json:"workspaceHistory,omitempty"`
}

// AgentEntry is the in-memory shape of one agent row, used during
// resolution. UserID is the owning account (mirrors agents.user_id).
// Per-agent model overrides aren't carried here — they live in the
// configs table at scope=agent and are merged in via scope.SettingInto
// during userspace load.
type AgentEntry struct {
	ID     string `json:"id"`
	UserID string `json:"userId,omitempty"`
	// Name mirrors agents.name (the operator-given display name) and is
	// carried through to ResolvedAgent.DisplayName so the system prompt
	// can stamp a fallback identity line when IDENTITY.md is empty.
	Name                 string                     `json:"name,omitempty"`
	Workspace            string                     `json:"workspace,omitempty"`
	MaxTokens            int                        `json:"maxTokens,omitempty"`
	Temperature          float64                    `json:"temperature,omitempty"`
	MaxToolIterations    int                        `json:"maxToolIterations,omitempty"`
	MaxParallelToolCalls int                        `json:"maxParallelToolCalls,omitempty"`
	Skills               []string                   `json:"skills,omitempty"`
	MCPServers           map[string]MCPServerConfig `json:"mcpServers,omitempty"`
	AlwaysLoadSkills     []string                   `json:"alwaysLoadSkills,omitempty"`
	Thinking             string                     `json:"thinking,omitempty"`
	Sandbox              SandboxCfg                 `json:"sandbox,omitempty"`
	PolicyPreset         string                     `json:"policy,omitempty"`
	// PromptMode selects how heavily the framework system prompt
	// participates AND which built-in tools the LLM sees. Empty =
	// "agent" (current default) for backward compatibility. See
	// PromptMode* constants. The built-in tool set per mode is
	// hardcoded in builtinAllowForMode (internal/agent/loop.go) —
	// extension via Plugin / MCP, not per-agent allowlists, by design.
	PromptMode string `json:"promptMode,omitempty"`
	// SplitReplies overrides the system-wide WeChatCfg.SplitReplies
	// setting for THIS agent. Nil = inherit system default; non-nil =
	// authoritative for this agent. Pointer (not bool) because we need
	// to distinguish "operator hasn't touched it" from "operator
	// explicitly turned it off". The agent uses the effective value to
	// (1) decide whether to advertise the SplitMessageMarker in the
	// system-prompt hint, and (2) stamp OutboundMessage.AllowSplit so
	// the WeChat adapter knows whether to honor the marker.
	SplitReplies *bool `json:"splitReplies,omitempty"`
	// AutoPersist overrides MemoryCfg.AutoPersist.Enabled for this agent.
	// Same pointer semantics as SplitReplies. When true, the agent's
	// runPostTurn fires a background LLM call every N turns to distill
	// recent messages into USER.md (chatter profile) and MEMORY.md
	// (long-term facts) — the chatbot-mode persistence path since that
	// mode's curated tool allowlist excludes write_file.
	AutoPersist *bool `json:"autoPersist,omitempty"`
	// WorkspaceHistory — same pointer semantics as AutoPersist: nil = inherit
	// system workspaceHistory.enabled, non-nil = authoritative for this agent.
	WorkspaceHistory *bool `json:"workspaceHistory,omitempty"`
}

// PromptMode controls which framework sections BuildSystemPromptAs emits.
// Chatbot-style products (companion, customer support, role-play) cannot
// inherit the agent-shaped instructions (task delegation, todo tracking,
// tool-use discipline, sandbox rules) without their character bleeding
// into a generic AI-assistant tone. PromptMode lets a deployment opt out
// of those sections per agent.
const (
	// PromptModeAgent emits the full framework prompt (task delegation,
	// todo.md, tool-use discipline, sandbox rules, workspace self-update,
	// scheduling). Default when PromptMode is empty.
	PromptModeAgent = "agent"
	// PromptModeChatbot keeps the minimal identity scaffolding
	// (file-purpose schema, confidentiality, date) and drops every
	// agent-loop instruction so chatbot persona files (SOUL.md /
	// IDENTITY.md / USER.md / MEMORY.md) shape behavior directly.
	PromptModeChatbot = "chatbot"
	// PromptModeCustomize emits ONLY the bootstrap files (plus a date
	// anchor). The author is responsible for putting any framework
	// guidance they need inside SOUL.md / IDENTITY.md themselves —
	// this mode hands the floor over to the persona files completely.
	// (Renamed from PromptModeMinimal to make the intent more obvious:
	// you're CUSTOMIZING the system prompt yourself, not asking fastclaw
	// for a minimal version of its built-in one.)
	PromptModeCustomize = "customize"
)

// ChannelConfig holds per-channel runtime configuration. Built by the
// channels scope resolver from system/user/agent rows.
type ChannelConfig struct {
	Enabled  bool                     `json:"enabled"`
	BotToken string                   `json:"botToken,omitempty"`
	AppToken string                   `json:"appToken,omitempty"`
	Accounts map[string]AccountConfig `json:"accounts,omitempty"`
}

type AccountConfig struct {
	BotToken string `json:"botToken,omitempty"`
	// BaseURL is the per-account API base used by adapters whose
	// upstream isn't a fixed hostname (e.g. WeChat iLink hands out a
	// region-specific baseurl on QR confirmation). Empty for
	// Telegram/Discord/Slack — they all hit fixed endpoints.
	BaseURL string `json:"baseUrl,omitempty"`
	// UserID is an extra account-scoped identifier some adapters need
	// alongside BotToken (WeChat iLink's `ilink_user_id`, used as the
	// X-WECHAT-UIN seed and for typing/getconfig calls). Empty when
	// not applicable.
	UserID string `json:"userId,omitempty"`
	// EncryptKey is the symmetric key used by adapters whose upstream
	// optionally encrypts webhook payloads (Feishu's "加密策略 →
	// Encrypt Key"). Empty when the user hasn't configured encryption
	// in the upstream console — adapters then expect plaintext bodies.
	EncryptKey string `json:"encryptKey,omitempty"`
	// UseLongConn switches inbound transport to a long-lived
	// connection (WebSocket) initiated outbound from fastclaw rather
	// than the platform POSTing to a public webhook. Currently only
	// honored by the Feishu adapter; ignored by adapters that don't
	// offer this mode. When true, verification/encrypt keys are
	// unused (the WS connection is authenticated by appID/appSecret)
	// and no public URL needs to be reachable.
	UseLongConn bool `json:"useLongConn,omitempty"`
}

type Binding struct {
	AgentID string `json:"agentId"`
	Match   Match  `json:"match"`
}

type Match struct {
	Channel   string `json:"channel"`
	AccountID string `json:"accountId,omitempty"`
	Peer      *Peer  `json:"peer,omitempty"`
}

type Peer struct {
	Kind string `json:"kind,omitempty"`
	ID   string `json:"id,omitempty"`
}

// AgentFileConfigLoader is the indirection point for layer-3 agent config.
// Gateway boot wires it to read from agents.config rows in the DB.
var AgentFileConfigLoader func(agentID, home string) (AgentFileConfig, bool) = defaultAgentFileConfigLoader

func defaultAgentFileConfigLoader(_, home string) (AgentFileConfig, bool) {
	if home == "" {
		return AgentFileConfig{}, false
	}
	data, err := os.ReadFile(filepath.Join(home, "agent.json"))
	if err != nil {
		return AgentFileConfig{}, false
	}
	var cfg AgentFileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return AgentFileConfig{}, false
	}
	return cfg, true
}

// AgentFileConfig is the schema for an agent's per-row override JSON
// (agents.config column). Per-agent providers/channels live in their own
// scoped DB tables and are NOT persisted here.
type AgentFileConfig struct {
	Model                string                     `json:"model,omitempty"`
	MaxTokens            int                        `json:"maxTokens,omitempty"`
	Temperature          float64                    `json:"temperature,omitempty"`
	MaxToolIterations    int                        `json:"maxToolIterations,omitempty"`
	MaxParallelToolCalls int                        `json:"maxParallelToolCalls,omitempty"`
	Workspace            string                     `json:"workspace,omitempty"`
	Skills               SkillsConfig               `json:"skills,omitempty"`
	MCPServers           map[string]MCPServerConfig `json:"mcpServers,omitempty"`
	ToolProviders        map[string]ToolProviderCfg `json:"toolProviders,omitempty"`
	Tools                map[string]ToolCategoryCfg `json:"tools,omitempty"`
	Providers            map[string]ProviderConfig  `json:"providers,omitempty"`
	// PromptMode mirrors AgentEntry.PromptMode at the file-config layer.
	// Non-empty values override the entry-level setting.
	PromptMode string `json:"promptMode,omitempty"`
	// SplitReplies mirrors AgentEntry.SplitReplies. Nil =
	// inherit; non-nil = authoritative for this agent.
	SplitReplies *bool `json:"splitReplies,omitempty"`
	// AutoPersist mirrors AgentEntry.AutoPersist. Nil = inherit;
	// non-nil = authoritative for this agent.
	AutoPersist *bool `json:"autoPersist,omitempty"`
	// WorkspaceHistory — same pointer semantics as AutoPersist: nil = inherit
	// system workspaceHistory.enabled, non-nil = authoritative for this agent.
	WorkspaceHistory *bool `json:"workspaceHistory,omitempty"`
	// Admins gates write-mode slash commands (/new /reset /undo /retry /compact
	// /model /personality) in IM channels. Keyed by channel name ("discord",
	// "telegram", "slack", ...), each value is the platform-side user IDs
	// allowed to run those commands on that channel. Empty/absent list = no
	// gate (anyone can run the command — backward-compatible default).
	//
	// On web/api the gate falls through to msg.UserID == agent owner UUID
	// regardless of this field, since those channels carry the FastClaw
	// identity directly and don't need a per-platform allowlist.
	Admins map[string][]string `json:"admins,omitempty"`
}

type SkillsConfig struct {
	Disabled   []string `json:"disabled,omitempty"`
	AlwaysLoad []string `json:"alwaysLoad,omitempty"`
}

type SkillsCfg struct {
	Install      SkillsInstallCfg                    `json:"install,omitempty"`
	Entries      map[string]SkillEntryCfg            `json:"entries,omitempty"`
	AgentEntries map[string]map[string]SkillEntryCfg `json:"agentEntries,omitempty"`
	Load         SkillsLoadCfg                       `json:"load,omitempty"`
	AlwaysLoad   []string                            `json:"alwaysLoad,omitempty"`
}

type SkillsInstallCfg struct {
	NodeManager string `json:"nodeManager,omitempty"`
}

type SkillEntryCfg struct {
	Enabled bool              `json:"enabled"`
	APIKey  string            `json:"apiKey,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type SkillsLoadCfg struct {
	ExtraDirs []string `json:"extraDirs,omitempty"`
}

// ResolvedAgent is the fully merged config for a single agent.
type ResolvedAgent struct {
	ID     string
	UserID string
	// DisplayName mirrors agents.name — the human-readable name the
	// operator gave the agent ("Bob", "tdj", "Sonny"). Used as a
	// fallback identity line in the system prompt when IDENTITY.md
	// is empty so the model doesn't introduce itself as "Claude".
	DisplayName          string
	Home                 string
	Workspace            string
	Model                string
	MaxTokens            int
	Temperature          float64
	MaxToolIterations    int
	MaxParallelToolCalls int
	Thinking             string
	Skills               SkillsConfig
	MCPServers           map[string]MCPServerConfig
	Sandbox              SandboxCfg
	PolicyPreset         string
	ToolProviders        map[string]ToolProviderCfg
	Tools                map[string]ToolCategoryCfg
	Providers            map[string]ProviderConfig
	// Admins is the per-channel admin allowlist for write-mode slash
	// commands. See AgentFileConfig.Admins for semantics + default.
	Admins map[string][]string
	// PromptMode selects the system-prompt assembly profile AND the
	// built-in tool set the LLM sees. See AgentEntry.PromptMode for
	// semantics. Empty = PromptModeAgent.
	PromptMode string
	// SplitReplies — nil = inherit system WeChatCfg.SplitReplies,
	// non-nil = authoritative for this agent. The agent stamps the
	// EFFECTIVE value (override OR system default) on every
	// OutboundMessage.AllowSplit at send time.
	SplitReplies *bool
	// AutoPersist — nil = inherit system MemoryCfg.AutoPersist.Enabled,
	// non-nil = authoritative for this agent. Drives whether the
	// runPostTurn hook fires AutoPersistMemory (the LLM-driven distill-
	// to-USER.md/MEMORY.md pass) every N turns.
	AutoPersist *bool
	// WorkspaceHistory — nil = inherit system workspaceHistory.enabled,
	// non-nil = authoritative for this agent. Drives whether runPostTurn
	// snapshots the session workspace into the git history repo.
	WorkspaceHistory *bool
}

type TeamEntry struct {
	Agents        []string `json:"agents"`
	DefaultAgent  string   `json:"defaultAgent,omitempty"`
	GroupBehavior string   `json:"groupBehavior,omitempty"`
}

type TeamConfig struct {
	Name    string            `json:"name"`
	Agents  []string          `json:"agents"`
	Routing map[string]string `json:"routing"`
}

// HomeDir returns the FastClaw root directory (default ~/.fastclaw).
// Holds the sqlite db, sandbox roots, and FS-materialized agent caches.
func HomeDir() (string, error) {
	if h := os.Getenv("FASTCLAW_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".fastclaw"), nil
}

// AgentHomeDir returns ~/.fastclaw/agents/{agentID}/agent — the FS cache
// directory the runtime materializes agent identity files into. agents.id
// is globally unique so no user namespace is needed.
func AgentHomeDir(agentID string) (string, error) {
	if agentID == "" {
		return "", errors.New("config.AgentHomeDir: agentID is required")
	}
	home, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "agents", agentID, "agent"), nil
}

// AgentWorkspaceDir returns the agent's working directory for user-facing
// artifacts: ~/.fastclaw/workspaces/<agent_id>/. agents.id is globally
// unique so no user namespace is needed; per-session sub-directories are
// added by the workspace store at write time (see workspace.LocalFS).
func AgentWorkspaceDir(agentID string) (string, error) {
	if agentID == "" {
		return "", errors.New("config.AgentWorkspaceDir: agentID is required")
	}
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

// ApplyDefaults fills in zero-valued knobs on Agents.Defaults.
func ApplyDefaults(cfg *Config) {
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

// MergedAgentConfig merges defaults with an agent entry to produce a fully
// resolved agent config.
func (cfg *Config) MergedAgentConfig(entry AgentEntry) ResolvedAgent {
	home, _ := AgentHomeDir(entry.ID)
	workspace := expandPath(entry.Workspace)
	if workspace == "" {
		workspace, _ = AgentWorkspaceDir(entry.ID)
	}

	resolved := ResolvedAgent{
		ID:                   entry.ID,
		UserID:               entry.UserID,
		DisplayName:          entry.Name,
		Home:                 home,
		Workspace:            workspace,
		Model:                cfg.Agents.Defaults.Model,
		MaxTokens:            cfg.Agents.Defaults.MaxTokens,
		Temperature:          cfg.Agents.Defaults.Temperature,
		MaxToolIterations:    cfg.Agents.Defaults.MaxToolIterations,
		MaxParallelToolCalls: cfg.Agents.Defaults.MaxParallelToolCalls,
		Thinking:             cfg.Agents.Defaults.Thinking,
		Sandbox:              cfg.Sandbox,
		PolicyPreset:         cfg.Agents.Defaults.PolicyPreset,
		WorkspaceHistory:     cfg.Agents.Defaults.WorkspaceHistory,
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
	if entry.MaxParallelToolCalls > 0 {
		resolved.MaxParallelToolCalls = entry.MaxParallelToolCalls
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
	if entry.PromptMode != "" {
		resolved.PromptMode = entry.PromptMode
	}
	if entry.SplitReplies != nil {
		v := *entry.SplitReplies
		resolved.SplitReplies = &v
	}
	if entry.AutoPersist != nil {
		v := *entry.AutoPersist
		resolved.AutoPersist = &v
	}
	if entry.WorkspaceHistory != nil {
		v := *entry.WorkspaceHistory
		resolved.WorkspaceHistory = &v
	}

	if len(cfg.MCPServers) > 0 {
		resolved.MCPServers = make(map[string]MCPServerConfig, len(cfg.MCPServers))
		for k, v := range cfg.MCPServers {
			resolved.MCPServers[k] = v
		}
	}
	if len(cfg.Providers) > 0 {
		resolved.Providers = make(map[string]ProviderConfig, len(cfg.Providers))
		for k, v := range cfg.Providers {
			resolved.Providers[k] = v
		}
	}
	if len(cfg.ToolProviders) > 0 {
		resolved.ToolProviders = make(map[string]ToolProviderCfg, len(cfg.ToolProviders))
		for k, v := range cfg.ToolProviders {
			resolved.ToolProviders[k] = v
		}
	}
	if len(cfg.Tools) > 0 {
		resolved.Tools = make(map[string]ToolCategoryCfg, len(cfg.Tools))
		for k, v := range cfg.Tools {
			resolved.Tools[k] = v
		}
	}

	if fileCfg, ok := AgentFileConfigLoader(entry.ID, home); ok {
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
		if fileCfg.MaxParallelToolCalls > 0 {
			resolved.MaxParallelToolCalls = fileCfg.MaxParallelToolCalls
		}
		resolved.Skills = fileCfg.Skills
		if len(fileCfg.Admins) > 0 {
			resolved.Admins = make(map[string][]string, len(fileCfg.Admins))
			for ch, ids := range fileCfg.Admins {
				cp := make([]string, len(ids))
				copy(cp, ids)
				resolved.Admins[ch] = cp
			}
		}
		for k, v := range fileCfg.MCPServers {
			if resolved.MCPServers == nil {
				resolved.MCPServers = make(map[string]MCPServerConfig)
			}
			resolved.MCPServers[k] = v
		}
		for k, v := range fileCfg.Providers {
			if resolved.Providers == nil {
				resolved.Providers = make(map[string]ProviderConfig)
			}
			resolved.Providers[k] = v
		}
		for k, v := range fileCfg.ToolProviders {
			if resolved.ToolProviders == nil {
				resolved.ToolProviders = make(map[string]ToolProviderCfg)
			}
			resolved.ToolProviders[k] = v
		}
		for k, v := range fileCfg.Tools {
			if resolved.Tools == nil {
				resolved.Tools = make(map[string]ToolCategoryCfg)
			}
			resolved.Tools[k] = v
		}
		if fileCfg.PromptMode != "" {
			resolved.PromptMode = fileCfg.PromptMode
		}
		if fileCfg.SplitReplies != nil {
			v := *fileCfg.SplitReplies
			resolved.SplitReplies = &v
		}
		if fileCfg.AutoPersist != nil {
			v := *fileCfg.AutoPersist
			resolved.AutoPersist = &v
		}
	}

	// Chatbot mode: cap tool iterations at a lower default (5) unless
	// the operator explicitly set a value on the agent entry. Chatbots
	// are conversational — 20 tool rounds burns tokens and makes the
	// user wait too long.
	if resolved.PromptMode == PromptModeChatbot && entry.MaxToolIterations == 0 {
		const chatbotDefaultIter = 5
		if resolved.MaxToolIterations > chatbotDefaultIter {
			resolved.MaxToolIterations = chatbotDefaultIter
		}
	}

	return resolved
}

// ResolveAgents builds resolved agent configs from a list of entries.
// Source-of-truth lookup happens in the caller (DB ListAgents); this
// function only does the merge.
func ResolveAgents(cfg *Config, entries []AgentEntry) []ResolvedAgent {
	out := make([]ResolvedAgent, 0, len(entries))
	for _, e := range entries {
		if e.ID == "" {
			continue
		}
		out = append(out, cfg.MergedAgentConfig(e))
	}
	return out
}

// LoadTeam reads a team.json file from the FS skills bundle.
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
