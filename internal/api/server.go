package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// UserResolver looks up a user space by user ID. Gateway implements this.
type UserResolver interface {
	UserSpaceFor(userID string) (*UserSpaceView, error)
	LocalAgentManager() *agent.Manager
	IsCloudMode() bool
}

// UserSpaceView is the subset of gateway.UserSpace that the API layer needs.
// Defining it here avoids importing gateway from api (which would create a
// cycle: gateway already imports api indirectly via cmd/fastclaw).
type UserSpaceView struct {
	UserID   string
	Agents   *agent.Manager
	Config   *config.Config
}

// Server handles the OpenAI-compatible API and WebSocket gateway.
type Server struct {
	resolver    UserResolver
	token       string // local-mode bearer token (fallback)
	registry    *users.Registry
	gatewayCfg  *config.GatewayCfg
	limiter     *rateLimiter
}

// NewServer creates a new API server. token is the single bearer token used
// in local mode; registry is consulted in cloud mode to map incoming tokens
// to user IDs. registry may be nil in local mode.
func NewServer(resolver UserResolver, token string, registry *users.Registry, gatewayCfg *config.GatewayCfg) *Server {
	var rpm int
	if gatewayCfg != nil {
		rpm = gatewayCfg.RateLimit.RPM
	}
	return &Server{
		resolver:   resolver,
		token:      token,
		registry:   registry,
		gatewayCfg: gatewayCfg,
		limiter:    newRateLimiter(rpm),
	}
}

// RegisterRoutes registers API routes on the given mux.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	// Always register WebSocket (needed for ChatClaw)
	mux.HandleFunc("/ws", s.HandleWebSocket)

	// CORS preflight for all /v1/* routes
	mux.HandleFunc("OPTIONS /v1/", s.handleCORS)

	getUserID := func(r *http.Request) string { return config.UserIDFromContext(r.Context()) }

	// Chat completions endpoint
	if s.gatewayCfg == nil || s.gatewayCfg.HTTP.Endpoints.ChatCompletions.Enabled {
		mux.HandleFunc("POST /v1/chat/completions",
			s.authMiddleware(rateLimitMiddleware(s.limiter, getUserID, s.HandleChatCompletions)))
	}

	// Agents list endpoint
	if s.gatewayCfg == nil || s.gatewayCfg.HTTP.Endpoints.Agents.Enabled {
		mux.HandleFunc("GET /v1/agents",
			s.authMiddleware(rateLimitMiddleware(s.limiter, getUserID, s.HandleListAgents)))
	}
}

// handleCORS responds to CORS preflight requests.
func (s *Server) handleCORS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-fastclaw-agent-id, x-fastclaw-session-key")
	w.Header().Set("Access-Control-Max-Age", "86400")
	w.WriteHeader(http.StatusNoContent)
}

// HandleListAgents handles GET /v1/agents.
func (s *Server) HandleListAgents(w http.ResponseWriter, r *http.Request) {
	space, err := s.userSpaceFor(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": map[string]string{"message": err.Error(), "type": "authentication_error"},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": buildAgentList(space)})
}

func buildAgentList(space *UserSpaceView) []map[string]string {
	all := space.Agents.All()
	modelMap := make(map[string]string)
	if space.Config != nil {
		for _, ra := range config.ResolveAgents(space.Config) {
			modelMap[ra.ID] = ra.Model
		}
	}

	agents := make([]map[string]string, 0, len(all))
	for _, ag := range all {
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

// userSpaceFor resolves the user space from the request's context (set by
// authMiddleware). Falls back to the local user if no context value is set,
// which matches local-mode behaviour.
func (s *Server) userSpaceFor(r *http.Request) (*UserSpaceView, error) {
	userID := config.UserIDFromContext(r.Context())
	space, err := s.resolver.UserSpaceFor(userID)
	if err != nil {
		return nil, err
	}
	return space, nil
}

// authMiddleware validates the Bearer token and tags the request context
// with the resolved user ID. In local mode every authenticated request is
// tagged with config.DefaultUserID; in cloud mode the token is looked up
// in the user registry.
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")

		authMode := ""
		if s.gatewayCfg != nil {
			authMode = s.gatewayCfg.Auth.Mode
		}

		// No auth configured — allow through as the local user.
		if authMode == "none" || (s.token == "" && s.registry == nil) {
			r = r.WithContext(config.WithUserID(r.Context(), config.DefaultUserID))
			next(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if auth == "" {
			writeUnauth(w, "missing Authorization header")
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		if token == auth {
			writeUnauth(w, "Authorization header must use Bearer scheme")
			return
		}

		userID, err := s.resolveUserID(token)
		if err != nil {
			writeUnauth(w, err.Error())
			return
		}

		r = r.WithContext(config.WithUserID(r.Context(), userID))
		next(w, r)
	}
}

// resolveUserID maps a bearer token to a user ID using (in order):
//   1. the cloud user registry, if configured
//   2. the local bearer token, which resolves to config.DefaultUserID
func (s *Server) resolveUserID(token string) (string, error) {
	if s.registry != nil {
		if id, ok := s.registry.LookupByToken(token); ok {
			return id, nil
		}
	}
	if s.token != "" && token == s.token {
		return config.DefaultUserID, nil
	}
	return "", errors.New("invalid token")
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
