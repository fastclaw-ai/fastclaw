package provider

import (
	"strings"
	"testing"
)

// Two overlapping turns on one session: one padded a stopped result for
// call_x, the other appended the real one. The API 400s on a second
// tool_result for one id, so only the real result may go out.
func TestFindOrphanToolCalls_DuplicateAndStrayResults(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "call_x", Type: "function", Function: FunctionCall{Name: "write_file"}},
			{ID: "call_y", Type: "function", Function: FunctionCall{Name: "set_timezone"}},
		}},
		{Role: "tool", ToolCallID: "call_x", Content: "(stopped — execution was interrupted before the tool returned)"},
		{Role: "tool", ToolCallID: "call_y", Content: "Timezone saved"},
		{Role: "tool", ToolCallID: "call_x", Content: "Written 297 bytes to USER.md"},
		{Role: "tool", ToolCallID: "call_z", Content: "not called here"},
		{Role: "assistant", Content: "done"},
		{Role: "tool", ToolCallID: "call_y", Content: "stray, after a plain reply"},
		{Role: "user", Content: "again"},
	}
	orphanAssist, orphanTool := findOrphanToolCalls(msgs)
	if orphanAssist[1] {
		t.Fatal("assistant flagged orphan though every call has a result")
	}
	for i, want := range map[int]bool{2: true, 3: false, 4: false, 5: true, 7: true} {
		if orphanTool[i] != want {
			t.Errorf("orphanTool[%d] = %v, want %v", i, orphanTool[i], want)
		}
	}

	wire := toAPIMessages(msgs, "")
	joined := ""
	for _, w := range wire {
		joined += string(w) + "\n"
	}
	if n := strings.Count(joined, `"tool_call_id":"call_x"`); n != 1 {
		t.Errorf("call_x results on the wire = %d, want 1\n%s", n, joined)
	}
	if strings.Contains(joined, "stopped") || strings.Contains(joined, "call_z") || strings.Contains(joined, "stray") {
		t.Errorf("pad, stray or uncalled result reached the wire:\n%s", joined)
	}

	// Anthropic: one tool_result per tool_use.
	_, am := toAnthropicMessages(msgs)
	n := 0
	for _, m := range am {
		n += strings.Count(string(m.Content), `"tool_use_id":"call_x"`)
	}
	if n != 1 {
		t.Errorf("anthropic call_x tool_results = %d, want 1", n)
	}
}
