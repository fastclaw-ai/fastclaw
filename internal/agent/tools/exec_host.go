package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// hostCommandWaitDelay bounds how long Wait blocks on I/O *after* the
// process group has been signalled. Belt-and-suspenders behind the group
// kill: if some descendant escapes the group (a daemon that calls
// setsid, a process the shell disowned), the inherited pipe would
// otherwise keep CombinedOutput blocked indefinitely. With WaitDelay set
// Go abandons the copy and returns the bytes buffered so far.
const hostCommandWaitDelay = 5 * time.Second

// runHostCommand runs `sh -c command` on the host under a deadline that
// actually holds.
//
// The naive form — exec.CommandContext + CombinedOutput — silently fails
// to enforce its own timeout. CommandContext's default Cancel kills only
// the direct `sh` child; every grandchild survives, and because they
// inherited the write end of the pipe CombinedOutput reads from, Wait
// blocks until the LAST descendant exits. A `find / -name x | head`
// whose 120s deadline fires at t=120s therefore returned at t=18m30s,
// once find had finished walking the entire filesystem. The turn just
// hung, and the model got a bare `signal: killed` with no hint that a
// timeout was what killed it.
//
// Three things fix it, and all three are needed:
//
//  1. setProcessGroup — spawn `sh` as a group leader so the whole tree
//     is addressable. Same call bash_session.go already makes for
//     backgrounded shells (see TestShellManager_KillReachesGrandchildren).
//  2. cmd.Cancel — SIGKILL the group (kill(-pgid)) instead of the lone
//     `sh`, so the pipe writers actually die.
//  3. cmd.WaitDelay — cap the post-kill I/O wait for anything that
//     escaped the group.
//
// On timeout the error is rewritten into something actionable. `signal:
// killed` reads like a crash and sends the model looking for a bug in
// its command; "timed out after 120s" tells it to narrow the command or
// raise the timeout, which is the actual remedy.
//
// dir is the working directory (the turn's session workspace, see
// Registry.HostWorkDir); it is created if missing. "" keeps the daemon's
// own cwd, which is never what a chatter's command should write into.
func runHostCommand(execCtx context.Context, command, dir string, env []string, timeout time.Duration) (string, error) {
	cmd := exec.CommandContext(execCtx, "sh", "-c", command)
	cmd.Env = env
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err == nil {
			cmd.Dir = dir
		}
	}

	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return killProcessGroup(cmd.Process.Pid)
	}
	cmd.WaitDelay = hostCommandWaitDelay

	out, err := cmd.CombinedOutput()
	result := string(out)
	if err == nil {
		return result, nil
	}

	// Deadline beats the raw wait error: whatever Wait reports after a
	// group kill (signal: killed, ErrWaitDelay, exit status 1 from a
	// half-run pipeline) is noise compared to "you ran out of time".
	if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
		timeoutErr := fmt.Errorf(
			"command timed out after %s and its process group was killed. "+
				"Partial output above (if any) is all it produced. "+
				"Narrow the command — filesystem-wide scans like `find /` will not finish — "+
				"or re-run with a larger `timeout`",
			timeout)
		return fmt.Sprintf("%s\nError: %s", result, timeoutErr.Error()), timeoutErr
	}

	return fmt.Sprintf("%s\nError: %s", result, err.Error()), err
}
