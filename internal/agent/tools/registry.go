package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

// ToolFunc is a function that executes a tool with JSON arguments and returns a result string.
type ToolFunc func(ctx context.Context, args json.RawMessage) (string, error)

// ToolSource indicates where a tool was registered from.
type ToolSource int

const (
	SourceBuiltin ToolSource = iota // built-in tool
	SourceMCP                       // MCP server tool
	SourcePlugin                    // plugin-provided tool
)

// Registry holds all registered tools.
type Registry struct {
	tools       map[string]registeredTool
	sandboxRoot string // if non-empty, file tools reject paths outside this dir
	executor    sandbox.Executor // if non-nil, all file+exec tools route through this
	// File tool roots. systemRoot is the agent metadata dir (SOUL.md etc.);
	// userRoot is where user-facing artifacts go. A relative path whose base
	// matches a known system filename routes to systemRoot; everything else
	// goes to userRoot.
	systemRoot  string
	userRoot    string
}

type registeredTool struct {
	def    provider.Tool
	fn     ToolFunc
	source ToolSource
}

// NewRegistry creates a new tool registry with built-in tools.
// NewRegistry creates a Registry whose file tools route relative paths between
// two roots: system files (SOUL.md, IDENTITY.md, etc.) land in systemRoot;
// everything else lands in userRoot. Passing the same value for both gives
// the legacy single-root behavior.
func NewRegistry(systemRoot, userRoot string) *Registry {
	r := &Registry{
		tools:      make(map[string]registeredTool),
		systemRoot: systemRoot,
		userRoot:   userRoot,
	}
	r.registerBuiltins()
	return r
}

// Register adds a tool to the registry (as a built-in tool).
func (r *Registry) Register(name, description string, parameters interface{}, fn ToolFunc) {
	r.RegisterFrom(name, description, parameters, fn, SourceBuiltin)
}

// RegisterFrom adds a tool to the registry with an explicit source.
// Plugin-sourced tools can override built-in tools with the same name.
func (r *Registry) RegisterFrom(name, description string, parameters interface{}, fn ToolFunc, source ToolSource) {
	r.tools[name] = registeredTool{
		def: provider.Tool{
			Type: "function",
			Function: provider.ToolFunction{
				Name:        name,
				Description: description,
				Parameters:  parameters,
			},
		},
		fn:     fn,
		source: source,
	}
}

// HasBuiltin returns true if a built-in tool with the given name exists.
func (r *Registry) HasBuiltin(name string) bool {
	t, ok := r.tools[name]
	return ok && t.source == SourceBuiltin
}

// GetFunc returns the ToolFunc for a tool by name, or nil if not found.
func (r *Registry) GetFunc(name string) ToolFunc {
	t, ok := r.tools[name]
	if !ok {
		return nil
	}
	return t.fn
}

// Definitions returns all tool definitions for the LLM.
func (r *Registry) Definitions() []provider.Tool {
	defs := make([]provider.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.def)
	}
	return defs
}

// Execute runs a tool by name with the given arguments.
func (r *Registry) Execute(ctx context.Context, name string, args string) (string, error) {
	tool, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}

	result, err := tool.fn(ctx, json.RawMessage(args))
	if err != nil {
		return result + "\n[Analyze the error above and try a different approach.]", err
	}
	return result, nil
}

// SetSandboxConfig updates the exec tool to use sandbox mode.
func (r *Registry) SetSandboxConfig(sbCfg *SandboxConfig) {
	registerExecWithSandbox(r, sbCfg)
}

// SetSandboxRoot restricts the file tools (read_file, write_file, list_dir)
// to paths under root. Absolute paths outside the root and relative paths
// that traverse above it are rejected. When root is empty (default), no
// restriction is applied — this is the local single-user mode. In cloud
// mode the root is typically set to the user's directory
// (~/.fastclaw/users/{userID}).
func (r *Registry) SetSandboxRoot(root string) {
	r.sandboxRoot = root
}

// SetExecutor attaches a sandbox Executor. When set, read_file, write_file,
// list_dir, and exec are ALL forwarded to the executor instead of operating
// on the host filesystem. This is the mode used for cloud deployments where
// each user gets an isolated container/VM with their own runtime + files.
func (r *Registry) SetExecutor(ex sandbox.Executor) {
	r.executor = ex
	// Re-register built-in tools to use the executor.
	registerSandboxedFile(r, ex)
	registerSandboxedExec(r, ex)
}

func (r *Registry) registerBuiltins() {
	registerExec(r)
	registerFile(r)
	registerMessage(r)
}
