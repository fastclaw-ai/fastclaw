package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A regular chatter's exec that probes our IP never runs; the owner's does.
func TestToolAdapterRefusesGuestProbes(t *testing.T) {
	ran := false
	fn := func(ctx context.Context, args json.RawMessage) (string, error) { ran = true; return "1.2.3.4", nil }
	input := map[string]interface{}{"command": "curl -s ifconfig.me"}

	guest := &toolAdapter{name: "exec", fn: fn, guest: true}
	res, _ := guest.Call(context.Background(), input, nil)
	if ran || !res.IsError || !strings.Contains(res.Error, "private infrastructure") {
		t.Fatalf("guest probe ran=%v result=%+v", ran, res)
	}

	owner := &toolAdapter{name: "exec", fn: fn}
	if _, _ = owner.Call(context.Background(), input, nil); !ran {
		t.Fatal("owner's command was refused")
	}
}

func TestGuestToolResultView(t *testing.T) {
	if got := guestToolResultView("load_skill", "# secret skill instructions"); strings.Contains(got, "secret") {
		t.Errorf("skill instructions reached the card: %q", got)
	}
	long := strings.Repeat("字", guestToolCardMax+10)
	if got := guestToolResultView("exec", long); len([]rune(got)) > guestToolCardMax+2 {
		t.Errorf("card not shortened: %d runes", len([]rune(got)))
	}
	if got := guestToolResultView("exec", "ok"); got != "ok" {
		t.Errorf("short output changed: %q", got)
	}
}
