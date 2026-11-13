package setup

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/usermcp"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// --- Custom MCP servers: ones the caller added themselves (docs/connectors.md) ---
//
// A server belongs to the one user who added it (a product's end user via
// X-Fastclaw-End-User included), who offers it on agents they made.
// Everyone chatting with such an agent may use it there. Header values
// never leave the server. An OAuth server is added without signing in;
// each person signs in with their own account (oauth/start, usually from
// a link the agent sends in the chat).
//
//   GET    /api/mcp-servers        → {servers:[{id,name,url,headerNames,toolCount,createdAt,
//                                    auth,signIn,agentIds}], max}
//   POST   /api/mcp-servers        {name, url, headers?, redirectUri?, clientName?}: checked
//                                  as MCP, then saved. An OAuth server is saved as is;
//                                  redirectUri registers its client now.
//   PATCH  /api/mcp-servers/{id}   {name, url, headers?}: headers absent keeps them;
//                                  an empty value keeps that header's saved value
//   DELETE /api/mcp-servers/{id}
//   POST   /api/mcp-servers/oauth/start  {serverId, redirectUri, clientName?}
//                                  → {authorizeUrl}: the caller signs in to one of
//                                  their servers with their own account
//   POST   /api/mcp-servers/oauth/finish {state, code} → {server}: as the person who
//                                  started it. The account's own key with no end user
//                                  finishes it for whichever of its end users started
//                                  it (a link opened from a chat, not signed in).
//   PUT    /api/mcp-servers/{id}/agents  {agentIds}: the agents (the caller made)
//                                  one of their servers is offered on
//   GET    /api/agents/{id}/mcp-servers  → {serverIds}
//   PUT    /api/agents/{id}/mcp-servers  {serverIds}: the caller's servers offered
//                                  on an agent they made

func (s *Server) registerMCPServerRoutes(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/mcp-servers", auth(s.handleListMCPServers))
	mux.HandleFunc("POST /api/mcp-servers", auth(s.handleAddMCPServer))
	mux.HandleFunc("PATCH /api/mcp-servers/{id}", auth(s.handleUpdateMCPServer))
	mux.HandleFunc("DELETE /api/mcp-servers/{id}", auth(s.handleDeleteMCPServer))
	mux.HandleFunc("POST /api/mcp-servers/oauth/start", auth(s.handleStartMCPOAuth))
	mux.HandleFunc("POST /api/mcp-servers/oauth/finish", auth(s.handleFinishMCPOAuth))
	mux.HandleFunc("PUT /api/mcp-servers/{id}/agents", auth(s.handleSetMCPServerAgents))
	mux.HandleFunc("GET /api/agents/{id}/mcp-servers", auth(s.handleGetAgentMCPServers))
	mux.HandleFunc("PUT /api/agents/{id}/mcp-servers", auth(s.handleSetAgentMCPServers))
}

func (s *Server) mcpServerCaller(w http.ResponseWriter, r *http.Request) (*usermcp.Service, string, bool) {
	uid := s.effectiveUserID(r)
	if uid == "" {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "code": "unauthorized"})
		return nil, "", false
	}
	svc := usermcp.Get()
	if svc == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "custom MCP servers are not available", "code": "not_configured"})
		return nil, "", false
	}
	return svc, uid, true
}

// mcpServerView: signIn is the caller's own sign-in to an OAuth server
// ("none" | "ok" | "broken"; "" for a header server).
func mcpServerView(ctx context.Context, svc *usermcp.Service, srv usermcp.Server, uid string, agentIDs []string) map[string]any {
	if agentIDs == nil {
		agentIDs = []string{}
	}
	return map[string]any{
		"id": srv.ID, "name": srv.Name, "url": srv.URL, "headerNames": srv.HeaderNames(),
		"toolCount": srv.ToolCount, "createdAt": srv.CreatedAt,
		"auth": srv.Auth(), "signIn": svc.TokenStatus(ctx, srv, uid), "agentIds": agentIDs,
	}
}

func mcpServerError(w http.ResponseWriter, err error) {
	code := usermcp.Code(err)
	status := http.StatusBadRequest
	switch code {
	case "not_found":
		status = http.StatusNotFound
	case "internal":
		status = http.StatusInternalServerError
		code = "internal"
	}
	jsonResponse(w, status, map[string]any{"error": code, "code": code})
}

func (s *Server) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := s.mcpServerCaller(w, r)
	if !ok {
		return
	}
	list, err := svc.List(r.Context(), uid)
	if err != nil {
		mcpServerError(w, err)
		return
	}
	agents, err := svc.ServerAgents(r.Context(), uid)
	if err != nil {
		mcpServerError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, srv := range list {
		out = append(out, mcpServerView(r.Context(), svc, srv, uid, agents[srv.ID]))
	}
	jsonResponse(w, http.StatusOK, map[string]any{"servers": out, "max": usermcp.MaxServers})
}

func (s *Server) handleAddMCPServer(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	svc, uid, ok := s.mcpServerCaller(w, r)
	if !ok {
		return
	}
	var req struct {
		Name        string            `json:"name"`
		URL         string            `json:"url"`
		Headers     map[string]string `json:"headers"`
		RedirectURI string            `json:"redirectUri"`
		ClientName  string            `json:"clientName"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	var client *usermcp.OAuthClient
	if req.RedirectURI != "" {
		client = &usermcp.OAuthClient{RedirectURI: req.RedirectURI, ClientName: req.ClientName}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	srv, err := svc.Add(ctx, uid, req.Name, req.URL, req.Headers, client)
	if err != nil {
		mcpServerError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"server": mcpServerView(ctx, svc, srv, uid, nil)})
}

func (s *Server) handleUpdateMCPServer(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	svc, uid, ok := s.mcpServerCaller(w, r)
	if !ok {
		return
	}
	var req struct {
		Name    string            `json:"name"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	srv, err := svc.Update(ctx, uid, r.PathValue("id"), req.Name, req.URL, req.Headers)
	if err != nil {
		mcpServerError(w, err)
		return
	}
	agents, _ := svc.ServerAgents(ctx, uid)
	jsonResponse(w, http.StatusOK, map[string]any{"server": mcpServerView(ctx, svc, srv, uid, agents[srv.ID])})
}

func (s *Server) handleDeleteMCPServer(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	svc, uid, ok := s.mcpServerCaller(w, r)
	if !ok {
		return
	}
	if err := svc.Delete(r.Context(), uid, r.PathValue("id")); err != nil {
		mcpServerError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleStartMCPOAuth(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	svc, uid, ok := s.mcpServerCaller(w, r)
	if !ok {
		return
	}
	var req struct {
		ServerID    string `json:"serverId"`
		RedirectURI string `json:"redirectUri"`
		ClientName  string `json:"clientName"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil || req.ServerID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	srv, owner, err := svc.Server(ctx, req.ServerID)
	if err != nil {
		mcpServerError(w, err)
		return
	}
	// Here only for one's own servers; everyone else signs in from the
	// link an agent offering it sends in their chat.
	if srv == nil || owner != uid {
		mcpServerError(w, &usermcp.Error{Code: "not_found"})
		return
	}
	authURL, err := svc.StartOAuth(ctx, uid, *srv, req.RedirectURI, req.ClientName)
	if err != nil {
		mcpServerError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"authorizeUrl": authURL})
}

func (s *Server) handleFinishMCPOAuth(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	svc, uid, ok := s.mcpServerCaller(w, r)
	if !ok {
		return
	}
	var req struct {
		State string `json:"state"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil || req.State == "" || req.Code == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	var accept func(string) bool
	ident, _ := auth.FromContext(r.Context())
	if ident.AuthMethod == "apikey" && ident.EndUser == "" && !ident.IsActingAs() {
		// The account's backend, for a link opened outside its sign-in:
		// whichever of its end users (or itself) started this state.
		account := ident.AccountID()
		accept = func(starter string) bool {
			if starter == account {
				return true
			}
			u, err := s.dataStore.GetUser(ctx, starter)
			return err == nil && u != nil && u.Role == users.RoleAppUser && u.OwnerUserID == account
		}
		uid = ""
	}
	srv, err := svc.FinishOAuth(ctx, uid, req.State, req.Code, accept)
	if err != nil {
		mcpServerError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"server": map[string]any{"id": srv.ID, "name": srv.Name, "auth": srv.Auth()}})
}

// madeAgent is the agent when the caller made it: their own agent, or (a
// product's end user) one of the account's agents marked theirs.
func (s *Server) madeAgent(r *http.Request, uid, agentID string) *store.AgentRecord {
	rec, err := s.dataStore.GetAgent(r.Context(), agentID)
	if err != nil || rec == nil {
		return nil
	}
	ident, _ := auth.FromContext(r.Context())
	if rec.UserID == uid {
		return rec
	}
	if ident.EndUser != "" && rec.UserID == ident.AccountID() {
		owner := strings.TrimSpace(store.AgentEndUserOwner(rec))
		if owner != "" && owner == strings.TrimSpace(ident.EndUser) {
			return rec
		}
	}
	return nil
}

func (s *Server) mcpAgentOwner(w http.ResponseWriter, r *http.Request, uid string) *store.AgentRecord {
	rec := s.madeAgent(r, uid, r.PathValue("id"))
	if rec == nil {
		mcpServerError(w, &usermcp.Error{Code: "not_found"})
	}
	return rec
}

func (s *Server) handleSetMCPServerAgents(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	svc, uid, ok := s.mcpServerCaller(w, r)
	if !ok {
		return
	}
	var req struct {
		AgentIDs []string `json:"agentIds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || len(req.AgentIDs) > usermcp.MaxServerAgents {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	for _, id := range req.AgentIDs {
		if s.madeAgent(r, uid, id) == nil {
			mcpServerError(w, &usermcp.Error{Code: "not_found"})
			return
		}
	}
	ids, err := svc.SetServerAgents(r.Context(), uid, r.PathValue("id"), req.AgentIDs)
	if err != nil {
		mcpServerError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"agentIds": ids})
}

func (s *Server) handleGetAgentMCPServers(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := s.mcpServerCaller(w, r)
	if !ok {
		return
	}
	rec := s.mcpAgentOwner(w, r, uid)
	if rec == nil {
		return
	}
	ids, err := svc.AgentServerIDs(r.Context(), rec.ID)
	if err != nil {
		mcpServerError(w, err)
		return
	}
	if ids == nil {
		ids = []string{}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"serverIds": ids})
}

func (s *Server) handleSetAgentMCPServers(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	svc, uid, ok := s.mcpServerCaller(w, r)
	if !ok {
		return
	}
	rec := s.mcpAgentOwner(w, r, uid)
	if rec == nil {
		return
	}
	var req struct {
		ServerIDs []string `json:"serverIds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	ids, err := svc.SetAgentServers(r.Context(), uid, rec.ID, req.ServerIDs)
	if err != nil {
		mcpServerError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"serverIds": ids})
}
