package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// scriptedStreamProvider fails or succeeds ChatStream with a canned
// payload and records every model it was asked for, so tests can
// assert exactly which links of the fallback chain were exercised.
type scriptedStreamProvider struct {
	content string
	err     error
	models  []string
}

func (p *scriptedStreamProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	return nil, errors.New("scriptedStreamProvider: Chat not used by these tests")
}

func (p *scriptedStreamProvider) ChatStream(_ context.Context, _ []provider.Message, _ []provider.Tool, model string, _ int, _ float64) (*provider.StreamReader, error) {
	p.models = append(p.models, model)
	if p.err != nil {
		return nil, p.err
	}
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{Content: p.content, Done: true}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

func drainStream(t *testing.T, sr *provider.StreamReader) string {
	t.Helper()
	var b strings.Builder
	for {
		chunk, ok := sr.Next()
		if !ok {
			break
		}
		b.WriteString(chunk.Content)
	}
	if err := sr.Err(); err != nil {
		t.Fatalf("stream err: %v", err)
	}
	return b.String()
}

// fallbackAgent builds the minimal Agent chatStreamWithFallback needs.
// The full constructor drags in skills/memory/filesystem wiring and
// none of it participates in the failover decision.
func fallbackAgent(primary provider.Provider, fallbacks ...fallbackClient) *Agent {
	return &Agent{
		name:           "test-agent",
		provider:       primary,
		model:          "primary/model-a",
		maxTokens:      128,
		modelFallbacks: fallbacks,
	}
}

// An availability failure on the primary (503) must fail over and
// deliver the fallback's stream — the user gets an answer instead of
// the generic apology line.
func TestChatStreamFallsBackOnAvailabilityError(t *testing.T) {
	primary := &scriptedStreamProvider{err: &provider.APIError{StatusCode: 503, Body: "overloaded"}}
	fb := &scriptedStreamProvider{content: "from fallback"}
	a := fallbackAgent(primary, fallbackClient{model: "backup/model-b", prov: fb})

	sr, served, err := a.chatStreamWithFallback(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("expected fallback to succeed, got %v", err)
	}
	if served != "backup/model-b" {
		t.Errorf("served model = %q, want backup/model-b", served)
	}
	if got := drainStream(t, sr); got != "from fallback" {
		t.Errorf("content = %q, want %q", got, "from fallback")
	}
	if len(fb.models) != 1 || fb.models[0] != "backup/model-b" {
		t.Errorf("fallback should be called once with its own model, got %v", fb.models)
	}
}

// A 400 is the request's fault, not the provider's — replaying the
// same payload at another vendor would just burn quota on a second
// rejection. The fallback must never be touched.
func TestChatStreamNoFallbackOnRequestError(t *testing.T) {
	primary := &scriptedStreamProvider{err: &provider.APIError{StatusCode: 400, Body: "bad request"}}
	fb := &scriptedStreamProvider{content: "never delivered"}
	a := fallbackAgent(primary, fallbackClient{model: "backup/model-b", prov: fb})

	_, _, err := a.chatStreamWithFallback(context.Background(), nil, nil)
	var apiErr *provider.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
		t.Fatalf("want the original 400 back, got %v", err)
	}
	if len(fb.models) != 0 {
		t.Fatalf("fallback must not be called on 4xx, got calls for %v", fb.models)
	}
}

// No fallbacks configured → identical to the old direct ChatStream
// call: the primary's error propagates unchanged.
func TestChatStreamErrorUnchangedWithoutFallbacks(t *testing.T) {
	primaryErr := &provider.APIError{StatusCode: 503, Body: "overloaded"}
	primary := &scriptedStreamProvider{err: primaryErr}
	a := fallbackAgent(primary)

	_, _, err := a.chatStreamWithFallback(context.Background(), nil, nil)
	if !errors.Is(err, primaryErr) {
		t.Fatalf("error should propagate unchanged, got %v", err)
	}
	if len(primary.models) != 1 || primary.models[0] != "primary/model-a" {
		t.Errorf("primary should be called exactly once, got %v", primary.models)
	}
}

// Healthy primary: the wrapper is a pass-through and the fallback
// chain stays cold.
func TestChatStreamPrimaryHealthySkipsFallbacks(t *testing.T) {
	primary := &scriptedStreamProvider{content: "from primary"}
	fb := &scriptedStreamProvider{content: "never delivered"}
	a := fallbackAgent(primary, fallbackClient{model: "backup/model-b", prov: fb})

	sr, served, err := a.chatStreamWithFallback(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("primary should succeed, got %v", err)
	}
	if served != "primary/model-a" {
		t.Errorf("served model = %q, want primary/model-a", served)
	}
	if got := drainStream(t, sr); got != "from primary" {
		t.Errorf("content = %q, want %q", got, "from primary")
	}
	if len(fb.models) != 0 {
		t.Errorf("fallback must stay cold when primary works, got %v", fb.models)
	}
}

// Every link down → the LAST error surfaces (each hop's cause was
// already logged) and every fallback was tried in order.
func TestChatStreamAllFallbacksFailReturnsLastError(t *testing.T) {
	primary := &scriptedStreamProvider{err: &provider.APIError{StatusCode: 503, Body: "primary down"}}
	fb1 := &scriptedStreamProvider{err: &provider.APIError{StatusCode: 502, Body: "fb1 down"}}
	lastErr := &provider.APIError{StatusCode: 529, Body: "fb2 down"}
	fb2 := &scriptedStreamProvider{err: lastErr}
	a := fallbackAgent(primary,
		fallbackClient{model: "backup/one", prov: fb1},
		fallbackClient{model: "backup/two", prov: fb2},
	)

	sr, _, err := a.chatStreamWithFallback(context.Background(), nil, nil)
	if sr != nil {
		t.Fatal("no stream should be returned when every model fails")
	}
	if !errors.Is(err, lastErr) {
		t.Fatalf("want the last fallback's error, got %v", err)
	}
	if len(fb1.models) != 1 || len(fb2.models) != 1 {
		t.Errorf("both fallbacks should be tried once, got fb1=%v fb2=%v", fb1.models, fb2.models)
	}
}

// fallbacksForAgent must skip entries it can't build a provider for —
// a typo'd fallback degrades to a logged skip, never a broken agent or
// a request fired at the wrong vendor's base URL.
func TestFallbacksForAgentSkipsUnresolvable(t *testing.T) {
	rc := config.ResolvedAgent{
		ID:    "a1",
		Model: "openai/gpt-5.5",
		ModelFallbacks: []string{
			"openai/gpt-5.5",           // same as primary → skip
			"missing/model",            // no provider entry → skip
			"nokey/model",              // provider has no API key → skip
			"anthropic/claude-fable-5", // resolvable → kept
		},
		Providers: map[string]config.ProviderConfig{
			"openai":    {APIKey: "k1"},
			"nokey":     {},
			"anthropic": {APIKey: "k2", APIType: "anthropic-messages"},
		},
	}
	fbs := fallbacksForAgent(rc)
	if len(fbs) != 1 {
		t.Fatalf("want exactly one resolvable fallback, got %d", len(fbs))
	}
	if fbs[0].model != "anthropic/claude-fable-5" || fbs[0].prov == nil {
		t.Fatalf("want the anthropic fallback with a built provider, got %+v", fbs[0])
	}
}
