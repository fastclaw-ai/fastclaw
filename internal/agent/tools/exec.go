package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

type execArgs struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"` // seconds, default 30
	Sandbox bool   `json:"sandbox,omitempty"` // force sandbox for this call
}

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
func RegisterExecWithSkillEnv(r *Registry, sbCfg *SandboxConfig, envProvider SkillEnvProvider, skillDirs []string) {
	registerExecFull(r, sbCfg, envProvider, skillDirs)
}

func registerExecFull(r *Registry, sbCfg *SandboxConfig, envProvider SkillEnvProvider, skillDirs []string) {
	r.Register("exec", "Execute a shell command and return stdout/stderr", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "The shell command to execute",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Timeout in seconds (default 30)",
			},
			"sandbox": map[string]interface{}{
				"type":        "boolean",
				"description": "Force execution in sandbox container",
			},
		},
		"required": []string{"command"},
	}, makeExecToolFull(sbCfg, envProvider, skillDirs))
}

func makeExecTool(sbCfg *SandboxConfig) ToolFunc {
	return makeExecToolFull(sbCfg, nil, nil)
}

func makeExecToolFull(sbCfg *SandboxConfig, envProvider SkillEnvProvider, skillDirs []string) ToolFunc {
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

		timeout := 30
		if args.Timeout > 0 {
			timeout = args.Timeout
		}

		execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()

		// Use sandbox if enabled or forced
		useSandbox := args.Sandbox || (sbCfg != nil && sbCfg.Enabled)
		if useSandbox && sbCfg != nil && sbCfg.Pool != nil {
			sb := sbCfg.Pool.Get(sbCfg.AgentID, sbCfg.Image, sbCfg.Workspace, sbCfg.Policy)
			return sb.Exec(execCtx, args.Command, "/workspace")
		}

		cmd := exec.CommandContext(execCtx, "sh", "-c", args.Command)

		// Inject skill-specific env vars if the command references a skill directory
		if envProvider != nil && skillDirs != nil {
			skillEnv := resolveSkillEnv(args.Command, envProvider, skillDirs)
			if len(skillEnv) > 0 {
				cmd.Env = mergeEnv(os.Environ(), skillEnv)
			}
		}

		output, err := cmd.CombinedOutput()

		result := string(output)
		if err != nil {
			return fmt.Sprintf("%s\nError: %s", result, err.Error()), err
		}

		return result, nil
	}
}

// resolveSkillEnv checks if the command path references a skill directory
// and returns the skill's configured env vars.
func resolveSkillEnv(command string, envProvider SkillEnvProvider, skillDirs []string) map[string]string {
	// Check if any skill directory appears in the command
	for _, dir := range skillDirs {
		if strings.Contains(command, dir) {
			// Extract skill name from the path after the skill dir
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

// registerSandboxedExec re-registers the exec tool so it delegates to a
// sandbox.Executor instead of running on the host.
func registerSandboxedExec(r *Registry, ex sandbox.Executor) {
	r.Register("exec", "Execute a shell command in the sandbox and return stdout/stderr", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "The shell command to execute",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Timeout in seconds (default 30)",
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
		timeout := 30
		if args.Timeout > 0 {
			timeout = args.Timeout
		}
		return ex.Exec(ctx, args.Command, time.Duration(timeout)*time.Second)
	})
}
