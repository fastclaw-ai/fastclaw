package agent

import (
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/channels"
)

func TestRenderChannelHintsForMultiBubbleReplies(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		enabled bool
		want    bool
	}{
		{name: "enabled on web", channel: "web", enabled: true, want: true},
		{name: "enabled on IM", channel: "telegram", enabled: true, want: true},
		{name: "disabled on web", channel: "web", enabled: false, want: false},
		{name: "not injected into API responses", channel: "api", enabled: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hint := renderChannelHints(bus.InboundMessage{Channel: tt.channel}, tt.enabled)
			if (hint != "") != tt.want {
				t.Fatalf("renderChannelHints() = %q, want non-empty=%v", hint, tt.want)
			}
			if !tt.want {
				return
			}
			if !strings.Contains(hint, channels.SplitMessageMarker) {
				t.Fatalf("hint does not advertise split marker %q: %q", channels.SplitMessageMarker, hint)
			}
			if !strings.Contains(hint, "default to 2–4") || !strings.Contains(hint, "1–2 short sentences") {
				t.Fatalf("hint does not require short multi-message replies: %q", hint)
			}
		})
	}
}
