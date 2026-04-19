package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/session"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// UserSpace holds the per-user state that can be multiplexed inside one
// gateway process. Channels, cron, webhook and plugins remain global (bound
// to the local admin user); the HTTP API multiplexes across user spaces.
type UserSpace struct {
	UserID       string
	Config       *config.Config
	Provider     provider.Provider
	Agents       *agent.Manager
	SandboxPool  sandbox.ExecutorPool // nil in local mode / when sandbox is disabled
}

// loadUserSpace reads a user's config and instantiates their agent manager.
// The shared message bus is reused so that cross-user plumbing (typing
// indicators, outbound routing) stays in one place.
func loadUserSpace(userID string, mb *bus.MessageBus, st store.Store) (*UserSpace, error) {
	cfg, err := config.LoadForUser(userID)
	if err != nil {
		return nil, fmt.Errorf("load config for user %q: %w", userID, err)
	}

	prov := newProviderFromConfig(cfg)

	resolved := config.ResolveAgentsForUser(cfg, userID)
	var managerOpts []agent.ManagerOption
	if st != nil {
		managerOpts = append(managerOpts,
			agent.WithSessionStore(session.NewStoreAdapter(st)),
			agent.WithMemoryStore(agent.NewMemoryStoreAdapter(st)),
		)
	}
	agentMgr, err := agent.NewManager(resolved, prov, mb, managerOpts...)
	if err != nil {
		return nil, fmt.Errorf("create agent manager for user %q: %w", userID, err)
	}

	// Tag each agent with the owning user ID so hooks (e.g. mem0) can
	// namespace per-user data, and register web-search tools if configured.
	exaKey := cfg.ExaSearch.APIKey
	if exaKey == "" {
		exaKey = os.Getenv("EXA_API_KEY")
	}
	for _, ag := range agentMgr.All() {
		ag.SetOwnerUserID(userID)
		if cfg.WebSearch.APIKey != "" {
			ag.RegisterWebSearchTool(cfg.WebSearch.APIKey)
		}
		if exaKey != "" {
			ag.RegisterExaSearchTool(exaKey)
		}
	}

	var pool sandbox.ExecutorPool

	// If a sandbox backend is configured, attach a full Executor to each agent
	// so ALL tool calls (exec + file ops) run inside the sandbox.
	{
		// Check if any agent has sandbox enabled — use the first one's
		// config to decide the pool backend.
		for _, rc := range config.ResolveAgentsForUser(cfg, userID) {
			if rc.Sandbox.Enabled {
				switch rc.Sandbox.Backend {
				case "e2b":
					// E2B needs an API key — check config first, then env.
					apiKey := rc.Sandbox.E2BKey
					if apiKey == "" {
						apiKey = os.Getenv("E2B_API_KEY")
					}
					template := rc.Sandbox.Image // reuse Image field as E2B template
					if template == "" {
						template = "base"
					}
					pool = sandbox.NewE2BExecutorPool(apiKey, template, 30*time.Minute)
					slog.Info("sandbox executor pool created",
						"user", userID, "backend", "e2b", "template", template)
				default: // "docker" or empty
					userDir, _ := config.UserDir(userID)
					pool = sandbox.NewDockerExecutorPool(
						rc.Sandbox.Image, userDir, nil,
					)
					slog.Info("sandbox executor pool created",
						"user", userID, "backend", "docker")
				}
				break
			}
		}

		if pool != nil {
			// Attach a sandbox executor to each agent — all tools go
			// through the container.
			for _, ag := range agentMgr.All() {
				ex, err := pool.Get(context.Background(), ag.Name())
				if err != nil {
					slog.Warn("sandbox executor creation failed",
						"user", userID, "agent", ag.Name(), "error", err)
					continue
				}
				ag.ToolRegistry().SetExecutor(ex)
			}
		} else {
			// No sandbox backend → path-only restriction.
			userDir, err := config.UserDir(userID)
			if err == nil {
				for _, ag := range agentMgr.All() {
					ag.ToolRegistry().SetSandboxRoot(userDir)
				}
				slog.Info("path sandbox enabled", "user", userID)
			}
		}
	}

	slog.Info("loaded user space", "user", userID, "agents", agentMgr.Names())

	return &UserSpace{
		UserID:      userID,
		Config:      cfg,
		Provider:    prov,
		Agents:      agentMgr,
		SandboxPool: pool,
	}, nil
}

// newProviderFromConfig picks an LLM provider from a user's config with the
// same fallback logic Gateway.New used to use inline.
func newProviderFromConfig(cfg *config.Config) provider.Provider {
	var providerCfg config.ProviderConfig
	defaultModel := cfg.Agents.Defaults.Model
	if parts := strings.SplitN(defaultModel, "/", 2); len(parts) == 2 {
		if p, ok := cfg.Providers[parts[0]]; ok {
			providerCfg = p
		}
	}
	if providerCfg.APIKey == "" {
		for _, key := range []string{"default", "openai", "openrouter"} {
			if p, ok := cfg.Providers[key]; ok {
				providerCfg = p
				break
			}
		}
	}
	if providerCfg.APIKey == "" {
		for _, p := range cfg.Providers {
			providerCfg = p
			break
		}
	}
	return provider.NewProvider(providerCfg.APIKey, providerCfg.APIBase, providerCfg.APIType)
}

// userSpaceRegistry is a thread-safe lazy-loaded map of user spaces owned by
// the gateway. The "local" user is preloaded at gateway startup and is NEVER
// evicted; other users are loaded on first request and evicted after an idle
// period to free memory when they stop chatting.
type userSpaceRegistry struct {
	mu       sync.RWMutex
	spaces   map[string]*userSpaceEntry
	bus      *bus.MessageBus
	store    store.Store // optional DB store for sessions/memory
	idleTTL  time.Duration // how long before an idle user is evicted (0 = never)
	pinned   map[string]bool // user IDs that must never be evicted (e.g. "local")
}

type userSpaceEntry struct {
	space    *UserSpace
	lastUsed time.Time
}

func newUserSpaceRegistry(mb *bus.MessageBus, st ...store.Store) *userSpaceRegistry {
	reg := &userSpaceRegistry{
		spaces:  make(map[string]*userSpaceEntry),
		bus:     mb,
		idleTTL: 30 * time.Minute,
		pinned:  make(map[string]bool),
	}
	if len(st) > 0 && st[0] != nil {
		reg.store = st[0]
	}
	return reg
}

// put stores a preloaded user space that is pinned (never evicted).
func (r *userSpaceRegistry) put(space *UserSpace) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spaces[space.UserID] = &userSpaceEntry{space: space, lastUsed: time.Now()}
	r.pinned[space.UserID] = true
}

// get returns a user space if already loaded, refreshing its last-used time.
func (r *userSpaceRegistry) get(userID string) (*UserSpace, bool) {
	r.mu.RLock()
	e, ok := r.spaces[userID]
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}
	// Touch under write lock (cheap: just a time assignment).
	r.mu.Lock()
	e.lastUsed = time.Now()
	r.mu.Unlock()
	return e.space, true
}

// getOrLoad returns the user space for a given user, loading it on first
// access and refreshing the idle timer on subsequent accesses.
func (r *userSpaceRegistry) getOrLoad(userID string) (*UserSpace, error) {
	if sp, ok := r.get(userID); ok {
		return sp, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.spaces[userID]; ok {
		e.lastUsed = time.Now()
		return e.space, nil
	}

	sp, err := loadUserSpace(userID, r.bus, r.store)
	if err != nil {
		return nil, err
	}
	r.spaces[userID] = &userSpaceEntry{space: sp, lastUsed: time.Now()}
	return sp, nil
}

// all returns a snapshot of all loaded user spaces.
func (r *userSpaceRegistry) all() []*UserSpace {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*UserSpace, 0, len(r.spaces))
	for _, e := range r.spaces {
		out = append(out, e.space)
	}
	return out
}

// evictIdle removes user spaces that have been idle longer than idleTTL.
// Pinned spaces (e.g. "local") are never evicted. Call periodically from a
// background goroutine.
func (r *userSpaceRegistry) evictIdle() int {
	if r.idleTTL <= 0 {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := time.Now().Add(-r.idleTTL)
	evicted := 0
	for uid, e := range r.spaces {
		if r.pinned[uid] {
			continue
		}
		if e.lastUsed.Before(cutoff) {
			delete(r.spaces, uid)
			evicted++
			slog.Info("evicted idle user space", "user", uid,
				"idle", time.Since(e.lastUsed).Round(time.Second))
		}
	}
	return evicted
}

// startEvictor runs a background loop that periodically evicts idle user
// spaces. Stops when ctx is cancelled.
func (r *userSpaceRegistry) startEvictor(ctx context.Context) {
	if r.idleTTL <= 0 {
		return
	}
	interval := r.idleTTL / 3
	if interval < time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n := r.evictIdle(); n > 0 {
				slog.Info("user space eviction sweep", "evicted", n, "remaining", len(r.spaces))
			}
		}
	}
}
