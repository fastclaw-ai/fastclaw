package setup

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/api"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/channels"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/push"
	"github.com/fastclaw-ai/fastclaw/internal/runtime"
	"github.com/fastclaw-ai/fastclaw/internal/session"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/taskqueue"
	"github.com/fastclaw-ai/fastclaw/internal/usage"
	"github.com/fastclaw-ai/fastclaw/internal/users"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// AgentHandle is the surface the web UI uses to talk to a running agent.
type AgentHandle interface {
	Name() string
	HandleWebChat(ctx context.Context, sessionId, projectIdHint, userID, text string, imageURLs []string, params map[string]any) string
	HandleWebChatStream(ctx context.Context, sessionId, projectIdHint, userID, text string, imageURLs []string, params map[string]any, events chan<- agent.ChatEvent) string
	// SteerWeb buffers a message into an in-flight turn for the session;
	// returns false when no turn is running (caller falls back to a
	// normal send).
	SteerWeb(sessionId, projectIDHint, text string) bool
	WebChatHistory(sessionId string) []map[string]any
	WebChatSessions() []session.WebSession
	DeleteWebChatSession(sessionId string) error
	RenameWebChatSession(sessionId, title string) error
	// MoveWebChatSession reassigns the chat to a different project (or
	// detaches when projectID==""). Migrates workspace files between
	// the old and new scope dirs and releases any active sandbox so
	// the next turn cold-starts at the new bind-mount path.
	MoveWebChatSession(ctx context.Context, sessionId, projectID string) error
	ReloadWorkspaceFiles()
	// WriteSessionAttachments materializes user-uploaded bytes (data
	// URLs / HTTPS URLs) into the agent's session workspace so skills can
	// read them via /workspace/<filename>. Returns one AttachmentResult
	// per successfully written item (input order, each tagged with its
	// input Index); per-item errors are skipped. Images are sniffed,
	// policy-gated and compressed inside — downgraded images are stored
	// but flagged not vision-safe (see agent.VisionGate).
	WriteSessionAttachments(ctx context.Context, sessionID, projectID string, atts []agent.Attachment) []agent.AttachmentResult
	// RegisteredTools returns the live tool registry projection — what
	// this agent currently has loaded (built-ins + MCP + plugin tools).
	// Used by the Tools tab to render the allowlist checkbox picker.
	RegisteredTools() []tools.ToolInfo
}

// AgentProvider is implemented by gateway.UserSpace's agent manager — used
// by handlers that legitimately need to enumerate the *current caller's*
// agents (resolved through the user resolver, not from a global pool).
type AgentProvider interface {
	AllAgents() []AgentHandle
	AgentByID(id string) AgentHandle
	ReloadAgents() error
}

// Server hosts the web UI + admin API. Multi-user is unconditional —
// every request must resolve to a real users.id via the auth.Resolver.
type Server struct {
	port           int
	bind           string
	gatewayCfg     *config.GatewayCfg
	userResolver   api.UserResolver
	taskQueue      *taskqueue.Queue
	apiServer      *api.Server
	authResolver   *auth.Resolver
	accounts       *users.Accounts
	apikeys        *users.APIKeys
	dataStore      store.Store
	workspaceStore workspace.Store
	webChan        *channels.WebChannel
	pushClient     *push.APNSClient
	// chatEvents fans live agent chat events out to subscribed SSE
	// clients across browser tabs. Lazy-init on first use so older
	// callers that didn't wire it explicitly still work.
	chatEvents *agent.EventHub
	usage      usage.Meter
	startedAt  time.Time
	// runtimeMgr powers the coding-agent project runtime (live dev server
	// + preview). Optional: nil when the deployment hasn't wired a
	// sandbox-backed runtime, in which case the /runtime endpoints return
	// 503 instead of nil-panicking. Set via SetRuntimeManager at boot.
	runtimeMgr *runtime.Manager
}

// SetRuntimeManager wires the project runtime manager. Call once at boot
// after constructing the Server; leaving it unset disables the coding-
// agent preview endpoints (they 503).
func (s *Server) SetRuntimeManager(m *runtime.Manager) { s.runtimeMgr = m }

// NewServer creates a setup wizard server on the given port.
func NewServer(port int) *Server {
	return &Server{port: port, bind: "loopback", startedAt: time.Now()}
}

// SetGatewayConfig sets the gateway configuration for bind address and HTTP endpoints.
func (s *Server) SetGatewayConfig(cfg *config.GatewayCfg) {
	s.gatewayCfg = cfg
	if cfg.Bind != "" {
		s.bind = cfg.Bind
	}
	if cfg.Port > 0 {
		s.port = cfg.Port
	}
}

// SetTaskQueue sets the task queue for the tasks API endpoint.
func (s *Server) SetTaskQueue(tq *taskqueue.Queue) {
	s.taskQueue = tq
}

// SetAPIServer sets the OpenAI-compatible API server for /v1/* and /ws routes.
func (s *Server) SetAPIServer(apiSrv *api.Server) {
	s.apiServer = apiSrv
}

// SetUserResolver sets the per-user agent routing resolver.
func (s *Server) SetUserResolver(resolver api.UserResolver) {
	s.userResolver = resolver
}

// SetStore sets the storage backend.
func (s *Server) SetStore(st store.Store) {
	s.dataStore = st
	if st != nil {
		s.accounts, _ = users.NewAccounts(st)
		s.apikeys, _ = users.NewAPIKeys(st)
	}
	if s.pushClient == nil {
		s.pushClient = push.NewAPNSClientFromEnv()
	}
}

// SetWorkspaceStore installs the blob store used for agent-generated artifacts.
func (s *Server) SetWorkspaceStore(ws workspace.Store) {
	s.workspaceStore = ws
}

// SetUsageMeter installs the per-tenant resource counter.
func (s *Server) SetUsageMeter(m usage.Meter) {
	s.usage = m
}

// SetAuth installs the auth resolver. Required.
func (s *Server) SetAuth(resolver *auth.Resolver) {
	s.authResolver = resolver
}

// SetWebChannel installs the in-process fan-out used by the SSE
// subscription endpoint. When set, /api/chat/subscribe holds an SSE
// stream open per (agent, session) pair and forwards every outbound
// message routed to channel="web" — this is what surfaces cron-fired
// agent replies live in the dashboard chat panel.
func (s *Server) SetWebChannel(wc *channels.WebChannel) {
	s.webChan = wc
	if wc != nil {
		wc.SetPushHandler(func(msg bus.OutboundMessage) {
			go s.handleWebPushOutbound(msg)
		})
	}
}

// chatEventHub returns the lazy-initialized hub. Centralized so every
// chat handler reaches the same instance — without this, the streaming
// handler's hub publish would never reach the subscribe handler.
func (s *Server) chatEventHub() *agent.EventHub {
	if s.chatEvents == nil {
		s.chatEvents = agent.NewEventHub()
	}
	return s.chatEvents
}

// ChatEventHub exposes the hub so the gateway can attach a stream
// pipeline onto bus-fired web turns (cron / goal continuation /
// heartbeat / sub-agent), giving them the same SSE streaming a
// user-typed turn gets. Wraps chatEventHub's lazy-init.
func (s *Server) ChatEventHub() *agent.EventHub { return s.chatEventHub() }

// authMiddleware wraps the auth.Resolver's Middleware. Required for every
// authenticated route.
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	if s.authResolver == nil {
		return func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "auth not configured"})
		}
	}
	return s.authResolver.Middleware(next)
}

// optionalAuth is the bootstrap-friendly variant for endpoints reachable
// before login (status, login, onboard).
func (s *Server) optionalAuth(next http.HandlerFunc) http.HandlerFunc {
	if s.authResolver == nil {
		return next
	}
	return s.authResolver.Optional(next)
}

// requireSuperAdmin gates handlers behind platform-admin authority:
// either a super_admin session or a type=admin apikey. Despite the name,
// this accepts both — the apikey path is the only way a programmatic
// admin client can hit /api/admin/* without a browser cookie. Use
// auth.RequireSuperAdmin directly for the rare cases that need the
// session-only flavor.
func (s *Server) requireSuperAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.authMiddleware(auth.RequirePlatformAdmin(next))
}

// Run starts the HTTP server and blocks until the context is canceled.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()

	// Health probes (unauthenticated).
	healthz := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /livez", healthz)
	mux.HandleFunc("GET /readyz", healthz)

	auth := s.authMiddleware
	opt := s.optionalAuth
	admin := s.requireSuperAdmin

	// Bootstrap / login.
	mux.HandleFunc("GET /api/status", opt(s.handleStatus))
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", auth(s.handleLogout))
	mux.HandleFunc("GET /api/me", auth(s.handleMe))
	mux.HandleFunc("PUT /api/me", auth(s.handleUpdateMe))
	mux.HandleFunc("POST /api/me/avatar", auth(s.handleUploadMyAvatar))
	mux.HandleFunc("POST /api/me/password", auth(s.handleChangeMyPassword))
	mux.HandleFunc("POST /api/push/devices", auth(s.handleSavePushDevice))
	mux.HandleFunc("DELETE /api/push/devices/{token}", auth(s.handleDeletePushDevice))
	mux.HandleFunc("POST /api/test-provider", opt(s.handleTestProvider))
	mux.HandleFunc("POST /api/onboard", s.handleOnboard)
	mux.HandleFunc("POST /api/register", s.handleRegister)
	mux.HandleFunc("GET /api/public/agents", s.handlePublicAgents)
	mux.HandleFunc("GET /api/public/skills", s.handlePublicSkills)
	mux.HandleFunc("GET /api/admin/registration", admin(s.handleGetRegistration))
	mux.HandleFunc("PUT /api/admin/registration", admin(s.handleSetRegistration))
	mux.HandleFunc("GET /api/admin/chats", admin(s.handleAdminChats))

	// Per-user config (system_settings + scoped providers/channels).
	mux.HandleFunc("GET /api/config", auth(s.handleGetConfig))
	mux.HandleFunc("POST /api/config", auth(s.handleUpdateConfig))

	// Chat
	mux.HandleFunc("POST /api/chat", auth(s.handleChat))
	mux.HandleFunc("POST /api/chat/stream", auth(s.handleChatStream))
	mux.HandleFunc("POST /api/chat/team/stream", auth(s.handleTeamChatStream))
	mux.HandleFunc("POST /api/chat/steer", auth(s.handleChatSteer))
	mux.HandleFunc("GET /api/chats", auth(s.handleChats))
	mux.HandleFunc("GET /api/chat/history", auth(s.handleChatHistory))
	mux.HandleFunc("GET /api/chat/todo", auth(s.handleChatTodo))
	mux.HandleFunc("GET /api/chat/sessions", auth(s.handleChatSessions))
	mux.HandleFunc("PUT /api/chat/sessions/{key}", auth(s.handleRenameSession))
	mux.HandleFunc("DELETE /api/chat/sessions/{key}", auth(s.handleDeleteSession))
	mux.HandleFunc("PATCH /api/chat/sessions/{key}/project", auth(s.handleMoveSessionProject))
	// Long-lived SSE subscription so cron-fired (and other async)
	// messages reach the open chat panel without a manual refresh.
	mux.HandleFunc("GET /api/chat/subscribe", auth(s.handleChatSubscribe))

	// Agents
	mux.HandleFunc("GET /api/agents", auth(s.handleListAgents))
	mux.HandleFunc("POST /api/agents", auth(s.handleCreateAgent))
	mux.HandleFunc("GET /api/agents/{id}", auth(s.handleGetAgent))
	mux.HandleFunc("PUT /api/agents/{id}", auth(s.handleUpdateAgent))
	mux.HandleFunc("GET /api/agents/{id}/config", auth(s.handleGetAgentConfig))
	mux.HandleFunc("GET /api/agents/{id}/tools/registered", auth(s.handleListAgentRegisteredTools))
	mux.HandleFunc("DELETE /api/agents/{id}", auth(s.handleDeleteAgent))

	mux.HandleFunc("GET /api/agents/{id}/files", auth(s.handleAgentFileList))
	mux.HandleFunc("GET /api/agents/{id}/files.zip", auth(s.handleAgentFilesZip))
	mux.HandleFunc("GET /api/agents/{id}/files/{path...}", auth(s.handleAgentFile))
	mux.HandleFunc("POST /api/agents/{id}/files", auth(s.handleAgentFileUpload))
	mux.HandleFunc("GET /api/agents/{id}/sessions/{sessionId}/history", auth(s.handleAgentSessionHistory))
	mux.HandleFunc("POST /api/agents/{id}/sessions/{sessionId}/history/restore", auth(s.handleAgentSessionHistoryRestore))
	// Self-hosted-only: opens the workspace dir in the operator's
	// native file browser (Finder/Explorer/xdg-open). Hosted
	// deployments 403 inside the handler — chatters there don't
	// own the daemon's filesystem.
	mux.HandleFunc("POST /api/agents/{id}/workspace/reveal", auth(s.handleAgentWorkspaceReveal))

	mux.HandleFunc("GET /api/agents/{id}/system-files/{name}", auth(s.handleGetAgentSystemFile))
	mux.HandleFunc("PUT /api/agents/{id}/system-files/{name}", auth(s.handlePutAgentSystemFile))
	mux.HandleFunc("DELETE /api/agents/{id}/system-files/{name}", auth(s.handleDeleteAgentSystemFile))
	mux.HandleFunc("GET /api/agents/{id}/knowledge-files", auth(s.handleListAgentKnowledgeFiles))
	mux.HandleFunc("POST /api/agents/{id}/knowledge-files", auth(s.handleUploadAgentKnowledgeFile))
	mux.HandleFunc("GET /api/agents/{id}/knowledge-files/{name}", auth(s.handleGetAgentKnowledgeFile))
	mux.HandleFunc("DELETE /api/agents/{id}/knowledge-files/{name}", auth(s.handleDeleteAgentKnowledgeFile))

	// Per-agent projects: named workspace folders that group chats and
	// share files across all sessions inside them. POST .../sessions
	// is the "New chat in project" path — pre-creates the session row
	// stamped with project_id so the very first turn already routes
	// workspace IO to projects/<pid>/.
	mux.HandleFunc("GET /api/agents/{id}/projects", auth(s.handleListProjects))
	mux.HandleFunc("POST /api/agents/{id}/projects", auth(s.handleCreateProject))
	mux.HandleFunc("PATCH /api/agents/{id}/projects/{pid}", auth(s.handleUpdateProject))
	mux.HandleFunc("DELETE /api/agents/{id}/projects/{pid}", auth(s.handleDeleteProject))

	// Project runtime: the coding-agent "live app" layer on top of a
	// project — a long-lived dev-server sandbox + preview URL. The
	// upstream SaaS shell drives a project entirely through these.
	mux.HandleFunc("GET /api/agents/{id}/projects/{pid}/runtime", auth(s.handleGetRuntime))
	mux.HandleFunc("POST /api/agents/{id}/projects/{pid}/runtime/up", auth(s.handleRuntimeUp))
	mux.HandleFunc("POST /api/agents/{id}/projects/{pid}/runtime/sleep", auth(s.handleRuntimeSleep))
	mux.HandleFunc("POST /api/agents/{id}/projects/{pid}/runtime/wake", auth(s.handleRuntimeWake))
	mux.HandleFunc("DELETE /api/agents/{id}/projects/{pid}/runtime", auth(s.handleRuntimeStop))
	mux.HandleFunc("GET /api/agents/{id}/projects/{pid}/preview", auth(s.handleRuntimePreview))
	// Scope-flexible preview lookup (sessionId or projectId query param) —
	// lets the chat workspace panel surface an "open preview" entry for the
	// current chat, including loose chats that have no project.
	mux.HandleFunc("GET /api/agents/{id}/preview", auth(s.handleScopePreview))
	// Scope-flexible build/dev log tail — the preview panel polls this while
	// the app scaffolds so the user sees the live pnpm-install output.
	mux.HandleFunc("GET /api/agents/{id}/preview/logs", auth(s.handleScopePreviewLogs))
	// Files the agent changed vs the template baseline (git diff in the
	// running app) — lets the workspace tree show only this task's output.
	mux.HandleFunc("GET /api/agents/{id}/changed-files", auth(s.handleChangedFiles))
	mux.HandleFunc("GET /api/agents/{id}/projects/{pid}/runtime/logs", auth(s.handleRuntimeLogs))

	// Per-agent channels (IM bot bindings)
	mux.HandleFunc("GET /api/agents/{id}/channels", auth(s.handleListAgentChannels))
	mux.HandleFunc("POST /api/agents/{id}/channels/telegram", auth(s.handleConnectAgentTelegram))
	mux.HandleFunc("POST /api/agents/{id}/channels/discord", auth(s.handleConnectAgentDiscord))
	mux.HandleFunc("POST /api/agents/{id}/channels/slack", auth(s.handleConnectAgentSlack))
	mux.HandleFunc("POST /api/agents/{id}/channels/wechat/login", auth(s.handleStartAgentWeChatLogin))
	mux.HandleFunc("GET /api/agents/{id}/channels/wechat/login/status", auth(s.handleAgentWeChatLoginStatus))
	mux.HandleFunc("POST /api/agents/{id}/channels/line", auth(s.handleConnectAgentLINE))
	mux.HandleFunc("POST /api/agents/{id}/channels/feishu", auth(s.handleConnectAgentFeishu))
	mux.HandleFunc("DELETE /api/agents/{id}/channels/{type}/{accountId}", auth(s.handleDisconnectAgentChannel))
	mux.HandleFunc("PATCH /api/agents/{id}/channels/{type}/{accountId}", auth(s.handleUpdateAgentChannel))

	// Feishu (飞书) event webhook. UNAUTHENTICATED — Feishu posts here
	// without a fastclaw bearer token. Per-event security comes from
	// the verification_token validated inside the adapter against the
	// payload's header.token. The {appId} path segment scopes the
	// receive to one registered channel.
	mux.HandleFunc("POST /api/feishu/webhook/{appId}", s.handleFeishuWebhook)

	// LINE Messaging API event webhook. UNAUTHENTICATED at the fastclaw
	// layer — per-event security is HMAC-SHA256(channel_secret, body)
	// validated by the adapter against the `x-line-signature` header.
	// The {accountId} path segment is the bot's userId, scoping the
	// receive to one registered channel.
	mux.HandleFunc("POST /api/line/webhook/{accountId}", s.handleLINEWebhook)

	// Skills
	mux.HandleFunc("GET /api/skills", auth(s.handleListSkills))
	mux.HandleFunc("GET /api/skills/search", auth(s.handleSearchSkills))
	mux.HandleFunc("POST /api/skills/install", auth(s.handleInstallSkill))
	mux.HandleFunc("POST /api/skills/upload", auth(s.handleUploadSkill))
	mux.HandleFunc("DELETE /api/skills/{name}", admin(s.handleDeleteSkill))
	mux.HandleFunc("GET /api/agents/{id}/skills", auth(s.handleListAgentSkills))
	mux.HandleFunc("DELETE /api/agents/{id}/skills/{name}", auth(s.handleDeleteAgentSkill))

	// Plugins (super_admin only).
	mux.HandleFunc("GET /api/plugins", admin(s.handleListPlugins))
	mux.HandleFunc("PUT /api/plugins/{id}", admin(s.handleUpdatePlugin))
	// Hook plugin discovery — read-only metadata for the per-agent
	// Plugins toggle on the Context page. Agent owners (not just
	// admins) need this to know what plugins they can enable.
	mux.HandleFunc("GET /api/plugins/hook", auth(s.handleListHookPlugins))

	// Tools (super_admin only).
	mux.HandleFunc("GET /api/tools", admin(s.handleGetTools))
	mux.HandleFunc("PUT /api/tools", admin(s.handleSaveTools))

	// Channels (read-only list of registered channel adapters at runtime)
	mux.HandleFunc("GET /api/channels", auth(s.handleListChannels))

	// Scoped CRUD: providers + channels at system / user / agent scope.
	mux.HandleFunc("GET /api/providers", auth(s.handleListProviders))
	mux.HandleFunc("POST /api/providers", auth(s.handleCreateProvider))
	mux.HandleFunc("PUT /api/providers/{id}", auth(s.handleUpdateProvider))
	mux.HandleFunc("DELETE /api/providers/{id}", auth(s.handleDeleteProvider))
	mux.HandleFunc("POST /api/providers/{id}/test", auth(s.handleTestStoredProvider))
	mux.HandleFunc("GET /api/scoped-channels", auth(s.handleListScopedChannels))
	mux.HandleFunc("POST /api/scoped-channels", auth(s.handleCreateScopedChannel))
	mux.HandleFunc("PUT /api/scoped-channels/{id}", auth(s.handleUpdateScopedChannel))
	mux.HandleFunc("DELETE /api/scoped-channels/{id}", auth(s.handleDeleteScopedChannel))

	// Cron jobs (per-user, config-defined catalog)
	mux.HandleFunc("GET /api/cron", auth(s.handleListCronJobs))
	mux.HandleFunc("POST /api/cron", auth(s.handleCreateCronJob))
	mux.HandleFunc("PUT /api/cron/{id}", auth(s.handleUpdateCronJob))
	mux.HandleFunc("DELETE /api/cron/{id}", auth(s.handleDeleteCronJob))

	// Per-agent cron jobs (DB-backed, includes anything the agent
	// scheduled itself via create_cron_job at runtime).
	mux.HandleFunc("GET /api/agents/{id}/cron", auth(s.handleListAgentCronJobs))
	mux.HandleFunc("DELETE /api/agents/{id}/cron/{jobId}", auth(s.handleDeleteAgentCronJob))
	mux.HandleFunc("PUT /api/agents/{id}/cron/{jobId}", auth(s.handleToggleAgentCronJob))

	// Tasks
	mux.HandleFunc("GET /api/tasks", admin(s.handleListTasks))

	// Apikeys (per-user, with agent multi-select).
	mux.HandleFunc("GET /api/apikeys", auth(s.handleListAPIKeys))
	mux.HandleFunc("POST /api/apikeys", auth(s.handleCreateAPIKey))
	mux.HandleFunc("DELETE /api/apikeys/{id}", auth(s.handleDeleteAPIKey))
	mux.HandleFunc("POST /api/apikeys/{id}/rotate", auth(s.handleRotateAPIKey))
	mux.HandleFunc("PUT /api/apikeys/{id}/agents", auth(s.handleSetAPIKeyAgents))

	// Users — flat resource paths. Top-level CRUD is admin-only;
	// nested {id}/apikeys + {id}/agents accept admin-or-self
	// (gated in-handler via requireUserOrAdmin).
	mux.HandleFunc("GET /api/users", admin(s.handleListUsers))
	mux.HandleFunc("POST /api/users", admin(s.handleCreateUser))
	mux.HandleFunc("PUT /api/users/{id}", admin(s.handleUpdateUser))
	mux.HandleFunc("DELETE /api/users/{id}", admin(s.handleDeleteUser))
	mux.HandleFunc("POST /api/users/{id}/password", admin(s.handleResetUserPassword))
	mux.HandleFunc("POST /api/users/{id}/apikeys", auth(s.handleCreateUserAPIKey))
	mux.HandleFunc("GET /api/users/{id}/agents", auth(s.handleListUserAgents))
	mux.HandleFunc("POST /api/users/{id}/agents", auth(s.handleCreateUserAgent))
	// Cross-tenant agent list moved into /api/agents?all=true (admin-only
	// when the param is set). /api/usage replaces /api/admin/usage.
	mux.HandleFunc("GET /api/usage", admin(s.handleGetUsage))
	// Per-agent usage: owner-gated (or super_admin) inside the handler
	// itself via requireAgentOwner, so we use the plain auth wrapper here.
	mux.HandleFunc("GET /api/agents/{id}/usage", auth(s.handleGetAgentUsage))

	// OpenAI-compatible API and WebSocket gateway.
	if s.apiServer != nil {
		s.apiServer.RegisterRoutes(mux)
	}

	// Static UI files.
	webRoot, err := fs.Sub(webFS, "web")
	if err != nil {
		return fmt.Errorf("setup: embed sub: %w", err)
	}
	mux.Handle("/", spaHandler{fs: webRoot})

	var addr string
	if s.bind == "all" {
		addr = fmt.Sprintf("0.0.0.0:%d", s.port)
	} else {
		addr = fmt.Sprintf("127.0.0.1:%d", s.port)
	}
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("setup: listen %s: %w", addr, err)
	}
	slog.Info("web UI running", "url", fmt.Sprintf("http://localhost:%d", s.port))
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// spaHandler serves the embedded Next.js UI with SPA-style fallback.
type spaHandler struct {
	fs fs.FS
}

func (h spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path != "/" && strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}
	fsPath := strings.TrimPrefix(path, "/")
	if fsPath == "" {
		fsPath = "."
	}
	if f, err := h.fs.Open(fsPath); err == nil {
		stat, statErr := f.Stat()
		f.Close()
		if statErr == nil && !stat.IsDir() {
			http.ServeFileFS(w, r, h.fs, fsPath)
			return
		}
	}
	var indexPath string
	if fsPath == "." {
		indexPath = "index.html"
	} else {
		indexPath = fsPath + "/index.html"
	}
	if f, err := h.fs.Open(indexPath); err == nil {
		f.Close()
		http.ServeFileFS(w, r, h.fs, indexPath)
		return
	}
	if strings.HasPrefix(fsPath, "agents/") {
		parts := strings.SplitN(fsPath, "/", 3)
		if len(parts) >= 3 && parts[1] != "default" {
			directFallback := "agents/default/" + parts[2]
			if f, err := h.fs.Open(directFallback); err == nil {
				stat, statErr := f.Stat()
				f.Close()
				if statErr == nil && !stat.IsDir() {
					http.ServeFileFS(w, r, h.fs, directFallback)
					return
				}
			}
			dirFallback := "agents/default/" + parts[2] + "/index.html"
			if f, err := h.fs.Open(dirFallback); err == nil {
				f.Close()
				http.ServeFileFS(w, r, h.fs, dirFallback)
				return
			}
			// Nested dynamic segment fallback: routes like
			// agents/[id]/chat/[session] and agents/[id]/project/[pid]
			// emit a single placeholder ("_") at build time. Substitute
			// "_" for any segment that sits immediately under a known
			// dynamic-parent (chat, project), regardless of what
			// follows. This covers BOTH the page HTML
			//   /chat/<sid>/                            → /chat/_/index.html
			// AND the per-route RSC payloads Next 16 fetches during
			// client-side navigation:
			//   /chat/<sid>/index.txt                   → /chat/_/index.txt
			//   /chat/<sid>/__next.agents.$d$id.chat.$d$session.__PAGE__.txt
			//   …
			// Without this, App Router's RSC fetch on a sidebar click
			// gets a 404 (or the root index.html), gives up on soft
			// navigation, and falls back to window.location — which
			// flickers the page and tears down any in-flight stream.
			// Add new dynamic routes to dynamicParents below as they
			// get introduced.
			dynamicParents := map[string]bool{"chat": true, "project": true}
			sub := strings.Split(parts[2], "/")
			substituted := false
			for i := 0; i < len(sub)-1; i++ {
				if dynamicParents[sub[i]] && sub[i+1] != "_" {
					sub[i+1] = "_"
					substituted = true
				}
			}
			if substituted {
				placeholder := "agents/default/" + strings.Join(sub, "/")
				if f, err := h.fs.Open(placeholder); err == nil {
					stat, statErr := f.Stat()
					f.Close()
					if statErr == nil && !stat.IsDir() {
						http.ServeFileFS(w, r, h.fs, placeholder)
						return
					}
				}
				placeholderIndex := placeholder + "/index.html"
				if f, err := h.fs.Open(placeholderIndex); err == nil {
					f.Close()
					http.ServeFileFS(w, r, h.fs, placeholderIndex)
					return
				}
			}
		}
	}
	htmlPath := fsPath + ".html"
	if f, err := h.fs.Open(htmlPath); err == nil {
		f.Close()
		http.ServeFileFS(w, r, h.fs, htmlPath)
		return
	}
	http.ServeFileFS(w, r, h.fs, "index.html")
}
