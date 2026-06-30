package tools

// Background-shell management — Claude-Code-style.
//
// One tool call (`exec` with run_in_background=true) launches a long-
// running command and returns immediately with a `bash_id`. The agent
// observes progress via `bash_output(bash_id)` (returns new stdout/stderr
// since the last call) and terminates with `kill_shell(bash_id)`.
//
// Scope (deliberately narrow):
//   - host-mode os/exec only; sandbox-mode background is a v2 follow-up
//   - tail-only observation; no send-keys / paste / interactive control
//     (those use cases route through tmux invoked from regular `exec`)
//   - sessions are agent-private and live until killed or Registry.Close
//
// Multi-tenant isolation: every session is stamped with the ownerKey passed
// to Start (a deterministic fold of agentID + chatterUserID + sessionKey —
// the same identity tuple workspace.Store namespaces by). Get then refuses
// cross-owner reads/writes, so a chatter who guesses another's bash_id
// (the ids are a monotonic sequence bash_1, bash_2, …) cannot read that
// session's stdout/stderr or kill its process. An empty ownerKey means
// "no tenancy boundary enforced" — used by boot/admin paths that run
// outside any chat (no chatter to isolate against) and by legacy tests.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// bufferCap caps the output retained per session. When exceeded, the
// oldest bytes are dropped FIFO. 4 MiB comfortably holds 30 minutes of
// dev-server logs while bounding total memory at 4 MiB × live sessions.
const bufferCap = 4 * 1024 * 1024

// outputBuffer is a thread-safe FIFO byte buffer with a hard cap.
// It tracks the total number of bytes ever written ("absolute offsets")
// so callers reading "since last check" can survive truncations: when
// older bytes get dropped, an existing read cursor advances to the
// current head and the caller learns some output was lost.
type outputBuffer struct {
	mu       sync.Mutex
	data     []byte
	head     int // absolute offset of data[0]; equals total bytes dropped
	total    int // absolute offset just past data[end]; equals total bytes ever written
	maxBytes int
}

func newOutputBuffer(maxBytes int) *outputBuffer {
	return &outputBuffer{maxBytes: maxBytes}
}

// Write appends p to the buffer, dropping the oldest bytes if cap is
// exceeded. Always succeeds; returns len(p), nil to satisfy io.Writer
// (so it can plug directly into exec.Cmd.Stdout / Stderr).
func (b *outputBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	b.total += len(p)
	if len(b.data) > b.maxBytes {
		drop := len(b.data) - b.maxBytes
		b.data = b.data[drop:]
		b.head += drop
	}
	return len(p), nil
}

// readSince returns content from absolute offset `since` onward. If
// `since` is below the head (older content was dropped), returns
// everything currently held with `dropped=true` so the caller can warn
// the model. Returns the new absolute offset for the caller to remember.
func (b *outputBuffer) readSince(since int) (out []byte, dropped bool, newSince int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if since < b.head {
		dropped = true
		since = b.head
	}
	// since is now ≥ b.head, so start ≥ 0 by construction. The only
	// remaining out-of-range case is "caller's cursor is ahead of what
	// we've ever produced" (since > b.total), which means there's
	// nothing new yet.
	start := since - b.head
	if start > len(b.data) {
		return nil, dropped, b.total
	}
	// Copy out so the caller can release the lock without aliasing.
	out = append([]byte(nil), b.data[start:]...)
	return out, dropped, b.total
}

// bashSession is a single backgrounded shell command and the state
// needed to observe and terminate it.
type bashSession struct {
	id        string
	owner     string // ownerKey stamped at Start; Get refuses mismatched callers
	command   string
	startedAt time.Time

	cmd    *exec.Cmd
	cancel context.CancelFunc
	out    *outputBuffer

	// readCursor is the absolute offset already returned to bash_output.
	// Guarded by readMu (separate from outputBuffer.mu so writers and
	// readers don't contend on one lock).
	readMu     sync.Mutex
	readCursor int

	// done flips to true exactly once when cmd.Wait returns. exitCode is
	// written before done; reading exitCode is safe iff done is true
	// (acquire-release via atomic.Bool).
	done     atomic.Bool
	exitCode int
	exitErr  error
}

// status reports a session's runtime state.
type bashStatus int

const (
	statusRunning bashStatus = iota
	statusExited
)

// snapshot returns a consistent view of the session's terminal state.
// status is observable while running; exitCode is meaningful only when
// status == statusExited.
func (s *bashSession) snapshot() (status bashStatus, exitCode int, exitErr error) {
	if !s.done.Load() {
		return statusRunning, 0, nil
	}
	return statusExited, s.exitCode, s.exitErr
}

// readNew pulls all output produced since the last readNew call on this
// session. dropped=true means the buffer rolled past the read cursor
// and some bytes are permanently gone.
func (s *bashSession) readNew() (out []byte, dropped bool) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	out, dropped, next := s.out.readSince(s.readCursor)
	s.readCursor = next
	return out, dropped
}

// kill signals SIGKILL via the cancel function tied to the session's
// own context. Returns nil if the session was already done. Idempotent.
func (s *bashSession) kill() error {
	if s.done.Load() {
		return nil
	}
	s.cancel()
	return nil
}

// shellManager owns every backgrounded session for a Registry. Sessions
// outlive the request context that started them — they die on kill, on
// natural exit, or on Registry.Close.
type shellManager struct {
	mu      sync.Mutex
	shells  map[string]*bashSession
	counter int
	closed  bool // Close was called; Start refuses new sessions
}

func newShellManager() *shellManager {
	return &shellManager{shells: make(map[string]*bashSession)}
}

// Start launches command via `sh -c` and returns its bash_id. The
// session lives until kill or natural exit; the caller's ctx does NOT
// propagate to the child — that ctx dies at turn end and would take
// every backgrounded process with it.
//
// ownerKey is stamped onto the session so Get can refuse cross-owner
// access (multi-tenant isolation). Pass "" only when there is no chat-
// level chatter to isolate against — boot/admin paths and tests that
// don't model tenancy. Every chat-driven exec call must pass a real key
// derived from (agentID, chatterUserID, sessionKey); see OwnerKeyFor in
// turnctx.go for the canonical constructor.
func (m *shellManager) Start(command string, env []string, ownerKey string) (*bashSession, error) {
	if command == "" {
		return nil, errors.New("command is required")
	}

	// Refuse new sessions after Close. Without this, a Start that
	// races with shutdown could land its session in the post-Close map
	// and leak the process forever.
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("shell manager closed")
	}
	m.mu.Unlock()

	// context.Background here is intentional — a backgrounded shell
	// must outlive the turn that spawned it. The session's own cancel
	// is the only path that terminates it before natural exit.
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	// Fail closed: if the caller didn't pass an explicit env, build a
	// scrubbed one rather than letting Go default to bare os.Environ()
	// inheritance — that path is how daemon secrets reached chat replies.
	if env != nil {
		cmd.Env = env
	} else {
		cmd.Env = buildSubprocessEnv(nil)
	}

	// Spawn into a fresh process group so kill_shell reaches every
	// descendant (e.g. `sh -c "npm run dev"` forks node — without group
	// kill, killing sh leaves node running). Override CommandContext's
	// default Cancel (which only kills cmd.Process directly) to send
	// SIGKILL to the whole group.
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return killProcessGroup(cmd.Process.Pid)
	}

	out := newOutputBuffer(bufferCap)
	cmd.Stdout = out
	cmd.Stderr = out // outputBuffer's Write is mutex-protected so concurrent stdout+stderr is safe

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start background shell: %w", err)
	}

	// Re-check closed under the same lock that does the insert. The
	// pre-Start check above is just an early-out to avoid forking sh
	// when we know we're shutting down — the race-safe verdict is here.
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel() // group-kill the shell we just spawned
		return nil, errors.New("shell manager closed")
	}
	m.counter++
	id := fmt.Sprintf("bash_%d", m.counter)
	s := &bashSession{
		id:        id,
		owner:     ownerKey,
		command:   command,
		startedAt: time.Now(),
		cmd:       cmd,
		cancel:    cancel,
		out:       out,
	}
	m.shells[id] = s
	m.mu.Unlock()

	// Reaper: cmd.Wait returns when the process exits OR when ctx is
	// cancelled (kill). Capture exit code, then flip done.
	go func() {
		err := cmd.Wait()
		s.exitErr = err
		var ee *exec.ExitError
		if err == nil {
			s.exitCode = 0
		} else if errors.As(err, &ee) {
			s.exitCode = ee.ExitCode()
		} else {
			// Killed by cancel, or pipe / IO error. Use -1 to signal
			// "abnormal termination" — the agent can disambiguate from
			// the exit_err string returned by snapshot.
			s.exitCode = -1
		}
		s.done.Store(true)
		// Note: we deliberately do NOT remove the session from the map
		// here. bash_output remains useful after exit so the agent can
		// fetch the final output and exit status. Registry.Close handles
		// cleanup, or a future TTL eviction can be layered on top.
	}()

	return s, nil
}

// Get fetches a session by bash_id. It returns nil — indistinguishable
// from "no such id" — when ownerKey is non-empty and does not match the
// session's owner, so a caller cannot probe whether a guessed bash_id
// exists for someone else. An empty ownerKey skips the check (boot/admin
// paths and legacy tests that run outside any chat tenancy).
func (m *shellManager) Get(id, ownerKey string) *bashSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.shells[id]
	if s == nil {
		return nil
	}
	if ownerKey != "" && s.owner != ownerKey {
		return nil
	}
	return s
}

// Close kills every live session, clears the registry, and refuses any
// future Start. Idempotent. Called from Registry.Close on agent
// shutdown so backgrounded processes don't outlive their owner.
func (m *shellManager) Close() {
	m.mu.Lock()
	shells := m.shells
	m.shells = make(map[string]*bashSession)
	m.closed = true
	m.mu.Unlock()
	for _, s := range shells {
		s.cancel()
	}
}

// list returns a snapshot of all current sessions, sorted by id.
// Currently used only by tests; exposed for future list_shells tool.
func (m *shellManager) list() []*bashSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*bashSession, 0, len(m.shells))
	for _, s := range m.shells {
		out = append(out, s)
	}
	return out
}
