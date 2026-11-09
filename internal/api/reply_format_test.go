package api

import (
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

func TestReplyTextForChatCallersKeepsMessages(t *testing.T) {
	reply := "有结果了。\n<|split|>\n他主要在做 AI 应用。"
	if got := replyTextFor(bus.InboundMessage{ChatReply: true}, reply); got != reply {
		t.Fatalf("chat caller: got %q, want the markers kept", got)
	}
	want := "有结果了。\n\n\n\n他主要在做 AI 应用。"
	if got := replyTextFor(bus.InboundMessage{}, reply); got != want {
		t.Fatalf("plain API caller: got %q, want %q", got, want)
	}
}
