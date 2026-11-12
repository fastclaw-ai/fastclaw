package agl

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fastclaw-ai/fastclaw/internal/billing"
)

// Tool mode, built-in tools: on a computer with no coding agent, the LLM
// agent works on it directly with agl's own read / write / edit / exec
// (agl ≥ 0.3.14, `tools.list` / `tools.call`). agl never calls a model on
// this path and asks nobody: the hub link's access decides which tools
// exist (ask/read_only → read; edit → read, write, edit; full → all four),
// and only the person can raise it, on their computer
// (`agl connect … --access full`).

// MinBuiltinVersion is the first agl with built-in tools.
const MinBuiltinVersion = "0.3.14"

const (
	builtinListTimeout = 3 * time.Second
	builtinCallTimeout = 60 * time.Second
	builtinCacheTTL    = 2 * time.Minute
	// builtinExecWaitMs is how long an exec start/read waits for output
	// when the model doesn't say: fewer round trips than agl's 1s.
	builtinExecWaitMs = 15000
	// MaxBuiltinResultBytes caps one built-in tool result for the model.
	MaxBuiltinResultBytes = 32 << 10
)

// BuiltinTool is one built-in tool a machine offers this hub.
type BuiltinTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// BuiltinCall is one call of a built-in tool.
type BuiltinCall struct {
	UserID  string // the chatter (set by the turn)
	Machine string // id or name; "" = the first computer offering Name
	Name    string // read | write | edit | exec
	Args    json.RawMessage
}

type builtinEntry struct {
	tools []BuiltinTool
	at    time.Time
}

type builtinCache struct {
	mu sync.Mutex
	m  map[string]builtinEntry
}

// UsesBuiltinTools reports whether m is worked on with built-in tools:
// online, agl new enough, and no coding agent available on it.
func UsesBuiltinTools(m LocalMachine) bool {
	return m.Online && len(m.Agents) == 0 && CompareVersions(m.AglVersion, MinBuiltinVersion) >= 0
}

// BuiltinTools lists the built-in tools m offers this hub (filtered by the
// link's access), or nil when it offers none or isn't a UsesBuiltinTools
// machine. Cached briefly: access only changes when the person relinks.
func (t *Tools) BuiltinTools(ctx context.Context, m LocalMachine) []BuiltinTool {
	if t == nil || !UsesBuiltinTools(m) {
		return nil
	}
	t.builtin.mu.Lock()
	e, ok := t.builtin.m[m.ID]
	t.builtin.mu.Unlock()
	if ok && time.Since(e.at) < builtinCacheTTL {
		return e.tools
	}
	ctx, cancel := context.WithTimeout(ctx, builtinListTimeout)
	defer cancel()
	raw, err := t.link.Request(ctx, m.ID, "tools.list", map[string]any{})
	if err != nil {
		return nil
	}
	var list []BuiltinTool
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil
	}
	tools := list[:0]
	for _, bt := range list {
		if builtinNames[bt.Name] {
			tools = append(tools, bt)
		}
	}
	t.builtin.mu.Lock()
	if t.builtin.m == nil {
		t.builtin.m = map[string]builtinEntry{}
	}
	t.builtin.m[m.ID] = builtinEntry{tools: tools, at: time.Now()}
	t.builtin.mu.Unlock()
	return tools
}

var builtinNames = map[string]bool{"read": true, "write": true, "edit": true, "exec": true}

func offers(tools []BuiltinTool, name string) bool {
	for _, bt := range tools {
		if bt.Name == name {
			return true
		}
	}
	return false
}

// CallBuiltin runs one built-in tool on the chatter's computer and returns
// agl's result as JSON for the model. Failures the model should explain
// are *ToolError.
func (t *Tools) CallBuiltin(ctx context.Context, c BuiltinCall) (string, error) {
	if !builtinNames[c.Name] {
		return "", toolErr("Unknown tool %q.", c.Name)
	}
	m, err := t.pickBuiltin(ctx, c)
	if err != nil {
		return "", err
	}
	args := map[string]any{}
	if len(c.Args) > 0 {
		if err := json.Unmarshal(c.Args, &args); err != nil || args == nil {
			return "", toolErr("Invalid arguments: %v", err)
		}
	}
	delete(args, "machine")
	switch c.Name {
	case "read", "write", "edit":
		p, _ := args["path"].(string)
		clean, err := builtinPath(p, m.DefaultCwd)
		if err != nil {
			return "", err
		}
		args["path"] = clean
	case "exec":
		action, _ := args["action"].(string)
		if _, ok := args["wait_ms"]; !ok && action != "cancel" {
			args["wait_ms"] = builtinExecWaitMs
		}
		if _, ok := args["cwd"]; !ok && action != "read" && action != "cancel" && m.DefaultCwd != "" {
			args["cwd"] = m.DefaultCwd
		}
	}
	ctx, cancel := context.WithTimeout(ctx, builtinCallTimeout)
	defer cancel()
	raw, err := t.link.Request(ctx, m.ID, "tools.call", map[string]any{"name": c.Name, "arguments": args})
	if err != nil {
		if errors.Is(err, ErrOffline) {
			return "", toolErr("The computer %s went offline. Tell the person to check it and try again.", m.Name)
		}
		return "", toolErr("%s on %s failed: %s", c.Name, m.Name, clipText(err.Error(), 500))
	}
	billing.Record(ctx, billing.Event{
		Kind: billing.KindLocalAgent, Provider: "agl", Model: "builtin:" + c.Name,
		Units: 1, Unit: "call",
		Detail: map[string]any{"machine": m.ID},
	})
	return capBuiltin(string(raw)), nil
}

// pickBuiltin chooses the computer for c: the named one, else the first
// online one without a coding agent that offers c.Name.
func (t *Tools) pickBuiltin(ctx context.Context, c BuiltinCall) (LocalMachine, error) {
	machines, err := t.Machines(ctx, c.UserID)
	if err != nil {
		return LocalMachine{}, err
	}
	want := strings.TrimSpace(c.Machine)
	for _, m := range machines {
		if want != "" && m.ID != want && !strings.EqualFold(m.Name, want) {
			continue
		}
		if want != "" && !m.Online {
			return LocalMachine{}, toolErr("The computer %s is offline. Tell the person to make sure it is on, connected, and agl is running (agl status), then try again.", m.Name)
		}
		if want != "" && len(m.Agents) > 0 {
			return LocalMachine{}, toolErr("%s has coding agents (%s); use run_local_agent there instead.", m.Name, agentList(m))
		}
		offered := t.BuiltinTools(ctx, m)
		if offers(offered, c.Name) {
			return m, nil
		}
		if want != "" {
			names := make([]string, 0, len(offered))
			for _, bt := range offered {
				names = append(names, bt.Name)
			}
			allowed := strings.Join(names, ", ")
			if allowed == "" {
				allowed = "nothing"
			}
			return LocalMachine{}, toolErr("%s doesn't allow %q from here; its link allows: %s. To allow more, the person re-links that computer with more access (full for commands, edit for file changes).", m.Name, c.Name, allowed)
		}
	}
	if want != "" {
		return LocalMachine{}, toolErr("No computer named %q. The person's computers: %s.", want, machineList(machines))
	}
	return LocalMachine{}, toolErr("No online computer allows %q from here.", c.Name)
}

// builtinPath validates a file path for read/write/edit: absolute, ~/…,
// or relative to the machine's default folder, and never inside a hidden
// directory or a dotfile (~/.ssh, ~/.aws, .git, .env, …), so a turn can't
// lift or plant keys and config even if the model is talked into it.
func builtinPath(p, defaultCwd string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || strings.ContainsRune(p, 0) || len(p) > 1024 || !utf8.ValidString(p) {
		return "", toolErr(`"path" is required.`)
	}
	if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~/") {
		cwd := strings.TrimSpace(defaultCwd)
		if cwd == "" {
			return "", toolErr("Give the full path (absolute or starting with ~/).")
		}
		p = strings.TrimRight(cwd, "/") + "/" + p
	}
	home := strings.HasPrefix(p, "~/")
	clean := path.Clean(strings.TrimPrefix(p, "~"))
	for _, seg := range strings.Split(clean, "/") {
		if strings.HasPrefix(seg, ".") {
			return "", toolErr("Hidden files and folders (names starting with \".\") are off limits.")
		}
	}
	if home {
		return "~" + clean, nil
	}
	return clean, nil
}

func capBuiltin(s string) string {
	if len(s) <= MaxBuiltinResultBytes {
		return s
	}
	cut := MaxBuiltinResultBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n[result truncated: read a smaller range]"
}
