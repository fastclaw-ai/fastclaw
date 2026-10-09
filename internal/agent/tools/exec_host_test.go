//go:build unix

package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRunHostCommand_TimeoutKillsGrandchildren is the regression guard
// for the 18-minute hang: `sh -c "<long thing> | head"` whose deadline
// fires while a grandchild still holds the output pipe.
//
// Before the process-group fix this returned only when `sleep` exited on
// its own (i.e. after the full 30s), not when the 1s deadline fired —
// exactly the shape of `find / -name x | head -10` outliving its 120s
// timeout by 18 minutes. The assertion that matters is the WALL CLOCK:
// the call must come back on the deadline, not on the child's schedule.
func TestRunHostCommand_TimeoutKillsGrandchildren(t *testing.T) {
	start := time.Now()
	out, err := runHostCommand(
		mustDeadline(t, 1*time.Second),
		// `sh` forks a subshell that outlives a naive kill of `sh` and
		// keeps the CombinedOutput pipe open for 30s.
		"(sleep 30; echo late) | cat",
		"",
		buildSubprocessEnv(nil),
		1*time.Second,
	)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected a timeout error, got nil (output %q)", out)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("runHostCommand blocked %s past its 1s deadline — the process "+
			"group kill is not reaching the grandchild holding the pipe", elapsed)
	}
	if strings.Contains(out, "late") {
		t.Errorf("grandchild survived the deadline and still wrote output: %q", out)
	}
}

// TestRunHostCommand_TimeoutErrorIsActionable pins the message the model
// actually reads. A bare `signal: killed` reads like a crash and sends
// the model debugging its command instead of narrowing it.
func TestRunHostCommand_TimeoutErrorIsActionable(t *testing.T) {
	_, err := runHostCommand(mustDeadline(t, 500*time.Millisecond), "sleep 30", "", buildSubprocessEnv(nil), 500*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"timed out after", "500ms", "timeout"} {
		if !strings.Contains(msg, want) {
			t.Errorf("timeout error missing %q; got: %s", want, msg)
		}
	}
	if strings.Contains(msg, "signal: killed") {
		t.Errorf("raw wait error leaked into the model-facing message: %s", msg)
	}
}

// TestRunHostCommand_SuccessAndFailurePassThrough keeps the happy path
// and ordinary non-zero exits behaving exactly as before the rewrite.
func TestRunHostCommand_SuccessAndFailurePassThrough(t *testing.T) {
	out, err := runHostCommand(mustDeadline(t, 10*time.Second), "echo hello", "", buildSubprocessEnv(nil), 10*time.Second)
	if err != nil {
		t.Fatalf("echo failed: %v (%q)", err, out)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("stdout not captured: %q", out)
	}

	out, err = runHostCommand(mustDeadline(t, 10*time.Second), "echo oops >&2; exit 3", "", buildSubprocessEnv(nil), 10*time.Second)
	if err == nil {
		t.Fatal("expected a non-zero exit to surface as an error")
	}
	if !strings.Contains(out, "oops") {
		t.Errorf("stderr not captured: %q", out)
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Errorf("plain non-zero exit misreported as a timeout: %v", err)
	}
}

func mustDeadline(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

// The host shell runs in the session workspace (created on demand), so
// "./out.png" lands where write_file and the chat's file links look.
func TestRunHostCommandUsesWorkDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions", "s-1")
	out, err := runHostCommand(mustDeadline(t, 10*time.Second), "pwd -P", dir, buildSubprocessEnv(nil), 10*time.Second)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	if strings.TrimSpace(out) != want {
		t.Fatalf("pwd = %q, want %q", strings.TrimSpace(out), want)
	}
}
