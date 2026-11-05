package agl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// User-facing lines (WeChat). Kept short; the product's users read Chinese.
const (
	MsgOffline         = "你的电脑当前不在线。请确认电脑已开机联网、agl 正在运行（终端执行 agl status 查看），然后再发一次。"
	MsgNoMachine       = "这个智能体绑定的电脑已失效或已被移除，请在网页上重新连接电脑。"
	MsgDMOnly          = "电脑上的智能体只能在私聊中使用。"
	MsgNotOwner        = "这个智能体只接受绑定它的微信账号发来的消息。"
	MsgApproval        = "需要确认：%s\n回复 1 允许 / 2 本会话都允许 / 3 拒绝"
	MsgNewSession      = "已开始新会话。"
	MsgStopped         = "已停止当前任务。"
	MsgNothingToStop   = "当前没有正在运行的任务。"
	MsgDisconnected    = "你的电脑连接已断开，本次任务已中断。"
	MsgFailed          = "电脑上的智能体出错了：%s"
	MsgStartFailed     = "无法在你的电脑上启动智能体：%s"
	MsgEmptyReply      = "（已完成，没有文字回复）"
	MsgAborted         = "（已停止）"
	MsgTruncated       = "（回复过长，后面的内容已省略）"
	MsgApprovalTimeout = "确认已超时，已自动拒绝。"
	MsgStalled         = "电脑上的智能体长时间没有响应，本次任务已放弃等待。"
)

var approvalAck = map[string]string{
	"allow":         "已允许。",
	"allow_session": "已允许（本会话内不再询问）。",
	"deny":          "已拒绝。",
}

const (
	// MaxReplyChunk is the longest single WeChat bubble we send (runes).
	MaxReplyChunk = 1800
	// maxReplyChunks caps one reply's bubbles.
	maxReplyChunks = 10
	// maxReplyBytes caps the text buffered for one turn.
	maxReplyBytes  = 256 << 10
	typingInterval = 5 * time.Second
	// turnIdleTimeout: agl sends a heartbeat every 15s while a turn
	// runs; this long without any event means the turn is lost.
	turnIdleTimeout = 20 * time.Minute
	chatIdleExit    = 10 * time.Minute
	chatInboxSize   = 32
)

// ApprovalDecision maps a reply to a pending permission request:
// 1 → allow, 2 → allow_session, 3 → deny.
func ApprovalDecision(text string) (string, bool) {
	switch strings.TrimSpace(text) {
	case "1", "１":
		return "allow", true
	case "2", "２":
		return "allow_session", true
	case "3", "３":
		return "deny", true
	}
	return "", false
}

// SplitReply splits text into WeChat-sized bubbles of at most max runes,
// preferring paragraph and line breaks, and caps the bubble count.
func SplitReply(text string, max int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var out []string
	for text != "" {
		if utf8.RuneCountInString(text) <= max {
			out = append(out, text)
			break
		}
		// Byte offset of the max-th rune.
		cut, n := 0, 0
		for i := range text {
			if n == max {
				cut = i
				break
			}
			n++
		}
		head := text[:cut]
		if i := strings.LastIndex(head, "\n\n"); i > cut/2 {
			cut = i
		} else if i := strings.LastIndex(head, "\n"); i > cut/2 {
			cut = i
		}
		out = append(out, strings.TrimSpace(text[:cut]))
		text = strings.TrimSpace(text[cut:])
		if len(out) == maxReplyChunks {
			if text != "" {
				out[len(out)-1] += "\n" + MsgTruncated
			}
			break
		}
	}
	return out
}

// Link is what the bridge needs from the hub (a fake in tests).
type Link interface {
	Online(machineID string) bool
	Request(ctx context.Context, machineID, method string, params any) (json.RawMessage, error)
	Subscribe(machineID string) (<-chan Event, func())
}

// Sender delivers replies to the chat a message came from.
type Sender interface {
	Send(ctx context.Context, out bus.OutboundMessage)
	Typing(channel, accountID, chatID string)
}

// Inbound is one IM message for a runtime "agl" agent.
type Inbound struct {
	Agent *store.AgentRecord
	// SpaceUserID is the user space the message was routed in (the
	// channel binder). It must own both the agent and the machine.
	SpaceUserID string
	Channel     string
	AccountID   string
	ChatID      string
	MessageID   string
	Text        string
	PeerKind    string
	// SenderAllowed is false when the channel knows who scanned / paired
	// it and this sender is someone else.
	SenderAllowed bool
}

// Bridge runs IM turns of runtime "agl" agents on the owner's machine.
type Bridge struct {
	link  Link
	store store.MachineStore
	out   Sender

	mu    sync.Mutex
	chats map[string]*chat
}

// NewBridge returns a bridge over link.
func NewBridge(link Link, st store.MachineStore, out Sender) *Bridge {
	return &Bridge{link: link, store: st, out: out, chats: map[string]*chat{}}
}

type pendingApproval struct {
	sessionID string
	requestID string
}

// chat is one IM conversation with one agl agent. Messages are handled
// in order by its worker; a collector goroutine relays the session's
// events while turns are outstanding.
type chat struct {
	key   string
	inbox chan Inbound

	mu          sync.Mutex
	machineID   string
	sessionID   string
	steer       bool
	collecting  bool
	outstanding map[string]bool
	approval    *pendingApproval
	stop        func() // ends the collector
	// gen identifies the current collector; a superseded one (after
	// /new or a rebind) must not touch the chat's state.
	gen uint64
}

func chatKeyOf(in Inbound) string {
	return in.Agent.ID + "\x00" + in.Channel + "\x00" + in.AccountID + "\x00" + in.ChatID
}

// SessionName is the agl session name for an agent's chat.
func SessionName(agentID, chatID string) string { return "fc:" + agentID + ":" + chatID }

// Handle queues in for its chat's worker and returns at once.
func (b *Bridge) Handle(in Inbound) {
	if in.Agent == nil {
		return
	}
	key := chatKeyOf(in)
	b.mu.Lock()
	c := b.chats[key]
	if c == nil {
		c = &chat{key: key, inbox: make(chan Inbound, chatInboxSize), outstanding: map[string]bool{}}
		b.chats[key] = c
		go b.work(c)
	}
	select {
	case c.inbox <- in:
	default:
		slog.Warn("agl: chat inbox full, message dropped", "agent", in.Agent.ID, "chat", in.ChatID)
	}
	b.mu.Unlock()
}

func (b *Bridge) work(c *chat) {
	idle := time.NewTimer(chatIdleExit)
	defer idle.Stop()
	for {
		select {
		case in := <-c.inbox:
			b.handle(c, in)
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(chatIdleExit)
		case <-idle.C:
			b.mu.Lock()
			c.mu.Lock()
			busy := c.collecting || len(c.inbox) > 0
			c.mu.Unlock()
			if !busy {
				delete(b.chats, c.key)
				b.mu.Unlock()
				return
			}
			b.mu.Unlock()
			idle.Reset(chatIdleExit)
		}
	}
}

func (b *Bridge) reply(in Inbound, text string) {
	if text == "" || b.out == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b.out.Send(ctx, bus.OutboundMessage{
		Channel:   in.Channel,
		AccountID: in.AccountID,
		AgentID:   in.Agent.ID,
		ChatID:    in.ChatID,
		Text:      text,
	})
}

func (b *Bridge) replyLong(in Inbound, text string) {
	for _, part := range SplitReply(text, MaxReplyChunk) {
		b.reply(in, part)
	}
}

// CheckOwnership re-checks, for one turn, that the agent is an agl agent
// owned by the user space it runs in, and that its machine belongs to
// the same user and is not revoked. It returns the machine and config.
func CheckOwnership(ctx context.Context, st store.MachineStore, rec *store.AgentRecord, spaceUserID string) (*store.MachineRecord, RuntimeConfig, error) {
	rc := RuntimeConfigOf(rec.Config)
	if !IsAglAgent(rec) || rec.UserID == "" || rec.UserID != spaceUserID || rc.MachineID == "" || st == nil {
		return nil, rc, errors.New("agent not runnable here")
	}
	m, err := st.GetMachine(ctx, rc.MachineID)
	if err != nil {
		return nil, rc, err
	}
	if m.Revoked() || m.UserID != rec.UserID {
		return nil, rc, errors.New("machine revoked or foreign")
	}
	return m, rc, nil
}

func (b *Bridge) handle(c *chat, in Inbound) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	m, rc, err := CheckOwnership(ctx, b.store, in.Agent, in.SpaceUserID)
	if err != nil {
		slog.Warn("agl: turn refused", "agent", in.Agent.ID, "space", in.SpaceUserID, "error", err)
		b.reply(in, MsgNoMachine)
		return
	}
	if in.PeerKind == "group" {
		b.reply(in, MsgDMOnly)
		return
	}
	if !in.SenderAllowed {
		b.reply(in, MsgNotOwner)
		return
	}
	text := strings.TrimSpace(in.Text)
	name := SessionName(in.Agent.ID, in.ChatID)

	c.mu.Lock()
	if c.machineID != m.ID {
		// Rebound to another machine: forget the old session.
		c.machineID, c.sessionID, c.steer = m.ID, "", false
		c.endCollectorLocked()
	}
	approval := c.approval
	collecting := c.collecting
	c.mu.Unlock()

	// A pending permission request takes 1/2/3 as its answer.
	if approval != nil {
		if decision, ok := ApprovalDecision(text); ok {
			err := approve(ctx, b.link, m.ID, approval, decision)
			c.mu.Lock()
			if c.approval == approval {
				c.approval = nil
			}
			c.mu.Unlock()
			if err != nil {
				b.reply(in, fmt.Sprintf(MsgFailed, err))
				return
			}
			b.reply(in, approvalAck[decision])
			return
		}
	}

	// No approval in memory, but the reply is 1/2/3: after a Fastclaw
	// restart the pending request only lives in agl's session history.
	// Recover it, resume relaying that turn, then answer it.
	if approval == nil && !collecting {
		if decision, ok := ApprovalDecision(text); ok && b.link.Online(m.ID) {
			if p := b.recoverApproval(ctx, m.ID, name); p != nil {
				events, unsubscribe := b.link.Subscribe(m.ID)
				done := make(chan struct{})
				var once sync.Once
				stop := func() { once.Do(func() { close(done) }) }
				c.mu.Lock()
				c.endCollectorLocked()
				c.gen++
				gen := c.gen
				c.sessionID = p.sessionID
				c.collecting = true
				c.outstanding = map[string]bool{p.turnID: true}
				c.stop = stop
				c.mu.Unlock()
				go b.collect(c, gen, in, p.sessionID, events, unsubscribe, done, p.turnID, p.text)
				if err := approve(ctx, b.link, m.ID, &pendingApproval{sessionID: p.sessionID, requestID: p.requestID}, decision); err != nil {
					stop()
					b.reply(in, fmt.Sprintf(MsgFailed, err))
					return
				}
				b.reply(in, approvalAck[decision])
				return
			}
		}
	}

	switch strings.ToLower(text) {
	case "/stop":
		if !collecting {
			b.reply(in, MsgNothingToStop)
			return
		}
		if _, err := b.link.Request(ctx, m.ID, "session.abort", map[string]any{"session_id": name}); err != nil && !errors.Is(err, ErrOffline) {
			b.reply(in, fmt.Sprintf(MsgFailed, err))
			return
		}
		b.reply(in, MsgStopped)
		return
	case "/new":
		if b.link.Online(m.ID) {
			if collecting {
				_, _ = b.link.Request(ctx, m.ID, "session.abort", map[string]any{"session_id": name})
			}
			// Closing deletes the session; the next prompt creates a
			// fresh one under the same name.
			_, _ = b.link.Request(ctx, m.ID, "session.close", map[string]any{"session_id": name})
		}
		c.mu.Lock()
		c.sessionID, c.steer = "", false
		c.endCollectorLocked()
		c.mu.Unlock()
		b.reply(in, MsgNewSession)
		return
	}
	if text == "" {
		return
	}
	if !b.link.Online(m.ID) {
		b.reply(in, MsgOffline)
		return
	}

	turnID := newTurnID()
	c.mu.Lock()
	if c.collecting && c.sessionID != "" {
		// A turn is running: steer into it when the agent can, else queue.
		mode := "queue"
		if c.steer {
			mode = "steer"
		}
		sid := c.sessionID
		c.outstanding[turnID] = true
		c.mu.Unlock()
		if _, err := b.prompt(ctx, m.ID, sid, text, mode, turnID); err != nil {
			c.mu.Lock()
			delete(c.outstanding, turnID)
			c.mu.Unlock()
			b.reply(in, fmt.Sprintf(MsgFailed, err))
		}
		return
	}
	c.mu.Unlock()

	// Idle: (re)open the session, start relaying its events, prompt.
	sid, steer, err := b.openSession(ctx, m.ID, rc, name)
	if err != nil {
		if errors.Is(err, ErrOffline) {
			b.reply(in, MsgOffline)
		} else {
			b.reply(in, fmt.Sprintf(MsgStartFailed, err))
		}
		return
	}
	events, unsubscribe := b.link.Subscribe(m.ID)
	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }
	c.mu.Lock()
	c.endCollectorLocked()
	c.gen++
	gen := c.gen
	c.sessionID, c.steer = sid, steer
	c.collecting = true
	c.outstanding = map[string]bool{turnID: true}
	c.stop = stop
	c.mu.Unlock()
	go b.collect(c, gen, in, sid, events, unsubscribe, done, "", "")

	if _, err := b.prompt(ctx, m.ID, sid, text, "queue", turnID); err != nil {
		stop()
		if errors.Is(err, ErrOffline) {
			b.reply(in, MsgOffline)
		} else {
			b.reply(in, fmt.Sprintf(MsgFailed, err))
		}
	}
}

// endCollectorLocked stops the current collector, if any, and detaches
// it from the chat's state. c.mu must be held.
func (c *chat) endCollectorLocked() {
	if c.stop != nil {
		c.stop()
	}
	c.gen++
	c.stop = nil
	c.collecting = false
	c.outstanding = map[string]bool{}
	c.approval = nil
}

func newTurnID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return "fc-" + hex.EncodeToString(buf[:])
}

type sessionMeta struct {
	ID       string   `json:"id"`
	Access   string   `json:"access"`
	Features []string `json:"features"`
}

// openSession creates or resumes the chat's named session and brings its
// access level in line with the agent's config.
func (b *Bridge) openSession(ctx context.Context, machineID string, rc RuntimeConfig, name string) (string, bool, error) {
	return openSession(ctx, b.link, machineID, rc.AglAgent, rc.Cwd, rc.Access, name)
}

// openSession creates or resumes the named session on machineID with
// agent/cwd/access, re-applying access with session.configure when the
// existing session runs at another level. It returns the session id and
// whether the agent can steer a running turn.
func openSession(ctx context.Context, link Link, machineID, agent, cwd, access, name string) (string, bool, error) {
	params := map[string]any{"agent": agent, "name": name, "access": access}
	if cwd != "" {
		params["cwd"] = cwd
	}
	raw, err := link.Request(ctx, machineID, "session.create", params)
	if err != nil {
		return "", false, err
	}
	var meta sessionMeta
	if err := json.Unmarshal(raw, &meta); err != nil || meta.ID == "" {
		return "", false, errors.New("bad session.create response")
	}
	if meta.Access != "" && meta.Access != access {
		if _, err := link.Request(ctx, machineID, "session.configure", map[string]any{"session_id": meta.ID, "access": access}); err != nil {
			return "", false, err
		}
	}
	steer := false
	for _, f := range meta.Features {
		if f == "steer" {
			steer = true
		}
	}
	return meta.ID, steer, nil
}

// approve answers a pending permission request with decision
// (allow | allow_session | deny).
func approve(ctx context.Context, link Link, machineID string, p *pendingApproval, decision string) error {
	_, err := link.Request(ctx, machineID, "session.approve", map[string]any{
		"session_id": p.sessionID, "request_id": p.requestID, "decision": decision,
	})
	return err
}

// approvalPrompt is the chat line for a permission_request event.
func approvalPrompt(e AgentEvent) string {
	title := strings.TrimSpace(e.Title)
	if title == "" {
		title = e.Tool
	}
	return fmt.Sprintf(MsgApproval, clipText(title, 300))
}

func (b *Bridge) prompt(ctx context.Context, machineID, sessionID, text, mode, turnID string) (json.RawMessage, error) {
	return b.link.Request(ctx, machineID, "session.prompt", map[string]any{
		"session_id": sessionID, "text": text, "mode": mode, "turn_id": turnID,
	})
}

// collect relays one session's events to the chat until every turn it
// started has ended.
// adoptTurn/prefix resume a turn that was already running (recovered
// after a restart): its text so far and its turn_ended count as ours.
func (b *Bridge) collect(c *chat, gen uint64, in Inbound, sessionID string, events <-chan Event, unsubscribe func(), done <-chan struct{}, adoptTurn, prefix string) {
	defer unsubscribe()
	typing := time.NewTicker(typingInterval)
	defer typing.Stop()
	idle := time.NewTimer(turnIdleTimeout)
	defer idle.Stop()
	if b.out != nil {
		b.out.Typing(in.Channel, in.AccountID, in.ChatID)
	}

	var buf strings.Builder
	ours := adoptTurn != "" // the running turn is one we started
	buf.WriteString(prefix)
	finish := func() {
		c.mu.Lock()
		if c.gen == gen {
			c.collecting = false
			c.outstanding = map[string]bool{}
			c.approval = nil
			c.stop = nil
		}
		c.mu.Unlock()
	}
	for {
		select {
		case <-done:
			finish()
			return
		case <-idle.C:
			b.reply(in, MsgStalled)
			finish()
			return
		case <-typing.C:
			if b.out != nil {
				b.out.Typing(in.Channel, in.AccountID, in.ChatID)
			}
		case ev := <-events:
			if ev.Disconnected {
				if s := strings.TrimSpace(buf.String()); s != "" {
					b.replyLong(in, s)
				}
				b.reply(in, MsgDisconnected)
				finish()
				return
			}
			if ev.SessionID != sessionID {
				continue
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(turnIdleTimeout)
			e := ev.Event
			switch e.Kind {
			case "turn_started":
				c.mu.Lock()
				ours = c.gen == gen && c.outstanding[e.TurnID]
				c.mu.Unlock()
				buf.Reset()
			case "steered":
				c.mu.Lock()
				delete(c.outstanding, e.TurnID)
				c.mu.Unlock()
			case "text_delta":
				// Commentary is an interim progress note, not the answer.
				if ours && e.Phase != "commentary" && buf.Len() < maxReplyBytes {
					buf.WriteString(e.Text)
				}
			case "permission_request":
				if !ours {
					continue
				}
				c.mu.Lock()
				if c.gen == gen {
					c.approval = &pendingApproval{sessionID: sessionID, requestID: e.RequestID}
				}
				c.mu.Unlock()
				b.reply(in, approvalPrompt(e))
			case "permission_resolved":
				c.mu.Lock()
				cleared := c.approval != nil && c.approval.requestID == e.RequestID
				if cleared {
					c.approval = nil
				}
				c.mu.Unlock()
				if cleared && e.By == "timeout" {
					b.reply(in, MsgApprovalTimeout)
				}
			case "turn_ended":
				c.mu.Lock()
				if c.gen != gen {
					c.mu.Unlock()
					return
				}
				mine := c.outstanding[e.TurnID]
				delete(c.outstanding, e.TurnID)
				c.approval = nil
				left := len(c.outstanding)
				if left == 0 {
					c.collecting = false
					c.stop = nil
				}
				c.mu.Unlock()
				if mine {
					text := strings.TrimSpace(buf.String())
					switch e.Outcome {
					case "failed":
						msg := e.Error
						if msg == "" {
							msg = "unknown error"
						}
						if text != "" {
							b.replyLong(in, text)
						}
						b.reply(in, fmt.Sprintf(MsgFailed, clipText(msg, 300)))
					case "aborted":
						if text != "" {
							b.replyLong(in, text+"\n"+MsgAborted)
						}
					default:
						if text == "" {
							text = MsgEmptyReply
						}
						b.replyLong(in, text)
					}
				}
				buf.Reset()
				ours = false
				if left == 0 {
					return
				}
			}
		}
	}
}

type recoveredApproval struct {
	sessionID string
	turnID    string
	requestID string
	text      string // the turn's answer text so far
}

// recoverApproval reads the named session's history and returns its
// unanswered permission request when the last turn is still running.
func (b *Bridge) recoverApproval(ctx context.Context, machineID, name string) *recoveredApproval {
	raw, err := b.link.Request(ctx, machineID, "session.history", map[string]any{"session_id": name, "limit": 500})
	if err != nil {
		return nil
	}
	var envs []struct {
		SessionID string     `json:"session_id"`
		Event     AgentEvent `json:"event"`
	}
	if json.Unmarshal(raw, &envs) != nil {
		return nil
	}
	var (
		sessionID, turn string
		text            strings.Builder
		pending         []string
	)
	for _, e := range envs {
		if e.SessionID != "" {
			sessionID = e.SessionID
		}
		switch e.Event.Kind {
		case "turn_started":
			turn, pending = e.Event.TurnID, nil
			text.Reset()
		case "text_delta":
			if turn != "" && e.Event.Phase != "commentary" && text.Len() < maxReplyBytes {
				text.WriteString(e.Event.Text)
			}
		case "permission_request":
			if turn != "" && e.Event.RequestID != "" {
				pending = append(pending, e.Event.RequestID)
			}
		case "permission_resolved":
			for i, id := range pending {
				if id == e.Event.RequestID {
					pending = append(pending[:i], pending[i+1:]...)
					break
				}
			}
		case "turn_ended":
			turn, pending = "", nil
			text.Reset()
		}
	}
	if turn == "" || len(pending) == 0 || sessionID == "" {
		return nil
	}
	return &recoveredApproval{sessionID: sessionID, turnID: turn, requestID: pending[len(pending)-1], text: text.String()}
}

func clipText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
