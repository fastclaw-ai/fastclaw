package provider

import (
	"strings"
	"testing"
)

// Every system message reaches Anthropic's single system string, in order:
// the agent loop adds notes (channel hints, the agent roster, local
// agents, …) after the main prompt, and keeping only the last one dropped
// the main prompt.
func TestToAnthropicMessagesJoinsSystemMessages(t *testing.T) {
	system, out := toAnthropicMessages([]Message{
		{Role: "system", Content: "MAIN PROMPT"},
		{Role: "system", Content: "ROSTER"},
		{Role: "system", Content: "  "},
		{Role: "system", Content: "REMINDER"},
		{Role: "user", Content: "hi"},
	})
	if system != "MAIN PROMPT\n\nROSTER\n\nREMINDER" {
		t.Fatalf("system = %q", system)
	}
	if len(out) != 1 || out[0].Role != "user" || strings.Contains(system, "hi") {
		t.Fatalf("messages = %+v", out)
	}
}
