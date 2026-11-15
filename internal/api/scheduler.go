package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/billing"
	"github.com/fastclaw-ai/fastclaw/internal/trig"
)

// The account's scheduler (docs/scheduler.md): a trig deployment that keeps
// the cron jobs its API turns create. When a job is due, trig posts to the
// account's product, which runs the turn and delivers the reply.
//
//	GET /v1/scheduler   {"url", "key_set"}
//	PUT /v1/scheduler   {"url": "https://…", "api_key": "trig_…"}; {"url": ""} removes it

// HandleGetScheduler handles GET /v1/scheduler. The key is never returned.
func (s *Server) HandleGetScheduler(w http.ResponseWriter, r *http.Request) {
	account, ok := s.meteringAccount(w, r)
	if !ok {
		return
	}
	u, key, err := s.store.GetScheduler(r.Context(), account)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, errTypeServer, codeInternal, "could not read the scheduler")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": u, "key_set": u != "" && key != ""})
}

// HandlePutScheduler handles PUT /v1/scheduler. The URL and key are
// checked against trig (GET /v1/app) before they're saved.
func (s *Server) HandlePutScheduler(w http.ResponseWriter, r *http.Request) {
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
		APIKey string  `json:"api_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil || req.URL == nil {
		writeBadRequest(w, `body must be {"url": "https://…", "api_key": "trig_…"} (or {"url": ""} to remove)`)
		return
	}
	u := strings.TrimRight(strings.TrimSpace(*req.URL), "/")
	key := strings.TrimSpace(req.APIKey)
	if u != "" {
		if key == "" {
			writeBadRequest(w, "api_key is required")
			return
		}
		if err := billing.ValidateHookURL(r.Context(), u); err != nil {
			writeBadRequest(w, err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err := trig.New(u, key).Ping(ctx); err != nil {
			writeBadRequest(w, "trig refused the url or key: "+err.Error())
			return
		}
	}
	if err := s.store.SetScheduler(r.Context(), account, u, key); err != nil {
		writeAPIError(w, http.StatusInternalServerError, errTypeServer, codeInternal, "could not save the scheduler")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": u, "key_set": u != ""})
}
