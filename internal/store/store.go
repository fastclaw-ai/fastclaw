// Package store is the single persistence layer for FastClaw. The database
// is mandatory (sqlite by default; postgres for production); there is no
// file-only fallback. Every per-user table requires a real users.id row;
// callers that haven't resolved a user must 401, not invent a placeholder.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is returned by Get* methods when the row does not exist. Use
// errors.Is(err, store.ErrNotFound) at call sites.
var ErrNotFound = errors.New("store: not found")

// Store is the unified interface for all persistent data.
//
// Tables fall into three buckets:
//   - account-scoped (users, web_sessions, apikeys): keyed by users.id
//   - agent-scoped (agents, agent_files, cron_jobs): keyed by agents.id;
//     ownership is on agents.user_id
//   - per-(user, agent) (sessions): chat history is private to one user
//   - scope-tagged (configs): rows carry (scope, scope_id, kind, name)
type Store interface {
	// --- Users ---
	CreateUser(ctx context.Context, u *UserRecord) error
	GetUser(ctx context.Context, id string) (*UserRecord, error)
	GetUserByLogin(ctx context.Context, usernameOrEmail string) (*UserRecord, error)
	GetUserByExternal(ctx context.Context, apikeyID, externalID string) (*UserRecord, error)
	ListUsers(ctx context.Context) ([]UserRecord, error)
	UpdateUser(ctx context.Context, u *UserRecord) error
	DeleteUser(ctx context.Context, id string) error
	CountUsers(ctx context.Context) (int, error)

	// --- Web sessions (login cookies) ---
	CreateWebSession(ctx context.Context, sess *WebSessionRecord) error
	GetWebSession(ctx context.Context, sid string) (*WebSessionRecord, error)
	DeleteWebSession(ctx context.Context, sid string) error
	DeleteExpiredWebSessions(ctx context.Context, before time.Time) error

	// --- API keys (per user) ---
	ListAPIKeys(ctx context.Context, userID string) ([]APIKeyRecord, error)
	GetAPIKey(ctx context.Context, id string) (*APIKeyRecord, error)
	CreateAPIKey(ctx context.Context, ak *APIKeyRecord) error
	DeleteAPIKey(ctx context.Context, id string) error
	RotateAPIKey(ctx context.Context, id, keyHash, keyPrefix string) error
	LookupAPIKeyByHash(ctx context.Context, keyHash string) (*APIKeyRecord, error)

	// --- API key ↔ agent permissions (M:N) ---
	SetAPIKeyAgents(ctx context.Context, apikeyID string, agentIDs []string) error
	ListAPIKeyAgents(ctx context.Context, apikeyID string) ([]string, error)
	APIKeyCanAccessAgent(ctx context.Context, apikeyID, agentID string) (bool, error)

	// --- Agents (atomic; agents.id is globally unique) ---
	ListAgents(ctx context.Context, ownerUserID string) ([]AgentRecord, error)
	GetAgent(ctx context.Context, agentID string) (*AgentRecord, error)
	SaveAgent(ctx context.Context, agent *AgentRecord) error
	DeleteAgent(ctx context.Context, agentID string) error
	ListAllAgents(ctx context.Context) ([]AgentRecord, error)

	// --- Sessions (per user, per agent — chat history is private) ---
	GetSession(ctx context.Context, userID, agentID, sessionKey string) (*SessionRecord, error)
	SaveSession(ctx context.Context, userID, agentID, sessionKey string, session *SessionRecord) error
	ListSessions(ctx context.Context, userID, agentID string) ([]SessionMeta, error)
	// ListSessionOwnerPairs returns every distinct (user_id, agent_id)
	// pair present in the sessions table. Used by the admin Chats page
	// to discover non-owner sessions: when a chatter binds their own bot
	// to a public agent (or messages a public agent on the web), the
	// session row is saved under that chatter's user_id, not the agent
	// owner's — so an owner-keyed ListSessions misses them. Iterating
	// pairs lets the admin view enumerate every (chatter, agent) tuple
	// that has chat history, regardless of who owns the agent.
	ListSessionOwnerPairs(ctx context.Context) ([]SessionOwnerPair, error)
	DeleteSession(ctx context.Context, userID, agentID, sessionKey string) error
	RenameSession(ctx context.Context, userID, agentID, sessionKey, title string) error
	// MoveSession reassigns a session to a different project (or
	// detaches it when projectID is ""). Used by the sidebar
	// drag-and-drop affordance. Workspace file migration is the
	// caller's responsibility — this only flips sessions.project_id.
	MoveSession(ctx context.Context, userID, agentID, sessionKey, projectID string) error
	// ResolveActiveSessionKey returns the most recently updated session_key
	// for the (channel, accountID, chatID) triple, or ErrNotFound. Used by
	// IM routing to pick the conversation thread an inbound message
	// belongs to without forcing the channel adapter to track session IDs.
	ResolveActiveSessionKey(ctx context.Context, userID, agentID, channel, accountID, chatID string) (string, error)
	// LookupSessionTriple returns the (channel, accountID, chatID) for a
	// known session_key — the inverse of ResolveActiveSessionKey. Web
	// chat handlers use it to recover the chat_id when the URL only
	// carries the session_key, so workspace artifacts stay namespaced
	// under the original conversation rather than re-keyed by session.
	LookupSessionTriple(ctx context.Context, userID, agentID, sessionKey string) (channel, accountID, chatID string, err error)
	// LookupSessionProject returns the project_id of a session_key, or
	// "" if the session is loose (no project). Used by the workspace
	// path resolver to pick projects/<id>/ over sessions/<chat>/ when
	// mounting the sandbox.
	LookupSessionProject(ctx context.Context, userID, agentID, sessionKey string) (string, error)

	// --- Projects (per user, per agent — workspace folder grouping) ---
	//
	// A project is just (name, description) plus a stable id; the
	// workspace dir is derived from id. Sessions opt in by setting
	// project_id at create time; existing rows can be moved later by
	// updating sessions.project_id (file migration is the caller's
	// problem). DeleteProject blocks when any session still references
	// the row — callers either delete the chats first or use a soft
	// detach (clearing project_id back to '').
	ListProjects(ctx context.Context, userID, agentID string) ([]ProjectRecord, error)
	GetProject(ctx context.Context, userID, agentID, projectID string) (*ProjectRecord, error)
	SaveProject(ctx context.Context, p *ProjectRecord) error
	DeleteProject(ctx context.Context, userID, agentID, projectID string) error
	CountProjectSessions(ctx context.Context, userID, agentID, projectID string) (int, error)

	// --- Project runtimes (the live-app layer on top of a project) ---
	//
	// At most one row per (user, agent, project). Get returns
	// ErrNotFound when a project has no runtime yet. Save upserts.
	// ListAllProjectRuntimes is for the idle sweeper, which needs to
	// enumerate every live runtime regardless of owner to evict stale
	// containers — it is NOT user-scoped on purpose.
	GetProjectRuntime(ctx context.Context, userID, agentID, projectID string) (*ProjectRuntimeRecord, error)
	SaveProjectRuntime(ctx context.Context, r *ProjectRuntimeRecord) error
	DeleteProjectRuntime(ctx context.Context, userID, agentID, projectID string) error
	ListAllProjectRuntimes(ctx context.Context) ([]ProjectRuntimeRecord, error)

	// --- Session messages (append-only per-turn archive) ---
	//
	// Mirrors every Append into session_messages, separate from the
	// sessions.messages JSONB working set. AppendSessionMessage assigns
	// the next seq atomically inside one INSERT (COALESCE(MAX(seq),-1)+1)
	// so callers don't pass a seq. ListSessionMessages returns all rows
	// for one session in ascending seq order — that's the full history,
	// untouched by compaction. DeleteSession cascades to clean these up.
	AppendSessionMessage(ctx context.Context, userID, agentID, sessionKey string, msg SessionMessage) error
	ListSessionMessages(ctx context.Context, userID, agentID, sessionKey string) ([]SessionMessage, error)
	// CountChatterUserMessages returns how many role='user' rows this
	// chatter has accumulated under the agent — across all sessions,
	// all channels. Used by the autoPersist gate as a *durable* "every
	// N user turns" counter that survives daemon restart and UserSpace
	// invalidation (the previous in-memory `turnCount` reset on both).
	// Counts only rows where chatter_user_id matches; legacy rows where
	// the column is empty are skipped — those predate per-chatter
	// resolution and conflating them with the new chatter would
	// over-count.
	CountChatterUserMessages(ctx context.Context, agentID, chatterUserID string) (int, error)

	// --- Chat events (in-flight streaming deltas, persisted for resume) ---
	//
	// Every event the agent emits during a turn (content chunk,
	// tool_call, error, done) lands here with a per-session
	// auto-incremented seq. Clients that disconnect mid-turn (refresh,
	// network blip, mobile app backgrounded) reconnect with their
	// last-seen seq and receive the missed delta — without this the
	// agent's reply becomes invisible until the parent session row is
	// next loaded. Cleared by DeleteSession alongside session_messages.
	AppendSessionEvent(ctx context.Context, userID, agentID, sessionKey, eventType string, data []byte) (int64, error)
	ListSessionEventsSince(ctx context.Context, userID, agentID, sessionKey string, sinceSeq int64) ([]SessionEventRecord, error)
	LatestSessionEventSeq(ctx context.Context, userID, agentID, sessionKey string) (int64, error)

	// --- Agent files ---
	//
	// SOUL.md, IDENTITY.md, MEMORY.md, AGENTS.md, BOOTSTRAP.md, etc.
	// Layered: user_id="" is the shared template (edited via the admin
	// Customize page), user_id=u_xxx is that user's personal override.
	// Read picks user-specific over template via fallback; write hits
	// the (agentID, userID, filename) row exactly.
	// GetAgentFile prefers the caller's own row, falling back to the
	// agent owner's row. Use GetAgentFileExact for a strict (agent,
	// user, filename) lookup that bypasses the overlay.
	GetAgentFile(ctx context.Context, agentID, userID, filename string) ([]byte, error)
	GetAgentFileExact(ctx context.Context, agentID, userID, filename string) ([]byte, error)
	SaveAgentFile(ctx context.Context, agentID, userID, filename string, data []byte) error
	DeleteAgentFile(ctx context.Context, agentID, userID, filename string) error
	ListAgentFiles(ctx context.Context, agentID, userID string) ([]string, error)

	// --- Configs (providers / channels / settings live here) ---
	//
	// One table backs all three concept families. Each row is keyed by
	// (kind, user_id, agent_id, name) and carries a JSON `data` payload.
	//
	//   kind="provider": LLM provider (name = provider key, e.g. "openai")
	//   kind="channel":  channel adapter (name = channel type, e.g. "telegram")
	//   kind="setting":  config namespace (name = "agents.defaults", "sandbox", …)
	//
	// `credential_key` is only populated for kind="channel" — it's the
	// stable lookup key the inbound dispatcher uses to find the row when a
	// message arrives. `enabled` lets a row hide an outer-scope row in the
	// merge (used by channels: an inner-scope disabled row erases the
	// outer entry).
	//
	// ListConfigs(kind, userID, agentID) returns rows that match BOTH ids
	// exactly. Pass empty for either to filter the corresponding ownership
	// dimension. Pass both empty to get only system/global rows.
	ListConfigs(ctx context.Context, kind, userID, agentID string) ([]ConfigRecord, error)
	// ListConfigsByUser returns every row of a given kind owned by userID
	// regardless of agent_id. The UserSpace assembly uses this to surface
	// channel rows where the caller is the binder on a foreign agent —
	// rows that ListConfigs(kind, userID, ownedAgentID) misses because
	// the loop only visits agents the user owns. Pass userID="" to get
	// system-scope rows (equivalent to ListConfigs(kind, "", "")).
	ListConfigsByUser(ctx context.Context, kind, userID string) ([]ConfigRecord, error)
	// QueryAllConfigs returns every row of a given kind regardless of
	// ownership. Used by the gateway boot path to register every
	// channel adapter on disk and by admin tooling that lists all
	// rows of a kind across users/agents.
	QueryAllConfigs(ctx context.Context, kind string) ([]ConfigRecord, error)
	GetConfig(ctx context.Context, id string) (*ConfigRecord, error)
	GetConfigByName(ctx context.Context, kind, userID, agentID, name string) (*ConfigRecord, error)
	SaveConfig(ctx context.Context, c *ConfigRecord) error
	DeleteConfig(ctx context.Context, id string) error
	LookupChannelByCredential(ctx context.Context, channelType, credKey string) (*ConfigRecord, error)

	// --- Cron jobs (per agent) ---
	//
	// Cron rows are owned by an agent; the executing identity is the
	// agent's user_id. List by ownerUserID joins against agents.
	ListCronJobsByOwner(ctx context.Context, ownerUserID string) ([]CronJobRecord, error)
	ListCronJobsByAgent(ctx context.Context, agentID string) ([]CronJobRecord, error)
	GetCronJob(ctx context.Context, jobID string) (*CronJobRecord, error)
	SaveCronJob(ctx context.Context, job *CronJobRecord) error
	DeleteCronJob(ctx context.Context, jobID string) error
	GetDueCronJobs(ctx context.Context, now time.Time) ([]CronJobRecord, error)
	LockCronJob(ctx context.Context, jobID, instanceID string) (bool, error)
	UpdateCronJobRun(ctx context.Context, jobID string, lastRun, nextRun time.Time) error
	// IncrementCronJobFailure atomically bumps failure_count and returns
	// the new count. Used by the scheduler when a tick can't deliver to
	// the configured channel; the caller decides whether to delete the
	// row at threshold.
	IncrementCronJobFailure(ctx context.Context, jobID string) (int, error)
	// GetNextDueTime returns the earliest next_run across all enabled
	// cron jobs. Used by the scheduler to sleep precisely until the
	// next job is due instead of polling.
	GetNextDueTime(ctx context.Context) (time.Time, error)

	// --- Channel leases (singleton gate for polling channels) ---
	//
	// Cross-process leader election for one (channel, account_id) pair.
	// AcquireChannelLease returns true on either fresh acquisition,
	// renewal-via-acquire, or steal-after-expiry. RenewChannelLease
	// returns false (NOT an error) when the lease was lost — callers
	// must stop the underlying poller immediately to avoid duplicate
	// inbound delivery. ReleaseChannelLease deletes the row so a peer
	// can take over without waiting for TTL.
	AcquireChannelLease(ctx context.Context, channel, accountID, holderID string, ttl time.Duration) (bool, error)
	RenewChannelLease(ctx context.Context, channel, accountID, holderID string, ttl time.Duration) (bool, error)
	ReleaseChannelLease(ctx context.Context, channel, accountID, holderID string) error

	// --- Goals (per agent × session) ---
	//
	// At most one row per (agent_id, session_key); enforced by a
	// UNIQUE index. CreateGoal returns ErrGoalAlreadyExists when the
	// pair is taken; callers must DeleteGoal first to start a new one.
	CreateGoal(ctx context.Context, g *GoalRecord) error
	GetGoalBySession(ctx context.Context, agentID, sessionKey string) (*GoalRecord, error)
	// UpdateGoal writes mutable fields back. Caller-immutable fields
	// (ID, AgentID, SessionKey, OwnerUserID, Objective, CreatedAt) are
	// ignored.
	UpdateGoal(ctx context.Context, g *GoalRecord) error
	DeleteGoal(ctx context.Context, goalID string) error

	Close() error
}

// ErrGoalAlreadyExists is returned by CreateGoal when the
// (agent_id, session_key) UNIQUE constraint trips. Callers translate
// this to a user-visible "clear the existing goal first" error.
var ErrGoalAlreadyExists = errors.New("goal already exists for this session")

// UserRecord is one row of the users table.
//
// Roles: "super_admin" | "user" are first-party humans who log in via
// password / token. "app_user" is provisioned by an api_key on behalf of
// a downstream application; for these rows APIKeyID identifies the key
// that minted them and ExternalID is the calling app's own user
// identifier (free-form). Together they give each external end-user a
// stable fastclaw user_id without anyone logging in.
type UserRecord struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	DisplayName  string `json:"displayName,omitempty"`
	Role         string `json:"role"`   // "super_admin" | "user" | "app_user"
	Status       string `json:"status"` // "active" | "disabled"
	APIKeyID     string `json:"apikeyId,omitempty"`
	ExternalID   string `json:"externalId,omitempty"`
	// AvatarURL is a self-contained data: URL ("data:image/png;base64,...")
	// stored inline to avoid a separate blob path. Cap is enforced by the
	// handler at write time (256KB by default). Empty means "no avatar"
	// — UI falls back to initials.
	AvatarURL string `json:"avatarUrl,omitempty"`
	// AgentQuota caps how many agents this user may self-create via
	// POST /api/agents. Semantics:
	//   -1 (default) — unlimited
	//    0          — self-creation forbidden (e.g. single-tenant
	//                 customers whose agent is provisioned by admin)
	//    N > 0      — at most N owned agents at once
	// Admin provisioning paths (POST /api/admin/users/{id}/agents)
	// bypass this — quota only governs caller-initiated creation.
	AgentQuota int64     `json:"agentQuota"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// WebSessionRecord backs cookie-based login state.
type WebSessionRecord struct {
	SID       string    `json:"sid"`
	UserID    string    `json:"userId"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// APIKeyRecord is one row of the apikeys table. KeyHash is SHA256(token);
// the plaintext is shown to the caller exactly once at create/rotate.
//
// Type is the key's authority tier:
//   - "admin": full platform — issues users, manages providers/models/skills
//   - "user":  the apikey owner's own resources — can create agents,
//     access every agent owned by the apikey owner (resolved at auth time)
//   - "agent": locked to the explicit list in apikey_agents — cannot
//     create agents
type APIKeyRecord struct {
	ID        string    `json:"id"`
	UserID    string    `json:"userId"`
	Name      string    `json:"name,omitempty"`
	KeyHash   string    `json:"-"`
	KeyPrefix string    `json:"keyPrefix,omitempty"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"createdAt"`
}

// AgentRecord is the persisted state for one agent. agents.id is globally
// unique; UserID is who owns the agent. The agent itself is the atomic
// unit — sessions, cron jobs, and apikey ACLs all reference agents.id
// directly, never (user_id, agent_id).
// Per-agent model overrides used to live in agents.model; they now live
// in configs as kind=setting, scope=agent, scope_id=<aid>, name=
// "agents.defaults", which is the same path system + user defaults take.
// Resolution happens in loadUserSpace via scope.SettingInto.
// IsPublic flips the "anyone with the link can chat" gate. Default
// false (private — owner-only). When true, requireAgentReadable +
// resolveAgent let any authenticated session lazy-attach the agent
// into their own UserSpace; sessions/memory/agent_files still
// partition per chatter, so only the agent identity is shared.
type AgentRecord struct {
	ID        string                 `json:"id"`
	UserID    string                 `json:"userId"`
	Name      string                 `json:"name"`
	Config    map[string]interface{} `json:"config,omitempty"`
	IsPublic  bool                   `json:"isPublic"`
	CreatedAt time.Time              `json:"createdAt"`
	UpdatedAt time.Time              `json:"updatedAt"`
}

// SessionRecord holds a conversation session.
//
// Channel / AccountID / ChatID identify the upstream conversation this
// session belongs to (e.g. ("wechat", "<bot account id>", "<openid>") or
// ("web", "", "<frontend session id>")). These are persisted once on
// INSERT and never overwritten by an UPDATE — a session's home doesn't
// move once it's created. Multiple session rows can share the same
// triple; the active one for IM routing is resolved by max(updated_at).
type SessionRecord struct {
	Channel   string `json:"channel,omitempty"`
	AccountID string `json:"accountId,omitempty"`
	ChatID    string `json:"chatId,omitempty"`
	// ProjectID groups sessions sharing a workspace folder; empty =
	// loose chat (each session gets its own per-chat sandbox dir).
	// Like the channel triple it's persisted on INSERT only and
	// preserved on UPDATE.
	ProjectID string           `json:"projectId,omitempty"`
	Messages  []SessionMessage `json:"messages"`
	UpdatedAt time.Time        `json:"updatedAt"`
}

// SessionMessage is a single message in a session.
type SessionMessage struct {
	Role         string                 `json:"role"`
	Content      string                 `json:"content"`
	ContentParts interface{}            `json:"contentParts,omitempty"`
	ToolCalls    interface{}            `json:"toolCalls,omitempty"`
	ToolCallID   string                 `json:"toolCallId,omitempty"`
	Name         string                 `json:"name,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	Timestamp    time.Time              `json:"timestamp"`
	Thinking     string                 `json:"thinking,omitempty"`
	RawAssistant json.RawMessage        `json:"rawAssistant,omitempty"`
	// Origin mirrors provider.Message.Origin — empty for genuine user
	// / assistant messages, non-empty for runtime-injected ones
	// (currently only "goal_context"). Stored as a column on
	// session_messages (see migrateSessionMessagesAddOrigin).
	Origin string `json:"origin,omitempty"`
}

// SessionEventRecord is one row of session_events — a single delta the
// agent emitted during a turn. Data is opaque JSON whose shape depends
// on Type ("content", "tool_call", "error", "done", ...).
type SessionEventRecord struct {
	UserID     string    `json:"userId,omitempty"`
	AgentID    string    `json:"agentId,omitempty"`
	SessionKey string    `json:"sessionKey,omitempty"`
	Seq        int64     `json:"seq"`
	Type       string    `json:"type"`
	Data       []byte    `json:"data,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// SessionOwnerPair is one (user_id, agent_id) tuple returned by
// ListSessionOwnerPairs — represents "this user has at least one
// session with this agent." The admin Chats view fans out per pair to
// pull each chatter's session list, so non-owner conversations on
// public agents (where session.user_id ≠ agent.user_id) get surfaced.
type SessionOwnerPair struct {
	UserID  string `json:"userId"`
	AgentID string `json:"agentId"`
}

// SessionMeta is summary info for a session (for listing).
type SessionMeta struct {
	Key          string    `json:"key"`
	Channel      string    `json:"channel,omitempty"`
	AccountID    string    `json:"accountId,omitempty"`
	ChatID       string    `json:"chatId,omitempty"`
	ProjectID    string    `json:"projectId,omitempty"`
	Title        string    `json:"title,omitempty"`
	MessageCount int       `json:"messageCount"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// ProjectRecord is a per-(user, agent) named workspace folder. Sessions
// reference a project via sessions.project_id; every session in the
// same project mounts workspaces/<agent>/projects/<id>/ as its sandbox
// /workspace, so files are shared across the project's chats.
//
// A project is private to its creator (the user_id, agent_id pair),
// matching SessionRecord's ownership model — different users sharing
// the same agent each have their own projects, never seeing each
// other's.
type ProjectRecord struct {
	UserID      string    `json:"-"`
	AgentID     string    `json:"-"`
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ProjectRuntimeRecord is the live-app layer that sits ON TOP of a
// ProjectRecord — at most one per (user, agent, project). The
// ProjectRecord owns the source tree (the shared workspace folder);
// this record owns the *running instance* of that source: a long-lived
// sandbox container, a dev server, and a preview URL. The two are
// deliberately separate tables so the existing project feature (chat
// grouping + shared files) keeps its exact semantics and the coding-
// agent runtime is purely additive.
//
// Lifecycle of Status:
//
//	none        — record exists but nothing is provisioned yet
//	scaffolding — template is being copied into the workspace
//	starting    — sandbox is up, dev server is booting
//	running     — dev server is serving; PreviewURL is live
//	sleeping    — container evicted to save compute; Wake re-creates it
//	crashed     — dev server exited non-zero; LastError has the detail
type ProjectRuntimeRecord struct {
	UserID      string `json:"-"`
	AgentID     string `json:"-"`
	ProjectID   string `json:"projectId"`
	TemplateRef string `json:"templateRef,omitempty"`
	Status      string `json:"status"`
	// DevPort is the container-internal port the dev server listens on
	// (e.g. 3000 for ShipAny). HostPort is the host-published port the
	// preview gateway reverse-proxies to; 0 means not currently
	// published (sleeping / never started). PreviewURL is the
	// user-facing URL the gateway resolves to HostPort.
	DevPort    int    `json:"devPort,omitempty"`
	HostPort   int    `json:"hostPort,omitempty"`
	PreviewURL string `json:"previewUrl,omitempty"`
	// ContainerID is the long-lived sandbox container backing this
	// runtime. Empty when sleeping/none. Stored so a process restart can
	// re-adopt or clean up orphaned containers.
	ContainerID string `json:"-"`
	// GitRef is the commit the agent snapshotted after the last turn, so
	// Revert can roll the workspace back a step.
	GitRef    string    `json:"gitRef,omitempty"`
	LastError string    `json:"lastError,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Kinds for ConfigRecord.
const (
	KindProvider = "provider"
	KindChannel  = "channel"
	KindSetting  = "setting"
	KindMCP      = "mcp"
)

// ConfigRecord is one row of the configs table — the unified
// home for providers, channels, and namespaced settings.
//
//   - kind says which family this row belongs to
//   - (user_id, agent_id) says who owns it; the empty-string defaults
//     give us four natural ownership levels:
//     (”, ”)   = system / global
//     (X, ”)    = user X's private config
//     (”, Y)    = agent Y's "official" config (anyone using Y inherits)
//     (X, Y)     = user X's per-agent override on agent Y (multi-tenant)
//   - name is the lookup handle inside that family (provider key,
//     channel type, or setting namespace)
//   - data is the family-specific JSON payload
//
// CredentialKey is only meaningful for kind="channel" — see
// LookupChannelByCredential.
type ConfigRecord struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Scope is a denormalized label derived from (UserID, AgentID).
	// "system" / "user" / "agent" / "user-agent". The storage layer is
	// the single writer — SaveConfig always recomputes and overwrites
	// whatever the caller passed, so the column can't drift out of
	// sync with the (user_id, agent_id) source of truth. Kept so DB
	// dumps and ad-hoc queries (`WHERE scope='system'`) stay readable
	// without parsing the empty/non-empty pattern of the id columns.
	Scope         string                 `json:"scope,omitempty"`
	UserID        string                 `json:"userId,omitempty"`
	AgentID       string                 `json:"agentId,omitempty"`
	Name          string                 `json:"name"`
	Enabled       bool                   `json:"enabled"`
	CredentialKey string                 `json:"credentialKey,omitempty"`
	Data          map[string]interface{} `json:"data,omitempty"`
	CreatedAt     time.Time              `json:"createdAt"`
	UpdatedAt     time.Time              `json:"updatedAt"`
}

// computeConfigScope derives the scope label from the (userID, agentID)
// ownership pair. Single source of truth for the Scope column —
// SaveConfig calls this before every write, so any divergence between
// the columns and the label means a code path that bypassed SaveConfig
// (which shouldn't exist outside of test-only ad-hoc INSERTs).
func computeConfigScope(userID, agentID string) string {
	switch {
	case userID != "" && agentID != "":
		return "user-agent"
	case userID != "":
		return "user"
	case agentID != "":
		return "agent"
	default:
		return "system"
	}
}

// LegacyScope returns the scope label suitable for the HTTP-layer
// (scope, scopeId) JSON shape. Reads the persisted column when set;
// falls back to recomputing for rows that pre-date the column-add
// migration / are constructed in tests via raw INSERT.
func (r ConfigRecord) LegacyScope() string {
	if r.Scope != "" {
		return r.Scope
	}
	return computeConfigScope(r.UserID, r.AgentID)
}

// LegacyScopeID is the scopeID half of LegacyScope. For per-(user,
// agent) rows it returns "user_id/agent_id" so the JSON consumer has
// enough to round-trip.
func (r ConfigRecord) LegacyScopeID() string {
	switch {
	case r.UserID != "" && r.AgentID != "":
		return r.UserID + "/" + r.AgentID
	case r.UserID != "":
		return r.UserID
	case r.AgentID != "":
		return r.AgentID
	default:
		return ""
	}
}

// GoalRecord is the persisted shape of a /goal target. One per
// (agent, session) — UNIQUE index enforces it. See
// internal/agent/goal for the domain type and rationale.
type GoalRecord struct {
	ID          string `json:"id"`
	AgentID     string `json:"agentId"`
	SessionKey  string `json:"sessionKey"`
	OwnerUserID string `json:"ownerUserId"`

	// Routing tuple — see goal.Goal for the rationale. Continuations
	// publish onto this address so the prompt lands in the right chat.
	Channel   string `json:"channel,omitempty"`
	AccountID string `json:"accountId,omitempty"`
	ChatID    string `json:"chatId,omitempty"`
	ProjectID string `json:"projectId,omitempty"`

	Objective string `json:"objective"`
	Status    string `json:"status"` // active | paused | budget_limited | complete

	// TokenBudget is nil for unbounded goals. Stored as a nullable
	// BIGINT column.
	TokenBudget *int64 `json:"tokenBudget,omitempty"`
	TokensUsed  int64  `json:"tokensUsed"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CronJobRecord holds a scheduled job. agent_id is mandatory; user_id is
// also stored so "list a user's crons" doesn't need a join against
// agents and ownership checks can short-circuit.
type CronJobRecord struct {
	ID        string     `json:"id"`
	UserID    string     `json:"userId,omitempty"`
	AgentID   string     `json:"agentId"`
	Name      string     `json:"name"`
	Type      string     `json:"type"`
	Schedule  string     `json:"schedule"`
	Message   string     `json:"message"`
	Channel   string     `json:"channel"`
	ChatID    string     `json:"chatId"`
	AccountID string     `json:"accountId"`
	Timezone  string     `json:"timezone"`
	Enabled   bool       `json:"enabled"`
	LastRun   *time.Time `json:"lastRun,omitempty"`
	NextRun   *time.Time `json:"nextRun,omitempty"`
	// FailureCount is the number of consecutive fire-attempts whose
	// destination channel was missing/unreachable. UpdateCronJobRun
	// resets it to 0; IncrementCronJobFailure bumps it. The scheduler
	// deletes the row once it crosses an internal threshold.
	FailureCount int       `json:"failureCount,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

// StorageType identifies the storage backend.
type StorageType string

const (
	StoragePostgres StorageType = "postgres"
	StorageSQLite   StorageType = "sqlite"
)

// StorageConfig holds DB credentials. Populated from FASTCLAW_STORAGE_* env vars at boot.
type StorageConfig struct {
	Type        StorageType `json:"type"`
	DSN         string      `json:"dsn,omitempty"`
	AutoMigrate bool        `json:"autoMigrate,omitempty"`
}
