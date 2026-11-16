package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// emptyThenAnswerProvider returns `empties` replies with no text and no
// tool calls, then an answer.
type emptyThenAnswerProvider struct {
	mu      sync.Mutex
	calls   int
	empties int
}

func (p *emptyThenAnswerProvider) Chat(context.Context, []provider.Message, []provider.Tool, string, int, float64) (*provider.Response, error) {
	return nil, fmt.Errorf("unexpected nonstreaming request")
}

func (p *emptyThenAnswerProvider) ChatStream(context.Context, []provider.Message, []provider.Tool, string, int, float64) (*provider.StreamReader, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	chunks := make(chan provider.StreamChunk, 1)
	if n <= p.empties {
		chunks <- provider.StreamChunk{Done: true}
	} else {
		chunks <- provider.StreamChunk{Content: "在呢", Done: true}
	}
	close(chunks)
	return provider.NewStreamReader(chunks), nil
}

// A provider hiccup that returns an empty reply is asked again instead
// of failing the turn.
func TestEmptyReplyIsRetried(t *testing.T) {
	p := &emptyThenAnswerProvider{empties: maxEmptyReplyRetries}
	a := NewAgent(config.ResolvedAgent{ID: "helper", Home: t.TempDir(), Model: "test", MaxTokens: 1024}, p, bus.New(), t.TempDir())
	reply := a.HandleMessage(context.Background(), bus.InboundMessage{Channel: "telegram", AccountID: "bot", ChatID: "e1", UserID: "u1", Text: "在吗"})
	if reply != "在呢" {
		t.Fatalf("reply = %q", reply)
	}
	if p.calls != maxEmptyReplyRetries+1 {
		t.Fatalf("calls = %d, want %d", p.calls, maxEmptyReplyRetries+1)
	}
}

// Past the retries, the turn still fails rather than looping.
func TestEmptyReplyGivesUpAfterRetries(t *testing.T) {
	p := &emptyThenAnswerProvider{empties: 100}
	a := NewAgent(config.ResolvedAgent{ID: "helper", Home: t.TempDir(), Model: "test", MaxTokens: 1024}, p, bus.New(), t.TempDir())
	reply := a.HandleMessage(context.Background(), bus.InboundMessage{Channel: "telegram", AccountID: "bot", ChatID: "e2", UserID: "u1", Text: "在吗"})
	if reply != "model returned an empty response" {
		t.Fatalf("reply = %q", reply)
	}
	if p.calls != maxEmptyReplyRetries+1 {
		t.Fatalf("calls = %d, want %d", p.calls, maxEmptyReplyRetries+1)
	}
}
