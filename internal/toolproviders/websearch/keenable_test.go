package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/toolproviders"
)

func withKeenableServer(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	oldPublic, oldKeyed := keenablePublicURL, keenableKeyedURL
	keenablePublicURL, keenableKeyedURL = srv.URL+"/public", srv.URL+"/keyed"
	t.Cleanup(func() {
		keenablePublicURL, keenableKeyedURL = oldPublic, oldKeyed
		srv.Close()
	})
}

func TestKeenableWithoutKeyUsesPublicEndpoint(t *testing.T) {
	withKeenableServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/public" || r.Header.Get("X-API-Key") != "" || r.Header.Get("X-Keenable-Title") != "fastclaw" {
			t.Errorf("unexpected request: path=%s headers=%v", r.URL.Path, r.Header)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["query"] != "golang" || body["max_results"] != float64(3) {
			t.Errorf("unexpected body: %v", body)
		}
		_, _ = w.Write([]byte(`{"results":[
			{"title":"Go","url":"https://go.dev","description":"The Go language","snippet":"long"},
			{"title":"no url","description":"dropped"},
			{"title":"Snip","url":"https://example.com","snippet":"` + strings.Repeat("x", 500) + `"}
		]}`))
	})

	resp, err := (&Keenable{}).Execute(context.Background(), toolproviders.Request{
		Args: map[string]any{"query": "golang", "count": float64(3)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Text, "1. Go\n   URL: https://go.dev\n   The Go language") {
		t.Fatalf("unexpected text: %q", resp.Text)
	}
	if strings.Contains(resp.Text, "no url") || strings.Contains(resp.Text, strings.Repeat("x", 300)) {
		t.Fatalf("expected URL-less result dropped and snippet truncated: %q", resp.Text)
	}
}

func TestKeenableWithKeyUsesKeyedEndpoint(t *testing.T) {
	withKeenableServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/keyed" || r.Header.Get("X-API-Key") != "k" {
			t.Errorf("unexpected request: path=%s key=%q", r.URL.Path, r.Header.Get("X-API-Key"))
		}
		_, _ = w.Write([]byte(`{"results":[{"title":"Go","url":"https://go.dev","description":"d"}]}`))
	})

	if _, err := (&Keenable{}).Execute(context.Background(), toolproviders.Request{
		Args:   map[string]any{"query": "golang"},
		Config: toolproviders.ProviderConfig{APIKey: "k"},
	}); err != nil {
		t.Fatal(err)
	}
}
