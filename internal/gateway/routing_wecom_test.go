package gateway

import (
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

func TestWeComConversationIdentityTriples(t *testing.T) {
	dmA := bus.InboundMessage{Channel: "wecom", AccountID: "bot-1", ChatID: "member-a", UserID: "member-a", PeerKind: "dm"}
	dmB := bus.InboundMessage{Channel: "wecom", AccountID: "bot-1", ChatID: "member-b", UserID: "member-b", PeerKind: "dm"}
	channelA, accountA, chatA := dmA.SessionTriple()
	channelB, accountB, chatB := dmB.SessionTriple()
	if channelA != "wecom" || accountA != "bot-1" || channelB != "wecom" || accountB != "bot-1" || chatA == chatB {
		t.Fatalf("DM triples: A=(%q,%q,%q) B=(%q,%q,%q)", channelA, accountA, chatA, channelB, accountB, chatB)
	}

	groupA := bus.InboundMessage{Channel: "wecom", AccountID: "bot-1", ChatID: "group-1", UserID: "member-a", PeerKind: "group"}
	groupB := groupA
	groupB.UserID = "member-b"
	gaChannel, gaAccount, gaChat := groupA.SessionTriple()
	gbChannel, gbAccount, gbChat := groupB.SessionTriple()
	if gaChannel != gbChannel || gaAccount != gbAccount || gaChat != gbChat {
		t.Fatalf("group senders did not share a session: A=(%q,%q,%q) B=(%q,%q,%q)", gaChannel, gaAccount, gaChat, gbChannel, gbAccount, gbChat)
	}
	if groupA.UserID == groupB.UserID || groupA.SharedIdentity || groupB.SharedIdentity {
		t.Fatalf("group sender identity was lost: A=%#v B=%#v", groupA, groupB)
	}
}
