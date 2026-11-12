package agl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fastclaw-ai/fastclaw/internal/billing"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Tool mode: an LLM agent the user chats with (in a 1:1 IM chat) runs one
// turn of a coding agent on the CHATTER's own linked computer and uses
// the answer in its reply (docs/agl-machines.md "Tool mode"). The agent
// loop mounts the tools (internal/agent/tools/local_agents.go); this file
// runs them and owns the chat's pending permission request, which the
// gateway answers from the chat's next 1/2/3 before any LLM sees it.

// MsgWorking is the first notice of a tool run that takes a while, when
// nothing is known yet about what it's doing: "%s" is the agent's name.
// Later updates: progress.go.
const MsgWorking = "%s 正在你的电脑上处理…"

const (
	// ToolRunTimeout caps one run_local_agent call.
	ToolRunTimeout = 15 * time.Minute
	// toolNoticeAfter: a run still going after this sends MsgWorking.
	toolNoticeAfter = 20 * time.Second
	// MaxToolResultBytes caps the text handed back to the model.
	MaxToolResultBytes = 8 << 10
)

// ToolChat is the IM chat a tool run reports to (approvals, notices).
type ToolChat struct {
	Channel   string
	AccountID string
	ChatID    string
	// AgentID is the Fastclaw agent running the turn (outbound routing).
	AgentID string
}

func (c ToolChat) key() string { return c.Channel + "\x00" + c.AccountID + "\x00" + c.ChatID }

// WebToolChat is the chat of a dashboard conversation: userID chatting
// agentID in the stream session sessionKey. The page that answers its
// approvals builds the same value.
func WebToolChat(userID, agentID, sessionKey string) ToolChat {
	return ToolChat{Channel: "web", AccountID: userID, ChatID: agentID + "\x00" + sessionKey, AgentID: agentID}
}

// LocalMachine is one of the chatter's machines LLM agents may use.
type LocalMachine struct {
	ID            string
	Name          string
	Online        bool
	Agents        []store.MachineAgent // available and enabled ones only
	DefaultAccess string
	DefaultCwd    string
	// Settings holds the owner's model / effort per agent id.
	Settings map[string]store.MachineAgentSetting
	// AglVersion is the machine's agl (decides whether its agents'
	// capabilities are reported or assumed; see Capabilities).
	AglVersion string
}

// HasAgent reports whether agent may run on m. A machine that reported
// no agents yet accepts any (agl rejects unknown ones itself).
func (m LocalMachine) HasAgent(agent string) bool {
	if len(m.Agents) == 0 {
		return true
	}
	for _, a := range m.Agents {
		if strings.EqualFold(a.ID, agent) {
			return true
		}
	}
	return false
}

func (m LocalMachine) agentName(agent string) string {
	for _, a := range m.Agents {
		if strings.EqualFold(a.ID, agent) && a.Name != "" {
			return a.Name
		}
	}
	return agent
}

func (m LocalMachine) canonicalAgent(agent string) string {
	for _, a := range m.Agents {
		if strings.EqualFold(a.ID, agent) {
			return a.ID
		}
	}
	return agent
}

// Kinds of ToolNotice.
const (
	NoticeProgress         = "progress"          // Text: what the agent is doing
	NoticeApproval         = "approval"          // Text: what it asks permission for
	NoticeApprovalResolved = "approval_resolved" // Decision: allow | allow_session | deny | timeout | failed
	NoticeDone             = "done"              // the run ended (any outcome)
)

// ToolNotice is one update of a run, for a chat that can't take IM text
// (the web chat renders it as a card with approval buttons).
type ToolNotice struct {
	Kind     string
	Agent    string // display name
	Machine  string // display name
	Text     string
	Decision string
	Error    string // approval_resolved with Decision "failed"
}

// ToolRun is one run_local_agent call.
type ToolRun struct {
	// UserID owns the machines: the chatter the turn runs for, never
	// the agent owner. Set by the turn, not by the model.
	UserID string
	Chat   ToolChat
	// From the model:
	Agent   string
	Task    string
	Machine string // id or name; "" = the first online one with Agent
	Cwd     string // "" = the machine's default
	// Notify, when set, receives progress and approval requests instead
	// of IM messages to Chat (web chat). Approvals are then answered
	// with Decide, not a 1/2/3 reply.
	Notify func(ToolNotice)
}

// ToolError is a run failure the model should relay to the person.
type ToolError struct{ Msg string }

func (e *ToolError) Error() string { return e.Msg }

func toolErr(format string, a ...any) error { return &ToolError{Msg: fmt.Sprintf(format, a...)} }

type toolApproval struct {
	machineID string
	userID    string
	chat      ToolChat
	p         pendingApproval
	title     string
	agent     string // display names, for PendingApproval
	machine   string
	notify    func(ToolNotice) // nil: IM chat
	// answer, when set, receives the decision instead of session.approve:
	// a built-in tool call waiting in CallBuiltin (builtin.go).
	answer chan<- string
}

// Tools runs tool-mode turns on users' machines.
type Tools struct {
	link  Link
	store store.MachineStore
	out   Sender

	// Overridable in tests.
	Timeout       time.Duration
	NoticeAfter   time.Duration
	ProgressEvery time.Duration

	mu      sync.Mutex
	pending map[string]*toolApproval // ToolChat.key() → open permission request
	grants  *sessionGrants
	builtin builtinCache // machine → its built-in tools (builtin.go)
}

// NewTools returns the tool-mode runner over link.
func NewTools(link Link, st store.MachineStore, out Sender) *Tools {
	return &Tools{link: link, store: st, out: out, Timeout: ToolRunTimeout, NoticeAfter: toolNoticeAfter, ProgressEvery: progressEvery,
		pending: map[string]*toolApproval{}, grants: newSessionGrants()}
}

// Machines lists userID's active machines that allow LLM agents.
func (t *Tools) Machines(ctx context.Context, userID string) ([]LocalMachine, error) {
	if t == nil || t.store == nil || userID == "" {
		return nil, nil
	}
	list, err := t.store.ListMachines(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]LocalMachine, 0, len(list))
	for i := range list {
		m := &list[i]
		if m.Revoked() || !m.AllowAgents || m.UserID != userID {
			continue
		}
		lm := LocalMachine{ID: m.ID, Name: m.Name, Online: t.link.Online(m.ID), DefaultAccess: m.DefaultAccess, DefaultCwd: m.DefaultCwd,
			Settings: m.AgentSettings, AglVersion: m.Version}
		if lm.Name == "" {
			lm.Name = m.Hostname
		}
		if lm.Name == "" {
			lm.Name = m.ID
		}
		if lm.DefaultAccess == "" {
			lm.DefaultAccess = AccessAsk
		}
		for _, a := range m.Agents {
			if a.Available && !HiddenAgent(a.ID) && !m.AgentSetting(a.ID).Disabled {
				lm.Agents = append(lm.Agents, a)
			}
		}
		out = append(out, lm)
	}
	return out, nil
}

// ToolSessionName is the agl session of one (machine, agent, chat), so a
// follow-up in the same chat continues the same local conversation.
func ToolSessionName(machineID, agent string, chat ToolChat) string {
	sum := sha256.Sum256([]byte(chat.key()))
	return "fc:tool:" + machineID + ":" + agent + ":" + hex.EncodeToString(sum[:8])
}

// pick chooses the machine for r among the chatter's (re-read now).
func (t *Tools) pick(ctx context.Context, r ToolRun) (LocalMachine, error) {
	machines, err := t.Machines(ctx, r.UserID)
	if err != nil {
		return LocalMachine{}, err
	}
	if len(machines) == 0 {
		return LocalMachine{}, toolErr("The person has no computer linked that allows agents to run on it. Tell them to link one (or turn on \"allow agents\") in the app.")
	}
	if want := strings.TrimSpace(r.Machine); want != "" {
		for _, m := range machines {
			if m.ID == want || strings.EqualFold(m.Name, want) {
				if !m.HasAgent(r.Agent) {
					return LocalMachine{}, toolErr("%q is not available on %s. Available there: %s.", r.Agent, m.Name, agentList(m))
				}
				if !m.Online {
					return LocalMachine{}, toolErr("The computer %s is offline. Tell the person to make sure it is on, connected, and agl is running (agl status), then try again.", m.Name)
				}
				return m, nil
			}
		}
		return LocalMachine{}, toolErr("No computer named %q. The person's computers: %s.", want, machineList(machines))
	}
	var offline []string
	for _, m := range machines {
		if !m.HasAgent(r.Agent) {
			continue
		}
		if m.Online {
			return m, nil
		}
		offline = append(offline, m.Name)
	}
	if len(offline) > 0 {
		return LocalMachine{}, toolErr("The person's computer (%s) is offline. Tell them to make sure it is on, connected, and agl is running (agl status), then try again.", strings.Join(offline, ", "))
	}
	return LocalMachine{}, toolErr("No linked computer has the local agent %q. Available: %s.", r.Agent, availableList(machines))
}

func agentList(m LocalMachine) string {
	if len(m.Agents) == 0 {
		return "none reported"
	}
	ids := make([]string, 0, len(m.Agents))
	for _, a := range m.Agents {
		ids = append(ids, a.ID)
	}
	return strings.Join(ids, ", ")
}

func machineList(ms []LocalMachine) string {
	names := make([]string, 0, len(ms))
	for _, m := range ms {
		names = append(names, m.Name)
	}
	return strings.Join(names, ", ")
}

func availableList(ms []LocalMachine) string {
	var parts []string
	for _, m := range ms {
		state := "online"
		if !m.Online {
			state = "offline"
		}
		parts = append(parts, fmt.Sprintf("%s (%s): %s", m.Name, state, agentList(m)))
	}
	return strings.Join(parts, "; ")
}

// Run runs one turn of r.Agent on the chosen machine and returns its
// final answer (commentary skipped, capped at MaxToolResultBytes).
// Cancelling ctx aborts the turn on the machine. Failures the model
// should explain are *ToolError.
func (t *Tools) Run(ctx context.Context, r ToolRun) (string, error) {
	r.Agent = strings.TrimSpace(r.Agent)
	r.Task = strings.TrimSpace(r.Task)
	if r.Agent == "" || r.Task == "" {
		return "", toolErr(`"agent" and "task" are required.`)
	}
	if !ValidCwd(r.Cwd) {
		return "", toolErr(`"cwd" is invalid.`)
	}
	started := time.Now()
	m, err := t.pick(ctx, r)
	if err != nil {
		return "", err
	}
	agent := m.canonicalAgent(r.Agent)
	agentName := m.agentName(agent)
	cwd := strings.TrimSpace(r.Cwd)
	if cwd == "" {
		cwd = m.DefaultCwd
	}

	budget := t.Timeout
	if dl, ok := ctx.Deadline(); ok {
		// Leave the turn time to reply after a timeout.
		if left := time.Until(dl) * 4 / 5; left < budget {
			budget = left
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	// Subscribe before prompting so no event is missed.
	events, unsubscribe := t.link.Subscribe(m.ID)
	defer unsubscribe()

	name := ToolSessionName(m.ID, agent, r.Chat)
	sid, _, err := openSession(runCtx, t.link, m.ID, agent, cwd, m.DefaultAccess, name, m.Settings[agent])
	if err != nil {
		if errors.Is(err, ErrOffline) {
			return "", toolErr("The computer %s went offline. Tell the person to check it and try again.", m.Name)
		}
		return "", toolErr("Could not start %s on %s: %v", agentName, m.Name, err)
	}
	turnID := newTurnID()
	if _, err := t.link.Request(runCtx, m.ID, "session.prompt", map[string]any{
		"session_id": sid, "text": r.Task, "mode": "queue", "turn_id": turnID,
	}); err != nil {
		if errors.Is(err, ErrOffline) {
			return "", toolErr("The computer %s went offline. Tell the person to check it and try again.", m.Name)
		}
		return "", toolErr("Could not send the task to %s on %s: %v", agentName, m.Name, err)
	}
	defer t.clearApproval(r.Chat, sid)

	abort := func() {
		actx, acancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer acancel()
		_, _ = t.link.Request(actx, m.ID, "session.abort", map[string]any{"session_id": sid})
	}

	typing := time.NewTicker(typingInterval)
	defer typing.Stop()
	notice := time.NewTimer(t.NoticeAfter)
	defer notice.Stop()
	// Later progress updates start after the first notice.
	var updates <-chan time.Time
	var updateTicker *time.Ticker
	defer func() {
		if updateTicker != nil {
			updateTicker.Stop()
		}
	}()
	prog := newProgress(time.Now())
	// say reports progress: a notice for the web, an IM message otherwise.
	say := func(text string) {
		if r.Notify != nil {
			r.Notify(ToolNotice{Kind: NoticeProgress, Agent: agentName, Machine: m.Name, Text: text})
			return
		}
		t.send(r.Chat, text)
		// A bot message ends the client's typing cue; restart it.
		t.typing(r.Chat)
	}
	if r.Notify != nil {
		defer r.Notify(ToolNotice{Kind: NoticeDone, Agent: agentName, Machine: m.Name})
	} else {
		t.typing(r.Chat)
	}

	var buf strings.Builder
	ours := false
	for {
		select {
		case <-runCtx.Done():
			abort()
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return partial(buf.String()), toolErr("%s on %s did not finish within %s and was stopped. Tell the person; they can ask again with a smaller task.", agentName, m.Name, budget.Round(time.Second))
		case <-typing.C:
			if r.Notify == nil {
				t.typing(r.Chat)
			}
		case <-notice.C:
			if !t.hasApproval(r.Chat) {
				say(prog.first(agentName, time.Now()))
			}
			updateTicker = time.NewTicker(t.ProgressEvery)
			updates = updateTicker.C
		case <-updates:
			if t.hasApproval(r.Chat) {
				continue
			}
			if msg := prog.update(agentName, time.Now()); msg != "" {
				say(msg)
			}
		case ev := <-events:
			if ev.Disconnected {
				return partial(buf.String()), toolErr("The computer %s disconnected while %s was working; the task was interrupted. Tell the person.", m.Name, agentName)
			}
			if ev.SessionID != sid {
				continue
			}
			e := ev.Event
			if ours || (e.Kind == "turn_started" && e.TurnID == turnID) {
				prog.event(e)
			}
			switch e.Kind {
			case "turn_started":
				ours = e.TurnID == turnID
				buf.Reset()
			case "text_delta":
				if ours && e.Phase != "commentary" && buf.Len() < maxReplyBytes {
					buf.WriteString(e.Text)
				}
			case "permission_request":
				if !ours || e.RequestID == "" || e.DecidedBy != "" {
					continue
				}
				if t.grants.has(m.ID, sid) {
					autoApprove(t.link, m.ID, sid, e.RequestID)
					continue
				}
				title := approvalTitle(e)
				t.mu.Lock()
				t.pending[r.Chat.key()] = &toolApproval{machineID: m.ID, userID: r.UserID, chat: r.Chat,
					p:     pendingApproval{sessionID: sid, requestID: e.RequestID},
					title: title, agent: agentName, machine: m.Name, notify: r.Notify}
				t.mu.Unlock()
				if r.Notify != nil {
					r.Notify(ToolNotice{Kind: NoticeApproval, Agent: agentName, Machine: m.Name, Text: title})
				} else {
					t.send(r.Chat, approvalPrompt(e))
				}
			case "permission_resolved":
				t.mu.Lock()
				a := t.pending[r.Chat.key()]
				cleared := a != nil && a.p.sessionID == sid && a.p.requestID == e.RequestID
				if cleared {
					delete(t.pending, r.Chat.key())
				}
				t.mu.Unlock()
				if cleared && e.By == "timeout" {
					if r.Notify != nil {
						r.Notify(ToolNotice{Kind: NoticeApprovalResolved, Agent: agentName, Machine: m.Name, Decision: "timeout"})
					} else {
						t.send(r.Chat, MsgApprovalTimeout)
					}
				}
			case "turn_ended":
				if e.TurnID != turnID {
					continue
				}
				text := strings.TrimSpace(buf.String())
				switch e.Outcome {
				case "failed":
					msg := e.Error
					if msg == "" {
						msg = "unknown error"
					}
					return partial(text), toolErr("%s on %s failed: %s", agentName, m.Name, clipText(msg, 300))
				case "aborted":
					return partial(text), toolErr("%s on %s was stopped before it finished.", agentName, m.Name)
				}
				if text == "" {
					text = "(finished with no text reply)"
				}
				billing.Record(ctx, billing.Event{
					Kind: billing.KindLocalAgent, Provider: "agl", Model: agent,
					Units: 1, Unit: "run",
					Detail: map[string]any{"duration_ms": time.Since(started).Milliseconds(), "machine": m.ID},
				})
				return fmt.Sprintf("[%s on %s]\n%s", agentName, m.Name, capResult(text)), nil
			}
		}
	}
}

func partial(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return "Partial output:\n" + capResult(text)
}

func capResult(text string) string {
	if len(text) <= MaxToolResultBytes {
		return text
	}
	cut := MaxToolResultBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + fmt.Sprintf("\n[output truncated: %d of %d bytes shown]", cut, len(text))
}

func (t *Tools) send(c ToolChat, text string) {
	if t.out == nil || text == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t.out.Send(ctx, bus.OutboundMessage{Channel: c.Channel, AccountID: c.AccountID, AgentID: c.AgentID, ChatID: c.ChatID, Text: text})
}

func (t *Tools) typing(c ToolChat) {
	if t.out != nil {
		t.out.Typing(c.Channel, c.AccountID, c.ChatID)
	}
}

func (t *Tools) hasApproval(c ToolChat) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pending[c.key()] != nil
}

func (t *Tools) clearApproval(c ToolChat, sessionID string) {
	t.mu.Lock()
	if a := t.pending[c.key()]; a != nil && a.p.sessionID == sessionID {
		delete(t.pending, c.key())
	}
	t.mu.Unlock()
}

// HasPendingApproval reports whether chat has an open permission request
// from a tool run.
func (t *Tools) HasPendingApproval(chat ToolChat) bool { return t != nil && t.hasApproval(chat) }

// InterceptApproval answers the chat's open permission request when text
// is 1/2/3, acknowledges it in the chat, and reports true: the message is
// consumed and must not reach an LLM turn. senderUserID is the user space
// the message was routed in; it must own the machine. False when the
// chat has no open request or text is not a decision.
func (t *Tools) InterceptApproval(ctx context.Context, chat ToolChat, senderUserID, text string) bool {
	if t == nil {
		return false
	}
	decision, ok := ApprovalDecision(text)
	if !ok {
		return false
	}
	found, _ := t.Decide(ctx, chat, senderUserID, decision)
	return found
}

// PendingToolApproval is a chat's open permission request.
type PendingToolApproval struct {
	Agent   string `json:"agent"`
	Machine string `json:"machine"`
	Title   string `json:"title"`
}

// PendingApproval returns chat's open permission request if userID owns
// the machine it came from.
func (t *Tools) PendingApproval(chat ToolChat, userID string) (PendingToolApproval, bool) {
	if t == nil {
		return PendingToolApproval{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	a := t.pending[chat.key()]
	if a == nil || a.userID != userID {
		return PendingToolApproval{}, false
	}
	return PendingToolApproval{Agent: a.agent, Machine: a.machine, Title: a.title}, true
}

// Decide answers chat's open permission request with decision (allow |
// allow_session | deny) and reports it to the chat. found is false when
// the chat has no open request that userID may answer; err is set when
// the computer couldn't take the answer.
func (t *Tools) Decide(ctx context.Context, chat ToolChat, userID, decision string) (found bool, err error) {
	if t == nil {
		return false, nil
	}
	if _, ok := approvalAck[decision]; !ok {
		return false, fmt.Errorf("unknown decision %q", decision)
	}
	t.mu.Lock()
	a := t.pending[chat.key()]
	if a == nil || a.userID != userID {
		t.mu.Unlock()
		return false, nil
	}
	delete(t.pending, chat.key())
	t.mu.Unlock()
	if a.answer != nil {
		a.answer <- decision // buffered; the waiter runs the call
		if a.notify != nil {
			a.notify(ToolNotice{Kind: NoticeApprovalResolved, Agent: a.agent, Machine: a.machine, Decision: decision})
		} else {
			t.send(a.chat, approvalAck[decision])
		}
		return true, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := approve(ctx, t.link, a.machineID, &a.p, decision); err != nil {
		slog.Warn("agl: tool approval failed", "machine", a.machineID, "error", err)
		if a.notify != nil {
			a.notify(ToolNotice{Kind: NoticeApprovalResolved, Agent: a.agent, Machine: a.machine, Decision: "failed", Error: err.Error()})
		} else {
			t.send(a.chat, fmt.Sprintf(MsgFailed, err))
		}
		return true, err
	}
	t.grants.note(a.machineID, a.p.sessionID, decision)
	if a.notify != nil {
		a.notify(ToolNotice{Kind: NoticeApprovalResolved, Agent: a.agent, Machine: a.machine, Decision: decision})
	} else {
		t.send(a.chat, approvalAck[decision])
	}
	return true, nil
}
