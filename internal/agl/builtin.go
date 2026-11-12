package agl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fastclaw-ai/fastclaw/internal/billing"
)

// Tool mode, built-in tools: the LLM agent works on the person's computer
// directly with agl's own read / write / edit / exec (agl ≥ 0.3.14,
// `tools.list` / `tools.call`), next to run_local_agent when the computer
// also has a coding agent. The model picks per request: quick look-ups and
// a command or two here, long or multi-step work with the coding agent.
// agl never calls a model on this path.
//
// The hub link's access is the ceiling, and only the person can raise it,
// on their computer (`agl connect … --access`). From agl 0.3.15 we opt in
// to approvals: below `full`, a write or a command agl doesn't consider
// read-only is held (status approval_required) and the person answers it
// in the chat (1/2/3 or the web card), like a coding agent's permission
// request; we then call tools.approve. Older agl: ask/read_only → read;
// edit → read, write, edit; full → all four.

// MinBuiltinVersion is the first agl with built-in tools.
const MinBuiltinVersion = "0.3.14"

// BuiltinAgentName is how approvals and notices name the built-in tools.
const BuiltinAgentName = "agl"

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
	// Chat is where the person approves a held call (set by the turn);
	// Notify replaces IM messages for a web chat, as in ToolRun.
	Chat   ToolChat
	Notify func(ToolNotice)
}

type builtinEntry struct {
	tools []BuiltinTool
	at    time.Time
}

type builtinCache struct {
	mu sync.Mutex
	m  map[string]builtinEntry
}

// UsesBuiltinTools reports whether m offers built-in tools: online and
// agl new enough. Coding agents on it don't matter; the model chooses.
func UsesBuiltinTools(m LocalMachine) bool {
	return m.Online && CompareVersions(m.AglVersion, MinBuiltinVersion) >= 0
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
	raw, err := t.link.Request(ctx, m.ID, "tools.list", map[string]any{"approvals": true})
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
		// Commands run as the product (macOS privacy prompts name it);
		// the model doesn't choose this.
		delete(args, "app_name")
		if m.AppName != "" && action != "read" && action != "cancel" {
			args["app_name"] = m.AppName
		}
	}
	params := map[string]any{"name": c.Name, "arguments": args, "approvals": true}
	if s := builtinSession(c.Chat); s != "" {
		params["session"] = s
	}
	raw, err := t.builtinRequest(ctx, m, c.Name, "tools.call", params)
	if err != nil {
		return "", err
	}
	if held, ok := approvalRequired(raw); ok {
		if raw, err = t.awaitBuiltinApproval(ctx, c, m, held); err != nil {
			return "", err
		}
	}
	billing.Record(ctx, billing.Event{
		Kind: billing.KindLocalAgent, Provider: "agl", Model: "builtin:" + c.Name,
		Units: 1, Unit: "call",
		Detail: map[string]any{"machine": m.ID},
	})
	return capBuiltin(string(raw)), nil
}

func (t *Tools) builtinRequest(ctx context.Context, m LocalMachine, name, method string, params map[string]any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, builtinCallTimeout)
	defer cancel()
	raw, err := t.link.Request(ctx, m.ID, method, params)
	if err != nil {
		if errors.Is(err, ErrOffline) {
			return nil, toolErr("The computer %s went offline. Tell the person to check it and try again.", m.Name)
		}
		return nil, toolErr("%s on %s failed: %s", name, m.Name, clipText(err.Error(), 500))
	}
	return raw, nil
}

// builtinSession scopes agl's allow_session grants to one chat ("" = none).
func builtinSession(c ToolChat) string {
	if c == (ToolChat{}) {
		return ""
	}
	sum := sha256.Sum256([]byte(c.AgentID + "\x00" + c.key()))
	return "chat:" + hex.EncodeToString(sum[:12])
}

// heldCall is agl's answer for a call waiting for the person (0.3.15).
type heldCall struct {
	Status     string   `json:"status"`
	ApprovalID string   `json:"approval_id"`
	Title      string   `json:"title"`
	Options    []string `json:"options"`
	ExpiresAt  int64    `json:"expires_at"`
}

func approvalRequired(raw json.RawMessage) (heldCall, bool) {
	var h heldCall
	if json.Unmarshal(raw, &h) != nil || h.Status != "approval_required" || h.ApprovalID == "" {
		return heldCall{}, false
	}
	return h, true
}

// builtinApprovalWait caps how long a held call waits for the person
// (agl expires it after 10 minutes).
var builtinApprovalWait = 10 * time.Minute

// awaitBuiltinApproval asks the person in the chat to approve a held
// call, waits for the answer and passes it to agl, which runs the call on
// allow and returns its result.
func (t *Tools) awaitBuiltinApproval(ctx context.Context, c BuiltinCall, m LocalMachine, held heldCall) (json.RawMessage, error) {
	what := strings.TrimSpace(held.Title)
	if what == "" {
		what = c.Name
	}
	if c.Chat == (ToolChat{}) {
		t.answerHeld(m, held.ApprovalID, "deny")
		return nil, toolErr("%s on %s needs the person's approval, and there is no chat to ask in. Nothing was done.", c.Name, m.Name)
	}
	title := clipText(m.Name+" · "+what, 300)
	answer := make(chan string, 1)
	a := &toolApproval{machineID: m.ID, userID: c.UserID, chat: c.Chat, title: title, agent: BuiltinAgentName, machine: m.Name, notify: c.Notify, answer: answer}
	key := c.Chat.key()
	t.mu.Lock()
	if t.pending[key] != nil {
		t.mu.Unlock()
		t.answerHeld(m, held.ApprovalID, "deny")
		return nil, toolErr("Another approval is still waiting for the person's answer in this chat. Wait for it, then try again.")
	}
	t.pending[key] = a
	t.mu.Unlock()
	drop := func() {
		t.mu.Lock()
		if t.pending[key] == a {
			delete(t.pending, key)
		}
		t.mu.Unlock()
	}
	if c.Notify != nil {
		c.Notify(ToolNotice{Kind: NoticeApproval, Agent: BuiltinAgentName, Machine: m.Name, Text: title})
	} else {
		t.send(c.Chat, fmt.Sprintf(MsgApproval, title))
	}
	wait := builtinApprovalWait
	if held.ExpiresAt > 0 {
		if left := time.Until(time.UnixMilli(held.ExpiresAt)) - 5*time.Second; left < wait {
			wait = left
		}
	}
	timer := time.NewTimer(max(wait, time.Second))
	defer timer.Stop()
	var decision string
	select {
	case decision = <-answer:
	case <-timer.C:
		drop()
		t.answerHeld(m, held.ApprovalID, "deny")
		if c.Notify != nil {
			c.Notify(ToolNotice{Kind: NoticeApprovalResolved, Agent: BuiltinAgentName, Machine: m.Name, Decision: "timeout"})
		} else {
			t.send(c.Chat, MsgApprovalTimeout)
		}
		return nil, toolErr("The person didn't answer the approval request for %q in time, so nothing was done.", what)
	case <-ctx.Done():
		drop()
		t.answerHeld(m, held.ApprovalID, "deny")
		return nil, ctx.Err()
	}
	if decision == "deny" {
		t.answerHeld(m, held.ApprovalID, "deny")
		return nil, toolErr("The person denied %q on %s. Nothing was done; don't try it another way unless they ask.", what, m.Name)
	}
	return t.builtinRequest(ctx, m, c.Name, "tools.approve", map[string]any{"approval_id": held.ApprovalID, "decision": decision})
}

// answerHeld tells agl a held call won't run, so it isn't left waiting.
func (t *Tools) answerHeld(m LocalMachine, id, decision string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = t.link.Request(ctx, m.ID, "tools.approve", map[string]any{"approval_id": id, "decision": decision})
	}()
}

// pickBuiltin chooses the computer for c: the named one, else the first
// online one that offers c.Name.
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
			if len(m.Agents) > 0 {
				return LocalMachine{}, toolErr("%s doesn't allow %q from here; its link allows: %s. Hand the whole task to run_local_agent on %s instead (%s); the person approves its actions there.", m.Name, c.Name, allowed, m.Name, agentList(m))
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
