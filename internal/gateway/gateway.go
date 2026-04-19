package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/channels"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/cron"
	"github.com/fastclaw-ai/fastclaw/internal/plugin"
	"github.com/fastclaw-ai/fastclaw/internal/session"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/taskqueue"
	"github.com/fastclaw-ai/fastclaw/internal/webhook"
)

// Gateway is the main orchestrator that starts all services.
//
// It owns a registry of per-user spaces. The "local" user is loaded at
// startup and drives channels, cron, webhooks and plugins (these remain
// host-global features). In cloud mode, additional users are loaded lazily
// on first HTTP request and get isolated agents + sessions + memory.
type Gateway struct {
	config       *config.Config // local user's config
	bus          *bus.MessageBus
	users        *userSpaceRegistry
	localSpace   *UserSpace // shortcut to users.get(config.DefaultUserID)
	agents       *agent.Manager // alias for localSpace.Agents (channels/cron/plugins)
	chanMgr      *channels.Manager
	bindings     []config.Binding
	botUsernames map[string]string          // agentID -> bot username
	teams        map[string]config.TeamEntry // team name -> team config
	mu           sync.RWMutex
	dedup        sync.Map                    // dedup key -> dedupEntry
	heartbeats   []*agent.Heartbeat
	scheduler    *cron.Scheduler
	webhookSrv   *webhook.Server
	pluginMgr    *plugin.Manager
	taskQueue    *taskqueue.Queue
	store        store.Store
}

// New creates a new Gateway with multi-agent support.
func New(cfg *config.Config) (*Gateway, error) {
	mb := bus.New()

	// Initialize storage backend. Local user's config on the filesystem is
	// always the bootstrap; after reading it, we open the configured store
	// (FileStore or DBStore) for all subsequent per-user data.
	homeDir, _ := config.HomeDir()
	st, err := store.New(&store.StorageConfig{
		Type:        store.StorageType(cfg.Storage.Type),
		DSN:         cfg.Storage.DSN,
		AutoMigrate: cfg.Storage.AutoMigrate,
	}, homeDir)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	// Resolve provider + agents for the local user. This mirrors what
	// loadUserSpace does, but we keep the inline version here because the
	// caller already handed us a *config.Config (avoiding a redundant disk
	// read) and we want to surface any provider log lines.
	llm := newProviderFromConfig(cfg)
	slog.Info("local provider resolved", "defaultModel", cfg.Agents.Defaults.Model)

	resolved := config.ResolveAgents(cfg)
	var managerOpts []agent.ManagerOption
	if st != nil {
		managerOpts = append(managerOpts,
			agent.WithSessionStore(session.NewStoreAdapter(st)),
			agent.WithMemoryStore(agent.NewMemoryStoreAdapter(st)),
		)
	}
	agentMgr, err := agent.NewManager(resolved, llm, mb, managerOpts...)
	if err != nil {
		return nil, err
	}

	// Tag local agents with owner user ID for hook namespacing.
	for _, ag := range agentMgr.All() {
		ag.SetOwnerUserID(config.DefaultUserID)
	}

	localSpace := &UserSpace{
		UserID:   config.DefaultUserID,
		Config:   cfg,
		Provider: llm,
		Agents:   agentMgr,
	}
	userReg := newUserSpaceRegistry(mb, st)
	userReg.put(localSpace)

	slog.Info("agents loaded", "user", config.DefaultUserID, "count", len(resolved), "names", agentMgr.Names())

	// Create channel manager and register channel instances
	chanMgr := channels.NewManager(mb)

	if err := registerChannels(cfg, mb, chanMgr); err != nil {
		return nil, err
	}

	// Build agentID -> botUsername mapping from bindings + channel manager
	botUsernames := buildBotUsernames(cfg.Bindings, chanMgr)
	if len(botUsernames) > 0 {
		slog.Info("bot username mappings", "map", botUsernames)
	}

	teams := cfg.Teams
	if teams == nil {
		teams = make(map[string]config.TeamEntry)
	}

	// Set up group context for agents in teams
	for _, team := range teams {
		for _, agentID := range team.Agents {
			ag := agentMgr.AgentByID(agentID)
			if ag == nil {
				continue
			}
			var teammates []string
			for _, otherID := range team.Agents {
				if otherID != agentID {
					if uname, ok := botUsernames[otherID]; ok {
						teammates = append(teammates, "@"+uname)
					} else {
						teammates = append(teammates, otherID)
					}
				}
			}
			if botUname, ok := botUsernames[agentID]; ok {
				ag.SetGroupContext(&agent.GroupContext{
					BotUsername: botUname,
					Teammates:  teammates,
				})
			}
		}
	}

	// Set up heartbeats for each agent
	heartbeatInterval := time.Duration(cfg.Heartbeat.IntervalMinutes) * time.Minute
	if heartbeatInterval <= 0 {
		heartbeatInterval = agent.DefaultHeartbeatInterval
	}
	var heartbeats []*agent.Heartbeat
	for _, ag := range agentMgr.All() {
		hb := agent.NewHeartbeat(ag, mb, heartbeatInterval)
		heartbeats = append(heartbeats, hb)
	}

	// Set up cron scheduler
	var cronJobs []cron.Job
	for _, cj := range cfg.CronJobs {
		cronJobs = append(cronJobs, cron.Job{
			Name:     cj.Name,
			Type:     cron.JobType(cj.Type),
			Schedule: cj.Schedule,
			AgentID:  cj.AgentID,
			Channel:  cj.Channel,
			ChatID:   cj.ChatID,
			Message:  cj.Message,
		})
	}
	scheduler := cron.NewScheduler(cronJobs, mb)

	// Register web search tool for all agents if configured
	if cfg.WebSearch.APIKey != "" {
		for _, ag := range agentMgr.All() {
			ag.RegisterWebSearchTool(cfg.WebSearch.APIKey)
		}
		slog.Info("web search registered", "provider", cfg.WebSearch.Provider)
	}

	// Register Exa search tool for all agents if configured. Falls back to
	// EXA_API_KEY env var when the config field is empty.
	exaKey := cfg.ExaSearch.APIKey
	if exaKey == "" {
		exaKey = os.Getenv("EXA_API_KEY")
	}
	if exaKey != "" {
		for _, ag := range agentMgr.All() {
			ag.RegisterExaSearchTool(exaKey)
		}
		slog.Info("exa search registered")
	}

	// Register sub-agent spawner for all agents
	spawner := &gatewaySubAgentSpawner{agents: agentMgr}
	for _, ag := range agentMgr.All() {
		ag.SetSubAgentSpawner(spawner)
	}

	// Set up webhook server if enabled
	var webhookSrv *webhook.Server
	if cfg.Hooks.Enabled {
		webhookSrv = webhook.NewServer(cfg.Hooks.Token, cfg.Hooks.Path, &webhookAgentHandler{agents: agentMgr}, nil)
	}

	// Set up plugin manager
	var pluginMgr *plugin.Manager
	if cfg.Plugins.Enabled {
		pluginMgr = plugin.NewManager(mb)

		homeDir, _ := config.HomeDir()
		pluginPaths := []string{filepath.Join(homeDir, "plugins")}
		for _, p := range cfg.Plugins.Paths {
			pluginPaths = append(pluginPaths, p)
		}

		if err := pluginMgr.Discover(pluginPaths); err != nil {
			slog.Warn("plugin discovery error", "error", err)
		}

		// Apply per-plugin config
		if len(cfg.Plugins.Entries) > 0 {
			entries := make(map[string]plugin.PluginEntryCfg, len(cfg.Plugins.Entries))
			for k, v := range cfg.Plugins.Entries {
				entries[k] = plugin.PluginEntryCfg{
					Enabled: v.Enabled,
					Config:  v.Config,
				}
			}
			pluginMgr.ApplyConfig(entries)
		}
	}

	// Create task queue with config values
	maxConcurrent := cfg.TaskQueue.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 10
	}
	taskTimeoutSec := cfg.TaskQueue.TaskTimeoutSec
	if taskTimeoutSec <= 0 {
		taskTimeoutSec = 300
	}
	taskTimeout := time.Duration(taskTimeoutSec) * time.Second

	g := &Gateway{
		config:       cfg,
		bus:          mb,
		store:        st,
		users:        userReg,
		localSpace:   localSpace,
		agents:       agentMgr,
		chanMgr:      chanMgr,
		bindings:     cfg.Bindings,
		botUsernames: botUsernames,
		teams:        teams,
		heartbeats:   heartbeats,
		scheduler:    scheduler,
		webhookSrv:   webhookSrv,
		pluginMgr:    pluginMgr,
	}

	tq := taskqueue.NewQueue(maxConcurrent, taskTimeout, func(ctx context.Context, task *taskqueue.Task) (string, error) {
		ag := agentMgr.AgentByID(task.AgentID)
		if ag == nil {
			return "", fmt.Errorf("agent %q not found", task.AgentID)
		}

		// Send typing indicator and keep sending every 5s until done
		chanMgr.SendTyping(task.Message.Channel, task.AccountID, task.Message.ChatID)
		typingDone := make(chan struct{})
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-typingDone:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					chanMgr.SendTyping(task.Message.Channel, task.AccountID, task.Message.ChatID)
				}
			}
		}()

		reply := ag.HandleMessage(ctx, task.Message)
		close(typingDone)

		mb.Outbound <- bus.OutboundMessage{
			Channel:      task.Message.Channel,
			AccountID:    task.AccountID,
			ChatID:       task.Message.ChatID,
			Text:         reply,
			ReplyToMsgID: task.Message.MessageID,
		}
		return reply, nil
	})
	g.taskQueue = tq

	return g, nil
}

// AgentManager returns the local user's agent manager.
// For per-user routing in cloud mode, use UserSpaceFor.
func (g *Gateway) AgentManager() *agent.Manager {
	return g.agents
}

// LocalUserSpace returns the preloaded local ("host admin") user space that
// owns channels, cron jobs, plugins, and the webhook ingress.
func (g *Gateway) LocalUserSpace() *UserSpace {
	return g.localSpace
}

// UserSpaceFor returns the user space for the given user ID, loading it
// lazily from disk if needed. In local mode callers normally pass
// config.DefaultUserID and get the preloaded space; in cloud mode the HTTP
// auth middleware resolves the real user ID and this method fetches the
// matching space.
func (g *Gateway) UserSpaceFor(userID string) (*UserSpace, error) {
	if userID == "" {
		userID = config.DefaultUserID
	}
	return g.users.getOrLoad(userID)
}

// LocalAgentManager satisfies the api.UserResolver interface by exposing
// the local user's agent manager.
func (g *Gateway) LocalAgentManager() *agent.Manager {
	return g.agents
}

// IsCloudMode reports whether the gateway is configured to accept multiple
// users via HTTP auth. Channels, cron and plugins remain bound to the
// local user regardless.
func (g *Gateway) IsCloudMode() bool {
	return g.config != nil && g.config.Gateway.Mode == "cloud"
}

// Store returns the gateway's storage backend.
func (g *Gateway) Store() store.Store {
	return g.store
}

// TaskQueue returns the gateway's task queue.
func (g *Gateway) TaskQueue() *taskqueue.Queue {
	return g.taskQueue
}

// Run starts the gateway and blocks until shutdown signal.
func (g *Gateway) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	var wg sync.WaitGroup

	// Start idle user space evictor (cloud mode: free memory for inactive users)
	wg.Add(1)
	go func() {
		defer wg.Done()
		g.users.startEvictor(ctx)
	}()

	// Start config file watcher for hot-reload
	wg.Add(1)
	go g.startConfigWatcher(ctx, &wg)

	// Start dedup cleanup goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		g.cleanupDedup(ctx)
	}()

	// Start inbound message processor
	wg.Add(1)
	go func() {
		defer wg.Done()
		g.processInbound(ctx)
	}()

	// Start channel manager
	wg.Add(1)
	go func() {
		defer wg.Done()
		g.chanMgr.Start(ctx)
	}()

	// Start heartbeats for each agent
	for _, hb := range g.heartbeats {
		wg.Add(1)
		go func(h *agent.Heartbeat) {
			defer wg.Done()
			h.Start(ctx)
		}(hb)
	}

	// Start cron scheduler
	wg.Add(1)
	go func() {
		defer wg.Done()
		g.scheduler.Start(ctx)
	}()

	// Start webhook server if configured
	if g.webhookSrv != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			port := g.config.Hooks.Port
			if port == 0 {
				port = 18954
			}
			addr := fmt.Sprintf(":%d", port)
			if err := g.webhookSrv.ListenAndServe(ctx, addr); err != nil {
				slog.Error("webhook server error", "error", err)
			}
		}()
	}

	// Start plugins if enabled
	if g.pluginMgr != nil {
		if err := g.pluginMgr.StartAll(ctx); err != nil {
			slog.Error("plugin start error", "error", err)
		}

		// Register channel adapters for channel plugins
		for _, inst := range g.pluginMgr.ChannelPlugins() {
			adapter := plugin.NewChannelAdapter(g.pluginMgr, inst.Manifest.ID)
			g.chanMgr.Register(adapter)
			slog.Info("registered plugin channel", "plugin", inst.Manifest.ID)
		}

		// Register tool plugins with all agents
		for _, inst := range g.pluginMgr.ToolPlugins() {
			for _, ag := range g.agents.All() {
				if err := plugin.RegisterPluginTools(ctx, g.pluginMgr, inst.Manifest.ID, ag.ToolRegistry()); err != nil {
					slog.Error("register plugin tools failed", "plugin", inst.Manifest.ID, "agent", ag.Name(), "error", err)
				}
			}
		}

		// Register hook plugins with all agents
		for _, inst := range g.pluginMgr.HookPlugins() {
			for _, ag := range g.agents.All() {
				if err := plugin.RegisterPluginHooks(ctx, g.pluginMgr, inst.Manifest.ID, ag.HookRegistry(), ag.Name()); err != nil {
					slog.Error("register plugin hooks failed", "plugin", inst.Manifest.ID, "agent", ag.Name(), "error", err)
				}
			}
		}
	}

	slog.Info("gateway started")

	wg.Wait()

	// Stop task queue
	if g.taskQueue != nil {
		g.taskQueue.Stop()
	}

	// Stop plugins on shutdown
	if g.pluginMgr != nil {
		g.pluginMgr.StopAll()
	}

	slog.Info("gateway stopped")
	return nil
}
