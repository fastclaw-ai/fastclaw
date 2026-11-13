package setup

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/usermcp"
)

// --- Custom MCP servers: ones the caller added themselves (docs/connectors.md) ---
//
// Every route acts for the authenticated caller only (a product's end user
// via X-Fastclaw-End-User included); a server belongs to the one user who
// added it, and only that user's own turns can use it. Header values never
// leave the server.
//
//   GET    /api/mcp-servers        → {servers:[{id,name,url,headerNames,toolCount,createdAt}], max}
//   POST   /api/mcp-servers        {name, url, headers?}: checked as MCP, then saved
//   PATCH  /api/mcp-servers/{id}   {name, url, headers?}: headers absent keeps them;
//                                  an empty value keeps that header's saved value
//   DELETE /api/mcp-servers/{id}
//   POST   /api/mcp-servers/oauth/start  {name, url, serverId?, redirectUri, clientName?}
//                                  → {authorizeUrl}: send the person there; the
//                                  authorization server returns to redirectUri
//                                  with ?code&state
//   POST   /api/mcp-servers/oauth/finish {state, code} → {server}
//
// Adding a server that asks for OAuth answers code "oauth_required"; the
// caller then starts the OAuth flow (serverId re-authorizes an existing
// one, e.g. when it shows needsReauth).

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

func mcpServerView(srv usermcp.Server) map[string]any {
	return map[string]any{
		"id": srv.ID, "name": srv.Name, "url": srv.URL, "headerNames": srv.HeaderNames(),
		"toolCount": srv.ToolCount, "createdAt": srv.CreatedAt,
		"auth": srv.Auth(), "needsReauth": srv.NeedsReauth(),
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
		out = append(out, mcpServerView(srv))
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
	jsonResponse(w, http.StatusOK, map[string]any{"server": mcpServerView(srv)})
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
	jsonResponse(w, http.StatusOK, map[string]any{"server": mcpServerView(srv)})
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
	authURL, err := svc.StartOAuth(ctx, uid, usermcp.OAuthStart{
		ServerID: req.ServerID, Name: req.Name, URL: req.URL,
		RedirectURI: req.RedirectURI, ClientName: req.ClientName,
	})
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
	srv, err := svc.FinishOAuth(ctx, uid, req.State, req.Code)
	if err != nil {
		mcpServerError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"server": mcpServerView(srv)})
}
