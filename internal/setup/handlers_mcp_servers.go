package setup

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/usermcp"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// --- Custom MCP servers: ones the caller added themselves (docs/connectors.md) ---
//
// Every route acts for the authenticated caller only (a product's end user
// via X-Fastclaw-End-User included); a server belongs to the one user who
// added it, and only that user's own turns can use it. Header values and
// tokens never leave the server.
//
//   GET    /api/mcp-servers        → {servers:[{id,name,url,headerNames,toolCount,createdAt,
//                                    auth,signIn}], max}
//   POST   /api/mcp-servers        {name, url, headers?}: checked as MCP, then saved.
//                                  One that asks for OAuth answers "oauth_required".
//   PATCH  /api/mcp-servers/{id}   {name, url, headers?}: headers absent keeps them;
//                                  an empty value keeps that header's saved value
//   DELETE /api/mcp-servers/{id}
//   POST   /api/mcp-servers/oauth/start  {name, url, redirectUri, clientName?} adds an
//                                  OAuth server by signing in; {serverId, redirectUri}
//                                  signs in to one again → {authorizeUrl}
//   POST   /api/mcp-servers/oauth/finish {state, code} → {server}: as the person who
//                                  started it. The account's own key with no end user
//                                  finishes it for whichever of its end users started
//                                  it (a link opened from a chat, not signed in).

func (s *Server) registerMCPServerRoutes(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/mcp-servers", auth(s.handleListMCPServers))
	mux.HandleFunc("POST /api/mcp-servers", auth(s.handleAddMCPServer))
	mux.HandleFunc("PATCH /api/mcp-servers/{id}", auth(s.handleUpdateMCPServer))
	mux.HandleFunc("DELETE /api/mcp-servers/{id}", auth(s.handleDeleteMCPServer))
	mux.HandleFunc("POST /api/mcp-servers/oauth/start", auth(s.handleStartMCPOAuth))
	mux.HandleFunc("POST /api/mcp-servers/oauth/finish", auth(s.handleFinishMCPOAuth))
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
func mcpServerView(ctx context.Context, svc *usermcp.Service, srv usermcp.Server, uid string) map[string]any {
	return map[string]any{
		"id": srv.ID, "name": srv.Name, "url": srv.URL, "headerNames": srv.HeaderNames(),
		"toolCount": srv.ToolCount, "createdAt": srv.CreatedAt,
		"auth": srv.Auth(), "signIn": svc.TokenStatus(ctx, srv, uid),
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
	out := make([]map[string]any, 0, len(list))
	for _, srv := range list {
		out = append(out, mcpServerView(r.Context(), svc, srv, uid))
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
	srv, err := svc.Add(ctx, uid, req.Name, req.URL, req.Headers)
	if err != nil {
		mcpServerError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"server": mcpServerView(ctx, svc, srv, uid)})
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
	jsonResponse(w, http.StatusOK, map[string]any{"server": mcpServerView(ctx, svc, srv, uid)})
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
		Name        string `json:"name"`
		URL         string `json:"url"`
		RedirectURI string `json:"redirectUri"`
		ClientName  string `json:"clientName"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "code": "invalid_request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	in := usermcp.OAuthStart{Name: req.Name, URL: req.URL, RedirectURI: req.RedirectURI, ClientName: req.ClientName}
	if req.ServerID != "" {
		srv, owner, err := svc.Server(ctx, req.ServerID)
		if err != nil {
			mcpServerError(w, err)
			return
		}
		if srv == nil || owner != uid {
			mcpServerError(w, &usermcp.Error{Code: "not_found"})
			return
		}
		in.Server = srv
	}
	authURL, err := svc.StartOAuth(ctx, uid, in)
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
