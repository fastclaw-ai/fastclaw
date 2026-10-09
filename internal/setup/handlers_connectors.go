package setup

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/connectors"
)

// --- Connectors: the caller's own third-party accounts (docs/connectors.md) ---
//
// Every route acts for the authenticated caller only: the Connany subject
// is derived from the session here, never from the request body or URL,
// and Connany re-checks that every connection and session id belongs to
// that subject.
//
//   GET    /api/connectors                        catalog (+configured)
//   GET    /api/connectors/{name}/avatar          icon, proxied
//   GET    /api/connector-accounts                the caller's accounts
//   PATCH  /api/connector-accounts/{id}           {name?, isDefault?}
//   DELETE /api/connector-accounts/{id}           disconnect
//   POST   /api/connector-accounts/{id}/check     → {toolCount}
//   GET    /api/connector-accounts/{id}/access    shared resources
//   POST   /api/connector-authorizations          {requestId|connector|reconnectId|accessId} → {url, request}
//   GET    /api/connector-requests/{id}           a card's request status
//   POST   /api/connector-requests/{id}/recheck   "I've finished"
//   POST   /api/connector-requests/{id}/cancel    declined on the card
//   POST   /api/connector-requests/confirm        {providerSessionId}: the return page

func (s *Server) registerConnectorRoutes(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	connectors.SetWaker(s.wakeConnectorChat)
	mux.HandleFunc("GET /api/connectors", auth(s.handleConnectorCatalog))
	mux.HandleFunc("GET /api/connectors/{name}/avatar", auth(s.handleConnectorAvatar))
	mux.HandleFunc("GET /api/connector-accounts", auth(s.handleConnectorAccounts))
	mux.HandleFunc("PATCH /api/connector-accounts/{id}", auth(s.handleConnectorAccountUpdate))
	mux.HandleFunc("DELETE /api/connector-accounts/{id}", auth(s.handleConnectorAccountDelete))
	mux.HandleFunc("POST /api/connector-accounts/{id}/check", auth(s.handleConnectorAccountCheck))
	mux.HandleFunc("GET /api/connector-accounts/{id}/access", auth(s.handleConnectorAccountAccess))
	mux.HandleFunc("POST /api/connector-authorizations", auth(s.handleConnectorAuthorize))
	mux.HandleFunc("POST /api/connector-requests/confirm", auth(s.handleConnectorConfirm))
	mux.HandleFunc("GET /api/connector-requests/{id}", auth(s.handleConnectorRequestStatus))
	mux.HandleFunc("POST /api/connector-requests/{id}/recheck", auth(s.handleConnectorRequestRecheck))
	mux.HandleFunc("POST /api/connector-requests/{id}/cancel", auth(s.handleConnectorRequestCancel))
	// System → Tools → Connectors (super admin): where Connany is.
	mux.HandleFunc("GET /api/admin/connectors-config", s.requireSuperAdmin(s.handleGetConnectorsConfig))
	mux.HandleFunc("PUT /api/admin/connectors-config", s.requireSuperAdmin(s.handleSaveConnectorsConfig))
	mux.HandleFunc("DELETE /api/admin/connectors-config", s.requireSuperAdmin(s.handleClearConnectorsConfig))
}

// keyHint shows only the last four characters of a key.
func keyHint(key string) string {
	if len(key) <= 4 {
		return ""
	}
	return "…" + key[len(key)-4:]
}

// connectorsConfigView is the admin form's state. The key itself never
// leaves the server.
func (s *Server) connectorsConfigView(ctx context.Context) map[string]any {
	view := map[string]any{"enabled": connectors.Get() != nil, "source": "", "url": "", "apiKeySet": false, "apiKeyHint": ""}
	env := connectors.EnvFallback()
	view["envConfigured"] = env.URL != "" && env.APIKey != ""
	if s.dataStore != nil {
		if saved, ok, err := connectors.LoadSettings(ctx, s.dataStore); err == nil && ok {
			view["source"] = "settings"
			view["url"] = saved.URL
			view["apiKeySet"] = saved.APIKey != ""
			view["apiKeyHint"] = keyHint(saved.APIKey)
			return view
		}
	}
	if env.URL != "" && env.APIKey != "" {
		view["source"] = "env"
		view["url"] = env.URL
		view["apiKeySet"] = true
		view["apiKeyHint"] = keyHint(env.APIKey)
	}
	return view
}

func (s *Server) handleGetConnectorsConfig(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, s.connectorsConfigView(r.Context()))
}

// handleSaveConnectorsConfig checks the URL + key against Connany, saves
// them and switches the running service over — no restart. An empty key
// keeps the one already saved (the form never sees it).
func (s *Server) handleSaveConnectorsConfig(w http.ResponseWriter, r *http.Request) {
	if s.dataStore == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "no data store", "code": "service_unavailable"})
		return
	}
	var req struct {
		URL    string `json:"url"`
		APIKey string `json:"apiKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		connectorError(w, &connectors.Error{Code: "invalid_request"})
		return
	}
	url := strings.TrimRight(strings.TrimSpace(req.URL), "/")
	key := strings.TrimSpace(req.APIKey)
	if key == "" {
		if saved, ok, _ := connectors.LoadSettings(r.Context(), s.dataStore); ok {
			key = saved.APIKey
		} else if env := connectors.EnvFallback(); env.APIKey != "" {
			key = env.APIKey
		}
	}
	if url == "" || key == "" {
		connectorError(w, &connectors.Error{Code: "invalid_request"})
		return
	}
	probeCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := connectors.Probe(probeCtx, url, key); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": connectors.Code(err), "code": connectors.Code(err)})
		return
	}
	if err := connectors.SaveSettings(r.Context(), s.dataStore, connectors.Settings{URL: url, APIKey: key}); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if _, err := connectors.Apply(r.Context(), s.dataStore); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, s.connectorsConfigView(r.Context()))
}

// handleClearConnectorsConfig removes the saved settings; the env
// configuration (if any) applies again, else connectors turn off.
func (s *Server) handleClearConnectorsConfig(w http.ResponseWriter, r *http.Request) {
	if s.dataStore == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "no data store"})
		return
	}
	if err := connectors.ClearSettings(r.Context(), s.dataStore); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if _, err := connectors.Apply(r.Context(), s.dataStore); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, s.connectorsConfigView(r.Context()))
}

// connectorCaller resolves the service and the caller, or writes the error.
func (s *Server) connectorCaller(w http.ResponseWriter, r *http.Request) (*connectors.Service, string, bool) {
	svc := connectors.Get()
	if svc == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "connectors are not configured", "code": "not_configured"})
		return nil, "", false
	}
	uid := s.effectiveUserID(r)
	if uid == "" {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "code": "unauthorized"})
		return nil, "", false
	}
	return svc, uid, true
}

// connectorError answers with a closed code the UI words in the reader's
// language — never Connany's own message.
func connectorError(w http.ResponseWriter, err error) {
	code := connectors.Code(err)
	status := http.StatusBadGateway
	switch code {
	case "not_found", "connector_not_found":
		status = http.StatusNotFound
	case "invalid_request":
		status = http.StatusBadRequest
	case "rate_limited":
		status = http.StatusTooManyRequests
	case "already_done", "no_access_step", "reauth_required":
		status = http.StatusConflict
	}
	jsonResponse(w, status, map[string]any{"error": code, "code": code})
}

// connectorLang maps the UI locale to a BCP-47 tag for the catalog.
func connectorLang(raw string) string {
	switch {
	case strings.HasPrefix(raw, "zh"):
		if raw == "zh" {
			return "zh-CN"
		}
		return raw
	case raw == "":
		return "en"
	}
	return raw
}

func (s *Server) handleConnectorCatalog(w http.ResponseWriter, r *http.Request) {
	svc := connectors.Get()
	if svc == nil {
		jsonResponse(w, http.StatusOK, map[string]any{"configured": false, "categories": []any{}, "connectors": []any{}})
		return
	}
	cats, list, err := svc.Catalog(r.Context(), connectorLang(r.URL.Query().Get("lang")), r.URL.Query().Get("fresh") == "1")
	if err != nil {
		connectorError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, c := range list {
		out = append(out, map[string]any{
			"name": c.Name, "title": c.Title, "category": c.Category, "description": c.Description,
			// The browser never contacts the service's origin directly.
			"avatarUrl": "/api/connectors/" + c.Name + "/avatar",
		})
	}
	if cats == nil {
		cats = []connectors.Category{}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"configured": true, "categories": cats, "connectors": out})
}

func (s *Server) handleConnectorAvatar(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.connectorCaller(w, r)
	if !ok {
		return
	}
	data, ctype, err := svc.Avatar(r.Context(), r.PathValue("name"))
	if err != nil || len(data) == 0 {
		http.NotFound(w, r)
		return
	}
	if !strings.HasPrefix(ctype, "image/") {
		ctype = http.DetectContentType(data)
		if !strings.HasPrefix(ctype, "image/") && !strings.Contains(string(data[:min(len(data), 256)]), "<svg") {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(string(data[:min(len(data), 256)]), "<svg") {
			ctype = "image/svg+xml"
		}
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	// An SVG served from our origin must not run script.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

func (s *Server) handleConnectorAccounts(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := s.connectorCaller(w, r)
	if !ok {
		return
	}
	accounts, err := svc.Accounts(r.Context(), uid, r.URL.Query().Get("fresh") == "1")
	if err != nil {
		connectorError(w, err)
		return
	}
	if accounts == nil {
		accounts = []connectors.Account{}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"accounts": accounts})
}

func (s *Server) handleConnectorAccountUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	svc, uid, ok := s.connectorCaller(w, r)
	if !ok {
		return
	}
	var req struct {
		Name      *string `json:"name"`
		IsDefault *bool   `json:"isDefault"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		connectorError(w, &connectors.Error{Code: "invalid_request"})
		return
	}
	id := r.PathValue("id")
	if req.Name != nil {
		if err := svc.Rename(r.Context(), uid, id, *req.Name); err != nil {
			connectorError(w, err)
			return
		}
	}
	if req.IsDefault != nil && *req.IsDefault {
		if err := svc.SetDefault(r.Context(), uid, id); err != nil {
			connectorError(w, err)
			return
		}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleConnectorAccountDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	svc, uid, ok := s.connectorCaller(w, r)
	if !ok {
		return
	}
	if err := svc.Disconnect(r.Context(), uid, r.PathValue("id")); err != nil {
		connectorError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleConnectorAccountCheck(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := s.connectorCaller(w, r)
	if !ok {
		return
	}
	n, err := svc.Check(r.Context(), uid, r.PathValue("id"))
	if err != nil {
		connectorError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"toolCount": n})
}

func (s *Server) handleConnectorAccountAccess(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := s.connectorCaller(w, r)
	if !ok {
		return
	}
	access, err := svc.AccessOf(r.Context(), uid, r.PathValue("id"))
	if err != nil {
		connectorError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, access)
}

// handleConnectorAuthorize mints the one-use authorization link when the
// person clicks (Settings or a conversation card). The link is returned to
// that click only — never stored or shown to the model.
func (s *Server) handleConnectorAuthorize(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	svc, uid, ok := s.connectorCaller(w, r)
	if !ok {
		return
	}
	var req struct {
		RequestID   string `json:"requestId"`
		Connector   string `json:"connector"`
		ReconnectID string `json:"reconnectId"`
		AccessID    string `json:"accessId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		connectorError(w, &connectors.Error{Code: "invalid_request"})
		return
	}
	set := 0
	for _, v := range []string{req.RequestID, req.Connector, req.ReconnectID, req.AccessID} {
		if v != "" {
			set++
		}
	}
	if set != 1 {
		connectorError(w, &connectors.Error{Code: "invalid_request"})
		return
	}
	link, view, err := svc.Open(r.Context(), uid, connectors.OpenTarget{
		RequestID: req.RequestID, Connector: req.Connector, ReconnectID: req.ReconnectID, AccessID: req.AccessID,
	}, requestBaseURL(r)+"/connectors/done/")
	if err != nil {
		connectorError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"url": link, "request": view})
}

func (s *Server) handleConnectorRequestStatus(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := s.connectorCaller(w, r)
	if !ok {
		return
	}
	v, err := svc.Status(r.Context(), uid, r.PathValue("id"))
	if err != nil {
		connectorError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, v)
}

func (s *Server) handleConnectorRequestRecheck(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := s.connectorCaller(w, r)
	if !ok {
		return
	}
	v, err := svc.Confirm(r.Context(), uid, r.PathValue("id"), "", true)
	if err != nil {
		connectorError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, v)
}

func (s *Server) handleConnectorRequestCancel(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	svc, uid, ok := s.connectorCaller(w, r)
	if !ok {
		return
	}
	v, err := svc.Cancel(r.Context(), uid, r.PathValue("id"))
	if err != nil {
		connectorError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, v)
}

// handleConnectorConfirm is the return page's call. connany_session_id is
// only a pointer: it's looked up among requests this person started, then
// confirmed with Connany — the redirect itself proves nothing.
func (s *Server) handleConnectorConfirm(w http.ResponseWriter, r *http.Request) {
	svc, uid, ok := s.connectorCaller(w, r)
	if !ok {
		return
	}
	var req struct {
		ProviderSessionID string `json:"providerSessionId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ProviderSessionID == "" {
		connectorError(w, &connectors.Error{Code: "invalid_request"})
		return
	}
	v, err := svc.Confirm(r.Context(), uid, "", req.ProviderSessionID, true)
	if err != nil {
		connectorError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, v)
}

// wakeConnectorChat hands a [connector] note to the web conversation that
// asked for an account and runs a turn for it, streamed and persisted like
// a typed message (an open chat page sees it live through
// /api/chat/subscribe). The caller is the agent's owner — the only person
// whose turns mount connectors — so the agent comes from their space.
func (s *Server) wakeConnectorChat(userID, agentID, sessionID, text string) {
	if s.userResolver == nil {
		return
	}
	space, err := s.userResolver.UserSpaceFor(userID)
	if err != nil || space == nil || space.Agents == nil {
		return
	}
	ag := space.Agents.AgentByID(agentID)
	if ag == nil {
		return
	}
	// The hub key the chat page subscribes with: a web session's chat id.
	streamID := sessionID
	if s.dataStore != nil {
		if channel, _, chatID, err := s.dataStore.LookupSessionTriple(context.Background(), userID, agentID, sessionID); err == nil && channel == "web" && chatID != "" {
			streamID = chatID
		}
	}
	key := teamRunKey{userID, agentID, streamID}
	// A turn still running in that conversation (rare: the card ended the
	// asking turn) gets to finish first.
	deadline := time.Now().Add(10 * time.Minute)
	for {
		agentCtx, cancel := context.WithTimeout(context.Background(), agentTurnTimeout)
		if s.beginChatTurn(key, cancel) {
			hub := s.chatEventHub()
			agentCtx = agent.ContextWithStream(agentCtx, nil, s.dataStore, hub, userID, agentID, streamID)
			_ = ag.HandleConnectorWake(agentCtx, sessionID, userID, text)
			s.endChatTurn(key, agentCtx, false)
			cancel()
			return
		}
		cancel()
		if time.Now().After(deadline) {
			slog.Warn("connectors: conversation stayed busy; wake dropped", "agent", agentID, "session", sessionID)
			return
		}
		time.Sleep(3 * time.Second)
	}
}
