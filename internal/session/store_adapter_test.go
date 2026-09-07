package session

import (
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func TestDisplaySessionTitle(t *testing.T) {
	tests := []struct {
		name        string
		storedTitle string
		sessionKey  string
		chatID      string
		preview     string
		want        string
	}{
		{
			name:       "empty title uses first user message",
			sessionKey: "s-1783753587119-lvlph0",
			chatID:     "s-1783753500000-browser",
			preview:    "帮我分析一下这个问题",
			want:       "帮我分析一下这个问题",
		},
		{
			name:        "legacy session id title uses first user message",
			storedTitle: "s-1783753587119-lvlph0",
			sessionKey:  "s-1783753587119-lvlph0",
			chatID:      "s-1783753500000-browser",
			preview:     "帮我分析一下这个问题",
			want:        "帮我分析一下这个问题",
		},
		{
			name:        "legacy chat id title uses first user message",
			storedTitle: "s-1783753500000-browser",
			sessionKey:  "s-1783753587119-lvlph0",
			chatID:      "s-1783753500000-browser",
			preview:     "帮我分析一下这个问题",
			want:        "帮我分析一下这个问题",
		},
		{
			name:        "legacy prefixed chat id title uses first user message",
			storedTitle: "web_s-1783753500000-browser",
			sessionKey:  "s-1783753587119-lvlph0",
			chatID:      "s-1783753500000-browser",
			preview:     "帮我分析一下这个问题",
			want:        "帮我分析一下这个问题",
		},
		{
			name:        "custom title is preserved",
			storedTitle: "故障排查",
			sessionKey:  "s-1783753587119-lvlph0",
			chatID:      "s-1783753500000-browser",
			preview:     "帮我分析一下这个问题",
			want:        "故障排查",
		},
		{
			name:        "surrounding whitespace is normalized",
			storedTitle: "  故障排查  ",
			sessionKey:  "s-1783753587119-lvlph0",
			chatID:      "s-1783753500000-browser",
			preview:     "帮我分析一下这个问题",
			want:        "故障排查",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displaySessionTitle(tt.storedTitle, tt.sessionKey, tt.chatID, tt.preview); got != tt.want {
				t.Fatalf("displaySessionTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLatestMessagePreviewUsesNewestVisibleExchangeAndMatchingTime(t *testing.T) {
	first := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	last := first.Add(2 * time.Minute)
	messages := []store.SessionMessage{
		{Role: "user", Content: "hello", Timestamp: first},
		{Role: "tool", Content: "internal tool output", Timestamp: first.Add(time.Minute)},
		{Role: "assistant", Content: "  first line\n\nsecond line  ", Timestamp: last},
		{Role: "user", Content: "hidden goal prompt", Origin: "goal_context", Timestamp: last.Add(time.Minute)},
	}

	gotText, gotAt := latestMessagePreview(messages)
	if gotText != "first line second line" {
		t.Fatalf("latest message = %q, want %q", gotText, "first line second line")
	}
	if gotAt != last.UnixMilli() {
		t.Fatalf("latest message timestamp = %d, want %d", gotAt, last.UnixMilli())
	}
}

func TestLatestMessagePreviewFallsBackToImageUserTurn(t *testing.T) {
	when := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	messages := []store.SessionMessage{
		{
			Role: "user",
			ContentParts: []map[string]any{
				{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/image.png"}},
			},
			Timestamp: when,
		},
	}

	gotText, gotAt := latestMessagePreview(messages)
	if gotText != "[image]" {
		t.Fatalf("latest message = %q, want [image]", gotText)
	}
	if gotAt != when.UnixMilli() {
		t.Fatalf("latest message timestamp = %d, want %d", gotAt, when.UnixMilli())
	}
}
