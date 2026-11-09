package agent

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/agentcli"
	"github.com/fastclaw-ai/fastclaw/internal/billing"
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
	parts := strings.SplitN(rc.Model, "/", 2)
	if len(parts) == 2 {
		if pc, ok := rc.Providers[parts[0]]; ok && pc.APIKey != "" {
			return provider.NewProvider(pc.APIKey, pc.APIBase, pc.APIType)
		}
	}
	return shared
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
	billing         *billing.Service
	userID          string
	globalSkillsCfg config.SkillsCfg
	// toolAvailability answers "which tools would agent X have at
	// runtime". Only the gateway can say — tool registration depends on
	// per-agent provider credentials — so it is injected rather than
	// derived here. Nil makes check_agent report tool status as
	// unverified instead of guessing.
	toolAvailability func(ctx context.Context, agentID string) (map[string]bool, error)
	// agentResolver finds (loading on demand) another agent of the same
	// account for message_agent. Nil leaves the tool unregistered.
	agentResolver AgentResolver
	// agentLister lists the agents message_agent may reach, for the
	// per-turn roster. Nil: no roster.
	agentLister AgentLister
	// onAgentChanged is invoked after in-chat provisioning mutates an
	// agent, so a running gateway can reload rather than require a
	// restart.
	onAgentChanged func()
}

// WithAgentResolver supplies the lookup behind message_agent: an agent of
// the same account by id or display name, loaded if it was evicted.
func WithAgentResolver(fn AgentResolver) ManagerOption {
	return func(o *managerOpts) { o.agentResolver = fn }
}

// WithAgentLister supplies the roster behind message_agent: the agents a
// caller may reach (see AgentLister).
func WithAgentLister(fn AgentLister) ManagerOption {
	return func(o *managerOpts) { o.agentLister = fn }
}

// WithToolAvailability supplies the capability probe behind check_agent.
// Without it a freshly provisioned agent can be reported "configured"
// while being unable to do the one thing its skill exists for.
func WithToolAvailability(fn func(ctx context.Context, agentID string) (map[string]bool, error)) ManagerOption {
	return func(o *managerOpts) { o.toolAvailability = fn }
}

// WithAgentChangeHook registers a callback fired after in-chat agent
// provisioning, so the gateway can pick up the new/changed agent.
func WithAgentChangeHook(fn func()) ManagerOption {
	return func(o *managerOpts) { o.onAgentChanged = fn }
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

// WithBilling installs usage metering and billing authorization
// (docs/usage-metering.md) on every agent. Without it, agents with a data
// store meter into that store.
func WithBilling(svc *billing.Service) ManagerOption {
	return func(o *managerOpts) { o.billing = svc }
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
	// mu guards agents, defaultAgent and lastUsed. Agents are attached
	// on demand (UserSpace.EnsureAgent) while other goroutines look
	// them up, so every map access goes through it.
	mu           sync.RWMutex
	agents       map[string]*Agent
	defaultAgent *Agent
	// lastUsed records when each agent was last looked up by ID, so an
	// on-demand UserSpace can drop agents nobody has touched recently.
	lastUsed map[string]*atomic.Int64
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
		agents:   make(map[string]*Agent),
		lastUsed: make(map[string]*atomic.Int64),
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
		m.touchLocked(rc.ID)

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
	return m.buildAgentWithSkillsCfg(rc, prov, mb, m.opts.globalSkillsCfg)
}

// buildAgentWithSkillsCfg is buildAgent with an explicit skills cfg, so a
// one-off override never has to mutate the shared m.opts.
func (m *Manager) buildAgentWithSkillsCfg(rc config.ResolvedAgent, prov provider.Provider, mb *bus.MessageBus, skillsCfg config.SkillsCfg) *Agent {
	homeDir, _ := config.HomeDir()
	// Pass the global SkillsCfg through so SkillsLoader sees the
	// admin-UI-configured per-skill apiKey + env (and the per-agent
	// override map). Plain NewAgent constructs the loader with a
	// zero-value SkillsCfg, which is why FAL_KEY / REPLICATE_API_TOKEN
	// were never reaching the sandbox.
	ag := NewAgentWithSkillsCfg(rc, providerForAgent(rc, prov), mb, homeDir, skillsCfg)
	ag.SetOwnerUserID(m.uid)
	ag.SetAgentOwnerID(rc.UserID)
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
		// Owner-uploaded knowledge base: registered only when the store
		// can search it. The prompt's knowledge index section tells the
		// model when to call it (large corpora); small corpora are
		// injected in full and the tool goes unused.
		if searcher, ok := m.opts.memoryStore.(tools.KnowledgeSearcher); ok {
			tools.RegisterKnowledgeSearch(ag.registry, searcher)
		}
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

		// In-chat provisioning. Goes through the same Go API as the CLI
		// and the admin UI, so "set me up a new agent" behaves the same
		// whether or not a sandbox is configured — the previous path
		// (model composes `fastclaw ...`, exec runs it) only ever worked
		// when exec reached the operator's host.
		ownerID := rc.UserID
		if ownerID == "" {
			ownerID = m.uid
		}
		resolveTargetSkills := func(ctx context.Context, ref string) (string, error) {
			rec, err := agentcli.Resolve(ctx, m.opts.dataStore, ref)
			if err != nil {
				return "", err
			}
			if rec.UserID != ownerID {
				return "", fmt.Errorf("agent %q belongs to a different account — refusing to install into it", ref)
			}
			home, err := config.AgentHomeDir(rec.ID)
			if err != nil {
				return "", err
			}
			return filepath.Join(home, "skills"), nil
		}
		tools.RegisterAgentAdmin(ag.registry, tools.AgentAdminDeps{
			Store:            m.opts.dataStore,
			OwnerUserID:      ownerID,
			AgentHomeDir:     config.AgentHomeDir,
			ToolAvailability: m.opts.toolAvailability,
			OnChanged:        m.opts.onAgentChanged,
		})
		// Agent-to-agent private messages (message_agent), scoped to the
		// same owner account as provisioning.
		ag.registerMessageAgent(m.opts.agentResolver, m.opts.agentLister, ownerID)
		// install_skill / search_skills were written but never registered —
		// the tools existed as dead code, which is why provisioning had to
		// shell out to the CLI in the first place.
		tools.RegisterSkillInstallForTarget(
			ag.registry,
			filepath.Join(rc.Home, "skills"),
			resolveTargetSkills,
			func() { ag.refreshSkillsFromStore(m.uid) },
		)
	}
	// Stamp agentID even when no workspaceStore is wired (single-user
	// local mode), so usage metering can record per-agent rollups.
	ag.agentID = rc.ID
	if m.opts.meter != nil {
		ag.SetMeter(m.opts.meter)
		tools.RegisterBillingTools(ag.registry, m.opts.meter, m.opts.quotaStore)
	}
	if m.opts.quotaStore != nil {
		ag.SetQuotaStore(m.opts.quotaStore)
	}
	ag.billing = m.opts.billing
	if ag.billing == nil && m.opts.dataStore != nil {
		ag.billing = billing.NewService(m.opts.dataStore)
	}
	return ag
}

// AddAgent creates and registers a new agent dynamically (for hot-reload).
func (m *Manager) AddAgent(rc config.ResolvedAgent, prov provider.Provider, mb *bus.MessageBus) error {
	if err := m.insert(rc.ID, func() *Agent { return m.buildAgent(rc, prov, mb) }); err != nil {
		return err
	}
	slog.Info("agent added dynamically", "id", rc.ID, "model", rc.Model)
	return nil
}

// insert builds an agent outside the lock (building does IO) and adds it
// under the lock, refusing ids that are already loaded.
func (m *Manager) insert(id string, build func() *Agent) error {
	m.mu.RLock()
	_, exists := m.agents[id]
	m.mu.RUnlock()
	if exists {
		return fmt.Errorf("agent %q already exists", id)
	}
	ag := build()
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.agents[id]; exists {
		return fmt.Errorf("agent %q already exists", id)
	}
	m.agents[id] = ag
	m.touchLocked(id)
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
// The override is local to this build: m.opts.globalSkillsCfg is never
// mutated, so concurrent AddAgent calls keep the caller's own cfg.
func (m *Manager) AddAgentWithSkillsCfg(rc config.ResolvedAgent, prov provider.Provider, mb *bus.MessageBus, cfg config.SkillsCfg) error {
	if err := m.insert(rc.ID, func() *Agent { return m.buildAgentWithSkillsCfg(rc, prov, mb, cfg) }); err != nil {
		return err
	}
	slog.Info("agent added dynamically with override skills cfg", "id", rc.ID, "model", rc.Model)
	return nil
}

// RemoveAgent unregisters an agent by ID. No-op if the agent is not loaded.
func (m *Manager) RemoveAgent(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.agents[id]; !ok {
		return
	}
	delete(m.agents, id)
	delete(m.lastUsed, id)
	if m.defaultAgent != nil && m.defaultAgent.Name() == id {
		m.defaultAgent = nil
	}
	slog.Info("agent removed dynamically", "id", id)
}

// AgentByID returns an agent by its ID.
func (m *Manager) AgentByID(id string) *Agent {
	m.mu.RLock()
	ag := m.agents[id]
	ts := m.lastUsed[id]
	m.mu.RUnlock()
	if ts != nil {
		ts.Store(time.Now().UnixNano())
	}
	return ag
}

// Has reports whether id is loaded, without counting as a use.
func (m *Manager) Has(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.agents[id]
	return ok
}

// touchLocked stamps id as used now. Caller holds m.mu for writing.
func (m *Manager) touchLocked(id string) {
	ts := m.lastUsed[id]
	if ts == nil {
		ts = new(atomic.Int64)
		m.lastUsed[id] = ts
	}
	ts.Store(time.Now().UnixNano())
}

// EvictIdle removes agents that have not been looked up since cutoff,
// skipping any id for which keep returns true. Returns the evicted ids.
// Used by on-demand UserSpaces so an app with thousands of agents only
// holds the ones that are actually in use.
func (m *Manager) EvictIdle(cutoff time.Time, keep func(id string) bool) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var evicted []string
	for id := range m.agents {
		if keep != nil && keep(id) {
			continue
		}
		ts := m.lastUsed[id]
		if ts != nil && time.Unix(0, ts.Load()).After(cutoff) {
			continue
		}
		delete(m.agents, id)
		delete(m.lastUsed, id)
		if m.defaultAgent != nil && m.defaultAgent.Name() == id {
			m.defaultAgent = nil
		}
		evicted = append(evicted, id)
	}
	return evicted
}

// Len returns the number of loaded agents.
func (m *Manager) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.agents)
}

// ClearDefaultAgent drops the implicit default. An on-demand UserSpace
// calls it because "exactly one agent loaded" there says nothing about
// how many agents the account owns.
func (m *Manager) ClearDefaultAgent() {
	m.mu.Lock()
	m.defaultAgent = nil
	m.mu.Unlock()
}

// DefaultAgent returns the default agent (set when only one agent exists).
func (m *Manager) DefaultAgent() *Agent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.defaultAgent
}

// All returns all loaded agents.
func (m *Manager) All() []*Agent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*Agent, 0, len(m.agents))
	for _, ag := range m.agents {
		result = append(result, ag)
	}
	return result
}

// Names returns all agent IDs.
func (m *Manager) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
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
	m.mu.RLock()
	defer m.mu.RUnlock()
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	for id, ag := range m.agents {
		if rc, ok := byID[id]; ok {
			ag.provider = providerForAgent(rc, shared)
		} else {
			ag.provider = shared
		}
	}
}
