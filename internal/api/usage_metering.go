package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/billing"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// Usage metering and billing authorization for integrating apps
// (docs/usage-metering.md):
//
//	GET /v1/usage/events?after=<id>&limit=<n>  the calling account's usage events
//	PUT /v1/billing/hook                       register / remove the billing hook
//	GET /v1/billing/hook                       {"url", "secret_set"}

// meteringAccount is the account an account-API-key request acts for.
// Agent-scoped keys only reach some agents, so they can't read the
// account's billing; console sessions use the admin routes.
func (s *Server) meteringAccount(w http.ResponseWriter, r *http.Request) (string, bool) {
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		writeUnauth(w, "authentication required")
		return "", false
	}
	if ident.AuthMethod != "apikey" || ident.APIKeyType == users.APIKeyTypeAgent {
		writeAPIError(w, http.StatusForbidden, errTypePermission, codeForbidden, "an account API key is required")
		return "", false
	}
	if s.store == nil {
		writeAPIError(w, http.StatusServiceUnavailable, errTypeServer, codeNotConfigured, "usage metering not configured")
		return "", false
	}
	return ident.AccountID(), true
}

// HandleListUsageEvents handles GET /v1/usage/events: the events the
// calling account pays for (its own usage and its end users'), oldest
// first, after a cursor.
func (s *Server) HandleListUsageEvents(w http.ResponseWriter, r *http.Request) {
	account, ok := s.meteringAccount(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var after int64
	if v := strings.TrimSpace(q.Get("after")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeBadRequest(w, "after must be a non-negative integer")
			return
		}
		after = n
	}
	limit := 500
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeBadRequest(w, "limit must be 1-1000")
			return
		}
		limit = n
	}
	events, err := s.store.ListAccountUsageEvents(r.Context(), account, after, limit)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, errTypeServer, codeInternal, "could not list usage events")
		return
	}
	if events == nil {
		events = []store.MeteredEvent{}
	}
	next := after
	if len(events) > 0 {
		next = events[len(events)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events":     events,
		"next_after": next,
		"has_more":   len(events) == limit,
	})
}

// HandleGetBillingHook handles GET /v1/billing/hook. The secret is never
// returned.
func (s *Server) HandleGetBillingHook(w http.ResponseWriter, r *http.Request) {
	account, ok := s.meteringAccount(w, r)
	if !ok {
		return
	}
	url, secret, err := s.store.GetBillingHook(r.Context(), account)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, errTypeServer, codeInternal, "could not read the billing hook")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url, "secret_set": url != "" && secret != ""})
}

// HandlePutBillingHook handles PUT /v1/billing/hook:
// {"url": "https://…", "secret": "<≥ 32 chars>"}; {"url": ""} removes it.
func (s *Server) HandlePutBillingHook(w http.ResponseWriter, r *http.Request) {
	account, ok := s.meteringAccount(w, r)
	if !ok {
		return
	}
	if ident, _ := auth.FromContext(r.Context()); ident.ReadOnly() {
		writeAPIError(w, http.StatusForbidden, errTypePermission, codeForbidden, "read-only")
		return
	}
	var req struct {
		URL    *string `json:"url"`
		Secret string  `json:"secret"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil || req.URL == nil {
		writeBadRequest(w, `body must be {"url": "https://…", "secret": "…"} (or {"url": ""} to remove)`)
		return
	}
	hookURL := strings.TrimSpace(*req.URL)
	if hookURL != "" {
		if len(req.Secret) < billing.MinSecretLen {
			writeBadRequest(w, "secret must be at least 32 characters")
			return
		}
		if err := billing.ValidateHookURL(r.Context(), hookURL); err != nil {
			writeBadRequest(w, err.Error())
			return
		}
	}
	if err := s.store.SetBillingHook(r.Context(), account, hookURL, req.Secret); err != nil {
		writeAPIError(w, http.StatusInternalServerError, errTypeServer, codeInternal, "could not save the billing hook")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": hookURL, "secret_set": hookURL != ""})
}
