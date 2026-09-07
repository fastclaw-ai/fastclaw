package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/usage"
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

	getUserID := func(r *http.Request) string { return config.UserIDFromContext(r.Context()) }

	if s.gatewayCfg == nil || s.gatewayCfg.HTTP.Endpoints.ChatCompletions.Enabled {
		mux.HandleFunc("POST /v1/chat/completions",
			s.authMiddleware(rateLimitMiddleware(s.limiter, getUserID, s.HandleChatCompletions)))
	}
	if s.gatewayCfg == nil || s.gatewayCfg.HTTP.Endpoints.Agents.Enabled {
		mux.HandleFunc("GET /v1/agents",
			s.authMiddleware(rateLimitMiddleware(s.limiter, getUserID, s.HandleListAgents)))
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
}

// SetMeter installs the token usage meter for the /v1/usage endpoint.
func (s *Server) SetMeter(m usage.Meter) { s.meter = m }

// SetQuotaStore installs the quota store for /v1/quota endpoints.
func (s *Server) SetQuotaStore(qs usage.QuotaStore) { s.quotaStore = qs }

// RegisterAdminRoutes is kept as a no-op for callers that still call it
// during gateway boot. Admin user/apikey CRUD now lives under /api/admin
// in the setup server, which has proper cookie-session auth.
func (s *Server) RegisterAdminRoutes(mux *http.ServeMux) {}

func (s *Server) handleCORS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-fastclaw-agent-id, x-fastclaw-session-key")
	w.Header().Set("Access-Control-Max-Age", "86400")
	w.WriteHeader(http.StatusNoContent)
}

// HandleListAgents handles GET /v1/agents. Returns only the agents this
// caller is authorized for.
func (s *Server) HandleListAgents(w http.ResponseWriter, r *http.Request) {
	space, err := s.userSpaceFor(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": map[string]string{"message": err.Error(), "type": "authentication_error"},
		})
		return
	}
	ident, _ := auth.FromContext(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"agents": buildAgentList(space, ident)})
}

func buildAgentList(space *UserSpaceView, ident auth.Identity) []map[string]string {
	all := space.Agents.All()
	modelMap := make(map[string]string)
	if space.Config != nil {
		for _, ra := range config.ResolveAgents(space.Config, nil) {
			modelMap[ra.ID] = ra.Model
		}
	}
	agents := make([]map[string]string, 0, len(all))
	for _, ag := range all {
		if !ident.CanAccessAgent(ag.Name()) {
			continue
		}
		model := ag.Model()
		if model == "" {
			model = modelMap[ag.Name()]
		}
		agents = append(agents, map[string]string{
			"id":    ag.Name(),
			"name":  ag.Name(),
			"model": model,
		})
	}
	return agents
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
		s.authResolver.Middleware(next)(w, r)
	}
}

func writeUnauth(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"error": map[string]string{"message": msg, "type": "authentication_error"},
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
