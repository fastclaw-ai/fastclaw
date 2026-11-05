package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/agl"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/usage"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// UserResolver looks up a user space by user ID.
type UserResolver interface {
	UserSpaceFor(userID string) (*UserSpaceView, error)
	LocalAgentManager() *agent.Manager
	IsCloudMode() bool
}

// AgentInjector is the optional capability for resolvers that can
// dynamically attach a foreign agent_id into a caller's UserSpace.
// Used by public-link chat, API-key access, and super_admin's explicit
// read-only ?actAs= audit flow. Implementations MUST be idempotent.
type AgentInjector interface {
	EnsureAgent(ctx context.Context, userID, agentID string) error
}

// UserSpaceView is the subset of gateway.UserSpace that the API layer needs.
type UserSpaceView struct {
	UserID string
	Agents *agent.Manager
	Config *config.Config
}

// Server handles the OpenAI-compatible API and WebSocket gateway.
type Server struct {
	resolver     UserResolver
	authResolver *auth.Resolver
	gatewayCfg   *config.GatewayCfg
	limiter      *rateLimiter
	meter        usage.Meter
	quotaStore   usage.QuotaStore
	// store backs the /v1/agents management API and strict agent
	// resolution. Nil in unit tests that only exercise chat plumbing.
	store store.Store
	// workspace holds conversation workspaces; /v1 returns the files an
	// agent produces from it. Nil disables file return.
	workspace workspace.Store
	// aglHub links end users' own computers (/v1/machines, /agl). Nil
	// leaves both off.
	aglHub  *agl.Hub
	acpMu   sync.Mutex
	acpRuns map[string]*acpRunState
}

// NewServer creates a new API server. authResolver is mandatory — there is
// no fallback "local" auth.
func NewServer(resolver UserResolver, authResolver *auth.Resolver, gatewayCfg *config.GatewayCfg) *Server {
	var rpm int
	if gatewayCfg != nil {
		rpm = gatewayCfg.RateLimit.RPM
	}
	return &Server{
		resolver:     resolver,
		authResolver: authResolver,
		gatewayCfg:   gatewayCfg,
		limiter:      newRateLimiter(rpm),
	}
}

// RegisterRoutes registers API routes on the given mux.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/ws", s.HandleWebSocket)
	mux.HandleFunc("OPTIONS /v1/", s.handleCORS)
	mux.HandleFunc("OPTIONS /acp/", s.handleCORS)

	getUserID := func(r *http.Request) string { return config.UserIDFromContext(r.Context()) }

	if s.gatewayCfg == nil || s.gatewayCfg.HTTP.Endpoints.ChatCompletions.Enabled {
		mux.HandleFunc("POST /v1/chat/completions",
			s.authMiddleware(rateLimitMiddleware(s.limiter, getUserID, s.HandleChatCompletions)))
	}
	if s.gatewayCfg == nil || s.gatewayCfg.HTTP.Endpoints.Agents.Enabled {
		// Agent management for integrating apps. Agents belong to the
		// api key's account (platform agents) or, when created for an
		// end-user, to that end-user (private agents). A request naming
		// an end-user sees the platform agents plus that end-user's
		// private ones — see agentAccessible.
		for pattern, h := range map[string]http.HandlerFunc{
			"GET /v1/agents":                                         s.HandleListAgents,
			"POST /v1/agents":                                        s.HandleCreateAgent,
			"GET /v1/agents/{id}":                                    s.HandleGetAgent,
			"PATCH /v1/agents/{id}":                                  s.HandleUpdateAgent,
			"DELETE /v1/agents/{id}":                                 s.HandleDeleteAgent,
			"PUT /v1/agents/{id}/system-files/{name}":                s.HandlePutAgentSystemFile,
			"GET /v1/agents/{id}/sessions/{session}/files/{path...}": s.HandleGetSessionFile,
		} {
			mux.HandleFunc(pattern, s.authMiddleware(rateLimitMiddleware(s.limiter, getUserID, h)))
		}
	}
	// Explicit provisioning of an app_user for a downstream end-user.
	// Always available — any api_key call can use the same identity-
	// switch (header or `user` body field) without precreating, this
	// endpoint just exists for callers that prefer to mint up front and
	// store the returned fastclaw user_id locally.
	mux.HandleFunc("POST /v1/users",
		s.authMiddleware(rateLimitMiddleware(s.limiter, getUserID, s.HandleProvisionAppUser)))

	// Billing: usage query + quota management. Available to any
	// authenticated api_key caller so upstream SaaS apps (weclaw etc.)
	// can pull consumption data and set per-user ceilings.
	mux.HandleFunc("GET /v1/usage",
		s.authMiddleware(rateLimitMiddleware(s.limiter, getUserID, s.HandleGetUsage)))
	mux.HandleFunc("PUT /v1/quota",
		s.authMiddleware(rateLimitMiddleware(s.limiter, getUserID, s.HandleSetQuota)))
	mux.HandleFunc("GET /v1/quota",
		s.authMiddleware(rateLimitMiddleware(s.limiter, getUserID, s.HandleGetQuota)))
	mux.HandleFunc("DELETE /v1/quota",
		s.authMiddleware(rateLimitMiddleware(s.limiter, getUserID, s.HandleDeleteQuota)))

	// Machines linked through agl ("connect my computer") and the agl
	// hub the machines dial into. See docs/agl-machines.md.
	if s.aglHub != nil {
		for pattern, h := range map[string]http.HandlerFunc{
			"POST /v1/machines/pairing":        s.HandleCreateMachinePairing,
			"POST /v1/machines/pairing/status": s.HandleMachinePairingStatus,
			"GET /v1/machines":                 s.HandleListMachines,
			"PATCH /v1/machines/{id}":          s.HandleUpdateMachine,
			"DELETE /v1/machines/{id}":         s.HandleDeleteMachine,
		} {
			mux.HandleFunc(pattern, s.authMiddleware(rateLimitMiddleware(s.limiter, getUserID, h)))
		}
		s.aglHub.RegisterRoutes(mux)
	}

	// Agent Communication Protocol (ACP) 0.2. The protocol defines root
	// paths such as /agents and /runs relative to a server base URL. FastClaw
	// exposes that base at /acp because /agents is already a dashboard route.
	s.registerACPRoutes(mux, getUserID)
}

// SetMeter installs the token usage meter for the /v1/usage endpoint.
func (s *Server) SetMeter(m usage.Meter) { s.meter = m }

// SetQuotaStore installs the quota store for /v1/quota endpoints.
func (s *Server) SetQuotaStore(qs usage.QuotaStore) { s.quotaStore = qs }

// SetStore installs the platform store used by /v1/agents and by strict
// agent resolution on /v1/chat/completions.
func (s *Server) SetStore(st store.Store) { s.store = st }

// SetWorkspaceStore installs the conversation workspace store used to
// return agent-produced files from /v1.
func (s *Server) SetWorkspaceStore(ws workspace.Store) { s.workspace = ws }

// RegisterAdminRoutes is kept as a no-op for callers that still call it
// during gateway boot. Admin user/apikey CRUD now lives under /api/admin
// in the setup server, which has proper cookie-session auth.
func (s *Server) RegisterAdminRoutes(mux *http.ServeMux) {}

func (s *Server) handleCORS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-fastclaw-agent-id, x-fastclaw-session-key, x-fastclaw-end-user")
	w.Header().Set("Access-Control-Expose-Headers", "Run-ID")
	w.Header().Set("Access-Control-Max-Age", "86400")
	w.WriteHeader(http.StatusNoContent)
}

// userSpaceFor resolves the user space from the request's identity.
func (s *Server) userSpaceFor(r *http.Request) (*UserSpaceView, error) {
	uid := config.UserIDFromContext(r.Context())
	if uid == "" {
		return nil, errors.New("unauthorized")
	}
	return s.resolver.UserSpaceFor(uid)
}

// authMiddleware validates the apikey/cookie and stamps the resolved
// identity onto ctx. Apikey-only endpoints can additionally check
// Identity.CanAccessAgent for the requested agentID.
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if s.authResolver == nil {
			writeUnauth(w, "auth resolver not configured")
			return
		}
		// Optional + our own check (instead of auth.Middleware) so a
		// missing/invalid key gets the unified /v1 error body.
		s.authResolver.Optional(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := auth.FromContext(r.Context()); !ok {
				writeUnauth(w, "missing or invalid credentials")
				return
			}
			next(w, r)
		})(w, r)
	}
}

func writeUnauth(w http.ResponseWriter, msg string) {
	writeAPIError(w, http.StatusUnauthorized, errTypeAuthentication, codeUnauthorized, msg)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
