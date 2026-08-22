package bus

import (
	"encoding/json"
	"testing"
)

func TestOutboundMessageStreamFieldsRoundTrip(t *testing.T) {
	want := OutboundMessage{
		Channel: "wecom", AccountID: "bot-1", ChatID: "group-1",
		ReplyToMsgID: "msg-1", Text: "partial",
		StreamID: "stream-1", StreamState: StreamUpdate,
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got OutboundMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.StreamID != want.StreamID || got.StreamState != StreamUpdate {
		t.Fatalf("round trip = %#v", got)
	}
}

// TestSourceUserIsEmpty pins down the backwards-compat contract: every
// pre-existing producer (IM channels, web chat, webhook, OpenAI-compat
// API, plugins) leaves Source unset. If SourceUser ever stops being "",
// every one of those call sites silently becomes a non-user message
// and goal continuations / other Source-gated behavior break in
// surprising ways.
func TestSourceUserIsEmpty(t *testing.T) {
	if SourceUser != "" {
		t.Fatalf(`SourceUser must be "" for zero-value compatibility, got %q`, SourceUser)
	}
	var m InboundMessage
	if m.Source != SourceUser {
		t.Fatalf("zero-value InboundMessage.Source should equal SourceUser, got %q", m.Source)
	}
}

// TestSourceConstantsDistinct catches the typo class of bug where two
// constants collapse to the same string and a non-user message silently
// gets classified as user (or vice versa).
func TestSourceConstantsDistinct(t *testing.T) {
	all := map[string]string{
		"SourceUser":        SourceUser,
		"SourceCron":        SourceCron,
		"SourceHeartbeat":   SourceHeartbeat,
		"SourceSubAgent":    SourceSubAgent,
		"SourceGoalContext": SourceGoalContext,
	}
	seen := make(map[string]string, len(all))
	for name, val := range all {
		if prev, ok := seen[val]; ok {
			t.Errorf("%s and %s both equal %q — Source constants must be distinct", prev, name, val)
		}
		seen[val] = name
	}
}
