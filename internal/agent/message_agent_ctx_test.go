package agent

import (
	"context"
	"testing"
)

// The recipient's turn must not write to the caller's stream: its events
// would reach the person (its raw reply as a separate message) and its
// end-of-turn could close the caller's stream.
func TestRecipientTurnContextDetachesCallerStream(t *testing.T) {
	ch := make(chan ChatEvent, 4)
	legacy := make(chan ChatEvent, 4)
	ctx := ContextWithStream(context.Background(), ch, nil, nil, "u", "a", "s")
	ctx = ContextWithChatEvents(ctx, legacy)
	ctx = WithTurnModel(ctx, "openai/gpt-x")
	ctx = ContextWithLocalAgentsUser(ctx, "u")

	rctx := recipientTurnContext(ctx, 1)
	emitEvent(rctx, ChatEvent{Type: "content", Data: map[string]any{"text": "recipient reply"}})
	if len(ch) != 0 || len(legacy) != 0 {
		t.Fatalf("recipient event reached the caller's stream: %d / %d", len(ch), len(legacy))
	}
	if m, _ := rctx.Value(turnModelKey{}).(string); m != "" {
		t.Fatalf("recipient inherited the caller's turn model %q", m)
	}
	if localAgentsWebUser(rctx) != "" {
		t.Fatal("recipient inherited the caller's web local-agents user")
	}
	if d, _ := rctx.Value(messageAgentDepthKey{}).(int); d != 1 {
		t.Fatalf("depth = %d", d)
	}
	// The caller's own context is untouched.
	emitEvent(ctx, ChatEvent{Type: "content"})
	if len(ch) != 1 {
		t.Fatal("caller stream broken")
	}
}
