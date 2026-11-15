package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// planThenAnswerProvider says a plan with a tool call, then answers.
type planThenAnswerProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *planThenAnswerProvider) Chat(context.Context, []provider.Message, []provider.Tool, string, int, float64) (*provider.Response, error) {
	return nil, fmt.Errorf("unexpected nonstreaming request")
}

func (p *planThenAnswerProvider) ChatStream(context.Context, []provider.Message, []provider.Tool, string, int, float64) (*provider.StreamReader, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	chunks := make(chan provider.StreamChunk, 1)
	if n == 1 {
		chunks <- provider.StreamChunk{
			Content: "好，我先查一下，再告诉你",
			ToolCalls: []provider.ToolCall{{ID: "c1", Type: "function",
				Function: provider.FunctionCall{Name: "noop_probe", Arguments: "{}"}}},
			Done: true,
		}
	} else {
		chunks <- provider.StreamChunk{Content: "查好了：一切正常", Done: true}
	}
	close(chunks)
	return provider.NewStreamReader(chunks), nil
}

func newInterimAgent(t *testing.T) *Agent {
	t.Helper()
	a := NewAgent(config.ResolvedAgent{ID: "helper", Home: t.TempDir(), Model: "test", MaxTokens: 1024}, &planThenAnswerProvider{}, bus.New(), t.TempDir())
	a.registry.Register("noop_probe", "probe", map[string]any{"type": "object", "properties": map[string]any{}},
		func(context.Context, json.RawMessage) (string, error) { return "ok", nil })
	return a
}

// On an IM turn the plan goes out right away and the final reply doesn't
// repeat it.
func TestInterimTextGoesOutBeforeTheReply(t *testing.T) {
	a := newInterimAgent(t)
	var sent []string
	ctx := WithInterimSender(context.Background(), func(text string) { sent = append(sent, text) })
	reply := a.HandleMessage(ctx, bus.InboundMessage{Channel: "telegram", AccountID: "bot", ChatID: "c1", UserID: "u1", Text: "帮我查一下"})
	if len(sent) != 1 || sent[0] != "好，我先查一下，再告诉你" {
		t.Fatalf("interim = %q", sent)
	}
	if strings.Contains(reply, "我先查一下") || !strings.Contains(reply, "查好了") {
		t.Fatalf("reply = %q", reply)
	}
}

// Without a sender (web, API) the plan stays in the reply as before.
func TestInterimTextStaysInReplyWithoutSender(t *testing.T) {
	a := newInterimAgent(t)
	reply := a.HandleMessage(context.Background(), bus.InboundMessage{Channel: "telegram", AccountID: "bot", ChatID: "c2", UserID: "u1", Text: "帮我查一下"})
	if !strings.Contains(reply, "我先查一下") || !strings.Contains(reply, "查好了") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestPrefersChinese(t *testing.T) {
	for text, want := range map[string]bool{
		"你先帮我提交 10 个可以免费收录的导航站":            true,
		"submit my site to 10 directories": false,
		"帮我看下 https://example.com/pricing": true,
		"": false,
	} {
		if got := prefersChinese(text); got != want {
			t.Errorf("prefersChinese(%q) = %v, want %v", text, got, want)
		}
	}
}

// A Chinese turn that runs out of steps is told so in Chinese.
func TestTurnStopSpeaksTheirLanguage(t *testing.T) {
	zh := turnStop{reason: stopReasonCap, limit: 200, zh: true}
	if n := zh.notice("telegram"); !strings.Contains(n, "回复「继续」") {
		t.Fatalf("zh notice = %q", n)
	}
	if f := zh.fallback(); strings.Contains(f, "tool iterations") {
		t.Fatalf("zh fallback = %q", f)
	}
	if zh.notice("web") != "" {
		t.Fatal("web renders the badge; no in-band notice")
	}
}
