// Package gateway is the runtime orchestrator. It opens the store, hosts
// per-user UserSpaces (lazy-loaded on first auth), and starts the channel
// manager / cron scheduler / webhook server / plugin manager.
package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/agentcli"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/channels"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/cron"
	"github.com/fastclaw-ai/fastclaw/internal/plugin"
	"github.com/fastclaw-ai/fastclaw/internal/rediscoord"
	coderuntime "github.com/fastclaw-ai/fastclaw/internal/runtime"
	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/taskqueue"
	"github.com/fastclaw-ai/fastclaw/internal/toolproviders"
	"github.com/fastclaw-ai/fastclaw/internal/toolproviders/imagegen"
	"github.com/fastclaw-ai/fastclaw/internal/toolproviders/tts"
	"github.com/fastclaw-ai/fastclaw/internal/toolproviders/webfetch"
	"github.com/fastclaw-ai/fastclaw/internal/toolproviders/websearch"
	"github.com/fastclaw-ai/fastclaw/internal/usage"
	"github.com/fastclaw-ai/fastclaw/internal/users"
	"github.com/fastclaw-ai/fastclaw/internal/webhook"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
	"github.com/redis/go-redis/v9"
)

var toolProviderRegistry = func() *toolproviders.Registry {
	r := toolproviders.NewRegistry()
	websearch.RegisterAll(r)
	webfetch.RegisterAll(r)
	imagegen.RegisterAll(r)
	tts.RegisterAll(r)
	return r
}()

// ToolProviderRegistry exposes the registry for callers that want to list
// available providers (admin API).
func ToolProviderRegistry() *toolproviders.Registry { return toolProviderRegistry }

// DiagnoseAgent renders the readiness report for one agent, resolving
// its merged config so the capability probe is populated. Shared by
// `fastclaw agents doctor` and the in-chat check_agent tool.
func DiagnoseAgent(ctx context.Context, st store.Store, agentRef string) (string, error) {
	rec, err := agentcli.Resolve(ctx, st, agentRef)
	if err != nil {
		return "", err
	}
	cfg, err := assembleConfig(ctx, st, rec.UserID, rec.ID)
	if err != nil {
		return "", fmt.Errorf("assemble config for %s: %w", rec.ID, err)
	}
	deps := tools.AgentAdminDeps{
		Store:        st,
		OwnerUserID:  rec.UserID,
		AgentHomeDir: config.AgentHomeDir,
		ToolAvailability: func(context.Context, string) (map[string]bool, error) {
			return agentToolAvailability(cfg, rec.ID), nil
		},
	}
	return tools.DiagnoseAgent(ctx, deps, rec.ID, rec.Name), nil
}

// agentToolAvailability reports which provider-backed tools the given
// agent would have registered, using the same merged-config resolution
// registerAgentToolChains uses to actually register them. Built-in tools
// that always exist (exec, web_fetch's direct fallback, file ops) are not
// listed — this answers the question that has a non-obvious answer:
// which optional capabilities does this agent really have.
//
// Deriving it from the same source as registration is the point. A
// hand-maintained list would drift, and a drifted capability report is
// worse than none: it is what lets a provisioning run announce an agent
// as ready when its core tool was never wired.
func agentToolAvailability(cfg *config.Config, agentID string) map[string]bool {
	resolved := cfg.MergedAgentConfig(config.AgentEntry{ID: agentID})
	out := make(map[string]bool, 4)
	for _, category := range []string{"web_search", "image_gen", "tts", "web_fetch"} {
		out[category] = buildToolChainFromResolved(resolved, category) != nil
	}
	// web_fetch always exists — a direct built-in fetcher is registered
	// at agent construction and a configured chain only swaps the
	// backend.
	out["web_fetch"] = true
	return out
}

// registerAgentToolChains wires every provider-backed tool category onto
// the given agents using their merged config view (system + user + agent
// scopes overlaid by the resolver).
func registerAgentToolChains(cfg *config.Config, agents []*agent.Agent) {
	envSearxNG := strings.TrimSpace(os.Getenv("FASTCLAW_SEARXNG_ENDPOINT"))
	for _, ag := range agents {
		resolved := cfg.MergedAgentConfig(config.AgentEntry{ID: ag.Name()})
		chain := buildToolChainFromResolved(resolved, "web_search")
		// Fallback: if no web_search chain is configured AND
		// FASTCLAW_SEARXNG_ENDPOINT is set in the environment,
		// synthesize a one-provider chain pointing at that endpoint.
		// One-line setup ("docker run searxng …" + an env var) is the
		// difference between an agent that can find the right URL on
		// the first try and one that burns 11 rounds guessing — we
		// observed the latter in the wild and the cost of leaving
		// users without search is not worth the cost of injecting a
		// sensible default.
		if chain == nil && envSearxNG != "" {
			chain = synthesizeSearxNGChain(envSearxNG)
		}
		if chain != nil {
			ag.RegisterWebSearchChain(chain)
		}
		if chain := buildToolChainFromResolved(resolved, "image_gen"); chain != nil {
			ag.RegisterImageGenChain(chain)
		}
		if chain := buildToolChainFromResolved(resolved, "tts"); chain != nil {
			ag.RegisterTTSChain(chain)
		}
		// web_fetch: chain-first, otherwise the agent keeps the
		// built-in direct fetcher already registered at construction
		// time (RegisterWebFetch in loop.go), so this call only swaps
		// the backend when an admin actually configured a chain.
		if chain := buildToolChainFromResolved(resolved, "web_fetch"); chain != nil {
			ag.RegisterWebFetchChain(chain)
		}
	}
}

// synthesizeSearxNGChain builds an ad-hoc web_search chain backed
// solely by the SearxNG provider, configured from FASTCLAW_SEARXNG_ENDPOINT.
// Lets a fresh install enable search without going through the
// dashboard's tool-providers config — the most common reason a user
// in the wild never sees web_search is that they didn't realize they
// had to wire it up in two places (provider entry + category chain).
func synthesizeSearxNGChain(endpoint string) *toolproviders.Chain {
	chain := &toolproviders.Chain{
		Category:     "web_search",
		Order:        []string{"searxng"},
		AutoFallback: false,
		Registry:     toolProviderRegistry,
		GetConfig: func(name string) toolproviders.ProviderConfig {
			if name != "searxng" {
				return toolproviders.ProviderConfig{}
			}
			return toolproviders.ProviderConfig{Endpoint: endpoint}
		},
	}
	if !chain.Available() {
		return nil
	}
	return chain
}

func buildToolChainFromResolved(resolved config.ResolvedAgent, category string) *toolproviders.Chain {
	cat, ok := resolved.Tools[category]
	if !ok {
		return nil
	}
	order := cat.Chain()
	if len(order) == 0 {
		return nil
	}
	providers := resolved.ToolProviders
	chain := &toolproviders.Chain{
		Category:     category,
		Order:        order,
		AutoFallback: cat.FallbackEnabled(),
		Registry:     toolProviderRegistry,
		GetConfig: func(name string) toolproviders.ProviderConfig {
			pc := providers[name]
			return toolproviders.ProviderConfig{
				APIKey:   pc.APIKey,
				Endpoint: pc.Endpoint,
				Options:  pc.Options,
			}
		},
	}
	if !chain.Available() {
		return nil
	}
	return chain
}

// Gateway is the runtime orchestrator. It does not load any agents at
// boot; UserSpaces are constructed lazily when an authenticated request
// resolves to their owner.
//
// `sandboxPool` is the gateway-wide executor pool. Built once from the
// system-scope sandbox config and shared by every UserSpace. The
// per-UserSpace `SandboxPool` field is just a borrowed reference;
// shutdown closes this single pool.
type Gateway struct {
	bus         *bus.MessageBus
	users       *userSpaceRegistry
	chanMgr     *channels.Manager
	webChan     *channels.WebChannel
	scheduler   *cron.Scheduler
	webhookSrv  *webhook.Server
	pluginMgr   *plugin.Manager
	taskQueue   *taskqueue.Queue
	store       store.Store
	accounts    *users.Accounts
	workspace   workspace.Store
	sandboxPool sandbox.ExecutorPool
	usage       usage.Meter
	quotaStore  usage.QuotaStore
	envCfg      *config.EnvConfig
	// projectRuntime is the coding-agent runtime manager (live dev server
	// + preview). Set by SetProjectRuntime after construction; nil keeps
	// agents as plain assistants. Exposed to the setup server via
	// ProjectRuntime() so the HTTP /runtime endpoints share the instance
	// with the agent tools.
	projectRuntime *coderuntime.Manager
	// chatEvents, when set, lets bus-fired web turns (cron / goal
	// continuation / heartbeat / sub-agent) stream through the same
	// SSE hub a user-typed POST /api/chat turn uses. Nil-safe: unset
	// keeps the legacy bus.Outbound → WebChannel async-bubble path.
	chatEvents *agent.EventHub
	mu         sync.RWMutex
	dedup      sync.Map
}

// SetProjectRuntime wires the coding-agent runtime manager. Call once at
// boot before Run(); it propagates to the user-space registry so every
// agent loaded afterwards gains the preview tools. Safe to leave unset
// (agents stay plain assistants; the HTTP /runtime endpoints 503).
func (g *Gateway) SetProjectRuntime(m *coderuntime.Manager) {
	g.projectRuntime = m
	if g.users != nil {
		g.users.setProjectRuntime(m)
	}
}

// ProjectRuntime returns the coding-agent runtime manager, or nil when
// none is configured. The setup server uses it to back the HTTP
// /runtime endpoints with the same instance the agent tools use.
func (g *Gateway) ProjectRuntime() *coderuntime.Manager { return g.projectRuntime }

// SetChatEvents wires the agent event hub the setup server lazy-inits.
// Must be called before Run() so the very first bus-fired web turn
// streams through the hub rather than landing as one delayed async
// bubble. Safe to call once.
func (g *Gateway) SetChatEvents(h *agent.EventHub) { g.chatEvents = h }

// WebChannel returns the in-process fan-out for web SSE subscribers.
// Used by the setup server to register chat-stream subscribers so cron-
// fired (and other async) outbound messages reach live dashboard tabs.
func (g *Gateway) WebChannel() *channels.WebChannel { return g.webChan }

// Workspace returns the durable artifact store.
func (g *Gateway) Workspace() workspace.Store { return g.workspace }

// Usage returns the per-tenant resource meter.
func (g *Gateway) Usage() usage.Meter { return g.usage }

// QuotaStore returns the per-user quota store.
func (g *Gateway) QuotaStore() usage.QuotaStore { return g.quotaStore }

// Store returns the gateway's storage backend.
func (g *Gateway) Store() store.Store { return g.store }

// SandboxPool returns the gateway's shared system sandbox pool (nil when
// sandboxing is disabled). The project runtime borrows it so a dev-server
// preview runs in the SAME executor the coding agent writes files to —
// the only way edits reach the server on backends (E2B) without a shared
// host mount.
func (g *Gateway) SandboxPool() sandbox.ExecutorPool { return g.sandboxPool }

// TaskQueue returns the gateway's task queue.
func (g *Gateway) TaskQueue() *taskqueue.Queue { return g.taskQueue }

// EnvConfig returns the bootstrap config (FASTCLAW_* env vars).
func (g *Gateway) EnvConfig() *config.EnvConfig { return g.envCfg }

// New creates a Gateway. Storage + workspace + plugin manager + channel
// manager + cron scheduler + webhook all initialize here, but no agents
// are loaded until an authenticated request hits a user.
func New(env *config.EnvConfig) (*Gateway, error) {
	if env == nil {
		env = &config.EnvConfig{}
	}
	holderID := uuid.NewString()
	slog.Info("gateway holder id", "id", holderID)

	var redisClient *redis.Client
	mb := bus.New()
	if env.Redis.Enabled {
		addr := strings.TrimSpace(env.Redis.Addr)
		if addr == "" {
			addr = "127.0.0.1:6379"
		}
		redisClient = redis.NewClient(&redis.Options{
			Addr:     addr,
			Username: env.Redis.Username,
			Password: env.Redis.Password,
			DB:       env.Redis.DB,
		})
		if err := redisClient.Ping(context.Background()).Err(); err != nil {
			return nil, fmt.Errorf("connect redis: %w", err)
		}
		mb = bus.NewRedis(bus.RedisConfig{
			Client:   redisClient,
			Prefix:   redisPrefix(env.Redis.Prefix),
			Group:    "fastclaw-gateway",
			Consumer: holderID,
		})
	}

	homeDir, _ := config.HomeDir()
	st, err := store.New(&store.StorageConfig{
		Type:        store.StorageType(env.Storage.Type),
		DSN:         env.Storage.DSN,
		AutoMigrate: env.Storage.AutoMigrate || env.Storage.Type == "" || env.Storage.Type == "sqlite",
	}, homeDir)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	// Wire layer-3 agent config (per-agent overrides) to read from the DB.
	config.AgentFileConfigLoader = makeStoreFirstAgentFileLoader(st)

	// Object store for agent-produced artifacts. Object store config lives
	// in system_settings for runtime-edited fields and FASTCLAW_OBJECT_STORE_*
	// env vars for ops-managed overrides.
	osCfg := readObjectStoreCfg(st)
	wsInner, err := workspace.Factory{
		Type:         osCfg.Type,
		LocalDir:     osCfg.Local.Root,
		AccountID:    osCfg.AccountID,
		AliyunIntern: osCfg.AliyunIntern,
		S3: workspace.S3Config{
			Endpoint:  osCfg.S3.Endpoint,
			Region:    osCfg.S3.Region,
			Bucket:    osCfg.S3.Bucket,
			Prefix:    osCfg.S3.Prefix,
			AccessKey: osCfg.S3.AccessKey,
			SecretKey: osCfg.S3.SecretKey,
			UseSSL:    osCfg.S3.UseSSL,
		},
	}.New(filepath.Join(homeDir, "workspaces"))
	if err != nil {
		return nil, fmt.Errorf("open object store: %w", err)
	}
	slog.Info("object store", "type", defaultStr(osCfg.Type, "local"))

	// LLM token metering: SQLMeter UPSERTs into token_usage_daily on the
	// same DB the Store opened, so admin reports survive restart. Falls
	// back to MemMeter if the store doesn't expose a *sql.DB (shouldn't
	// happen in real installs — only an embedded test double would).
	var meter usage.Meter = usage.NewMemMeter()
	var quotaStore usage.QuotaStore = usage.NewMemQuotaStore()
	if dbs, ok := st.(*store.DBStore); ok {
		meter = usage.NewSQLMeter(dbs.DB(), dbs.Dialect())
		quotaStore = usage.NewSQLQuotaStore(dbs.DB(), dbs.Dialect())
	}
	ws := wsInner

	// holderID is the per-process identifier used by the cross-replica
	// channel lease. Redis is preferred when configured; otherwise the
	// historical DB-backed lease keeps single-instance / no-Redis deploys
	// working unchanged.
	var leaser channels.Leaser = storeLeaser{st: st}
	if redisClient != nil {
		leaser = rediscoord.NewLeaser(redisClient, redisPrefix(env.Redis.Prefix))
		slog.Info("redis channel leaser enabled", "prefix", redisPrefix(env.Redis.Prefix))
	}
	chanMgr := channels.NewManagerWithLeaser(mb, leaser, holderID)
	// Always-on web channel: routes cron-fired (and any other
	// async-emitted) outbound messages to the dashboard's SSE
	// subscribers so the user sees the agent's reply live instead of
	// only on the next page reload.
	webChan := channels.NewWebChannel()
	chanMgr.Register(webChan)

	// Cron scheduler reads jobs directly from the DB on each tick — no
	// in-memory job list, no fastclaw.json copy. Each fired job carries
	// its OwnerUserID so processInbound can route into the right space.
	scheduler := cron.NewSchedulerFromStore(&cronStoreAdapter{st: st}, mb)
	// Pre-flight delivery check: when the configured destination
	// channel adapter isn't registered (e.g. wechat token died and
	// the row got purged), the scheduler increments failure_count
	// instead of firing into the void; rows that miss too many
	// consecutive ticks are auto-deleted.
	scheduler.SetChannelChecker(chanMgr)

	systemHooks := readSystemHooks(st)
	var webhookSrv *webhook.Server
	if systemHooks.Enabled {
		webhookSrv = webhook.NewServer(systemHooks.Token, systemHooks.Path, nil, nil)
	}

	var pluginMgr *plugin.Manager
	systemPlugins := readSystemPlugins(st)
	if systemPlugins.Enabled {
		pluginMgr = plugin.NewManager(mb)
		pluginPaths := []string{filepath.Join(homeDir, "plugins")}
		pluginPaths = append(pluginPaths, systemPlugins.Paths...)
		if err := pluginMgr.Discover(pluginPaths); err != nil {
			slog.Warn("plugin discovery error", "error", err)
		}
		if len(systemPlugins.Entries) > 0 {
			entries := make(map[string]plugin.PluginEntryCfg, len(systemPlugins.Entries))
			for k, v := range systemPlugins.Entries {
				entries[k] = plugin.PluginEntryCfg{Enabled: v.Enabled, Config: v.Config}
			}
			pluginMgr.ApplyConfig(entries)
		}
	}

	taskCfg := readSystemTaskQueue(st)
	maxConcurrent := taskCfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 10
	}
	taskTimeoutSec := taskCfg.TaskTimeoutSec
	if taskTimeoutSec <= 0 {
		taskTimeoutSec = 300
	}
	taskTimeout := time.Duration(taskTimeoutSec) * time.Second

	// System-wide sandbox pool. Built once at boot from the system-
	// scope sandbox config (env-merged) and shared across every
	// UserSpace. Lazy-injected agents (super_admin chat, app-mode
	// API-key callers whose `app_user` UserSpace owns no agents of
	// its own) need this — without a system-level pool, the per-user
	// builder produced nil for those spaces and the agent's exec tool
	// refused to run with "sandbox required but no executor available".
	systemSandboxPool := buildSystemSandboxPool(readSystemSandboxCfg(st), ws)

	// Accounts service is used by the inbound routing loop to lazy-mint
	// per-(channel, IM-sender) app_user rows so each chatter on an IM
	// channel ends up with their own stable fastclaw u_xxx id (and thus
	// their own per-chatter USER.md / MEMORY.md).
	accts, err := users.NewAccounts(st)
	if err != nil {
		return nil, fmt.Errorf("init accounts: %w", err)
	}

	g := &Gateway{
		bus:         mb,
		store:       st,
		accounts:    accts,
		workspace:   ws,
		usage:       meter,
		quotaStore:  quotaStore,
		sandboxPool: systemSandboxPool,
		users:       newUserSpaceRegistry(mb, st, ws, meter, quotaStore, systemSandboxPool, pluginMgr),
		chanMgr:     chanMgr,
		webChan:     webChan,
		scheduler:   scheduler,
		webhookSrv:  webhookSrv,
		pluginMgr:   pluginMgr,
		envCfg:      env,
	}

	if webhookSrv != nil {
		webhookSrv.SetHandler(&webhookAgentHandler{gateway: g})
	}

	tq := taskqueue.NewQueue(maxConcurrent, taskTimeout, func(ctx context.Context, task *taskqueue.Task) (string, error) {
		space, err := g.users.getOrLoad(ctx, task.OwnerUserID)
		if err != nil {
			return "", fmt.Errorf("load user space: %w", err)
		}
		ag := space.Agents.AgentByID(task.AgentID)
		if ag == nil {
			return "", fmt.Errorf("agent %q not found", task.AgentID)
		}
		if len(task.Message.MediaItems) > 0 {
			atts := make([]agent.Attachment, 0, len(task.Message.MediaItems))
			for _, item := range task.Message.MediaItems {
				mimeType := item.ContentType
				if mimeType == "" {
					mimeType = "application/octet-stream"
				}
				atts = append(atts, agent.Attachment{
					Name: item.Filename,
					URL:  "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(item.Bytes),
				})
			}
			results := ag.WriteSessionAttachments(ctx, task.Message.ChatID, task.Message.ProjectID, atts)
			if len(results) > 0 {
				var refs strings.Builder
				for _, res := range results {
					fmt.Fprintf(&refs, "[Attached: /workspace/%s]\n", res.Path)
				}
				task.Message.Text = refs.String() + task.Message.Text
			}
		}
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

		// IM channels show only the final reply — no per-tool_call
		// progress messages. Users see a typing indicator (above)
		// during the run; intermediate "calling X…" lines added too
		// much noise on multi-tool turns. Web UI subscribes to chat
		// events directly via HandleWebChatStream and is unaffected.

		// Snapshot the workspace before the turn. The post-turn media
		// fallback must only attach files created by this turn; sandbox
		// hydration/sync can refresh the mtime of every historical file,
		// so a timestamp-only filter can resend the whole workspace.
		workspaceBefore, workspaceSnapshotOK := snapshotWorkspacePaths(ctx, g.workspace, task.AgentID, task.Message.ProjectID, task.Message.ChatID)

		// Attach a stream pipeline for web-channel bus-fired turns so
		// events reach the same SSE hub the user-typed path uses. No-op
		// when the hub isn't wired (e.g. CLI/test harness) or the
		// session can't be resolved — the OutboundMessage push below
		// still delivers the final reply.
		webStreamed := false
		if g.chatEvents != nil && task.Message.Channel == "web" {
			if sess := ag.Sessions().Get(task.Message.Channel, task.Message.AccountID, task.Message.ChatID, task.Message.ProjectID); sess != nil {
				ctx = agent.ContextWithStream(ctx, nil, g.store, g.chatEvents, task.OwnerUserID, task.AgentID, sess.SessionKey())
				webStreamed = true
			}
		}

		reply := ag.HandleMessage(ctx, task.Message)
		close(typingDone)
		// Extract `![alt](workspace/relative/path)` markdown image refs
		// from the agent's reply, resolve their bytes via the
		// workspace.Store, and ship them as MediaItems so IM channels
		// can upload as photos. The textual placeholders are stripped
		// from the body so users don't see the raw `![](...)` syntax.
		text, items := splitMediaFromReply(ctx, g.workspace, task.AgentID, task.Message.ProjectID, task.Message.ChatID, reply)
		// Ordinary markdown links are how agents expose non-image final
		// deliverables (`[report](/workspace/report.pdf)`). Resolve those
		// explicitly as attachments too. Besides fixing document delivery,
		// this makes the final reply authoritative: when it names one or more
		// outputs, the fallback below does not sweep up draft/process files.
		text, fileItems := splitFilesFromReply(ctx, g.workspace, task.AgentID, task.Message.ProjectID, task.Message.ChatID, text)
		items = append(items, fileItems...)
		// Workspace fallback: list the session's files and attach
		// every image whose mtime falls in this turn's window. Catches
		// the case where the LLM emits a broken data URL (with
		// truncated base64 / literal "..." placeholders) but
		// image-tool already saved the real file to /workspace. Dedupe
		// by filename so we don't double-send anything
		// splitMediaFromReply already resolved.
		// Skip fallback when splitMediaFromReply already extracted
		// images — the explicit markdown refs are authoritative and
		// the time-based scan can pick up stale files whose mtime
		// was refreshed by sandbox mount/restart.
		// WeChat must never infer deliverables from every file created during
		// the turn: document tools commonly write drafts/conversion inputs in
		// addition to the final output. Its final reply is authoritative and
		// the system prompt requires the agent to link each final deliverable.
		// Other channels retain the legacy safety net for compatibility.
		if len(items) == 0 && task.Message.Channel != "wechat" {
			items = appendNewWorkspaceMedia(ctx, g.workspace, task.AgentID, task.Message.ProjectID, task.Message.ChatID, workspaceBefore, workspaceSnapshotOK, items)
		}
		// Web-streamed turns already delivered the reply via the hub.
		// Skip the outbound push entirely when there's no media; with
		// media, push with empty text so attachments still flow but
		// the chat panel doesn't double-render the text.
		if webStreamed && len(items) == 0 {
			return reply, nil
		}
		outText := text
		if webStreamed {
			outText = ""
		}
		out := bus.OutboundMessage{
			Channel:      task.Message.Channel,
			AccountID:    task.AccountID,
			AgentID:      task.AgentID,
			ChatID:       task.Message.ChatID,
			Text:         outText,
			ReplyToMsgID: task.Message.MessageID,
			ParseMode:    "Markdown",
			MediaItems:   items,
			// AllowSplit lets the WeChat adapter honor SplitMessageMarker
			// for multi-bubble output. Sourced from the originating
			// agent's per-agent setting (or the system fallback baked
			// into it at boot) — see Agent.SplitReplies.
			AllowSplit: ag.SplitReplies(),
		}
		// Bounded enqueue. If routeOutbound is wedged the task
		// shouldn't sit on its taskQueue slot forever — let ctx's
		// task-timeout serve as the upper bound and drop the reply
		// rather than blocking the next inbound from this user.
		select {
		case mb.Outbound <- out:
		case <-ctx.Done():
			slog.Warn("outbound enqueue cancelled", "agent", task.AgentID, "chat", task.Message.ChatID)
		}
		return reply, nil
	})
	g.taskQueue = tq

	// Register all enabled channel rows from the DB.
	if err := registerChannelsFromStore(st, mb, chanMgr); err != nil {
		slog.Warn("registerChannelsFromStore", "error", err)
	}

	return g, nil
}

// UserSpaceFor returns the resolved user's UserSpace, lazy-loading on
// first call. There is no implicit/local user — userID must be a real
// users.id.
func (g *Gateway) UserSpaceFor(userID string) (*UserSpace, error) {
	return g.UserSpaceForCtx(context.Background(), userID)
}

// UserSpaceForCtx is the ctx-aware variant; HTTP handlers should prefer
// it so the underlying DB queries inherit the request deadline.
func (g *Gateway) UserSpaceForCtx(ctx context.Context, userID string) (*UserSpace, error) {
	if userID == "" {
		return nil, fmt.Errorf("UserSpaceFor: userID required")
	}
	return g.users.getOrLoad(ctx, userID)
}

// LocalAgentManager satisfies the api.UserResolver interface — but there
// is no longer a "local" pinned manager. Callers that legitimately need
// any agent manager should resolve the request's own user_id and call
// UserSpaceFor.
func (g *Gateway) LocalAgentManager() *agent.Manager { return nil }

// EnsureAgent loads an agent that does not belong to userID into that
// user's UserSpace. Used by super_admin chat handlers — see
// UserSpace.EnsureAgent.
func (g *Gateway) EnsureAgent(ctx context.Context, userID, agentID string) error {
	sp, err := g.UserSpaceForCtx(ctx, userID)
	if err != nil {
		return err
	}
	return sp.EnsureAgent(ctx, g.store, g.bus, g.workspace, agentID)
}

// IsCloudMode is retained for a few callers that still branch on it but
// always returns true now: multi-user is unconditional.
func (g *Gateway) IsCloudMode() bool { return true }

// Run starts the gateway and blocks until the process gets SIGINT/SIGTERM.
// On Unix, SIGHUP triggers a hot reload of every cached UserSpace so the
// next request picks up store mutations made by the CLI or another peer.
func (g *Gateway) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-stopCh
		slog.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	reloadCh := make(chan os.Signal, 1)
	notifyReloadSignal(reloadCh)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-reloadCh:
				slog.Info("received reload signal, reloading agents and scheduled jobs")
				if err := g.ReloadAgents(); err != nil {
					slog.Warn("agent reload failed", "error", err)
				}
				cron.NotifyJobCreated()
			}
		}
	}()

	var wg sync.WaitGroup
	if err := g.bus.Start(ctx); err != nil {
		return fmt.Errorf("start message bus: %w", err)
	}
	wg.Add(1)
	go func() { defer wg.Done(); g.users.startEvictor(ctx) }()
	wg.Add(1)
	go func() { defer wg.Done(); g.cleanupDedup(ctx) }()
	wg.Add(1)
	go func() { defer wg.Done(); g.processInbound(ctx) }()
	wg.Add(1)
	go func() { defer wg.Done(); g.chanMgr.Start(ctx) }()
	if g.scheduler != nil {
		wg.Add(1)
		go func() { defer wg.Done(); g.scheduler.Start(ctx) }()
	}
	if g.webhookSrv != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			port := readSystemHooks(g.store).Port
			if port == 0 {
				port = 18954
			}
			addr := fmt.Sprintf(":%d", port)
			if err := g.webhookSrv.ListenAndServe(ctx, addr); err != nil {
				slog.Error("webhook server error", "error", err)
			}
		}()
	}
	if g.pluginMgr != nil {
		if err := g.pluginMgr.StartAll(ctx); err != nil {
			slog.Error("plugin start error", "error", err)
		}
		for _, inst := range g.pluginMgr.ChannelPlugins() {
			adapter := plugin.NewChannelAdapter(g.pluginMgr, inst.Manifest.ID)
			g.chanMgr.Register(adapter)
		}
		plugin.RegisterPluginProviders(ctx, g.pluginMgr, toolProviderRegistry)
	}
	slog.Info("gateway started")
	wg.Wait()
	if g.taskQueue != nil {
		g.taskQueue.Stop()
	}
	if g.pluginMgr != nil {
		g.pluginMgr.StopAll()
	}
	if g.sandboxPool != nil {
		g.sandboxPool.CloseAll()
	}
	slog.Info("gateway stopped")
	return nil
}

// makeStoreFirstAgentFileLoader returns a loader that reads per-agent
// config from the agents.config column.
func makeStoreFirstAgentFileLoader(st store.Store) func(string, string) (config.AgentFileConfig, bool) {
	return func(agentID, _ string) (config.AgentFileConfig, bool) {
		if st == nil || agentID == "" {
			return config.AgentFileConfig{}, false
		}
		// We need user_id for GetAgent now; iterate every user is
		// expensive. Instead use ListAllAgents and pick.
		all, err := st.ListAllAgents(context.Background())
		if err != nil {
			return config.AgentFileConfig{}, false
		}
		for _, ar := range all {
			if ar.ID != agentID {
				continue
			}
			if len(ar.Config) == 0 {
				return config.AgentFileConfig{}, false
			}
			blob, _ := json.Marshal(ar.Config)
			var cfg config.AgentFileConfig
			if err := json.Unmarshal(blob, &cfg); err == nil {
				return cfg, true
			}
		}
		return config.AgentFileConfig{}, false
	}
}

func defaultStr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func redisPrefix(v string) string {
	v = strings.Trim(v, ":")
	if v == "" {
		return "fastclaw"
	}
	return v
}

// readObjectStoreCfg pulls the "objectstore" setting namespace, then
// layers FASTCLAW_OBJECT_STORE_* env vars on top.
func readObjectStoreCfg(st store.Store) config.ObjectStoreCfg {
	cfg := &config.Config{}
	if st != nil {
		_ = scope.SettingInto(context.Background(), st, NSObjectStore, "", "", &cfg.ObjectStore)
	}
	config.LoadEnv().ApplyToConfig(cfg)
	return cfg.ObjectStore
}

func readSystemHooks(st store.Store) config.HooksCfg {
	var out config.HooksCfg
	if st != nil {
		_ = scope.SettingInto(context.Background(), st, NSHooks, "", "", &out)
	}
	return out
}

func readSystemPlugins(st store.Store) config.PluginsCfg {
	var out config.PluginsCfg
	if st != nil {
		_ = scope.SettingInto(context.Background(), st, NSPlugins, "", "", &out)
	}
	return out
}

func readSystemTaskQueue(st store.Store) config.TaskQueueCfg {
	var out config.TaskQueueCfg
	if st != nil {
		_ = scope.SettingInto(context.Background(), st, NSTaskQueue, "", "", &out)
	}
	return out
}

// readSystemSandboxCfg reads the system-scope sandbox setting and
// merges FASTCLAW_SANDBOX_* env vars on top. Source of truth for the
// gateway-wide sandbox pool.
func readSystemSandboxCfg(st store.Store) config.SandboxCfg {
	cfg := &config.Config{}
	if st != nil {
		_ = scope.SettingInto(context.Background(), st, NSSandbox, "", "", &cfg.Sandbox)
	}
	config.LoadEnv().ApplyToConfig(cfg)
	return cfg.Sandbox
}

// Setting namespace constants. Each maps to one row in configs
// with kind="setting". Adding a new namespace is a one-line append; the
// scope.Setting / SettingInto helpers handle merging across scopes.
const (
	NSAgentDefaults    = "agents.defaults"
	NSSandbox          = "sandbox"
	NSObjectStore      = "objectstore"
	NSHooks            = "hooks"
	NSPlugins          = "plugins"
	NSTaskQueue        = "taskqueue"
	NSToolProviders    = "tools.providers"
	NSToolCategories   = "tools.categories"
	NSSkillsInstall    = "skills.install"
	NSSkillsEntries    = "skills.entries"
	NSMemory           = "memory"
	NSWorkspaceHistory = "workspaceHistory"
	NSPrivacy          = "privacy"
	NSSkillsLearner    = "skillsLearner"
	NSHeartbeat        = "heartbeat"
	NSTeams            = "teams"
	NSBindings         = "bindings"
)

// registerChannelsFromStore loads every enabled channel from the
// channels table and starts a channel adapter for each. Falls back
// to configs (kind='channel') when the channels table is empty (pre-
// migration installs). The owner is captured per-row and resolved at
// message receipt time via LookupChannel / LookupChannelByCredential.
func registerChannelsFromStore(st store.Store, mb *bus.MessageBus, chanMgr *channels.Manager) error {
	if st == nil {
		return nil
	}
	// Try the new channels table first.
	chRows, err := st.ListAllChannels(context.Background())
	if err != nil {
		slog.Warn("ListAllChannels failed, falling back to configs", "error", err)
		chRows = nil
	}
	if len(chRows) > 0 {
		for _, r := range chRows {
			if !r.Enabled {
				continue
			}
			if err := registerChannelFromRecord(r, mb, chanMgr, st, false); err != nil {
				slog.Warn("register channel failed",
					"type", r.Type, "user_id", r.UserID, "agent_id", r.AgentID, "error", err)
			}
		}
		return nil
	}
	// Fallback: read from configs for pre-migration installs.
	rows, err := allChannelRows(st)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if !r.Enabled {
			continue
		}
		if err := registerChannelInstance(r, mb, chanMgr, st, false); err != nil {
			slog.Warn("register channel failed",
				"type", r.Name, "user_id", r.UserID, "agent_id", r.AgentID, "error", err)
		}
	}
	return nil
}

// allChannelRows returns every channel row regardless of ownership —
// system rows ("", "") plus per-user, per-agent, and per-(user, agent)
// rows. The boot path needs the union so each owner's adapter is
// hot-started; per-row routing is decided later at message-receipt
// time via LookupChannelByCredential.
func allChannelRows(st store.Store) ([]store.ConfigRecord, error) {
	rows, err := st.QueryAllConfigs(context.Background(), store.KindChannel)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// imgRefRegex matches markdown image references `![alt](path)`. We
// keep capture groups for both alt and path so the helper below can
// reuse them when building MediaItems and stripping the marker from
// the chat body.
var imgRefRegex = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)

// fileRefRegex intentionally accepts only /workspace links. Normal web links
// remain in the message, and image syntax has already been removed by
// splitMediaFromReply before this matcher runs.
var fileRefRegex = regexp.MustCompile(`\[([^\]]*)\]\((/workspace/[^)]+)\)`)

// splitFilesFromReply resolves explicitly linked workspace documents into
// outbound attachments and removes the raw markdown link from IM text.
func splitFilesFromReply(ctx context.Context, ws workspace.Store, agentID, projectID, sessionID, reply string) (string, []bus.MediaItem) {
	if reply == "" || ws == nil {
		return reply, nil
	}
	matches := fileRefRegex.FindAllStringSubmatchIndex(reply, -1)
	if len(matches) == 0 {
		return reply, nil
	}
	var items []bus.MediaItem
	var out strings.Builder
	cursor := 0
	for _, m := range matches {
		path := reply[m[4]:m[5]]
		key := strings.TrimPrefix(path, "/workspace/")
		rc, err := ws.Get(ctx, agentID, projectID, sessionID, key)
		if err != nil {
			slog.Warn("split file: workspace get failed", "key", key, "error", err)
			continue
		}
		data, readErr := io.ReadAll(rc)
		rc.Close()
		if readErr != nil || len(data) == 0 || len(data) > maxAttachmentBytes {
			continue
		}
		base := filepath.Base(key)
		items = append(items, bus.MediaItem{Filename: base, ContentType: mime.TypeByExtension(filepath.Ext(base)), Bytes: data})
		out.WriteString(reply[cursor:m[0]])
		cursor = m[1]
		if cursor < len(reply) && reply[cursor] == '\n' {
			cursor++
		}
	}
	if len(items) == 0 {
		return reply, nil
	}
	out.WriteString(reply[cursor:])
	return strings.TrimSpace(out.String()), items
}

// splitMediaFromReply pulls every `![alt](src)` ref out of `reply` and
// turns it into a MediaItem the IM channel can upload directly:
//
//   - data:image/...;base64,…   → decode bytes inline, strip from text
//   - /workspace/foo or foo     → fetch via workspace.Store, strip from text
//   - http:// or https://       → left in place (some IMs auto-embed URLs)
//
// Refs whose bytes can't be resolved still get **stripped** from the
// output (otherwise a 200KB base64 data URL or a broken
// `![alt](missing)` lands as raw text in the chat — caused the
// "telegram dumps base64" report). When we strip, alt text is dropped
// too because the agent's prose around it usually stands on its own.
//
// sessionID = msgChatID since the gateway routes one chat per session.
func splitMediaFromReply(ctx context.Context, ws workspace.Store, agentID, projectID, sessionID, reply string) (string, []bus.MediaItem) {
	if reply == "" {
		return reply, nil
	}
	matches := imgRefRegex.FindAllStringSubmatchIndex(reply, -1)
	if len(matches) == 0 {
		return reply, nil
	}
	var items []bus.MediaItem
	var out strings.Builder
	cursor := 0
	for _, m := range matches {
		path := reply[m[4]:m[5]]

		// Remote URLs: keep the markdown ref intact so the IM client
		// can render its own preview. Don't strip, don't fetch.
		if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
			continue
		}

		var bytes []byte
		var filename string

		if strings.HasPrefix(path, "data:") {
			b, name, err := decodeDataURL(path)
			if err != nil {
				// Common case: LLM hallucinates a data URL with a
				// truncated/abbreviated base64 ("...", placeholders,
				// random fake bytes). Expected — log at Debug and
				// rely on the workspace fallback to still deliver
				// the real file. Don't spam Warn for this.
				head := path
				if len(head) > 80 {
					head = head[:80] + "…"
				}
				slog.Debug("data URL decode failed (LLM-fabricated bytes are expected — workspace fallback covers it)",
					"agent", agentID, "error", err, "len", len(path), "head", head)
			} else {
				bytes = b
				filename = name
			}
		} else if ws != nil {
			key := strings.TrimPrefix(path, "/workspace/")
			key = strings.TrimPrefix(key, "workspace/")
			key = strings.TrimPrefix(key, "/")
			if key != "" {
				rc, err := ws.Get(ctx, agentID, projectID, sessionID, key)
				if err != nil {
					slog.Warn("split media: workspace get failed", "agent", agentID, "project", projectID, "session", sessionID, "key", key, "error", err)
				} else {
					data, rerr := io.ReadAll(rc)
					rc.Close()
					if rerr != nil {
						slog.Warn("split media: read failed", "key", key, "error", rerr)
					} else {
						bytes = data
						filename = filepath.Base(key)
					}
				}
			}
		}

		if len(bytes) > 0 {
			if len(bytes) > maxAttachmentBytes {
				slog.Warn("split media: skipping oversize attachment",
					"agent", agentID, "session", sessionID,
					"filename", filename, "size", len(bytes), "cap", maxAttachmentBytes)
			} else {
				items = append(items, bus.MediaItem{
					Filename:    filename,
					ContentType: mime.TypeByExtension(filepath.Ext(filename)),
					Bytes:       bytes,
				})
			}
		}

		// Strip the `![alt](src)` either way — leaving an unresolvable
		// ref in the chat body just shows raw markdown / a base64 blob.
		out.WriteString(reply[cursor:m[0]])
		cursor = m[1]
		// Drop the trailing newline after the image ref if one
		// follows — keeps the body tidy when the LLM put the ref on
		// its own line.
		if cursor < len(reply) && reply[cursor] == '\n' {
			cursor++
		}
	}
	out.WriteString(reply[cursor:])
	return strings.TrimSpace(out.String()), items
}

// decodeDataURL parses `data:image/png;base64,...` style URLs into raw
// bytes. Returns (bytes, suggested filename, error). Extension is
// derived from the MIME so IMs that sniff content-type by filename
// (Telegram does for documents) get a sensible default.
func decodeDataURL(dataURL string) ([]byte, string, error) {
	if !strings.HasPrefix(dataURL, "data:") {
		return nil, "", fmt.Errorf("not a data URL")
	}
	rest := dataURL[len("data:"):]
	commaIdx := strings.IndexByte(rest, ',')
	if commaIdx < 0 {
		return nil, "", fmt.Errorf("data URL missing payload")
	}
	meta := rest[:commaIdx]
	payload := rest[commaIdx+1:]
	mimeType := "application/octet-stream"
	isBase64 := false
	for _, part := range strings.Split(meta, ";") {
		switch {
		case part == "base64":
			isBase64 = true
		case strings.Contains(part, "/"):
			mimeType = part
		}
	}
	var raw []byte
	if isBase64 {
		// LLMs frequently soft-wrap long base64 payloads, putting
		// whitespace mid-string that stock StdEncoding rejects with
		// "illegal base64 data". Strip whitespace before decode and
		// fall through alternative alphabets / paddings so the
		// agent's markdown survives any of the common variants:
		// standard, URL-safe, with or without padding.
		clean := stripWhitespace(payload)
		decoded, err := decodeBase64Tolerant(clean)
		if err != nil {
			return nil, "", fmt.Errorf("base64 decode: %w", err)
		}
		raw = decoded
	} else {
		// URL-encoded text payload — rare for images but handle it.
		u, err := url.QueryUnescape(payload)
		if err != nil {
			return nil, "", fmt.Errorf("url unescape: %w", err)
		}
		raw = []byte(u)
	}
	return raw, "media" + mimeExt(mimeType), nil
}

func stripWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func decodeBase64Tolerant(s string) ([]byte, error) {
	// Try the std-alphabet variants first (LLM-emitted base64 almost
	// always uses `+/`). URL-alphabet variants are kept as long-shot
	// fallbacks but listed last so the reported error is the
	// std-alphabet failure (much more informative — URL alphabets
	// fail at byte 0 the moment they hit a `/` character, which is
	// useless for diagnosis).
	candidates := []struct {
		name string
		enc  *base64.Encoding
	}{
		{"std", base64.StdEncoding},
		{"raw_std", base64.RawStdEncoding},
		{"url", base64.URLEncoding},
		{"raw_url", base64.RawURLEncoding},
	}
	var errs []string
	for _, c := range candidates {
		if data, err := c.enc.DecodeString(s); err == nil {
			return data, nil
		} else {
			errs = append(errs, c.name+": "+err.Error())
		}
	}
	return nil, fmt.Errorf("all base64 encodings failed (%s)", strings.Join(errs, "; "))
}

// maxAttachmentBytes caps per-file outbound attachments. Sized to fit
// under the tightest IM platform limit we care about (Discord free tier
// = 25MB) and well under the WeChat CDN-upload timeout's practical
// ceiling (90s leaves headroom for ~25MB over typical residential
// uplinks). Files past this are skipped + logged rather than truncated;
// the recipient sees no attachment but the chat-side text still goes
// through.
const maxAttachmentBytes = 25 * 1024 * 1024

// snapshotWorkspacePaths records the paths present before an agent turn.
// A failed snapshot disables the implicit fallback for that turn: sending
// no inferred attachment is safer than resending every historical artifact.
func snapshotWorkspacePaths(ctx context.Context, ws workspace.Store, agentID, projectID, sessionID string) (map[string]struct{}, bool) {
	if ws == nil {
		return nil, false
	}
	objs, err := ws.List(ctx, agentID, projectID, sessionID)
	if err != nil {
		slog.Warn("workspace pre-turn snapshot failed", "agent", agentID, "project", projectID, "session", sessionID, "error", err)
		return nil, false
	}
	paths := make(map[string]struct{}, len(objs))
	for _, obj := range objs {
		paths[obj.Path] = struct{}{}
	}
	return paths, true
}

// appendNewWorkspaceMedia lists the session's workspace and attaches
// every shippable file whose path did not exist before the turn. This is the
// IM-side guarantee that "if a tool wrote a deliverable this turn, the
// user receives it" — independent of whether the LLM's reply markdown
// referenced it correctly (broken data URLs, missing refs, hallucinated
// filenames all bypass this path).
//
// Filter rules:
//   - extension is in the deliverable allowlist (see isShippableExt):
//     images / video / audio / common document containers. Notably
//     EXCLUDES .md / .txt / .csv / .json / source files — those are
//     usually agent scratchpads (todo.md, plans, intermediate output)
//     and auto-shipping them would be noise, not value.
//   - path was absent from the pre-turn snapshot. Existing files are not
//     inferred, even if their mtime was refreshed by sandbox hydration.
//   - size <= maxAttachmentBytes (skipped + logged otherwise; we'd
//     blow channel limits or timeout the CDN upload).
//   - filename not already in `existing` (dedupe — splitMediaFromReply
//     may have already resolved it).
//
// Logs counts at every filter stage so a future "no file attached"
// report can be diagnosed from logs alone.
func appendNewWorkspaceMedia(ctx context.Context, ws workspace.Store, agentID, projectID, sessionID string, before map[string]struct{}, snapshotOK bool, existing []bus.MediaItem) []bus.MediaItem {
	if ws == nil || !snapshotOK {
		return existing
	}
	objs, err := ws.List(ctx, agentID, projectID, sessionID)
	if err != nil {
		slog.Warn("workspace list failed for media fallback",
			"agent", agentID, "project", projectID, "session", sessionID, "error", err)
		return existing
	}

	have := make(map[string]bool, len(existing))
	for _, it := range existing {
		have[it.Filename] = true
	}

	candidateCount := 0
	newCount := 0
	oversizeCount := 0
	attached := 0
	for _, obj := range objs {
		if !isShippableExt(obj.Path) {
			continue
		}
		candidateCount++
		if _, existed := before[obj.Path]; existed {
			continue
		}
		newCount++
		base := filepath.Base(obj.Path)
		if have[base] {
			continue
		}
		// Early-skip oversize using the listing's size hint. Stores
		// that don't track size (Size == -1) fall through to the
		// post-read check below.
		if obj.Size > 0 && obj.Size > maxAttachmentBytes {
			oversizeCount++
			slog.Warn("workspace media fallback: skipping oversize file",
				"agent", agentID, "session", sessionID,
				"path", obj.Path, "size", obj.Size, "cap", maxAttachmentBytes)
			continue
		}
		rc, gerr := ws.Get(ctx, agentID, projectID, sessionID, obj.Path)
		if gerr != nil {
			slog.Warn("workspace get failed for media fallback",
				"path", obj.Path, "error", gerr)
			continue
		}
		data, rerr := io.ReadAll(rc)
		rc.Close()
		if rerr != nil || len(data) == 0 {
			continue
		}
		if len(data) > maxAttachmentBytes {
			oversizeCount++
			slog.Warn("workspace media fallback: skipping oversize file (post-read)",
				"agent", agentID, "session", sessionID,
				"path", obj.Path, "size", len(data), "cap", maxAttachmentBytes)
			continue
		}
		existing = append(existing, bus.MediaItem{
			Filename:    base,
			ContentType: mime.TypeByExtension(filepath.Ext(base)),
			Bytes:       data,
		})
		have[base] = true
		attached++
	}
	slog.Info("workspace media fallback",
		"agent", agentID, "session", sessionID,
		"total_objs", len(objs), "candidates", candidateCount,
		"new", newCount, "oversize", oversizeCount,
		"attached", attached)
	return existing
}

// isShippableExt is the "is this a deliverable" allowlist used by the
// workspace media fallback. Conservative on purpose — auto-shipping
// every .md / .txt / .json the agent writes would surface internal
// scratchpads (todo.md, plans, intermediate stash) as chat
// attachments. Add new extensions only when their files almost always
// represent "something the user asked for."
func isShippableExt(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	// Images
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg":
		return true
	// Video
	case ".mp4", ".mov", ".webm", ".mkv", ".avi":
		return true
	// Audio
	case ".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac":
		return true
	// Document containers — finished deliverables, not scratchpads.
	case ".pdf", ".docx", ".xlsx", ".pptx", ".zip":
		return true
	}
	return false
}

// mimeExt picks a filename extension from a MIME type — minimal table
// covering what image-tool / replicate / OpenAI image gen actually
// emit. Falls back to .bin for anything unknown.
func mimeExt(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/svg+xml":
		return ".svg"
	}
	return ".bin"
}
