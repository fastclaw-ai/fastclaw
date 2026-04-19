package agent

import "context"

// ChatEvent represents a real-time event emitted during the agent ReAct loop.
type ChatEvent struct {
	Type string         `json:"type"` // "content", "tool_call", "tool_result", "done"
	Data map[string]any `json:"data,omitempty"`
}

type chatEventsKey struct{}

// ChatEventsFromContext retrieves the events channel from context, if present.
func ChatEventsFromContext(ctx context.Context) chan<- ChatEvent {
	ch, _ := ctx.Value(chatEventsKey{}).(chan<- ChatEvent)
	return ch
}

// ContextWithChatEvents returns a new context with the events channel attached.
func ContextWithChatEvents(ctx context.Context, ch chan<- ChatEvent) context.Context {
	return context.WithValue(ctx, chatEventsKey{}, ch)
}

// emitEvent sends an event to the channel in context, if present. Non-blocking.
func emitEvent(ctx context.Context, evt ChatEvent) {
	ch := ChatEventsFromContext(ctx)
	if ch == nil {
		return
	}
	select {
	case ch <- evt:
	case <-ctx.Done():
	}
}
