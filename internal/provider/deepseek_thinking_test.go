package provider

import (
	"context"
	"encoding/json"
	"io"
	"testing"
)

func assistantThinking(t *testing.T, body []byte) []bool {
	t.Helper()
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	var out []bool
	for _, m := range req.Messages {
		if m.Role != "assistant" {
			continue
		}
		var blocks []map[string]interface{}
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			out = append(out, false)
			continue
		}
		out = append(out, len(blocks) > 0 && blocks[0]["type"] == "thinking")
	}
	return out
}

func thinkingHistory() []Message {
	return []Message{
		{Role: "user", Content: "list files"},
		{Role: "assistant", Thinking: "look", ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: FunctionCall{Name: "exec", Arguments: `{"command":"ls"}`}}}},
		{Role: "tool", ToolCallID: "c1", Content: "a.txt"},
		// A step where the model called a tool without thinking.
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c2", Type: "function", Function: FunctionCall{Name: "exec", Arguments: `{"command":"cat a.txt"}`}}}},
		{Role: "tool", ToolCallID: "c2", Content: "hi"},
		{Role: "assistant", Content: "done"},
		{Role: "user", Content: "thanks"},
	}
}

// DeepSeek rejects replayed assistant steps without a thinking block, so
// every one gets at least an empty block.
func TestDeepSeekAnthropicAddsEmptyThinking(t *testing.T) {
	p := NewAnthropic("k", "https://api.deepseek.com/anthropic")
	req, err := p.buildRequest(context.Background(), thinkingHistory(), nil, "deepseek-flash", 100, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(req.Body)
	got := assistantThinking(t, body)
	if len(got) != 3 {
		t.Fatalf("assistant messages = %d", len(got))
	}
	for i, ok := range got {
		if !ok {
			t.Errorf("assistant %d has no thinking block", i)
		}
	}
}

// Real Anthropic needs signed thinking blocks; nothing is invented there.
func TestAnthropicKeepsHistoryAsIs(t *testing.T) {
	p := NewAnthropic("k", "")
	req, err := p.buildRequest(context.Background(), thinkingHistory(), nil, "claude-opus-5-5", 100, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(req.Body)
	got := assistantThinking(t, body)
	if want := []bool{true, false, false}; len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("thinking = %v, want %v", got, want)
	}
}
