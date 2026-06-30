package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/buildinfo"
	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

type execArgs struct {
	Command         string `json:"command"`
	Stdin           string `json:"stdin,omitempty"`             // optional: piped to the command's stdin
	Timeout         int    `json:"timeout,omitempty"`           // seconds, default 120
	Sandbox         bool   `json:"sandbox,omitempty"`           // force sandbox for this call
	RunInBackground bool   `json:"run_in_background,omitempty"` // launch detached, return bash_id for bash_output / kill_shell
}

// MetaSandboxPrefix marks an exec result as having run inside a sandbox.
// Placed on the first line so the agent loop can extract it into the
// tool_result event metadata and strip it from the content the model sees.
// Uses the ASCII Unit Separator so it never collides with shell output.
const MetaSandboxPrefix = "\x1fFC_META:sandbox\x1f\n"

var dangerousCommands = []string{
	"rm -rf /",
	"mkfs",
	"dd if=",
	":(){:|:&};:",
	"> /dev/sda",
}

// SandboxConfig holds sandbox settings passed to the exec tool registration.
type SandboxConfig struct {
	Enabled   bool
	Image     string
	Pool      *sandbox.SandboxPool
	Workspace string
	AgentID   string
	Policy    *sandbox.Policy
}

// SkillEnvProvider returns environment variables for a skill by name.
type SkillEnvProvider func(skillName string) map[string]string

func registerExec(r *Registry) {
	registerExecWithSandbox(r, nil)
}

func registerExecWithSandbox(r *Registry, sbCfg *SandboxConfig) {
	registerExecFull(r, sbCfg, nil, nil)
}

// RegisterExecWithSkillEnv registers the exec tool with skill environment injection support.
// Caches envProvider + skillDirs on the Registry so a later SetExecutor
// (per-session sandbox bind) can re-apply env injection when it
// re-registers the exec closure — otherwise skills like image-tool run
// in the container without their FAL_KEY / REPLICATE_API_TOKEN.
func RegisterExecWithSkillEnv(r *Registry, sbCfg *SandboxConfig, envProvider SkillEnvProvider, skillDirs []string) {
	r.envProvider = envProvider
	r.skillDirs = skillDirs
	registerExecFull(r, sbCfg, envProvider, skillDirs)
}

func registerExecFull(r *Registry, sbCfg *SandboxConfig, envProvider SkillEnvProvider, skillDirs []string) {
	r.Register("exec", "Execute a shell command and return stdout/stderr. For binary or image output (PNG, JPEG, PDF, audio, video), write the file into the workspace (e.g. ./out.png) and reference it by relative path in your reply — do NOT base64-encode it into stdout, and do NOT inline data: URLs in your response. The workspace file will be surfaced to the user via the Files panel.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "The shell command to execute",
			},
			"stdin": map[string]interface{}{
				"type":        "string",
				"description": "Optional input piped to the command's stdin. Use this to feed JSON args to a skill script: command='python /skills/x/main.py', stdin='{\"prompt\":\"...\"}'.",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Timeout in seconds (default 120). Headless-browser workflows (camoufox-cli) need a longer ceiling for the first call — the daemon + browser cold-start can take 2-3 min when traffic is proxied; subsequent calls in the same session are sub-second.",
			},
			"sandbox": map[string]interface{}{
				"type":        "boolean",
				"description": "Force execution in sandbox container",
			},
			"run_in_background": map[string]interface{}{
				"type":        "boolean",
				"description": "Launch the command in the background and return a bash_id immediately. Use this for long-running processes (dev servers, build watchers, migrations). Read output later via bash_output(bash_id); terminate via kill_shell(bash_id). Background sessions live until killed or the agent shuts down.",
			},
		},
		"required": []string{"command"},
	}, makeExecToolFull(r, sbCfg, envProvider, skillDirs))
}

func makeExecTool(sbCfg *SandboxConfig) ToolFunc {
	return makeExecToolFull(nil, sbCfg, nil, nil)
}

// makeExecToolFull captures the registry pointer so it can consult the
// runtime `sandboxRequired` flag at call time — that's the contract
// SetSandboxRequired publishes when sandbox is configured at any layer
// up the stack, even if it was off at agent construction. Without this,
// a `pool.Get()` failure during bindSession would silently leak to the
// host shell.
func makeExecToolFull(r *Registry, sbCfg *SandboxConfig, envProvider SkillEnvProvider, skillDirs []string) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args execArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}

		if args.Command == "" {
			return "", fmt.Errorf("command is required")
		}

		// Check for dangerous commands
		lower := strings.ToLower(args.Command)
		for _, dc := range dangerousCommands {
			if strings.Contains(lower, dc) {
				return "", fmt.Errorf("dangerous command blocked: %s", args.Command)
			}
		}

		timeout := 120
		if args.Timeout > 0 {
			timeout = args.Timeout
		}

		execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()

		// If stdin was supplied, prepend a heredoc-style pipe so the
		// existing single-string exec path delivers it. Quoting `EOF`
		// disables variable expansion inside the heredoc body, so JSON
		// payloads don't get accidentally rewritten.
		command := args.Command
		if args.Stdin != "" {
			command = fmt.Sprintf("(cat <<'__FCSTDIN__'\n%s\n__FCSTDIN__\n) | %s", args.Stdin, args.Command)
		}

		// Use sandbox if enabled or forced. The registry's
		// sandboxRequired flag covers the case where the runtime decided
		// sandbox is mandatory after construction (sibling agent wanted
		// it, or admin flipped settings.sandbox.enabled mid-process).
		useSandbox := args.Sandbox || (sbCfg != nil && sbCfg.Enabled) || (r != nil && r.sandboxRequired)

		// Background mode is host-only in v1. Sandbox-mode background
		// would need StartBackground / Poll / Kill on sandbox.Executor
		// (or per-backend tmux-inside-container plumbing) — both are
		// follow-ups. Until then, point the model at tmux as a
		// workaround so it has a path forward inside sandboxes.
		if args.RunInBackground {
			if useSandbox {
				return "", fmt.Errorf("run_in_background is not yet supported in sandbox mode — start the long-running command via tmux inside the sandbox instead, e.g. exec({command: \"tmux new-session -d -s job '<your command>'\"}) then exec({command: \"tmux capture-pane -t job -p\"}) to read output and exec({command: \"tmux kill-session -t job\"}) to stop")
			}
			if r == nil || r.shellMgr == nil {
				return "", fmt.Errorf("run_in_background unavailable: shell manager not initialised")
			}
			// Always pass an explicit (scrubbed) env so the child
			// never inherits raw os.Environ() — that's the leak path
			// that put OSS AccessKey + DB DSN in chat replies.
			var skillEnv map[string]string
			if envProvider != nil && skillDirs != nil {
				skillEnv = resolveSkillEnv(args.Command, envProvider, skillDirs)
			}
			sessEnv := buildSubprocessEnv(skillEnv)
			// Stamp the shell with the calling chat's identity so
			// bash_output / kill_shell from a different chat can't
			// read or kill it (cross-tenant isolation). Falls back to
			// "" (no isolation) when no TurnContext is attached — i.e.
			// boot/admin paths with no chatter in flight.
			ownerKey := ""
			if tc := FromContext(ctx); tc != nil {
				ownerKey = OwnerKeyFor(r.agentID, tc.ChatterUserID, tc.SessionID)
			}
			s, err := r.shellMgr.Start(command, sessEnv, ownerKey)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Started background shell %s for command: %s\nUse bash_output(bash_id=%q) to read output, kill_shell(bash_id=%q) to terminate.", s.id, args.Command, s.id, s.id), nil
		}

		if useSandbox && sbCfg != nil && sbCfg.Pool != nil {
			sb := sbCfg.Pool.Get(sbCfg.AgentID, sbCfg.Image, sbCfg.Workspace, sbCfg.Policy)
			out, err := sb.Exec(execCtx, command, "/workspace")
			return MetaSandboxPrefix + out, err
		}
		// Sandbox was requested but no executor is wired — refuse rather
		// than running on the host shell. SetExecutor swaps this closure
		// for the sandboxed variant on successful session bind, so we
		// only land here when the executor pool failed (docker daemon
		// down, image pull failed, container start error). Returning a
		// clear error gives the model a chance to surface it instead of
		// the user seeing host-shell `command not found` mysteries.
		if useSandbox {
			return "", fmt.Errorf("sandbox required but no executor available — check that the sandbox backend (docker / e2b) is reachable and the configured image (%q) can start", sbCfgImage(sbCfg))
		}

		cmd := exec.CommandContext(execCtx, "sh", "-c", command)

		// Always set cmd.Env explicitly. Default Go behavior is to
		// inherit the parent's full env, which leaks daemon secrets
		// (FASTCLAW_STORAGE_DSN, FASTCLAW_OBJECT_STORE_*, ...) into
		// every shell the model can run.
		var skillEnv map[string]string
		if envProvider != nil && skillDirs != nil {
			skillEnv = resolveSkillEnv(args.Command, envProvider, skillDirs)
		}
		cmd.Env = buildSubprocessEnv(skillEnv)

		output, err := cmd.CombinedOutput()

		result := string(output)
		if err != nil {
			return fmt.Sprintf("%s\nError: %s", result, err.Error()), err
		}

		return result, nil
	}
}

// sbCfgImage returns the sandbox image name for diagnostic error messages.
// Returns "<unset>" so the user immediately sees that no image was even
// configured (vs. configured-but-unreachable).
func sbCfgImage(sbCfg *SandboxConfig) string {
	if sbCfg == nil || sbCfg.Image == "" {
		return "<unset>"
	}
	return sbCfg.Image
}

// resolveSkillEnv checks if the command path references a skill directory
// and returns the skill's configured env vars.
//
// Two matching paths:
//  1. host paths from skillDirs (e.g. "/Users/.../agents/<id>/skills") —
//     used when exec runs on the host shell.
//  2. sandbox-internal "/skills/<name>" prefix — every skill is mounted
//     into the docker container at that location regardless of where it
//     lives on the host, so commands the model writes inside the
//     sandbox use this form. Without this branch, env injection
//     silently broke for ALL sandbox calls (the host paths in
//     skillDirs never appear in /workspace-cd'd commands).
func resolveSkillEnv(command string, envProvider SkillEnvProvider, skillDirs []string) map[string]string {
	// 1. host paths
	for _, dir := range skillDirs {
		if strings.Contains(command, dir) {
			rest := command[strings.Index(command, dir)+len(dir):]
			if len(rest) > 0 && rest[0] == '/' {
				rest = rest[1:]
			}
			parts := strings.SplitN(rest, "/", 2)
			if len(parts) > 0 && parts[0] != "" {
				if env := envProvider(parts[0]); env != nil {
					return env
				}
			}
		}
	}
	// 2. sandbox /skills/<name>/... — fixed mount layout
	if idx := strings.Index(command, "/skills/"); idx >= 0 {
		rest := command[idx+len("/skills/"):]
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) > 0 && parts[0] != "" {
			if env := envProvider(parts[0]); env != nil {
				return env
			}
		}
	}
	return nil
}

// mergeEnv merges base env with additional vars. Additional vars override base.
func mergeEnv(base []string, additional map[string]string) []string {
	env := make([]string, 0, len(base)+len(additional))
	overridden := make(map[string]bool, len(additional))

	for _, e := range base {
		key := e
		if idx := strings.IndexByte(e, '='); idx >= 0 {
			key = e[:idx]
		}
		if _, ok := additional[key]; ok {
			overridden[key] = true
			continue // skip, will be added from additional
		}
		env = append(env, e)
	}

	for k, v := range additional {
		env = append(env, k+"="+v)
	}

	return env
}

// HostExecToolName is the tool name advertised for host-shell exec on
// self-hosted installs. Exported so callers (loop.go skill-dirs slice,
// future audit logs, etc.) can refer to it without re-stringing the
// literal.
const HostExecToolName = "host_exec"

// registerHostExec adds an escape-hatch exec tool that bypasses the
// sandbox executor and runs straight on the operator's host shell.
// Gated by buildinfo.IsHostExecAllowed() — only registered when the
// operator has explicitly opted in via FASTCLAW_ALLOW_HOST_EXEC=1
// AND a sandbox executor is present (otherwise `exec` already IS the
// host shell and host_exec would be a duplicate).
//
// Tool description spells out the boundary loudly so the model picks
// `exec` (sandbox) by default and only escapes to host_exec for
// genuine operator-environment work (`fastclaw upgrade`, `~/Downloads`,
// `launchctl`, system services, anything tied to the user's actual
// machine). The dangerousCommands shortlist still applies — sandbox vs
// host doesn't change the "no rm -rf /" rule.
//
// Default-OFF rationale: in any deployment reachable through an
// external IM channel (WeChat, Discord, Feishu, …), an unsuspecting
// chatter coaxing the model into host_exec is a privilege-escalation
// path. Operators who need host shell access opt in explicitly.
func registerHostExec(r *Registry, envProvider SkillEnvProvider, skillDirs []string) {
	r.Register(HostExecToolName,
		"Execute a shell command on the OPERATOR's host machine, bypassing the sandbox. "+
			"Use this ONLY for tasks tied to the user's actual environment — `fastclaw upgrade`, "+
			"reading their `~/Downloads`, listing host processes, running CLI tools they have "+
			"installed locally, similar host-side ops. For everything else (running scripts, "+
			"web requests, data processing, generating files for the user) use `exec` instead "+
			"so the work stays inside the sandbox. Same dangerous-command guard as exec; "+
			"`rm -rf /` and friends are still refused.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{
					"type":        "string",
					"description": "The shell command to execute on the host.",
				},
				"stdin": map[string]interface{}{
					"type":        "string",
					"description": "Optional input piped to the command's stdin.",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "Timeout in seconds (default 120). Headless-browser workflows (camoufox-cli) need a longer ceiling for the first call — the daemon + browser cold-start can take 2-3 min when traffic is proxied; subsequent calls in the same session are sub-second.",
				},
			},
			"required": []string{"command"},
		},
		func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
			var args execArgs
			if err := json.Unmarshal(rawArgs, &args); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			if args.Command == "" {
				return "", fmt.Errorf("command is required")
			}
			lower := strings.ToLower(args.Command)
			for _, dc := range dangerousCommands {
				if strings.Contains(lower, dc) {
					return "", fmt.Errorf("dangerous command blocked: %s", args.Command)
				}
			}
			timeout := 120
			if args.Timeout > 0 {
				timeout = args.Timeout
			}
			execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
			defer cancel()
			command := args.Command
			if args.Stdin != "" {
				command = fmt.Sprintf("(cat <<'__FCSTDIN__'\n%s\n__FCSTDIN__\n) | %s", args.Stdin, args.Command)
			}
			cmd := exec.CommandContext(execCtx, "sh", "-c", command)
			// host_exec is the operator's escape hatch — even so, scrub
			// daemon secrets from the inherited env. The operator
			// rarely needs FASTCLAW_STORAGE_DSN reachable from a host
			// shell, and never needs the model to be able to read it.
			var skillEnv map[string]string
			if envProvider != nil && skillDirs != nil {
				skillEnv = resolveSkillEnv(args.Command, envProvider, skillDirs)
			}
			cmd.Env = buildSubprocessEnv(skillEnv)
			out, err := cmd.CombinedOutput()
			result := string(out)
			if err != nil {
				return fmt.Sprintf("%s\nError: %s", result, err.Error()), err
			}
			return result, nil
		})
}

// registerSandboxedExec re-registers the exec tool so it delegates to a
// sandbox.Executor instead of running on the host. Skill env vars
// (FAL_KEY, REPLICATE_API_TOKEN, etc.) configured via the admin UI are
// injected into the container by prepending POSIX `export` statements
// to the command — sandbox.Executor.Exec only accepts a single command
// string so we can't pass env via process attribute the way the host
// path does.
func registerSandboxedExec(r *Registry, ex sandbox.Executor) {
	envProvider := r.envProvider
	skillDirs := r.skillDirs
	r.Register("exec", "Execute a shell command in the sandbox and return stdout/stderr. For binary or image output (PNG, JPEG, PDF, audio, video), write the file into the workspace (e.g. ./out.png) and reference it by relative path in your reply — do NOT base64-encode it into stdout, and do NOT inline data: URLs in your response. The workspace file will be surfaced to the user via the Files panel.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "The shell command to execute",
			},
			"stdin": map[string]interface{}{
				"type":        "string",
				"description": "Optional input piped to the command's stdin.",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Timeout in seconds (default 120). Headless-browser workflows (camoufox-cli) need a longer ceiling for the first call — the daemon + browser cold-start can take 2-3 min when traffic is proxied; subsequent calls in the same session are sub-second.",
			},
			"run_in_background": map[string]interface{}{
				"type":        "boolean",
				"description": "Launch in background. NOT YET SUPPORTED in sandbox mode — use `tmux new-session -d -s NAME '<cmd>'` directly instead.",
			},
		},
		"required": []string{"command"},
	}, func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args execArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
		if args.Command == "" {
			return "", fmt.Errorf("command is required")
		}
		if args.RunInBackground {
			return "", fmt.Errorf("run_in_background is not yet supported in sandbox mode — use tmux inside the sandbox instead: exec({command: \"tmux new-session -d -s job '<your command>'\"}) to start, exec({command: \"tmux capture-pane -t job -p\"}) to read, exec({command: \"tmux kill-session -t job\"}) to stop")
		}
		timeout := 120
		if args.Timeout > 0 {
			timeout = args.Timeout
		}
		command := args.Command
		// Stdin via heredoc (mirror the host path) so callers can pipe
		// JSON args to a skill script.
		if args.Stdin != "" {
			command = fmt.Sprintf("(cat <<'__FCSTDIN__'\n%s\n__FCSTDIN__\n) | %s", args.Stdin, args.Command)
		}
		// Inject the configured env for whichever skill the command
		// references (SK skill dirs may be host paths or the
		// container-internal /skills/<name> mount — resolveSkillEnv
		// matches both).
		injected := []string{}
		if envProvider != nil {
			skillEnv := resolveSkillEnv(args.Command, envProvider, skillDirs)
			if len(skillEnv) > 0 {
				var sb strings.Builder
				for k, v := range skillEnv {
					sb.WriteString("export ")
					sb.WriteString(k)
					sb.WriteString("=")
					sb.WriteString(shellQuote(v))
					sb.WriteString("; ")
					if v == "" {
						injected = append(injected, k+"=<empty>")
					} else {
						injected = append(injected, k+"=<set "+strconv.Itoa(len(v))+"chars>")
					}
				}
				sb.WriteString(command)
				command = sb.String()
			}
		}
		slog.Info("sandboxed exec",
			"backend", ex.Backend(),
			"envProviderSet", envProvider != nil,
			"skillDirsCount", len(skillDirs),
			"injected", injected,
			"cmdHead", firstN(args.Command, 80))
		out, err := ex.Exec(ctx, command, time.Duration(timeout)*time.Second)
		// Hint, don't auto-fall-back: an auto-retry to host shell would
		// silently breach the sandbox boundary on any prompt-injected
		// "make it fail in sandbox" trick AND would re-run a possibly
		// wrong command in a different filesystem. Surface a hint
		// instead so the LLM (or its operator-trained ChatBot) makes
		// an explicit decision. Only attach the hint when host_exec is
		// actually available — on hosted deployments it's not, and
		// suggesting a tool that doesn't exist just confuses the
		// model. We probe by tool name so the check is decoupled from
		// the deploy-mode flag — same answer, less coupling.
		if err != nil && looksLikeSandboxAbsence(err, out) && buildinfo.IsHostExecAllowed() {
			err = fmt.Errorf("%w\n[hint: this looks like a sandbox-environment miss (binary or path not present in the container). If the command needs the user's actual host machine — e.g. `fastclaw upgrade`, `~/Downloads`, host CLI tools — retry with the `host_exec` tool instead.]", err)
		}
		return MetaSandboxPrefix + out, err
	})
}

// looksLikeSandboxAbsence sniffs an exec error / output for the common
// "tried to run a host-only thing inside the sandbox" signatures so the
// hint we attach to the error is targeted, not noisy. Conservative —
// returns false unless we're fairly sure: a real failure (e.g. a script
// crashed mid-run) shouldn't get a "use host_exec instead" suggestion
// that would just send the LLM down the wrong path.
func looksLikeSandboxAbsence(err error, out string) bool {
	if err == nil {
		return false
	}
	hay := strings.ToLower(err.Error() + "\n" + out)
	patterns := []string{
		"command not found",
		"not found in path",
		": no such file or directory",
		"executable file not found",
	}
	for _, p := range patterns {
		if strings.Contains(hay, p) {
			return true
		}
	}
	return false
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// shellQuote single-quote-escapes a value for safe interpolation into
// a POSIX shell command. Used by sandboxed exec to prepend env vars
// without exposing the unescaped value to shell metacharacters.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
