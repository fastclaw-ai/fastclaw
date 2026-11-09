package toolproviders

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/billing"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

type memEvents struct {
	mu     sync.Mutex
	events []store.MeteredEvent
	target store.BillingTarget
}

func (m *memEvents) InsertMeteredEvent(_ context.Context, e *store.MeteredEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, *e)
	return nil
}

func (m *memEvents) BillingTarget(_ context.Context, uid string) (store.BillingTarget, error) {
	t := m.target
	t.AccountID = uid
	return t, nil
}

type fakeProvider struct {
	category, name string
	resp           Response
	err            error
	calls          int
}

func (f *fakeProvider) Category() string     { return f.category }
func (f *fakeProvider) Name() string         { return f.name }
func (f *fakeProvider) CredentialFree() bool { return true }
func (f *fakeProvider) Execute(context.Context, Request) (Response, error) {
	f.calls++
	return f.resp, f.err
}

func chainFor(p *fakeProvider, ref string) *Chain {
	r := NewRegistry()
	r.Register(p)
	return &Chain{Category: p.category, Order: []string{ref}, Registry: r, GetConfig: func(string) ProviderConfig { return ProviderConfig{} }}
}

func turnCtx(st *memEvents) (context.Context, *billing.Turn) {
	turn := billing.NewService(st).NewTurn("u_end", "agt_1", "sess", "web")
	return billing.WithTurn(context.Background(), turn), turn
}

func TestChainRecordsUsageEvents(t *testing.T) {
	st := &memEvents{}
	ctx, turn := turnCtx(st)
	cases := []struct {
		p                       *fakeProvider
		ref                     string
		args                    map[string]any
		kind, prov, model, unit string
		units                   float64
		detail                  map[string]any
	}{
		{&fakeProvider{category: "image_gen", name: "fal", resp: Response{Text: "Generated 2 image(s) for: cat\n\n1. ![image 1](https://x/1.png)\n2. ![image 2](https://x/2.png)\n"}},
			"fal/flux-pro", map[string]any{"prompt": "cat", "n": float64(2), "size": "1024x1024"},
			"image", "fal", "flux-pro", "image", 2, map[string]any{"size": "1024x1024"}},
		{&fakeProvider{category: "tts", name: "openai", resp: Response{Text: "Generated audio: a.mp3\nMEDIA:/tmp/a.mp3"}},
			"openai/tts-1", map[string]any{"text": "  你好, world  "},
			"tts", "openai", "tts-1", "char", 9, nil},
		{&fakeProvider{category: "web_search", name: "brave", resp: Response{Text: "results"}},
			"brave", map[string]any{"query": "q"}, "search", "brave", "", "call", 1, nil},
		{&fakeProvider{category: "web_fetch", name: "jina", resp: Response{Text: "page"}},
			"jina", map[string]any{"url": "https://x"}, "fetch", "jina", "", "call", 1, nil},
	}
	for i, c := range cases {
		if _, err := chainFor(c.p, c.ref).Execute(ctx, c.args); err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		if len(st.events) != i+1 {
			t.Fatalf("%s: events = %d", c.kind, len(st.events))
		}
		e := st.events[i]
		if e.Kind != c.kind || e.Provider != c.prov || e.Model != c.model || e.Unit != c.unit || e.Units != c.units ||
			e.TurnID != turn.ID || e.UserID != "u_end" || e.AgentID != "agt_1" || e.Channel != "web" || e.CostUSD != nil {
			t.Fatalf("%s event = %+v", c.kind, e)
		}
		for k, v := range c.detail {
			if e.Detail[k] != v {
				t.Fatalf("%s detail = %v", c.kind, e.Detail)
			}
		}
	}

	// A failed provider call records nothing.
	failing := &fakeProvider{category: "web_search", name: "exa", err: errors.New("bad request")}
	if _, err := chainFor(failing, "exa").Execute(ctx, map[string]any{"query": "q"}); err == nil {
		t.Fatal("expected error")
	}
	if len(st.events) != len(cases) {
		t.Fatalf("failed call recorded: %+v", st.events[len(st.events)-1])
	}
}

func TestChainToolDenialSkipsTheProvider(t *testing.T) {
	t.Setenv(billing.EnvAllowPrivate, "true")
	var asked []billing.AuthorizeRequest
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req billing.AuthorizeRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		asked = append(asked, req)
		_, _ = io.WriteString(w, `{"allow": false, "message": "Your image credits are used up."}`)
	}))
	defer hook.Close()
	st := &memEvents{target: store.BillingTarget{HookURL: hook.URL, HookSecret: "x"}}
	ctx, _ := turnCtx(st)

	img := &fakeProvider{category: "image_gen", name: "fal", resp: Response{Text: "![image 1](u)"}}
	_, err := chainFor(img, "fal/flux-pro").Execute(ctx, map[string]any{"prompt": "cat", "n": float64(3)})
	if err == nil || err.Error() != "Your image credits are used up." {
		t.Fatalf("denied image_gen = %v", err)
	}
	if img.calls != 0 || len(st.events) != 0 {
		t.Fatalf("provider ran (%d) or event recorded (%d)", img.calls, len(st.events))
	}
	if len(asked) != 1 || asked[0].Kind != "image" || asked[0].Provider != "fal" || asked[0].Model != "flux-pro" ||
		asked[0].Estimate == nil || asked[0].Estimate.Units != 3 || asked[0].Estimate.Unit != "image" {
		t.Fatalf("hook asked %+v", asked)
	}

	// Search and fetch are cheap: never asked about individually.
	search := &fakeProvider{category: "web_search", name: "brave", resp: Response{Text: "r"}}
	if _, err := chainFor(search, "brave").Execute(ctx, map[string]any{"query": "q"}); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || len(st.events) != 1 {
		t.Fatalf("search asked the hook (%d) / events %d", len(asked), len(st.events))
	}
}
