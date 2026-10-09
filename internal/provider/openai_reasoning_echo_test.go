package provider

import (
	"encoding/json"
	"testing"
)

// DeepSeek thinking mode rejects any assistant message without
// reasoning_content. Steps with no reasoning (skipped thinking, or served
// by a non-thinking backend behind a routing model) must still carry an
// empty one.
func TestToAPIMessagesEchoesEmptyReasoning(t *testing.T) {
	call := []ToolCall{{ID: "call_1", Type: "function", Function: FunctionCall{Name: "ls", Arguments: "{}"}}}
	msgs := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Thinking: "look first", ToolCalls: call},
		{Role: "tool", ToolCallID: "call_1", Content: "a.txt"},
		{Role: "assistant", RawAssistant: json.RawMessage(`{"role":"assistant","content":"","tool_calls":[{"id":"call_2","type":"function","function":{"name":"ls","arguments":"{}"}}]}`)},
		{Role: "tool", ToolCallID: "call_2", Content: "b.txt"},
		{Role: "assistant", Content: "done"},
	}
	wire := toAPIMessages(msgs, "autojev/fast")
	for _, i := range []int{1, 3, 5} {
		var obj map[string]any
		if err := json.Unmarshal(wire[i], &obj); err != nil {
			t.Fatal(err)
		}
		if _, ok := obj["reasoning_content"]; !ok {
			t.Errorf("assistant %d missing reasoning_content: %s", i, wire[i])
		}
	}

	// Plain OpenAI-style history: no field added.
	plain := []Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}
	if got := string(toAPIMessages(plain, "gpt-4o")[1]); got != `{"role":"assistant","content":"hello"}` {
		t.Errorf("non-thinking history changed: %s", got)
	}
	// DeepSeek by name, even before any reasoning was seen.
	var obj map[string]any
	json.Unmarshal(toAPIMessages(plain, "deepseek-chat")[1], &obj)
	if _, ok := obj["reasoning_content"]; !ok {
		t.Error("deepseek model should echo reasoning_content")
	}
}
