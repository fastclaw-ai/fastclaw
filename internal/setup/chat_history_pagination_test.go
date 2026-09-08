package setup

import "testing"

func TestPaginateChatHistoryKeepsTurnsWhole(t *testing.T) {
	history := []map[string]any{
		{"role": "user", "content": "one"},
		{"role": "assistant", "content": "reply one"},
		{"role": "user", "content": "two"},
		{"role": "assistant", "toolCalls": []any{"call"}},
		{"role": "tool", "content": "result"},
		{"role": "assistant", "content": "reply two"},
		{"role": "user", "content": "three"},
		{"role": "assistant", "content": "reply three"},
	}

	latest, start, hasMore := paginateChatHistory(history, 3, len(history))
	if start != 6 || !hasMore || len(latest) != 2 {
		t.Fatalf("latest page = start %d, hasMore %v, len %d; want 6, true, 2", start, hasMore, len(latest))
	}
	if latest[0]["role"] != "user" {
		t.Fatalf("latest page starts with %v; want user", latest[0]["role"])
	}

	older, olderStart, olderHasMore := paginateChatHistory(history, 3, start)
	if olderStart != 2 || !olderHasMore || len(older) != 4 {
		t.Fatalf("older page = start %d, hasMore %v, len %d; want 2, true, 4", olderStart, olderHasMore, len(older))
	}
	if older[0]["role"] != "user" || older[len(older)-1]["role"] != "assistant" {
		t.Fatalf("older page did not preserve a complete turn: %#v", older)
	}

	oldest, oldestStart, oldestHasMore := paginateChatHistory(history, 3, olderStart)
	if oldestStart != 0 || oldestHasMore || len(oldest) != 2 {
		t.Fatalf("oldest page = start %d, hasMore %v, len %d; want 0, false, 2", oldestStart, oldestHasMore, len(oldest))
	}
}
