package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

func billingHookServer(t *testing.T) (*Server, *store.DBStore, *users.Account, *http.ServeMux) {
	t.Helper()
	ctx := context.Background()
	s, st, accts := newSkillInstallAuthServer(t, ctx)
	resolver, err := auth.NewResolver(st)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAuth(resolver)
	acct := createSkillInstallTestUser(t, ctx, accts, "snapok", users.RoleUser)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/admin/usage/events", s.handleUsageEvents)
	mux.HandleFunc("GET /api/admin/users/{id}/billing-hold", s.handleGetBillingHold)
	mux.HandleFunc("PUT /api/admin/users/{id}/billing-hold", s.handleSetBillingHold)
	mux.HandleFunc("POST /api/admin/users/{id}/login-link", s.handleCreateLoginLink)
	mux.HandleFunc("GET /auth/login-link", s.handleRedeemLoginLink)
	return s, st.(*store.DBStore), acct, mux
}

func serve(mux *http.ServeMux, method, target string, body any) *httptest.ResponseRecorder {
	var rdr *bytes.Reader
	if body != nil {
		blob, _ := json.Marshal(body)
		rdr = bytes.NewReader(blob)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, rdr)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestBillingHoldEndpoints(t *testing.T) {
	_, _, acct, mux := billingHookServer(t)
	rec := serve(mux, "PUT", "/api/admin/users/"+acct.ID+"/billing-hold", map[string]any{"hold": true, "reason": "balance 0"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"hold":true`) {
		t.Fatalf("hold: %d %s", rec.Code, rec.Body.String())
	}
	rec = serve(mux, "GET", "/api/admin/users/"+acct.ID+"/billing-hold", nil)
	if !strings.Contains(rec.Body.String(), `"reason":"balance 0"`) {
		t.Fatalf("get: %s", rec.Body.String())
	}
	if rec = serve(mux, "PUT", "/api/admin/users/u_nope/billing-hold", map[string]any{"hold": true}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown user: %d", rec.Code)
	}
	if rec = serve(mux, "PUT", "/api/admin/users/"+acct.ID+"/billing-hold", map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing hold: %d", rec.Code)
	}
}

func TestUsageEventsEndpoint(t *testing.T) {
	_, st, acct, mux := billingHookServer(t)
	old := time.Now().UTC().Add(-time.Minute).Format("2006-01-02 15:04:05")
	for i := 0; i < 3; i++ {
		if _, err := st.DB().Exec(`INSERT INTO token_usage_log (user_id, agent_id, model, input_tokens, created_at) VALUES (?, 'agt', 'm', 7, ?)`, acct.ID, old); err != nil {
			t.Fatal(err)
		}
	}
	rec := serve(mux, "GET", "/api/admin/usage/events?limit=2", nil)
	var page struct {
		Events    []store.UsageEvent `json:"events"`
		NextAfter int64              `json:"next_after"`
		HasMore   bool               `json:"has_more"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Events) != 2 || !page.HasMore || page.Events[0].AccountID != acct.ID || page.Events[0].InputTokens != 7 {
		t.Fatalf("page 1: %s", rec.Body.String())
	}
	rec = serve(mux, "GET", "/api/admin/usage/events?after="+jsonInt(page.NextAfter), nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Events) != 1 || page.HasMore {
		t.Fatalf("page 2: %s", rec.Body.String())
	}
}

// Each pulled usage event carries cost_usd: the stored per-call cost, or
// null when the model had no price.
func TestUsageEventsCarryCost(t *testing.T) {
	_, st, acct, mux := billingHookServer(t)
	old := time.Now().UTC().Add(-time.Minute).Format("2006-01-02 15:04:05")
	for _, cost := range []any{0.0123, nil} {
		if _, err := st.DB().Exec(`INSERT INTO token_usage_log (user_id, agent_id, model, input_tokens, cost_usd, created_at) VALUES (?, 'agt', 'm', 7, ?, ?)`, acct.ID, cost, old); err != nil {
			t.Fatal(err)
		}
	}
	rec := serve(mux, "GET", "/api/admin/usage/events", nil)
	var page struct {
		Events []map[string]any `json:"events"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Events) != 2 {
		t.Fatalf("events: %s", rec.Body.String())
	}
	if page.Events[0]["cost_usd"] != 0.0123 {
		t.Fatalf("priced event: %v", page.Events[0])
	}
	if v, present := page.Events[1]["cost_usd"]; !present || v != nil {
		t.Fatalf("unpriced event must carry cost_usd: null, got %v (present=%v)", v, present)
	}
}

func jsonInt(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestLoginLink(t *testing.T) {
	_, _, acct, mux := billingHookServer(t)
	rec := serve(mux, "POST", "/api/admin/users/"+acct.ID+"/login-link", map[string]any{"redirect": "/console/agents/"})
	var out struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || !strings.HasPrefix(out.URL, "http://example.com/auth/login-link?") {
		t.Fatalf("mint: %d %s", rec.Code, rec.Body.String())
	}
	u, _ := url.Parse(out.URL)
	rec = serve(mux, "GET", u.RequestURI(), nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/console/agents/" {
		t.Fatalf("redeem: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), auth.SessionCookieName+"=") {
		t.Fatal("redeem must set the session cookie")
	}
	// Single use.
	rec = serve(mux, "GET", u.RequestURI(), nil)
	if rec.Header().Get("Set-Cookie") != "" || rec.Header().Get("Location") != "/" {
		t.Fatalf("second use: %q %q", rec.Header().Get("Set-Cookie"), rec.Header().Get("Location"))
	}
	// Redirects stay on this site.
	for _, bad := range []string{"https://evil.example", "//evil.example", "/\\evil.example"} {
		if got := safeRedirect(bad); got != "/" {
			t.Fatalf("safeRedirect(%q) = %q", bad, got)
		}
	}
}
