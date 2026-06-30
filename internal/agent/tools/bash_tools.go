package tools

// bash_output and kill_shell — companion tools to exec(run_in_background).
//
// Tool surface mirrors Claude Code's `BashOutput` / `KillShell` so prompts
// and skills written for that runtime port over without translation.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type bashOutputArgs struct {
	BashID string `json:"bash_id"`
	Filter string `json:"filter,omitempty"` // optional regex applied per output line
}

type killShellArgs struct {
	BashID string `json:"bash_id"`
}

const bashOutputDescription = `Read new stdout/stderr from a backgrounded shell since the last call. Use this to monitor a long-running process started with exec(run_in_background=true).

Returns:
  - new output produced since the previous bash_output call on this bash_id (each call advances a per-session cursor)
  - "[status] running" or "[status] exited (code=N)" — only "exited" rows guarantee the process is done; killed processes report code=-1 with the kill reason appended
  - a "[truncated]" line prepended if the 4 MiB per-session output buffer rolled past the read cursor (oldest bytes dropped FIFO)

Notes:
  - The session keeps running across calls until kill_shell or natural exit.
  - After exit, bash_output is still callable to read any final output and confirm the exit code.
  - The optional 'filter' regex is applied per output line (lines that don't match are dropped before return) — useful for tailing a noisy log when you only care about errors.`

const killShellDescription = `Terminate a backgrounded shell started by exec(run_in_background=true). Sends SIGKILL via process-group cancellation. Idempotent — calling it on an already-exited shell is a no-op and returns success.`

func registerBashOutput(r *Registry) {
	r.Register("bash_output", bashOutputDescription, map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"bash_id": map[string]interface{}{
				"type":        "string",
				"description": "Identifier returned by exec(run_in_background=true), e.g. \"bash_3\".",
			},
			"filter": map[string]interface{}{
				"type":        "string",
				"description": "Optional regex (RE2). Only output lines matching this pattern are returned.",
			},
		},
		"required": []string{"bash_id"},
	}, func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args bashOutputArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("bash_output: parse args: %w", err)
		}
		if args.BashID == "" {
			return "", fmt.Errorf("bash_output: bash_id is required")
		}
		if r.shellMgr == nil {
			return "", fmt.Errorf("bash_output: shell manager not initialised")
		}
		// Enforce the same chat-tenancy boundary Start stamped: a shell
		// started by one chatter is invisible to another. No TurnContext
		// (boot/admin) → ownerKey "" skips the check, matching legacy.
		ownerKey := ""
		if tc := FromContext(ctx); tc != nil {
			ownerKey = OwnerKeyFor(r.agentID, tc.ChatterUserID, tc.SessionID)
		}
		s := r.shellMgr.Get(args.BashID, ownerKey)
		if s == nil {
			return "", fmt.Errorf("bash_output: no such bash_id %q (call exec(run_in_background=true) first; ids are valid only within the same agent process)", args.BashID)
		}

		var filter *regexp.Regexp
		if args.Filter != "" {
			re, err := regexp.Compile(args.Filter)
			if err != nil {
				return "", fmt.Errorf("bash_output: invalid filter regex: %w", err)
			}
			filter = re
		}

		raw2, dropped := s.readNew()
		status, code, exitErr := s.snapshot()
		// Race fix: bytes can land in the buffer AFTER our readNew but
		// BEFORE the reaper flips done=true. Without this drain, an
		// "exited" report would silently miss the last few bytes (often
		// the most useful ones — the error message or summary line).
		// Drain only when exited; running shells can poll again later
		// for new output.
		if status == statusExited {
			more, dropped2 := s.readNew()
			if len(more) > 0 {
				raw2 = append(raw2, more...)
			}
			dropped = dropped || dropped2
		}
		body := string(raw2)
		if filter != nil && body != "" {
			body = filterLines(body, filter)
		}

		var sb strings.Builder
		if dropped {
			sb.WriteString("[truncated] earlier output exceeded the 4 MiB session cap and was dropped\n")
		}
		if body != "" {
			sb.WriteString(body)
			if !strings.HasSuffix(body, "\n") {
				sb.WriteByte('\n')
			}
		}
		switch status {
		case statusRunning:
			sb.WriteString("[status] running")
		case statusExited:
			fmt.Fprintf(&sb, "[status] exited (code=%d)", code)
			if exitErr != nil && code == -1 {
				// abnormal: killed, IO error, etc.
				fmt.Fprintf(&sb, " — %s", exitErr.Error())
			}
		}
		return sb.String(), nil
	})
}

func registerKillShell(r *Registry) {
	r.Register("kill_shell", killShellDescription, map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"bash_id": map[string]interface{}{
				"type":        "string",
				"description": "Identifier returned by exec(run_in_background=true).",
			},
		},
		"required": []string{"bash_id"},
	}, func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args killShellArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("kill_shell: parse args: %w", err)
		}
		if args.BashID == "" {
			return "", fmt.Errorf("kill_shell: bash_id is required")
		}
		if r.shellMgr == nil {
			return "", fmt.Errorf("kill_shell: shell manager not initialised")
		}
		// Same tenancy gate as bash_output — a chatter can only kill
		// shells their own chat started. No TurnContext → no gate.
		ownerKey := ""
		if tc := FromContext(ctx); tc != nil {
			ownerKey = OwnerKeyFor(r.agentID, tc.ChatterUserID, tc.SessionID)
		}
		s := r.shellMgr.Get(args.BashID, ownerKey)
		if s == nil {
			return "", fmt.Errorf("kill_shell: no such bash_id %q", args.BashID)
		}
		if s.done.Load() {
			_, code, _ := s.snapshot()
			return fmt.Sprintf("Already exited (code=%d).", code), nil
		}
		_ = s.kill()
		return fmt.Sprintf("Sent kill to %s.", s.id), nil
	})
}

// filterLines retains only lines matching re. Trailing newline policy
// follows the input: a body with a trailing newline keeps it, one
// without doesn't. Lines that don't match are dropped silently.
func filterLines(body string, re *regexp.Regexp) string {
	hadTrailingNL := strings.HasSuffix(body, "\n")
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	kept := make([]string, 0, len(lines))
	for _, l := range lines {
		if re.MatchString(l) {
			kept = append(kept, l)
		}
	}
	out := strings.Join(kept, "\n")
	if hadTrailingNL && out != "" {
		out += "\n"
	}
	return out
}
