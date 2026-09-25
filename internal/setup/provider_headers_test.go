package setup

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProviderHeadersMaskAndMerge(t *testing.T) {
	stored := map[string]string{
		"x-opencode-session": "{{session}}",
		"X-Secret":           "super-secret-token",
	}
	masked := maskProviderHeaders(stored)
	if masked["x-opencode-session"] != "{{session}}" {
		t.Fatalf("placeholder-only value should stay readable, got %q", masked["x-opencode-session"])
	}
	if masked["X-Secret"] == "super-secret-token" || !isMaskedSecret(masked["X-Secret"]) {
		t.Fatalf("secret header value leaked: %q", masked["X-Secret"])
	}

	// The dashboard sends back what it listed, plus edits.
	in := maps.Clone(masked)
	in["X-New"] = "v"
	in[" "] = "ignored"
	got := mergeProviderHeaders(in, stored)
	want := map[string]string{
		"x-opencode-session": "{{session}}",
		"X-Secret":           "super-secret-token",
		"X-New":              "v",
	}
	if !maps.Equal(got, want) {
		t.Fatalf("merge = %v, want %v", got, want)
	}
	if mergeProviderHeaders(map[string]string{}, stored) != nil {
		t.Fatalf("empty submission must clear headers")
	}
}

func TestRunProviderTestSendsCustomHeaders(t *testing.T) {
	var session string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session = r.Header.Get("X-Opencode-Session")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	res := runProviderTest(context.Background(), testProviderRequest{
		APIBase: srv.URL + "/v1", APIKey: "k", Model: "m", APIType: "openai-chat",
		Headers: map[string]string{"x-opencode-session": "{{session}}"},
	})
	if res["ok"] != true {
		t.Fatalf("provider test failed: %v", res)
	}
	if session == "" || session == "{{session}}" {
		t.Fatalf("test request session header = %q", session)
	}
}
