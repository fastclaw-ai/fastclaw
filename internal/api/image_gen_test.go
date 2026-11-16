package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/billing"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/toolproviders"
)

// pngBytes is enough of a PNG for content sniffing.
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)

// storedImages answers with one picture as bytes and one as a URL.
type storedImages struct {
	mu   sync.Mutex
	url  string
	args []map[string]any
}

func (f *storedImages) Category() string     { return "image_gen" }
func (f *storedImages) Name() string         { return "fal" }
func (f *storedImages) CredentialFree() bool { return true }
func (f *storedImages) Execute(_ context.Context, req toolproviders.Request) (toolproviders.Response, error) {
	f.mu.Lock()
	f.args = append(f.args, req.Args)
	f.mu.Unlock()
	return toolproviders.Response{
		Text:   "Generated 2 image(s)",
		Images: []toolproviders.Image{{Data: pngBytes, ContentType: "image/png"}, {URL: f.url}},
	}, nil
}

// image_gen stores the pictures in the chat's workspace: the model gets
// short /workspace paths (never image data or the provider's URL), and
// both pictures are metered. (Where they are stored: tools tests.)
func TestV1ImageGenStoresPicturesInTheWorkspace(t *testing.T) {
	h := newV1Harness(t)
	t.Setenv(billing.EnvAllowPrivate, "true")
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer cdn.Close()

	id := h.createAgent(h.keyA, "Painter", nil)
	if err := h.rt.EnsureAgent(context.Background(), h.appA, id); err != nil {
		t.Fatal(err)
	}
	images := &storedImages{url: cdn.URL + "/out.png"}
	reg := toolproviders.NewRegistry()
	reg.Register(images)
	h.rt.manager(h.appA).AgentByID(id).RegisterImageGenChain(&toolproviders.Chain{
		Category: "image_gen", Order: []string{"fal/seedream"}, Registry: reg,
		GetConfig: func(string) toolproviders.ProviderConfig { return toolproviders.ProviderConfig{} },
	})
	h.rt.prov.mu.Lock()
	h.rt.prov.script = []provider.Response{
		{ToolCalls: []provider.ToolCall{{ID: "call_1", Type: "function", Function: provider.FunctionCall{Name: "image_gen", Arguments: `{"prompt":"a cyberpunk cat","n":2,"aspect":"portrait"}`}}}},
		{Content: "done"},
	}
	h.rt.prov.mu.Unlock()

	code, _, body := h.chatTurn(h.keyA, id, "", false, "conv_img")
	if code != 200 {
		t.Fatalf("turn: %d %s", code, body)
	}
	h.rt.prov.mu.Lock()
	var toolResult string
	for _, m := range h.rt.prov.last {
		if m.Role == "tool" {
			toolResult = m.Content
		}
	}
	h.rt.prov.mu.Unlock()
	var out struct {
		Images     []string `json:"images"`
		PutInReply string   `json:"putInReply"`
	}
	if err := json.Unmarshal([]byte(toolResult), &out); err != nil || len(out.Images) != 2 {
		t.Fatalf("tool result = %q (%v)", toolResult, err)
	}
	if strings.Contains(toolResult, "base64") || strings.Contains(toolResult, cdn.URL) {
		t.Fatalf("image data or the provider URL reached the model: %q", toolResult)
	}
	for _, p := range out.Images {
		if !strings.HasPrefix(p, "/workspace/picture-") || !strings.HasSuffix(p, ".png") {
			t.Fatalf("path = %q", p)
		}
		if !strings.Contains(out.PutInReply, "![]("+p+")") {
			t.Fatalf("putInReply = %q lacks %s", out.PutInReply, p)
		}
	}
	if images.args[0]["aspect"] != "portrait" {
		t.Fatalf("provider args = %v", images.args[0])
	}

	// Metered per picture.
	h.ageUsageEvents()
	_, ev := h.do("GET", "/v1/usage/events?after=0", h.keyA, nil)
	units := 0.0
	for _, e := range eventsOf(ev) {
		if e["kind"] == "image" {
			units, _ = e["units"].(float64)
		}
	}
	if units != 2 {
		t.Fatalf("image units = %v, events %v", units, ev)
	}
}
