package agl

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func TestApprovalDecision(t *testing.T) {
	for in, want := range map[string]string{"1": "allow", " 2 ": "allow_session", "3": "deny", "１": "allow"} {
		if got, ok := ApprovalDecision(in); !ok || got != want {
			t.Errorf("%q → %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "4", "yes", "12", "1."} {
		if got, ok := ApprovalDecision(in); ok {
			t.Errorf("%q → %q, want no decision", in, got)
		}
	}
}

func TestSplitReply(t *testing.T) {
	if got := SplitReply("  hi  ", 10); len(got) != 1 || got[0] != "hi" {
		t.Fatalf("short: %q", got)
	}
	long := strings.Repeat("一二三四五\n", 10) // 60 runes
	parts := SplitReply(long, 20)
	for _, p := range parts {
		if n := len([]rune(p)); n > 20 {
			t.Fatalf("part of %d runes: %q", n, p)
		}
	}
	if strings.Join(parts, "\n") != strings.TrimSpace(long) {
		t.Fatalf("parts lost text: %q", parts)
	}
	huge := strings.Repeat("x", 30*maxReplyChunks)
	parts = SplitReply(huge, 10)
	if len(parts) != maxReplyChunks || !strings.Contains(parts[len(parts)-1], MsgTruncated) {
		t.Fatalf("cap: %d parts", len(parts))
	}
}

// fakeLink is a machine that records requests and lets the test push
// events.
type fakeLink struct {
	mu       sync.Mutex
	online   bool
	requests []string // method + " " + params JSON
	events   chan Event
	history  string // session.history result (JSON), when set
}

func newFakeLink() *fakeLink { return &fakeLink{online: true, events: make(chan Event, 64)} }

func (f *fakeLink) Online(string) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.online }

func (f *fakeLink) Request(_ context.Context, _ string, method string, params any) (json.RawMessage, error) {
	blob, _ := json.Marshal(params)
	f.mu.Lock()
	f.requests = append(f.requests, method+" "+string(blob))
	f.mu.Unlock()
	switch method {
	case "session.create":
		return json.RawMessage(`{"id":"s1","access":"ask","features":["steer"]}`), nil
	case "session.prompt":
		return json.RawMessage(`{"session_id":"s1"}`), nil
	case "fs.read":
		return json.RawMessage(`{"content_base64":"aGk=","size":2}`), nil
	case "session.history":
		f.mu.Lock()
		h := f.history
		f.mu.Unlock()
		if h != "" {
			return json.RawMessage(h), nil
		}
	}
	return json.RawMessage(`{}`), nil
}

func (f *fakeLink) Subscribe(string) (<-chan Event, func()) { return f.events, func() {} }

func (f *fakeLink) calls(method string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		if strings.HasPrefix(r, method+" ") {
			out = append(out, strings.TrimPrefix(r, method+" "))
		}
	}
	return out
}

type fakeSender struct {
	mu   sync.Mutex
	sent []string
}

func (s *fakeSender) Send(_ context.Context, out bus.OutboundMessage) {
	s.mu.Lock()
	s.sent = append(s.sent, out.Text)
	s.mu.Unlock()
}
func (s *fakeSender) Typing(string, string, string) {}

func (s *fakeSender) reset() { s.mu.Lock(); s.sent = nil; s.mu.Unlock() }

func (s *fakeSender) waitFor(t *testing.T, text string) {
	t.Helper()
	waitFor(t, "reply "+text, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, m := range s.sent {
			if strings.Contains(m, text) {
				return true
			}
		}
		return false
	})
}

type bridgeFixture struct {
	link   *fakeLink
	out    *fakeSender
	bridge *Bridge
	agent  *store.AgentRecord
	st     *store.DBStore
}

func newBridgeFixture(t *testing.T) *bridgeFixture {
	st := newTestStore(t)
	ctx := context.Background()
	for _, m := range []store.MachineRecord{
		{ID: "mch_alice", UserID: "u_alice", AglMachineID: "a", TokenHash: "h1"},
		{ID: "mch_bob", UserID: "u_bob", AglMachineID: "b", TokenHash: "h2"},
	} {
		m := m
		if err := st.CreateMachine(ctx, &m); err != nil {
			t.Fatal(err)
		}
	}
	f := &bridgeFixture{link: newFakeLink(), out: &fakeSender{}, st: st}
	f.bridge = NewBridge(f.link, st, f.out)
	f.agent = &store.AgentRecord{ID: "agt_1", UserID: "u_alice", Config: map[string]interface{}{
		"runtime": Runtime, "runtimeConfig": RuntimeConfig{MachineID: "mch_alice", AglAgent: "claude", Access: AccessAsk}.Map(),
	}}
	return f
}

func (f *bridgeFixture) msg(text string) Inbound {
	return Inbound{Agent: f.agent, SpaceUserID: "u_alice", Channel: "wechat", AccountID: "bot1", ChatID: "wx_alice",
		Text: text, PeerKind: "dm", SenderAllowed: true}
}

func TestBridgeTurnAndApprovalReply(t *testing.T) {
	f := newBridgeFixture(t)
	f.bridge.Handle(f.msg("fix the tests"))
	waitFor(t, "prompt", func() bool { return len(f.link.calls("session.prompt")) == 1 })
	create := f.link.calls("session.create")
	if len(create) != 1 || !strings.Contains(create[0], `"name":"fc:agt_1:wx_alice"`) || !strings.Contains(create[0], `"access":"ask"`) {
		t.Fatalf("session.create = %v", create)
	}
	var prompt struct {
		TurnID     string `json:"turn_id"`
		Text, Mode string
	}
	_ = json.Unmarshal([]byte(f.link.calls("session.prompt")[0]), &prompt)
	if prompt.Text != "fix the tests" || prompt.Mode != "queue" {
		t.Fatalf("prompt = %+v", prompt)
	}

	ev := func(e AgentEvent) { f.link.events <- Event{SessionID: "s1", Event: e} }
	ev(AgentEvent{Kind: "turn_started", TurnID: prompt.TurnID})
	ev(AgentEvent{Kind: "permission_request", RequestID: "r1", Tool: "Bash", Title: "Run go test ./..."})
	f.out.waitFor(t, "需要确认：Run go test ./...\n回复 1 允许 / 2 本会话都允许 / 3 拒绝")

	// "2" answers the request — not a new prompt.
	f.bridge.Handle(f.msg("2"))
	waitFor(t, "approve", func() bool { return len(f.link.calls("session.approve")) == 1 })
	if got := f.link.calls("session.approve")[0]; !strings.Contains(got, `"decision":"allow_session"`) || !strings.Contains(got, `"request_id":"r1"`) {
		t.Fatalf("approve = %s", got)
	}
	if n := len(f.link.calls("session.prompt")); n != 1 {
		t.Fatalf("approval reply became a prompt (%d prompts)", n)
	}
	// "2" covers the session: the next request is answered without asking.
	ev(AgentEvent{Kind: "permission_resolved", RequestID: "r1", By: "client"})
	f.out.reset()
	ev(AgentEvent{Kind: "permission_request", RequestID: "r2", Tool: "Bash", Title: "Run go vet"})
	waitFor(t, "auto approve", func() bool { return len(f.link.calls("session.approve")) == 2 })
	if got := f.link.calls("session.approve")[1]; !strings.Contains(got, `"request_id":"r2"`) || !strings.Contains(got, `"decision":"allow_session"`) {
		t.Fatalf("auto approve = %s", got)
	}

	ev(AgentEvent{Kind: "text_delta", Text: "planning…", Phase: "commentary"})
	ev(AgentEvent{Kind: "text_delta", Text: "All tests "})
	ev(AgentEvent{Kind: "text_delta", Text: "pass."})
	ev(AgentEvent{Kind: "turn_ended", TurnID: prompt.TurnID, Outcome: "completed"})
	f.out.waitFor(t, "All tests pass.")
	f.out.mu.Lock()
	for _, m := range f.out.sent {
		if strings.Contains(m, "planning") {
			t.Errorf("commentary leaked into the reply: %q", m)
		}
	}
	f.out.mu.Unlock()

	// Once the turn ended, "1" is an ordinary prompt again.
	f.bridge.Handle(f.msg("1"))
	waitFor(t, "second prompt", func() bool { return len(f.link.calls("session.prompt")) == 2 })
}

// After a Fastclaw restart the bridge has no approval in memory: a 1/2/3
// reply recovers the request from agl's history, answers it, and relays
// the rest of the interrupted turn.
func TestBridgeRecoversApprovalAfterRestart(t *testing.T) {
	f := newBridgeFixture(t)
	f.link.history = `[
	  {"session_id":"s1","seq":1,"event":{"kind":"turn_started","turn_id":"t9"}},
	  {"session_id":"s1","seq":2,"event":{"kind":"text_delta","text":"Checked. "}},
	  {"session_id":"s1","seq":3,"event":{"kind":"permission_request","request_id":"r9","tool":"Bash","title":"rm tmp"}}
	]`
	f.bridge.Handle(f.msg("1"))
	waitFor(t, "approve", func() bool { return len(f.link.calls("session.approve")) == 1 })
	if got := f.link.calls("session.approve")[0]; !strings.Contains(got, `"request_id":"r9"`) || !strings.Contains(got, `"session_id":"s1"`) || !strings.Contains(got, `"decision":"allow"`) {
		t.Fatalf("approve = %s", got)
	}
	if n := len(f.link.calls("session.prompt")); n != 0 {
		t.Fatalf("recovered approval became a prompt (%d prompts)", n)
	}
	f.out.waitFor(t, approvalAck["allow"])
	f.link.events <- Event{SessionID: "s1", Event: AgentEvent{Kind: "text_delta", Text: "Removed."}}
	f.link.events <- Event{SessionID: "s1", Event: AgentEvent{Kind: "turn_ended", TurnID: "t9", Outcome: "completed"}}
	f.out.waitFor(t, "Checked. Removed.")

	// A finished turn leaves nothing to recover: "1" is a prompt.
	f.link.mu.Lock()
	f.link.history = `[{"session_id":"s1","event":{"kind":"turn_started","turn_id":"t9"}},{"session_id":"s1","event":{"kind":"turn_ended","turn_id":"t9","outcome":"completed"}}]`
	f.link.mu.Unlock()
	f.bridge.Handle(f.msg("1"))
	waitFor(t, "prompt", func() bool { return len(f.link.calls("session.prompt")) == 1 })
}

func TestBridgeSteersWhileRunning(t *testing.T) {
	f := newBridgeFixture(t)
	f.bridge.Handle(f.msg("first"))
	waitFor(t, "prompt", func() bool { return len(f.link.calls("session.prompt")) == 1 })
	f.bridge.Handle(f.msg("also this"))
	waitFor(t, "steer", func() bool { return len(f.link.calls("session.prompt")) == 2 })
	if got := f.link.calls("session.prompt")[1]; !strings.Contains(got, `"mode":"steer"`) {
		t.Fatalf("second prompt = %s", got)
	}
	f.bridge.Handle(f.msg("/stop"))
	waitFor(t, "abort", func() bool { return len(f.link.calls("session.abort")) == 1 })
	f.out.waitFor(t, MsgStopped)
}

func TestBridgeRefusals(t *testing.T) {
	f := newBridgeFixture(t)
	ctx := context.Background()

	// Machine belongs to someone else.
	f.agent.Config["runtimeConfig"] = RuntimeConfig{MachineID: "mch_bob", AglAgent: "claude"}.Map()
	f.bridge.Handle(f.msg("hi"))
	f.out.waitFor(t, MsgNoMachine)

	// Agent runs in a user space that doesn't own it.
	f.agent.Config["runtimeConfig"] = RuntimeConfig{MachineID: "mch_alice", AglAgent: "claude"}.Map()
	in := f.msg("hi")
	in.SpaceUserID = "u_mallory"
	f.out.reset()
	f.bridge.Handle(in)
	f.out.waitFor(t, MsgNoMachine)

	// Another WeChat sender than the scanner.
	in = f.msg("hi")
	in.SenderAllowed = false
	f.bridge.Handle(in)
	f.out.waitFor(t, MsgNotOwner)

	// Groups.
	in = f.msg("hi")
	in.PeerKind = "group"
	f.bridge.Handle(in)
	f.out.waitFor(t, MsgDMOnly)

	// Offline machine.
	f.link.mu.Lock()
	f.link.online = false
	f.link.mu.Unlock()
	f.bridge.Handle(f.msg("hi"))
	f.out.waitFor(t, "不在线")

	// Revoked machine.
	if err := f.st.RevokeMachine(ctx, "mch_alice", time.Now()); err != nil {
		t.Fatal(err)
	}
	f.out.reset()
	f.bridge.Handle(f.msg("hi"))
	f.out.waitFor(t, MsgNoMachine)

	if n := len(f.link.calls("session.prompt")); n != 0 {
		t.Fatalf("a refused message reached the machine (%d prompts)", n)
	}
}
