package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureHeaders starts a server that records the headers of the last
// request and fails it, so Chat returns quickly with an error.
func captureHeaders(t *testing.T) (*httptest.Server, *http.Header) {
	t.Helper()
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		http.Error(w, `{"error":"stop"}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestProviderSendsCustomHeadersWithSessionID(t *testing.T) {
	headers := map[string]string{
		"x-opencode-session": SessionPlaceholder,
		"X-Static":           "static-value",
	}
	for _, apiType := range []string{"openai-chat", "anthropic-messages"} {
		t.Run(apiType, func(t *testing.T) {
			srv, got := captureHeaders(t)
			p := NewProvider("key", srv.URL, apiType, headers)
			msgs := []Message{{Role: "user", Content: "hi"}}

			_, _ = p.Chat(WithSessionKey(context.Background(), "telegram:bot:111"), msgs, nil, "m", 16, 0)
			first := got.Get("X-Opencode-Session")
			if got.Get("X-Static") != "static-value" {
				t.Fatalf("static header = %q", got.Get("X-Static"))
			}
			if first == "" || strings.Contains(first, "{{") || strings.Contains(first, "111") {
				t.Fatalf("session header must be a hashed ID, got %q", first)
			}

			_, _ = p.Chat(WithSessionKey(context.Background(), "telegram:bot:111"), msgs, nil, "m", 16, 0)
			if got.Get("X-Opencode-Session") != first {
				t.Fatalf("session ID not stable across turns of one conversation")
			}

			_, _ = p.Chat(WithSessionKey(context.Background(), "telegram:bot:222"), msgs, nil, "m", 16, 0)
			if got.Get("X-Opencode-Session") == first {
				t.Fatalf("different conversations must get different session IDs")
			}

			_, _ = p.Chat(context.Background(), msgs, nil, "m", 16, 0)
			if got.Get("X-Opencode-Session") == "" {
				t.Fatalf("calls outside a turn must still send the session header")
			}
		})
	}
}
