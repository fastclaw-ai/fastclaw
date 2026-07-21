package gateway

import (
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

func TestWeComDMDeduplicatesStableMessageID(t *testing.T) {
	g := &Gateway{}
	msg := bus.InboundMessage{
		Channel: "wecom", AccountID: "bot-1", ChatID: "member-1",
		UserID: "member-1", PeerKind: "dm", MessageID: "msg-1", Text: "hello",
	}
	if g.isDuplicate(msg) {
		t.Fatal("first WeCom DM was duplicate")
	}
	if !g.isDuplicate(msg) {
		t.Fatal("repeated WeCom DM message ID was not duplicate")
	}
}

func TestWeComGroupDeduplicatesByMessageID(t *testing.T) {
	g := &Gateway{}
	first := bus.InboundMessage{
		Channel: "wecom", AccountID: "bot-1", ChatID: "group-1",
		UserID: "member-1", PeerKind: "group", MessageID: "msg-1", Text: "first payload",
	}
	if g.isDuplicate(first) {
		t.Fatal("first WeCom group delivery was duplicate")
	}
	retry := first
	retry.Text = "payload normalized differently on retry"
	if !g.isDuplicate(retry) {
		t.Fatal("repeated WeCom group message ID was not duplicate")
	}

	distinct := first
	distinct.MessageID = "msg-2"
	distinct.Text = first.Text
	if g.isDuplicate(distinct) {
		t.Fatal("distinct WeCom group message ID was collapsed by text")
	}
}
