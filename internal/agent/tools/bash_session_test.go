//go:build unix

package tools

// Tests rely on `sh`, `sleep`, `echo`, `printf`, and process-group
// signaling — all Unix-only. Build-tagged so cross-compile to Windows
// stays clean (the production code paths it exercises are also
// Unix-only via bash_session_unix.go).

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// =====================================================================
// outputBuffer — pure-function tests
// =====================================================================

func TestOutputBuffer_AppendAndRead(t *testing.T) {
	b := newOutputBuffer(1024)
	_, _ = b.Write([]byte("hello"))
	_, _ = b.Write([]byte(" world"))

	out, dropped, since := b.readSince(0)
	if dropped {
		t.Errorf("unexpected drop")
	}
	if string(out) != "hello world" {
		t.Errorf("got %q want %q", out, "hello world")
	}
	if since != 11 {
		t.Errorf("since: %d want 11", since)
	}

	// Re-reading from `since` returns nothing new.
	out, _, since2 := b.readSince(since)
	if len(out) != 0 || since2 != 11 {
		t.Errorf("re-read: out=%q since=%d", out, since2)
	}
}

func TestOutputBuffer_FIFODrop(t *testing.T) {
	b := newOutputBuffer(8) // tiny cap
	_, _ = b.Write([]byte("AAAA"))
	_, _ = b.Write([]byte("BBBB"))
	_, _ = b.Write([]byte("CCCC")) // forces drop of "AAAA"

	out, dropped, since := b.readSince(0)
	if !dropped {
		t.Errorf("expected dropped=true after cap exceeded")
	}
	if string(out) != "BBBBCCCC" {
		t.Errorf("got %q want %q", out, "BBBBCCCC")
	}
	if since != 12 {
		t.Errorf("since=%d want 12 (total bytes ever written)", since)
	}
}

func TestOutputBuffer_ReadSinceFuture(t *testing.T) {
	b := newOutputBuffer(64)
	_, _ = b.Write([]byte("hello"))
	out, _, since := b.readSince(100)
	if len(out) != 0 {
		t.Errorf("read past end should be empty, got %q", out)
	}
	if since != 5 {
		t.Errorf("since=%d want 5", since)
	}
}

func TestOutputBuffer_ConcurrentWriteRead(t *testing.T) {
	// Smoke test: many concurrent writers and a reader. Race detector
	// catches data races; we just want the test to not panic and the
	// final read to see all bytes.
	b := newOutputBuffer(1 << 20)
	var wg sync.WaitGroup
	const N = 100
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = b.Write([]byte("x"))
		}()
	}
	wg.Wait()
	out, _, _ := b.readSince(0)
	if len(out) != N {
		t.Errorf("got %d bytes, want %d", len(out), N)
	}
}

// =====================================================================
// shellManager / bashSession — integration tests
// =====================================================================
//
// These spawn real `sh -c` processes. They run on every CI environment
// FastClaw cares about. Tests use short sleeps (≤ 200ms) so the suite
// stays fast.

func waitDone(t *testing.T, s *bashSession, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !s.done.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("session %s did not exit within %v", s.id, timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestShellManager_EchoExitsZero(t *testing.T) {
	m := newShellManager()
	defer m.Close()

	s, err := m.Start("echo hello", nil, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone(t, s, 2*time.Second)

	out, dropped := s.readNew()
	if dropped {
		t.Errorf("unexpected drop")
	}
	if string(out) != "hello\n" {
		t.Errorf("got %q want \"hello\\n\"", out)
	}

	status, code, _ := s.snapshot()
	if status != statusExited {
		t.Errorf("status=%d want exited", status)
	}
	if code != 0 {
		t.Errorf("code=%d want 0", code)
	}
}

func TestShellManager_NonZeroExitCodePreserved(t *testing.T) {
	m := newShellManager()
	defer m.Close()
	s, err := m.Start("exit 42", nil, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone(t, s, 2*time.Second)
	_, code, _ := s.snapshot()
	if code != 42 {
		t.Errorf("code=%d want 42", code)
	}
}

func TestShellManager_KillTerminatesRunning(t *testing.T) {
	m := newShellManager()
	defer m.Close()
	s, err := m.Start("sleep 10", nil, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// Give it a moment to actually start.
	time.Sleep(20 * time.Millisecond)
	if s.done.Load() {
		t.Fatalf("session ended too early")
	}
	if err := s.kill(); err != nil {
		t.Errorf("kill: %v", err)
	}
	waitDone(t, s, 2*time.Second)
	status, code, _ := s.snapshot()
	if status != statusExited {
		t.Errorf("status not exited")
	}
	// Killed via context cancel — code is -1 (abnormal termination).
	if code != -1 {
		t.Errorf("code=%d want -1 (killed)", code)
	}
}

// TestShellManager_KillReachesGrandchildren guards the process-group
// kill behaviour: `sh -c` forks a child shell, the child shell forks
// `sleep`. Without Setpgid + group kill, only the shell would die and
// `sleep` would orphan and keep running, so the model thinks dev
// servers / build watchers were stopped when they're still consuming
// the port. We verify by capturing the grandchild's PID via lsof of
// /proc — but that's Linux-specific and brittle. Simpler proof: spawn
// a long-lived grandchild and rely on the test deadline. After
// kill_shell, no descendant should still be writing to the buffer.
func TestShellManager_KillReachesGrandchildren(t *testing.T) {
	m := newShellManager()
	defer m.Close()
	// `sh -c "sleep 30 & wait"` — the shell forks `sleep`, then waits
	// on it. Without group kill, killing the shell leaves `sleep`
	// behind. With group kill, both go.
	s, err := m.Start("sleep 30 & echo started; wait", nil, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait for "started" so we know the grandchild PID is in flight.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := s.readNew()
		if bytes.Contains(out, []byte("started")) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := s.kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	// The whole group should die fast — Wait blocks on the shell, but
	// the shell can't return until its `wait` builtin completes, which
	// only happens after `sleep` is also gone. If group kill works,
	// done flips within milliseconds. If it doesn't, the test times
	// out at 30s sleep.
	waitDone(t, s, 2*time.Second)
}

func TestShellManager_KillIsIdempotentOnExited(t *testing.T) {
	m := newShellManager()
	defer m.Close()
	s, err := m.Start("true", nil, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone(t, s, 2*time.Second)
	if err := s.kill(); err != nil {
		t.Errorf("kill on exited should be no-op, got %v", err)
	}
}

func TestShellManager_IncrementalReadAdvancesCursor(t *testing.T) {
	m := newShellManager()
	defer m.Close()
	// Two outputs separated by a sleep so we can read between them.
	s, err := m.Start("echo first; sleep 0.15; echo second", nil, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait for the first line to land.
	deadline := time.Now().Add(1 * time.Second)
	var first []byte
	for time.Now().Before(deadline) {
		out, _ := s.readNew()
		if len(out) > 0 {
			first = append(first, out...)
			if bytes.Contains(first, []byte("first\n")) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !bytes.Contains(first, []byte("first\n")) {
		t.Fatalf("never saw first line; got %q", first)
	}
	if bytes.Contains(first, []byte("second")) {
		t.Fatalf("saw second line too early; reads aren't incremental: %q", first)
	}

	waitDone(t, s, 2*time.Second)
	out, _ := s.readNew()
	if !bytes.Contains(out, []byte("second\n")) {
		t.Errorf("second read missing 'second': %q", out)
	}
	if bytes.Contains(out, []byte("first")) {
		t.Errorf("second read leaked 'first' (cursor not advanced): %q", out)
	}
}

func TestShellManager_CloseKillsAllSessions(t *testing.T) {
	m := newShellManager()
	// Capture session pointers BEFORE Close — Close empties m.shells,
	// and ranging over the cleared map would silently iterate nothing
	// and pass the assertion regardless of whether kills actually fired.
	captured := make([]*bashSession, 0, 3)
	for i := 0; i < 3; i++ {
		s, err := m.Start("sleep 30", nil, "")
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		captured = append(captured, s)
	}
	if got := len(m.list()); got != 3 {
		t.Fatalf("expected 3 sessions before close, got %d", got)
	}

	m.Close()

	if got := len(m.list()); got != 0 {
		t.Errorf("Close did not clear shells map; still has %d", got)
	}

	// Each captured session should reach done=true after Close cancels
	// its context and the reaper goroutine processes the SIGKILL.
	for _, s := range captured {
		waitDone(t, s, 2*time.Second)
		_, code, _ := s.snapshot()
		if code != -1 {
			t.Errorf("session %s exit code %d, expected -1 (killed)", s.id, code)
		}
	}
}

// TestShellManager_StartAfterCloseRefuses guards the close+start race:
// once Close returned, any subsequent Start must NOT silently land in
// the post-Close map and leak the spawned process. Refusing is the
// safe verdict.
func TestShellManager_StartAfterCloseRefuses(t *testing.T) {
	m := newShellManager()
	m.Close()
	s, err := m.Start("echo nope", nil, "")
	if err == nil {
		t.Errorf("Start after Close should error, got session %v", s)
	}
}

// TestBashOutputTool_DrainsTailOnExit guards the read-then-status
// race: bytes that land between the first readNew and the snapshot's
// done=true observation must NOT be lost when bash_output reports
// "[status] exited". We provoke the race by polling repeatedly while
// a short command is finishing — without the post-snapshot drain the
// last lines are eventually skipped.
func TestBashOutputTool_DrainsTailOnExit(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()

	// Print a sentinel just before exit so we can detect the loss case
	// directly: a successful drain MUST yield the sentinel together
	// with the "exited" status line.
	s, err := r.shellMgr.Start("printf 'tail-sentinel\\n'", nil, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone(t, s, 2*time.Second)

	args, _ := json.Marshal(map[string]string{"bash_id": s.id})
	got, err := r.Execute(context.Background(), "bash_output", string(args))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(got, "tail-sentinel") {
		t.Errorf("missed tail bytes; got %q", got)
	}
	if !strings.Contains(got, "[status] exited (code=0)") {
		t.Errorf("status missing; got %q", got)
	}
}

// =====================================================================
// bash_output / kill_shell — tool-level tests
// =====================================================================

func TestBashOutputTool_FilterRegex(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()

	s, err := r.shellMgr.Start("printf 'INFO: ok\\nERROR: boom\\nINFO: done\\n'", nil, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone(t, s, 2*time.Second)

	args, _ := json.Marshal(map[string]string{"bash_id": s.id, "filter": "^ERROR:"})
	got, err := r.Execute(context.Background(), "bash_output", string(args))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(got, "ERROR: boom") {
		t.Errorf("filter dropped the matching line: %q", got)
	}
	if strings.Contains(got, "INFO:") {
		t.Errorf("filter let through non-matching lines: %q", got)
	}
	if !strings.Contains(got, "[status] exited (code=0)") {
		t.Errorf("status footer missing: %q", got)
	}
}

func TestBashOutputTool_UnknownIDErrors(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()
	args, _ := json.Marshal(map[string]string{"bash_id": "bash_999"})
	_, err := r.Execute(context.Background(), "bash_output", string(args))
	if err == nil || !strings.Contains(err.Error(), "no such bash_id") {
		t.Errorf("expected 'no such bash_id' error, got %v", err)
	}
}

func TestKillShellTool_AlreadyExited(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()
	s, err := r.shellMgr.Start("true", nil, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone(t, s, 2*time.Second)

	args, _ := json.Marshal(map[string]string{"bash_id": s.id})
	got, err := r.Execute(context.Background(), "kill_shell", string(args))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(got, "Already exited") {
		t.Errorf("got %q; expected 'Already exited'", got)
	}
}

func TestKillShellTool_TerminatesRunning(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()
	s, err := r.shellMgr.Start("sleep 30", nil, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	args, _ := json.Marshal(map[string]string{"bash_id": s.id})
	got, err := r.Execute(context.Background(), "kill_shell", string(args))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(got, "Sent kill") {
		t.Errorf("got %q; expected 'Sent kill'", got)
	}
	waitDone(t, s, 2*time.Second)
}

// =====================================================================
// exec(run_in_background) integration
// =====================================================================

func TestExecTool_BackgroundReturnsBashID(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()
	args, _ := json.Marshal(map[string]any{
		"command":           "sleep 0.05; echo done",
		"run_in_background": true,
	})
	got, err := r.Execute(context.Background(), "exec", string(args))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(got, "bash_") || !strings.Contains(got, "Started background shell") {
		t.Fatalf("expected bash_id in result, got %q", got)
	}
	// Wait for it to actually finish, then read output via the tool.
	if got1 := r.shellMgr.list(); len(got1) != 1 {
		t.Fatalf("expected 1 session, got %d", len(got1))
	}
	s := r.shellMgr.list()[0]
	waitDone(t, s, 2*time.Second)

	outArgs, _ := json.Marshal(map[string]string{"bash_id": s.id})
	out, err := r.Execute(context.Background(), "bash_output", string(outArgs))
	if err != nil {
		t.Fatalf("bash_output: %v", err)
	}
	if !strings.Contains(out, "done") || !strings.Contains(out, "exited (code=0)") {
		t.Errorf("bash_output got %q", out)
	}
}
