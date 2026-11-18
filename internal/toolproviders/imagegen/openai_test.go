package imagegen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/toolproviders"
)

func TestOpenAIDefaultsToCurrentImageModel(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"aGVsbG8="}]}`))
	}))
	defer server.Close()

	provider := &OpenAI{}
	response, err := provider.Execute(context.Background(), toolproviders.Request{
		Args: map[string]any{"prompt": "draw a cat"},
		Config: toolproviders.ProviderConfig{
			APIKey:   "test-key",
			Endpoint: server.URL,
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := body["model"]; got != "gpt-image-2.6-flare" {
		t.Fatalf("model = %v, want gpt-image-2.6-flare", got)
	}
	if got := body["size"]; got != "1024x1024" {
		t.Fatalf("size = %v, want 1024x1024", got)
	}
	if !strings.Contains(response.Text, "data:image/png;base64,aGVsbG8=") {
		t.Fatalf("text = %q, want inline base64 image", response.Text)
	}
}
