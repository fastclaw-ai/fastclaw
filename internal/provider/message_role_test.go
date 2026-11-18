package provider

import (
	"encoding/json"
	"testing"
)

func TestWireMessagesSkipRolelessHistory(t *testing.T) {
	history := []Message{
		{Role: "system", Content: "instructions"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Function: FunctionCall{Name: "lookup", Arguments: "{}"}}}},
		{},
		{Role: "tool", ToolCallID: "call_1", Content: "result"},
		{Role: "user", Content: "hello"},
	}

	openAI := toAPIMessages(history, "model")
	if len(openAI) != 4 || !json.Valid(openAI[1]) {
		t.Fatalf("unexpected OpenAI messages: %s", openAI)
	}
	var assistant apiMessage
	if err := json.Unmarshal(openAI[1], &assistant); err != nil || len(assistant.ToolCalls) != 1 {
		t.Fatalf("tool call lost: %s", openAI[1])
	}

	system, anthropic := toAnthropicMessages(history)
	if system != "instructions" || len(anthropic) != 3 || anthropic[0].Role != "assistant" ||
		anthropic[1].Role != "user" || anthropic[2].Role != "user" {
		t.Fatalf("unexpected Anthropic messages: %s, %+v", system, anthropic)
	}
	if history[2].Role != "" || history[3].Role != "tool" {
		t.Fatal("wire conversion changed history")
	}
}

func TestOpenAIRepairsCachedAssistantRole(t *testing.T) {
	raw := json.RawMessage(`{"role":"","content":"working","reasoning_content":"reasoning","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}`)
	out := toAPIMessages([]Message{
		{Role: "assistant", RawAssistant: raw},
		{Role: "tool", ToolCallID: "call_1", Content: "result"},
	}, "model")
	if len(out) != 2 {
		t.Fatalf("unexpected messages: %s", out)
	}
	var assistant apiMessage
	if err := json.Unmarshal(out[0], &assistant); err != nil || assistant.Role != "assistant" ||
		assistant.ReasoningContent != "reasoning" || len(assistant.ToolCalls) != 1 {
		t.Fatalf("unexpected repaired assistant: %s", out[0])
	}
}
