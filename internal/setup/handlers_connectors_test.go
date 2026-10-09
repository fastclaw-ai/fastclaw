package setup

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/connectors"
)

func TestConnectorsConfigAdmin(t *testing.T) {
	ctx := context.Background()
	s, resolver, admin, regular := newAuthTestServer(t, ctx)
	connectors.SetEnvFallback(connectors.Settings{})
	t.Cleanup(func() {
		connectors.SetEnvFallback(connectors.Settings{})
		_ = connectors.Configure(ctx, "", "", nil)
	})

	// A Connany that accepts only "good-key-1234".
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-key-1234" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"categories":[],"data":[{"name":"notion","title":"Notion","avatar_url":""}]}`))
	}))
	defer fake.Close()

	call := func(h http.HandlerFunc, method, body, userID string) (int, map[string]any) {
		t.Helper()
		req := authTestRequest(t, ctx, resolver, method, "/api/admin/connectors-config", userID)
		if body != "" {
			req.Body = io.NopCloser(strings.NewReader(body))
		}
		rr := httptest.NewRecorder()
		s.requireSuperAdmin(h)(rr, req)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}

	if code, _ := call(s.handleGetConnectorsConfig, http.MethodGet, "", regular.ID); code != http.StatusForbidden {
		t.Fatalf("regular user: %d", code)
	}
	if _, view := call(s.handleGetConnectorsConfig, http.MethodGet, "", admin.ID); view["enabled"] != false || view["source"] != "" {
		t.Fatalf("initial view: %v", view)
	}

	// A wrong key is refused and nothing is saved.
	code, out := call(s.handleSaveConnectorsConfig, http.MethodPut, `{"url":"`+fake.URL+`","apiKey":"wrong"}`, admin.ID)
	if code != http.StatusBadRequest || out["code"] != "invalid_key" || connectors.Get() != nil {
		t.Fatalf("wrong key: %d %v", code, out)
	}

	// A good key is saved and takes effect at once; the key never comes back.
	code, out = call(s.handleSaveConnectorsConfig, http.MethodPut, `{"url":"`+fake.URL+`/","apiKey":"good-key-1234"}`, admin.ID)
	if code != http.StatusOK || out["enabled"] != true || out["source"] != "settings" || out["apiKeyHint"] != "…1234" || out["url"] != fake.URL {
		t.Fatalf("save: %d %v", code, out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "good-key") {
		t.Fatalf("key leaked: %s", raw)
	}
	if connectors.Get() == nil {
		t.Fatal("service not configured after save")
	}

	// Saving again without a key keeps the stored one.
	if code, out = call(s.handleSaveConnectorsConfig, http.MethodPut, `{"url":"`+fake.URL+`","apiKey":""}`, admin.ID); code != http.StatusOK {
		t.Fatalf("keep key: %d %v", code, out)
	}

	// Clearing falls back to the env configuration (here: none → off).
	connectors.SetEnvFallback(connectors.Settings{URL: fake.URL, APIKey: "good-key-1234"})
	code, out = call(s.handleClearConnectorsConfig, http.MethodDelete, "", admin.ID)
	if code != http.StatusOK || out["source"] != "env" || out["enabled"] != true {
		t.Fatalf("clear → env: %d %v", code, out)
	}
	connectors.SetEnvFallback(connectors.Settings{})
	if _, out = call(s.handleClearConnectorsConfig, http.MethodDelete, "", admin.ID); out["enabled"] != false {
		t.Fatalf("clear → off: %v", out)
	}
}
