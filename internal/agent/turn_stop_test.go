package agent

import (
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// No explicit maxToolIterations means no fixed 20-round cut-off: the
// turn runs until the model finishes, a failure guard trips, or the
// backstop ceiling is reached.
func TestToolIterationLimit(t *testing.T) {
	if got := (&Agent{}).toolIterationLimit(); got != hardToolIterationCeiling {
		t.Fatalf("unset limit = %d, want ceiling %d", got, hardToolIterationCeiling)
	}
	if got := (&Agent{maxToolIterations: 5}).toolIterationLimit(); got != 5 {
		t.Fatalf("explicit limit = %d, want 5", got)
	}
}

// After the loop warning the detector restarts, so the turn is only cut
// off if the model repeats the identical call three more times.
func TestLoopDetectorReset(t *testing.T) {
	var d toolLoopDetector
	tc := provider.ToolCall{}
	tc.Function.Name = "exec"
	tc.Function.Arguments = `{"cmd":"tail out.log"}`
	for i := 0; i < 2; i++ {
		if d.Observe(tc, "running") {
			t.Fatalf("detected too early at call %d", i+1)
		}
	}
	if !d.Observe(tc, "running") {
		t.Fatal("third identical call should be detected")
	}
	d.Reset()
	if d.Observe(tc, "running") || d.Observe(tc, "running") {
		t.Fatal("detector should count from zero after Reset")
	}
	if !d.Observe(tc, "running") {
		t.Fatal("third identical call after Reset should be detected again")
	}
}

// A loop stop must not be reported to the user as a spent budget.
func TestTurnStopReportsReason(t *testing.T) {
	loop := turnStop{reason: stopReasonLoop, limit: 200}
	if m := loop.metadata(); m["iterationStopReason"] != stopReasonLoop || m["iterationCapReached"] != true {
		t.Fatalf("loop metadata = %v", m)
	}
	if n := loop.notice("feishu"); strings.Contains(n, "tool-call limit") || n == "" {
		t.Fatalf("loop notice = %q", n)
	}
	if loop.notice("web") != "" {
		t.Fatal("web renders the badge; no in-band notice")
	}
	if strings.Contains(loop.nudge().Content, "tool-call iterations") {
		t.Fatal("loop nudge must not claim the budget ran out")
	}
	capStop := turnStop{reason: stopReasonCap, limit: 20}
	if n := capStop.notice("feishu"); !strings.Contains(n, "20 tool-call limit") {
		t.Fatalf("cap notice = %q", n)
	}
}
