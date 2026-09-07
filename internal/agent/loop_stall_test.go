package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// Tests for the P0.3 / P1 additions from fastclaw-timeout-error-root-
// cause-analysis.md: buildFallbackReply (context-aware fallback instead
// of a static string) and maybeInjectSoftDeadlineWarning (soft-deadline
// heartbeat). Both are pure functions taking plain arguments, so they're
// unit-tested directly without needing a full Agent/Provider harness.

const staleGenericFallback = "Sorry, I encountered an error processing your request."

func TestBuildFallbackReply_TimeoutWithKnownFailingTool(t *testing.T) {
	got := buildFallbackReply(context.DeadlineExceeded, nil, "browser-use --doctor", "FAIL chrome running / FAIL daemon alive")
	if got == staleGenericFallback {
		t.Fatalf("expected a context-aware message, got the stale generic string")
	}
	if !strings.Contains(got, "browser-use --doctor") {
		t.Errorf("expected the failing tool name in the reply, got %q", got)
	}
	if !strings.Contains(got, "FAIL chrome running") {
		t.Errorf("expected the tool's failure summary in the reply, got %q", got)
	}
}

func TestBuildFallbackReply_CanceledWithKnownFailingTool(t *testing.T) {
	// context.Canceled must be treated the same as DeadlineExceeded —
	// both are "timed out" from buildFallbackReply's perspective.
	got := buildFallbackReply(context.Canceled, nil, "exec", "exit status 127: not found")
	if got == staleGenericFallback {
		t.Fatalf("expected a context-aware message, got the stale generic string")
	}
	if !strings.Contains(got, "exec") {
		t.Errorf("expected the failing tool name in the reply, got %q", got)
	}
}

func TestBuildFallbackReply_TimeoutWithoutKnownTool(t *testing.T) {
	// Timed out before any tool failure was recorded (e.g. the very
	// first LLM call in the turn hung) — no tool name to reference, but
	// still must not be the stale generic string.
	got := buildFallbackReply(context.DeadlineExceeded, nil, "", "")
	if got == staleGenericFallback {
		t.Fatalf("expected a distinct timeout message, got the stale generic string")
	}
	if !strings.Contains(strings.ToLower(got), "time") {
		t.Errorf("expected the reply to mention running out of time, got %q", got)
	}
}

func TestBuildFallbackReply_NonTimeoutErrorIncludesErrorText(t *testing.T) {
	// A non-context error (e.g. a transient 5xx that exhausted retries)
	// should surface the actual error, not a content-free static string.
	err := errors.New("API error 402: Insufficient Balance")
	got := buildFallbackReply(err, nil, "", "")
	if got == staleGenericFallback {
		t.Fatalf("expected the underlying error text, got the stale generic string")
	}
	if !strings.Contains(got, "Insufficient Balance") {
		t.Errorf("expected the underlying error text in the reply, got %q", got)
	}
}

func TestBuildFallbackReply_PreservesReplyPartsWhenPresent(t *testing.T) {
	// HandleMessage accumulates replyParts across iterations; when a
	// later iteration times out, any earlier assistant text already
	// produced this turn should still reach the user instead of being
	// discarded.
	parts := []string{"Here's what I found so far: X and Y."}
	got := buildFallbackReply(context.DeadlineExceeded, parts, "web_fetch", "connection refused")
	if !strings.Contains(got, "Here's what I found so far: X and Y.") {
		t.Errorf("expected prior reply parts to be preserved, got %q", got)
	}
}

func TestBuildFallbackReply_NilReplyPartsIsSafe(t *testing.T) {
	// HandleMessageStream doesn't accumulate replyParts at all and
	// passes nil — must not panic and must not print anything for the
	// (absent) prior content.
	got := buildFallbackReply(context.DeadlineExceeded, nil, "exec", "boom")
	if strings.Contains(got, "<nil>") {
		t.Errorf("nil replyParts leaked into output: %q", got)
	}
}

func TestMaybeInjectSoftDeadlineWarning_NoDeadlineNeverFires(t *testing.T) {
	ctx := context.Background()
	turnStart := time.Now()
	msgs := []provider.Message{{Role: "system", Content: "seed"}}

	for i := 0; i < 3; i++ {
		var fired bool
		msgs, fired = maybeInjectSoftDeadlineWarning(ctx, turnStart, msgs, false)
		if fired {
			t.Fatalf("iteration %d: expected fired=false with no ctx deadline, got true", i)
		}
		if len(msgs) != 1 {
			t.Fatalf("iteration %d: messages should be unchanged with no deadline, got len=%d", i, len(msgs))
		}
	}
}

func TestMaybeInjectSoftDeadlineWarning_PlentyOfTimeRemainingDoesNotFire(t *testing.T) {
	// total budget ~= 10s (turnStart ~= now), so remaining/total ~= 100%,
	// well above the 20% softDeadlineFraction threshold.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	turnStart := time.Now()
	msgs := []provider.Message{{Role: "system", Content: "seed"}}

	got, fired := maybeInjectSoftDeadlineWarning(ctx, turnStart, msgs, false)
	if fired {
		t.Fatalf("expected fired=false with ~100%% of budget remaining, got true")
	}
	if len(got) != 1 {
		t.Fatalf("messages should be unchanged, got len=%d", len(got))
	}
}

func TestMaybeInjectSoftDeadlineWarning_LowRemainingFiresOnce(t *testing.T) {
	// Simulate a turn that started 9s ago with 1s left on a ctx deadline
	// set now: total = 10s, remaining = 1s, remaining/total = 10% < 20%.
	turnStart := time.Now().Add(-9 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	msgs := []provider.Message{{Role: "system", Content: "seed"}}

	got, fired := maybeInjectSoftDeadlineWarning(ctx, turnStart, msgs, false)
	if !fired {
		t.Fatalf("expected fired=true with only 10%% of budget remaining, got false")
	}
	if len(got) != 2 {
		t.Fatalf("expected exactly one system message injected, got len=%d", len(got))
	}
	injected := got[len(got)-1]
	if injected.Role != "system" {
		t.Errorf("injected message role = %q, want %q", injected.Role, "system")
	}
	if !strings.Contains(strings.ToLower(injected.Content), "time budget") {
		t.Errorf("injected message should mention the time budget, got %q", injected.Content)
	}

	// Idempotency: a second call in the same turn, passing back
	// fired=true from the first call, must not inject a second nudge —
	// this is what prevents the warning from being repeated on every
	// remaining iteration.
	got2, fired2 := maybeInjectSoftDeadlineWarning(ctx, turnStart, got, fired)
	if !fired2 {
		t.Fatalf("expected fired=true to persist on the second call")
	}
	if len(got2) != len(got) {
		t.Fatalf("second call should not inject another message: got len=%d, want %d", len(got2), len(got))
	}
}

// TestUpdateSameToolFailStreak covers the P0.1 counting/reset rules
// directly. This closes the coverage gap noted when P0.1 first shipped
// inline in HandleMessage/HandleMessageStream — a full integration test
// through HandleMessage would need a fake Provider + fully-populated
// Agent (session manager, memory, ctxBuilder, engine, ...); extracting
// the pure decision function makes it directly testable instead.
func TestUpdateSameToolFailStreak(t *testing.T) {
	t.Run("consecutive failures of the same tool increment the streak", func(t *testing.T) {
		// Reproduces the actual 2026-07-06 incident sequence: two
		// differently-worded "browser-use --doctor" calls, both
		// failing. Arguments differ (one has an export PATH prefix)
		// but the tool name is the same both times — this is exactly
		// the pattern the old byte-identical-args loop detector missed.
		s := sameToolFailStreakState{}
		s = updateSameToolFailStreak(s, "exec", true, "exit status 127: browser-use not found")
		if s.streak != 1 || s.lastFailedTool != "exec" {
			t.Fatalf("after 1st failure: got streak=%d tool=%q, want streak=1 tool=%q", s.streak, s.lastFailedTool, "exec")
		}
		s = updateSameToolFailStreak(s, "exec", true, "FAIL chrome running / FAIL daemon alive")
		if s.streak != 2 {
			t.Fatalf("after 2nd failure of the same tool (different args): got streak=%d, want 2", s.streak)
		}
		if s.lastFailureText != "FAIL chrome running / FAIL daemon alive" {
			t.Errorf("lastFailureText should track the most recent failure, got %q", s.lastFailureText)
		}
		if s.streak < sameToolFailStreakLimit {
			t.Errorf("streak=%d should already meet sameToolFailStreakLimit=%d after 2 failures — this is the case that should have converged before the 300s wall in the real incident", s.streak, sameToolFailStreakLimit)
		}
	})

	t.Run("a different tool failing resets the streak to the new tool", func(t *testing.T) {
		s := sameToolFailStreakState{streak: 2, lastFailedTool: "exec"}
		s = updateSameToolFailStreak(s, "web_fetch", true, "connection refused")
		if s.streak != 1 || s.lastFailedTool != "web_fetch" {
			t.Fatalf("got streak=%d tool=%q, want streak=1 tool=%q", s.streak, s.lastFailedTool, "web_fetch")
		}
	})

	t.Run("the tracked tool succeeding clears the streak", func(t *testing.T) {
		s := sameToolFailStreakState{streak: 2, lastFailedTool: "exec", lastFailureText: "boom"}
		s = updateSameToolFailStreak(s, "exec", false, "")
		if s.streak != 0 || s.lastFailedTool != "" {
			t.Fatalf("got streak=%d tool=%q, want streak=0 tool=\"\"", s.streak, s.lastFailedTool)
		}
	})

	t.Run("an unrelated tool succeeding does not disturb an active streak", func(t *testing.T) {
		s := sameToolFailStreakState{streak: 2, lastFailedTool: "exec", lastFailureText: "boom"}
		s = updateSameToolFailStreak(s, "read_file", false, "")
		if s.streak != 2 || s.lastFailedTool != "exec" {
			t.Fatalf("unrelated success should not affect the streak: got streak=%d tool=%q, want streak=2 tool=%q", s.streak, s.lastFailedTool, "exec")
		}
	})

	t.Run("alternating success/failure of the same tool never accumulates a streak", func(t *testing.T) {
		s := sameToolFailStreakState{}
		seq := []bool{true, false, true, false, true}
		for _, failed := range seq {
			s = updateSameToolFailStreak(s, "flaky_tool", failed, "err")
		}
		// Last op in seq is failed=true, so streak should be exactly 1
		// (reset then incremented once), not accumulated across the
		// whole alternating history.
		if s.streak != 1 {
			t.Fatalf("alternating success/failure should not accumulate: got streak=%d, want 1", s.streak)
		}
	})
}

// The tool-call budget is the limit that actually fires in practice, yet
// it warned nobody until now — only the wall-clock budget did. A turn
// that plans five steps and gets cut off after four ends by reporting
// the fifth as done, because the model never learned it was running out.
func TestMaybeInjectIterationBudgetWarning_FiresOnceNearTheCap(t *testing.T) {
	ctx := context.Background()
	base := []provider.Message{{Role: "user", Content: "go"}}

	// 13 of 20 used: 7 left, above the 30% threshold — stay quiet.
	got, fired := maybeInjectIterationBudgetWarning(ctx, 13, 20, base, false)
	if fired || len(got) != len(base) {
		t.Fatalf("must not fire while budget is comfortable (fired=%v, msgs=%d)", fired, len(got))
	}

	// 14 of 20: 6 left, exactly at 30% — warn.
	got, fired = maybeInjectIterationBudgetWarning(ctx, 14, 20, base, false)
	if !fired {
		t.Fatal("expected the warning to fire with 30% of the budget left")
	}
	if len(got) != len(base)+1 {
		t.Fatalf("expected one appended system message, got %d", len(got))
	}
	warn := got[len(got)-1]
	if warn.Role != "system" {
		t.Errorf("warning should be a system message, got role %q", warn.Role)
	}
	for _, want := range []string{"Tool budget warning", "verification"} {
		if !strings.Contains(warn.Content, want) {
			t.Errorf("warning missing %q: %s", want, warn.Content)
		}
	}

	// Already fired — never append twice in one turn.
	got2, fired2 := maybeInjectIterationBudgetWarning(ctx, 18, 20, got, true)
	if !fired2 || len(got2) != len(got) {
		t.Errorf("warning must fire at most once per turn")
	}
}

// A budget already spent has nothing to warn about — the cap nudge takes
// over at that point.
func TestMaybeInjectIterationBudgetWarning_NoBudgetNoFire(t *testing.T) {
	ctx := context.Background()
	base := []provider.Message{{Role: "user", Content: "go"}}
	if _, fired := maybeInjectIterationBudgetWarning(ctx, 20, 20, base, false); fired {
		t.Error("must not fire when the budget is already exhausted")
	}
	if _, fired := maybeInjectIterationBudgetWarning(ctx, 0, 0, base, false); fired {
		t.Error("must not fire when no cap is configured")
	}
}

// The cap-reached banner is rendered by exactly one consumer — the web
// chat UI. On every other channel the metadata is dropped, so without an
// in-band notice a guillotined turn arrives looking like a finished,
// confident answer.
func TestIterationCapNoticeReachesNonWebChannels(t *testing.T) {
	if notice := iterationCapNotice("web", 20); notice != "" {
		t.Errorf("web renders its own badge; text notice would duplicate it: %q", notice)
	}
	for _, ch := range []string{"wechat", "discord", "feishu", "line", "api", ""} {
		notice := iterationCapNotice(ch, 20)
		if notice == "" {
			t.Errorf("channel %q silently drops the cap metadata and needs an in-band notice", ch)
			continue
		}
		if !strings.Contains(notice, "20") {
			t.Errorf("notice for %q should name the limit: %q", ch, notice)
		}
		if !strings.Contains(notice, "not necessarily verified") {
			t.Errorf("notice for %q should undercut completion claims: %q", ch, notice)
		}
	}
}

// The forced-synthesis nudge used to push purely toward "deliver
// content", which is how a truncated turn produced a confident
// completion report with a tick next to a step that never ran.
func TestCapReachedNudgeDemandsHonestyAboutTruncation(t *testing.T) {
	msg := capReachedNudge(20)
	if msg.Role != "system" {
		t.Fatalf("nudge role = %q", msg.Role)
	}
	for _, want := range []string{
		"ran out of tool budget",
		"did not run",
		"unless a tool result",
		"configured but not verified",
	} {
		if !strings.Contains(msg.Content, want) {
			t.Errorf("nudge should require honesty about %q:\n%s", want, msg.Content)
		}
	}
}

// The checklist is rendered beside the answer, so the two must not
// contradict each other. A turn that firefights a mid-turn failure and
// never returns to todo.md ends with a reply saying "all done" next to a
// panel reading 1/5, and nothing tells the user which is true.
func TestUncheckedTodoItemsParsesTheUIConvention(t *testing.T) {
	body := `- [x] 1. 创建新 agent (anthropic-art)
- [ ] 2. 为新 agent 配置模型
Some prose that is not a checkbox.
  - [ ] 3. 安装 skill
- [X] 4. 写入 IDENTITY.md
- [ ]
`
	got := uncheckedTodoItems(body)
	if len(got) != 2 {
		t.Fatalf("expected 2 unchecked items, got %d: %v", len(got), got)
	}
	if got[0] != "2. 为新 agent 配置模型" || got[1] != "3. 安装 skill" {
		t.Errorf("unexpected items: %v", got)
	}
	// Uppercase [X] counts as done, matching the panel's parser.
	for _, item := range got {
		if strings.Contains(item, "IDENTITY") {
			t.Errorf("[X] should count as completed: %v", got)
		}
	}
	if items := uncheckedTodoItems("- [x] all done\n"); len(items) != 0 {
		t.Errorf("a fully checked list has nothing pending, got %v", items)
	}
	if items := uncheckedTodoItems("no checkboxes here"); len(items) != 0 {
		t.Errorf("prose is not a checklist, got %v", items)
	}
}

func TestTodoReconcileNudgeAllowsHonestIncompleteness(t *testing.T) {
	msg := todoReconcileNudge([]string{"2. configure model", "5. verify"})
	if msg.Role != "system" {
		t.Fatalf("nudge role = %q", msg.Role)
	}
	if !strings.Contains(msg.Content, "2. configure model") || !strings.Contains(msg.Content, "5. verify") {
		t.Errorf("nudge should name the pending items: %s", msg.Content)
	}
	// Leaving an item unchecked is legitimate when the work didn't
	// happen — what's forbidden is the mismatch, not the incompleteness.
	if !strings.Contains(msg.Content, "which steps did not get done") {
		t.Errorf("nudge must accept an honest 'not done' resolution: %s", msg.Content)
	}
	if !strings.Contains(msg.Content, "Do not claim the task is complete") {
		t.Errorf("nudge must forbid the contradiction: %s", msg.Content)
	}
}

func TestTodoInstructionsRequireImmediateIncrementalUpdates(t *testing.T) {
	for _, want := range []string{
		"immediately before starting the next",
		"do not batch several completed steps",
		"never use apply_patch for todo.md",
	} {
		if !strings.Contains(taskDelegationContent, want) {
			t.Errorf("todo instructions missing %q", want)
		}
	}
}
