package agent

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/session"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/usage"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// providerForAgent picks an LLM provider for a single agent. Resolution:
//
//  1. Parse `rc.Model` as "<providerKey>/<modelId>".
//  2. Look up `rc.Providers[providerKey]`. `Providers` is the merged view
//     (global ← agent.json), so agent-exclusive providers shadow global
//     ones with the same key.
//  3. Fall back to the shared provider (the one the Manager/UserSpace
//     picked from global defaults) so old deployments without per-agent
//     providers keep working.
//
// This is what makes per-agent credentials real at runtime — each agent
// builds its own provider.Provider from its own API key+base, not the
// user-space-wide one.
func providerForAgent(rc config.ResolvedAgent, shared provider.Provider) provider.Provider {
	if p := providerForModel(rc, rc.Model); p != nil {
		return p
	}
	return shared
}

// providerForModel resolves one "<providerKey>/<modelId>" string against
// the agent's merged provider map (steps 1–2 of providerForAgent).
// Returns nil when the model has no provider prefix, the prefix isn't in
// rc.Providers, or the entry has no API key — callers pick the fallback
// behavior (shared provider for the primary model, skip-with-warning for
// fallback models).
func providerForModel(rc config.ResolvedAgent, model string) provider.Provider {
	parts := strings.SplitN(model, "/", 2)
	if len(parts) == 2 {
		if pc, ok := rc.Providers[parts[0]]; ok && pc.APIKey != "" {
			return provider.NewProvider(pc.APIKey, pc.APIBase, pc.APIType)
		}
	}
	return nil
}

// fallbackClient pairs a fallback model with the provider instance built
// to serve it. Resolved once at agent construction — same lifecycle as
// the primary provider — so chatStreamWithFallback never touches config
// on the hot path.
type fallbackClient struct {
	model string
	prov  provider.Provider
}

// fallbacksForAgent resolves rc.ModelFallbacks into ready-to-call
// clients. Unresolvable entries are skipped with a warning instead of
// failing agent construction: a typo'd fallback shouldn't take down an
// agent whose primary model works. Unlike providerForAgent there is no
// shared-provider fallback here — firing a fallback model id at
// whatever base URL the shared provider points at would turn every
// failover into a confusing 400 from the wrong vendor.
func fallbacksForAgent(rc config.ResolvedAgent) []fallbackClient {
	if len(rc.ModelFallbacks) == 0 {
		return nil
	}
	out := make([]fallbackClient, 0, len(rc.ModelFallbacks))
	for _, m := range rc.ModelFallbacks {
		if m == rc.Model {
			slog.Warn("model fallback skipped", "agent", rc.ID, "model", m, "reason", "same as primary model")
			continue
		}
		prov := providerForModel(rc, m)
		if prov == nil {
			slog.Warn("model fallback skipped", "agent", rc.ID, "model", m, "reason", "no provider with an API key for prefix")
			continue
		}
		out = append(out, fallbackClient{model: m, prov: prov})
	}
	return out
}

// ManagerOption configures optional Manager behavior.
type ManagerOption func(*managerOpts)

type managerOpts struct {
	sessionStore    session.SessionStore
	memoryStore     MemoryStore
	workspaceStore  workspace.Store
	dataStore       store.Store
	meter           usage.Meter
	quotaStore      usage.QuotaStore
	userID          string
	globalSkillsCfg config.SkillsCfg
}

func WithSessionStore(st session.SessionStore) ManagerOption {
	return func(o *managerOpts) { o.sessionStore = st }
}

func WithMemoryStore(st MemoryStore) ManagerOption {
	return func(o *managerOpts) { o.memoryStore = st }
}

// WithUserID tags every agent the Manager loads with the owning user, so
// store-backed Memory + Session calls scope rows by user_id. UserSpace
// passes the resolved user; local-mode gateway uses config.DefaultUserID.
func WithUserID(userID string) ManagerOption {
	return func(o *managerOpts) { o.userID = userID }
}

// WithWorkspaceStore installs a durable blob store on every agent's tool
// registry so file operations (write_file / read_file / list_dir) land in
// shared storage instead of pod-local filesystem.
func WithWorkspaceStore(ws workspace.Store) ManagerOption {
	return func(o *managerOpts) { o.workspaceStore = ws }
}

// WithDataStore exposes the platform's relational store to agents. The
// cron tool needs it to persist scheduled jobs that the cron.Scheduler
// later picks up; without it create_cron_job is omitted from the
// agent's tool list and time-bound requests fall back to natural-
// language reminders in HEARTBEAT.md (which only get a lazy 30-minute
// review and are wrong for short-fuse reminders).
func WithDataStore(st store.Store) ManagerOption {
	return func(o *managerOpts) { o.dataStore = st }
}

// WithMeter installs the admin-level token meter on every agent so each
// provider.Chat / ChatStream call records into token_usage_daily. Omit
// to disable metering (tests, single-user dev runs).
func WithMeter(m usage.Meter) ManagerOption {
	return func(o *managerOpts) { o.meter = m }
}

// WithQuotaStore installs per-user billing quota enforcement on every
// agent. The agent loop checks the owner's quota before processing a
// turn — when exceeded, the user gets a friendly rejection and no LLM
// tokens are burned. Omit to disable quota enforcement.
func WithQuotaStore(qs usage.QuotaStore) ManagerOption {
	return func(o *managerOpts) { o.quotaStore = qs }
}

// WithGlobalSkillsCfg propagates cfg.Skills (entries + agentEntries
// holding skill apiKey/env per skill or per (agent,skill)) into agents
// the manager constructs. Without this, buildAgent → NewAgent passes a
// zero-value SkillsCfg and SkillsLoader.SkillEnvVars sees empty
// entries — every skill runs without its configured FAL_KEY /
// REPLICATE_API_TOKEN regardless of what's saved in the DB.
func WithGlobalSkillsCfg(cfg config.SkillsCfg) ManagerOption {
	return func(o *managerOpts) { o.globalSkillsCfg = cfg }
}

// Manager loads and manages all agent instances.
type Manager struct {
	agents       map[string]*Agent
	defaultAgent *Agent
	// opts is retained so AddAgent (hot-reload after onboard / agent
	// create) can apply the same store wiring the constructor did.
	// Without this the freshly-added agent's tool registry never gets
	// SetSystemFileStore, so read_file falls through to host FS and
	// 404s on identity files (SOUL/IDENTITY/...) that live only in DB.
	opts managerOpts
	uid  string
}

// NewManager creates agents from resolved configs.
func NewManager(resolved []config.ResolvedAgent, prov provider.Provider, mb *bus.MessageBus, opts ...ManagerOption) (*Manager, error) {
	m := &Manager{
		agents: make(map[string]*Agent),
	}
	for _, o := range opts {
		o(&m.opts)
	}

	if _, err := config.HomeDir(); err != nil {
		return nil, err
	}

	m.uid = m.opts.userID
	if m.uid == "" {
		return nil, fmt.Errorf("agent.NewManager: WithUserID is required")
	}
	for _, rc := range resolved {
		ag := m.buildAgent(rc, prov, mb)
		m.agents[rc.ID] = ag

		slog.Info("loaded agent",
			"id", rc.ID,
			"model", rc.Model,
			"home", rc.Home,
			"workspace", rc.Workspace,
		)
	}

	// If only one agent, make it the default
	if len(m.agents) == 1 {
		for _, ag := range m.agents {
			m.defaultAgent = ag
		}
	}

	return m, nil
}

// buildAgent constructs an Agent and wires every store the Manager
// was configured with. Shared between NewManager's bootstrap loop and
// AddAgent's hot-reload path so a freshly-onboarded agent picks up the
// same DB-backed identity / memory / workspace plumbing.
func (m *Manager) buildAgent(rc config.ResolvedAgent, prov provider.Provider, mb *bus.MessageBus) *Agent {
	homeDir, _ := config.HomeDir()
	// Pass the global SkillsCfg through so SkillsLoader sees the
	// admin-UI-configured per-skill apiKey + env (and the per-agent
	// override map). Plain NewAgent constructs the loader with a
	// zero-value SkillsCfg, which is why FAL_KEY / REPLICATE_API_TOKEN
	// were never reaching the sandbox.
	ag := NewAgentWithSkillsCfg(rc, providerForAgent(rc, prov), mb, homeDir, m.opts.globalSkillsCfg)
	ag.SetOwnerUserID(m.uid)
	// Per-user skills bucket: chat-time `skills/...` writes route to
	// ~/.fastclaw/users/<uid>/, where SkillsLoader's "personal" layer
	// also scans (see SkillsLoader.WithUserID). Set userID on the
	// registry up front (the systemFileStore branch below also sets
	// it, but only when memoryStore is wired — without this hoist a
	// non-cloud install would store-mirror skills under agentID
	// instead of the per-user owner key, splitting the same skill's
	// content between two store namespaces). Skipped on legacy /
	// single-user installs where m.uid is empty — file.go falls back
	// to systemRoot (agent home) so existing skill bundles still work.
	if m.uid != "" {
		ag.registry.SetOwnerUserID(m.uid)
		if base := userSkillsRootDir(m.uid); base != "" {
			ag.registry.SetUserSkillsRoot(base)
		}
	}
	if m.opts.sessionStore != nil {
		ag.sessions = session.NewManagerWithStoreForUser(rc.Home+"/sessions", m.opts.sessionStore, m.uid, rc.ID)
	}
	if m.opts.memoryStore != nil {
		ag.memory = NewMemoryWithStoreForUser(rc.Home, m.opts.memoryStore, m.uid, rc.ID)
		ag.ctxBuilder.store = m.opts.memoryStore
		ag.ctxBuilder.agentID = rc.ID
		ag.ctxBuilder.userID = m.uid
		ag.memoryStore = m.opts.memoryStore
		// Identity files (SOUL/IDENTITY/USER/...) share the same DB
		// store as memory so write_file from the agent ends up in
		// the same rows the admin UI's Customize page reads.
		ag.registry.SetSystemFileStore(m.opts.memoryStore, rc.ID)
		// Tag the chatter (m.uid) for per-user files (USER.md /
		// MEMORY.md) and the agent's owner (rc.UserID) for identity
		// files (SOUL.md / IDENTITY.md / BOOTSTRAP.md / ...). Without
		// the second call, the agent's BOOTSTRAP flow would write
		// SOUL/IDENTITY/BOOTSTRAP under the chatter and the Customize
		// page (keyed on the agent owner) would never see them.
		ag.registry.SetOwnerUserID(m.uid)
		ag.registry.SetAgentOwnerUserID(rc.UserID)
	}
	if m.opts.workspaceStore != nil {
		ag.registry.SetWorkspaceStore(m.opts.workspaceStore, rc.ID)
		// Also make the store available to SkillsLoader so object-store
		// skills (global + per-agent) are hydrated on every turn. Without
		// this, pods that didn't handle the original upload will never
		// see a new skill.
		ag.workspaceStore = m.opts.workspaceStore
		ag.agentID = rc.ID
		// Refresh skills now that workspaceStore is wired — the initial
		// NewAgent pass loaded only the filesystem, missing anything that
		// lives only in OSS.
		ag.ReloadWorkspaceFiles()
	}
	if m.opts.dataStore != nil {
		// Cron tools need the relational store to persist scheduled
		// jobs; the closure also reads channel/chatID off the registry
		// at execute time (bindSession stamps them per-turn) so the
		// fired message routes back to the originating chat.
		tools.RegisterCronTools(ag.registry, m.opts.dataStore, m.uid, rc.ID)
		// set_timezone persists the chatter's IANA timezone into scope
		// prefs — the same rows the system-prompt date line and cron
		// scheduling resolve through. Needs the relational store, so it
		// rides the same guard as cron.
		tools.RegisterTimezoneTool(ag.registry, m.opts.dataStore)
		tools.RegisterPreferenceTool(ag.registry, m.opts.dataStore)
		// /goal feature: token-accounting hook + update_goal tool, all
		// keyed on the agent's owner (set above by SetOwnerUserID).
		// Same dataStore guard as cron because both features need the
		// relational store; agents without one degrade quietly.
		ag.WireGoals(m.opts.dataStore)
		// Stamp on Agent too so runtime checks (e.g. the autoPersist
		// cadence gate that counts session_messages instead of relying
		// on an in-memory counter that restart-clears) can hit the
		// store directly without re-plumbing through Manager.
		ag.dataStore = m.opts.dataStore
		// Date line in the chatter's timezone — needs dataStore for the
		// scope-prefs lookup, hence wired here and re-applied by
		// ReloadWorkspaceFiles after every ctxBuilder rebuild.
		ag.ctxBuilder.SetTimezoneResolver(ag.chatterLocation)
	}
	// Stamp agentID even when no workspaceStore is wired (single-user
	// local mode), so usage metering can record per-agent rollups.
	ag.agentID = rc.ID
	if m.opts.meter != nil {
		ag.SetMeter(m.opts.meter)
	}
	if m.opts.quotaStore != nil {
		ag.SetQuotaStore(m.opts.quotaStore)
	}
	return ag
}

// AddAgent creates and registers a new agent dynamically (for hot-reload).
func (m *Manager) AddAgent(rc config.ResolvedAgent, prov provider.Provider, mb *bus.MessageBus) error {
	if _, exists := m.agents[rc.ID]; exists {
		return fmt.Errorf("agent %q already exists", rc.ID)
	}
	m.agents[rc.ID] = m.buildAgent(rc, prov, mb)
	slog.Info("agent added dynamically", "id", rc.ID, "model", rc.Model)
	return nil
}

// AddAgentWithSkillsCfg is AddAgent + a one-shot skills cfg override that
// replaces m.opts.globalSkillsCfg for just this build. EnsureAgent (which
// injects a foreign agent into a different user's UserSpace) uses this so
// the SkillsLoader closure baked into the new agent picks up the agent's
// own agent-scope skill env (e.g. image-tool's REPLICATE_API_TOKEN) — the
// caller's UserSpace cfg doesn't carry it because the agent isn't owned
// by the caller.
//
// The override is local: m.opts.globalSkillsCfg is restored before
// returning so the next AddAgent on the same manager goes back to the
// caller's own cfg. Held under no extra lock — callers (UserSpace.
// EnsureAgent) already serialize via sp.mu.
func (m *Manager) AddAgentWithSkillsCfg(rc config.ResolvedAgent, prov provider.Provider, mb *bus.MessageBus, cfg config.SkillsCfg) error {
	if _, exists := m.agents[rc.ID]; exists {
		return fmt.Errorf("agent %q already exists", rc.ID)
	}
	prev := m.opts.globalSkillsCfg
	m.opts.globalSkillsCfg = cfg
	m.agents[rc.ID] = m.buildAgent(rc, prov, mb)
	m.opts.globalSkillsCfg = prev
	slog.Info("agent added dynamically with override skills cfg", "id", rc.ID, "model", rc.Model)
	return nil
}

// RemoveAgent unregisters an agent by ID. No-op if the agent is not loaded.
func (m *Manager) RemoveAgent(id string) {
	if _, ok := m.agents[id]; !ok {
		return
	}
	delete(m.agents, id)
	if m.defaultAgent != nil && m.defaultAgent.Name() == id {
		m.defaultAgent = nil
	}
	slog.Info("agent removed dynamically", "id", id)
}

// AgentByID returns an agent by its ID.
func (m *Manager) AgentByID(id string) *Agent {
	return m.agents[id]
}

// DefaultAgent returns the default agent (set when only one agent exists).
func (m *Manager) DefaultAgent() *Agent {
	return m.defaultAgent
}

// All returns all loaded agents.
func (m *Manager) All() []*Agent {
	result := make([]*Agent, 0, len(m.agents))
	for _, ag := range m.agents {
		result = append(result, ag)
	}
	return result
}

// Names returns all agent IDs.
func (m *Manager) Names() []string {
	names := make([]string, 0, len(m.agents))
	for name := range m.agents {
		names = append(names, name)
	}
	return names
}

// UpdateProvider replaces the LLM provider for all agents (hot-reload).
// Agents with their own per-agent provider override (agent.json providers
// shadowing the shared one) keep their dedicated provider — this call
// only affects agents that were using the shared instance.
func (m *Manager) UpdateProvider(prov provider.Provider) {
	for _, ag := range m.agents {
		ag.provider = prov
	}
}

// UpdateProviderResolved is like UpdateProvider but aware of per-agent
// provider overrides. For each agent it rebuilds the provider using the
// same rule NewManager applied at construction: agent-level `providers`
// in agent.json shadow the shared fallback.
func (m *Manager) UpdateProviderResolved(shared provider.Provider, resolved []config.ResolvedAgent) {
	byID := make(map[string]config.ResolvedAgent, len(resolved))
	for _, rc := range resolved {
		byID[rc.ID] = rc
	}
	for id, ag := range m.agents {
		if rc, ok := byID[id]; ok {
			ag.provider = providerForAgent(rc, shared)
		} else {
			ag.provider = shared
		}
	}
}
