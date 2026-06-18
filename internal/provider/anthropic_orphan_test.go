package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestToAnthropicMessagesOrphanToolUse covers the exact session shape
// that produced API error 400 "tool_use ids were found without
// tool_result blocks immediately after" — the agent's loop-detection
// path appended a system warning + a synthetic cap-reached assistant
// message between the orphan tool_use and the deferred tool_result
// pad, breaking Anthropic's "tool_result must follow immediately"
// invariant. toAnthropicMessages must strip the orphan tool_use AND
// the dangling tool_result so the wire request passes validation.
func TestToAnthropicMessagesOrphanToolUse(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "go"},
		// Orphan assistant: tool_call followed by a system message
		// instead of a tool reply — recreates the loop-detection
		// path's mid-loop break.
		{
			Role:    "assistant",
			Content: "trying tool",
			ToolCalls: []ToolCall{{
				ID:       "toolu_orphan",
				Type:     "function",
				Function: FunctionCall{Name: "write_file", Arguments: `{"path":"x"}`},
			}},
		},
		{Role: "system", Content: "Loop detected: stopping."},
		{Role: "assistant", Content: "I've reached the cap, here's what I have."},
		// Late tool_result for the orphan; would dangle on its own
		// (Anthropic 400's both "ids without results" AND "results
		// without ids" — strip them together).
		{Role: "tool", ToolCallID: "toolu_orphan", Content: "interrupted"},
	}

	_, out := toAnthropicMessages(msgs)

	// Verify: no tool_use blocks appear anywhere, AND no tool_result
	// blocks either.
	for _, am := range out {
		var blocks []any
		if json.Unmarshal(am.Content, &blocks) == nil {
			for _, b := range blocks {
				mp, ok := b.(map[string]any)
				if !ok {
					continue
				}
				if bt, _ := mp["type"].(string); bt == "tool_use" || bt == "tool_result" {
					t.Errorf("orphan %s survived wire build: %+v", bt, mp)
				}
			}
		}
	}

	// The orphan assistant's TEXT content must survive (we strip
	// tool_calls but keep "trying tool"), and the cap-reached
	// assistant must too.
	got := allText(out)
	if !strings.Contains(got, "trying tool") {
		t.Errorf("orphan assistant text dropped; out=%v", got)
	}
	if !strings.Contains(got, "reached the cap") {
		t.Errorf("post-orphan assistant text dropped; out=%v", got)
	}
}

// TestToAnthropicMessagesOrphanAssistantEmpty covers msg 127 from the
// reported session: an assistant message whose only payload was the
// orphan tool_use (content=""). Stripping the tool_use leaves nothing,
// so the whole message must be dropped — emitting an empty wire
// message trips "expected a string or a list".
func TestToAnthropicMessagesOrphanAssistantEmpty(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "go"},
		{
			Role: "assistant",
			// no Content, no Thinking, just an orphan tool_call
			ToolCalls: []ToolCall{{
				ID:       "toolu_empty_orphan",
				Type:     "function",
				Function: FunctionCall{Name: "write_file", Arguments: `{"path":"y"}`},
			}},
		},
		{Role: "system", Content: "Loop detected."},
		{Role: "assistant", Content: "bailing out"},
		{Role: "tool", ToolCallID: "toolu_empty_orphan", Content: "interrupted"},
	}

	_, out := toAnthropicMessages(msgs)
	// Should contain: user "go", assistant "bailing out". The
	// orphan-only assistant and dangling tool reply both vanish.
	if len(out) != 2 {
		t.Fatalf("expected 2 messages after orphan strip, got %d: %+v", len(out), out)
	}
	if out[0].Role != "user" || out[1].Role != "assistant" {
		t.Errorf("unexpected role sequence: %s, %s", out[0].Role, out[1].Role)
	}
}

// TestToAnthropicMessagesOrphanAssistantRawOnly covers the WeChat
// "second message hits 400" case: a previous turn failed mid-loop, so
// the assistant message kept its tool_use blocks but never got a
// matching tool_result. Its RawAssistant was populated by the SSE
// stream's [DONE] handler (so len(RawAssistant) > 0), but the field
// holds the now-orphaned tool_use payload — not a thinking block — and
// the assistant has no plain Content/Thinking to fall back on.
//
// Before the fix, the orphan-drop predicate gated on
// `len(m.RawAssistant) == 0` and so refused to drop this message; the
// later branches in toAnthropicMessages couldn't synthesize anything
// for it and emitted `content: null`, triggering Anthropic's
// "messages.N.content: Input should be a valid array".
//
// After the fix, RawAssistant is excluded from the predicate — orphan
// + no text-shaped payload = drop.
func TestToAnthropicMessagesOrphanAssistantRawOnly(t *testing.T) {
	rawAsst := json.RawMessage(`{"role":"assistant","content":"","tool_calls":[{"id":"toolu_x","type":"function","function":{"name":"exec","arguments":"{}"}}]}`)
	msgs := []Message{
		{Role: "user", Content: "go"},
		{
			Role:         "assistant",
			RawAssistant: rawAsst, // captured from a stream that never produced text
			ToolCalls: []ToolCall{{
				ID:       "toolu_x",
				Type:     "function",
				Function: FunctionCall{Name: "exec", Arguments: `{}`},
			}},
		},
		// No tool reply, no follow-up assistant — orphan.
		{Role: "user", Content: "好了吗"},
	}

	_, out := toAnthropicMessages(msgs)

	// The orphan-only assistant must be dropped. Remaining wire
	// messages: user "go", user "好了吗".
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(out), out)
	}
	for _, am := range out {
		if am.Role != "user" {
			t.Errorf("unexpected role survived: %s (content=%s)", am.Role, string(am.Content))
		}
		// Anthropic rejects `null` content with the exact 400 we're
		// trying to prevent; assert no message emits it.
		if string(am.Content) == "null" || len(am.Content) == 0 {
			t.Errorf("message has null/empty content (would 400): %+v", am)
		}
	}
}

func TestToAnthropicMessagesMiniMaxCompatFlattensToolHistory(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "You are concise."},
		{Role: "user", Content: "what os"},
		{
			Role:    "assistant",
			Content: "I will check.",
			ToolCalls: []ToolCall{{
				ID:       "t1",
				Type:     "function",
				Function: FunctionCall{Name: "exec", Arguments: `{"command":"cat /etc/os-release"}`},
			}},
		},
		{Role: "tool", ToolCallID: "t1", Content: "Debian 12"},
		{Role: "assistant", Content: "It is Debian 12."},
		{Role: "user", Content: "continue"},
	}

	_, out := toAnthropicMessagesCompat(msgs, true)

	if len(out) != 3 {
		t.Fatalf("expected flattened alternating history, got %d messages: %+v", len(out), out)
	}
	if out[0].Role != "user" || out[1].Role != "assistant" || out[2].Role != "user" {
		t.Fatalf("unexpected role sequence: %s, %s, %s", out[0].Role, out[1].Role, out[2].Role)
	}

	for _, am := range out {
		var blocks []any
		if json.Unmarshal(am.Content, &blocks) == nil {
			for _, b := range blocks {
				mp, ok := b.(map[string]any)
				if !ok {
					continue
				}
				if bt, _ := mp["type"].(string); bt == "tool_use" || bt == "tool_result" {
					t.Fatalf("MiniMax compat history must not replay %s blocks: %+v", bt, mp)
				}
			}
		}
	}

	got := allText(out)
	if !strings.Contains(got, "Tool call: exec") {
		t.Errorf("flattened assistant missing tool call summary: %q", got)
	}
	if !strings.Contains(got, "Debian 12") {
		t.Errorf("flattened assistant missing tool result/final content: %q", got)
	}
}

func TestToAnthropicMessagesMiniMaxCompatCoalescesConsecutiveUsers(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "first failed turn"},
		{Role: "user", Content: "retry"},
	}

	_, out := toAnthropicMessagesCompat(msgs, true)

	if len(out) != 1 {
		t.Fatalf("expected one coalesced user message, got %d: %+v", len(out), out)
	}
	if out[0].Role != "user" {
		t.Fatalf("expected user role, got %s", out[0].Role)
	}
	got := allText(out)
	if !strings.Contains(got, "first failed turn") || !strings.Contains(got, "retry") {
		t.Fatalf("coalesced user content missing pieces: %q", got)
	}
}

func allText(out []anthropicMessage) string {
	var sb strings.Builder
	for _, am := range out {
		// Content can be either a bare JSON string or a block array.
		var s string
		if json.Unmarshal(am.Content, &s) == nil && s != "" {
			sb.WriteString(s)
			sb.WriteString("\n")
			continue
		}
		var blocks []any
		if json.Unmarshal(am.Content, &blocks) == nil {
			for _, b := range blocks {
				mp, ok := b.(map[string]any)
				if !ok {
					continue
				}
				if t, _ := mp["type"].(string); t == "text" {
					if txt, _ := mp["text"].(string); txt != "" {
						sb.WriteString(txt)
						sb.WriteString("\n")
					}
				}
			}
		}
	}
	return sb.String()
}
