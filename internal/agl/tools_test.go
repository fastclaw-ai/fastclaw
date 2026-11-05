package agl

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

type toolsFixture struct {
	link  *fakeLink
	out   *fakeSender
	tools *Tools
	st    *store.DBStore
	chat  ToolChat
}

func newToolsFixture(t *testing.T) *toolsFixture {
	st := newTestStore(t)
	ctx := context.Background()
	claude := []store.MachineAgent{{ID: "claude", Name: "Claude Code", Available: true}, {ID: "codex", Name: "Codex", Available: false}}
	for _, m := range []store.MachineRecord{
		{ID: "mch_a1", UserID: "u_alice", Name: "MacBook", AglMachineID: "a1", TokenHash: "h1", AllowAgents: true, DefaultAccess: AccessEdit, DefaultCwd: "~/code", Agents: claude},
		{ID: "mch_a2", UserID: "u_alice", Name: "Desktop", AglMachineID: "a2", TokenHash: "h2", AllowAgents: false, Agents: claude},
		{ID: "mch_a3", UserID: "u_alice", Name: "Old", AglMachineID: "a3", TokenHash: "h3", AllowAgents: true, Agents: claude},
		{ID: "mch_b", UserID: "u_bob", Name: "BobMac", AglMachineID: "b", TokenHash: "h4", AllowAgents: true, Agents: claude},
	} {
		m := m
		if err := st.CreateMachine(ctx, &m); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RevokeMachine(ctx, "mch_a3", time.Now()); err != nil {
		t.Fatal(err)
	}
	f := &toolsFixture{link: newFakeLink(), out: &fakeSender{}, st: st,
		chat: ToolChat{Channel: "wechat", AccountID: "bot1", ChatID: "wx_alice", AgentID: "agt_persona"}}
	f.tools = NewTools(f.link, st, f.out)
	return f
}

func (f *toolsFixture) run(r ToolRun) (chan string, chan error) {
	res, errc := make(chan string, 1), make(chan error, 1)
	if r.UserID == "" {
		r.UserID = "u_alice"
	}
	if r.Chat == (ToolChat{}) {
		r.Chat = f.chat
	}
	go func() {
		out, err := f.tools.Run(context.Background(), r)
		res <- out
		errc <- err
	}()
	return res, errc
}

func (f *toolsFixture) promptTurn(t *testing.T) string {
	t.Helper()
	waitFor(t, "prompt", func() bool { return len(f.link.calls("session.prompt")) == 1 })
	var p struct {
		TurnID string `json:"turn_id"`
	}
	_ = json.Unmarshal([]byte(f.link.calls("session.prompt")[0]), &p)
	return p.TurnID
}

func (f *toolsFixture) ev(e AgentEvent) { f.link.events <- Event{SessionID: "s1", Event: e} }

func TestToolsMachinesOnlyAllowedOwnActive(t *testing.T) {
	f := newToolsFixture(t)
	ms, err := f.tools.Machines(context.Background(), "u_alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ID != "mch_a1" || ms[0].DefaultAccess != AccessEdit || ms[0].DefaultCwd != "~/code" {
		t.Fatalf("alice's machines = %+v", ms)
	}
	if len(ms[0].Agents) != 1 || ms[0].Agents[0].ID != "claude" {
		t.Fatalf("only available agents expected: %+v", ms[0].Agents)
	}
	if ms, _ := f.tools.Machines(context.Background(), "u_carol"); len(ms) != 0 {
		t.Fatalf("carol has no machines: %+v", ms)
	}
}

func TestToolsRunHappyPathWithApproval(t *testing.T) {
	f := newToolsFixture(t)
	f.tools.NoticeAfter = 50 * time.Millisecond
	res, errc := f.run(ToolRun{Agent: "Claude", Task: "run the tests"})
	turn := f.promptTurn(t)

	create := f.link.calls("session.create")
	want := `"name":"` + ToolSessionName("mch_a1", "claude", f.chat) + `"`
	if len(create) != 1 || !strings.Contains(create[0], want) || !strings.Contains(create[0], `"access":"edit"`) ||
		!strings.Contains(create[0], `"cwd":"~/code"`) || !strings.Contains(create[0], `"agent":"claude"`) {
		t.Fatalf("session.create = %v", create)
	}
	if !strings.HasPrefix(ToolSessionName("mch_a1", "claude", f.chat), "fc:tool:mch_a1:claude:") {
		t.Fatal("session name shape")
	}

	f.ev(AgentEvent{Kind: "turn_started", TurnID: turn})
	f.ev(AgentEvent{Kind: "permission_request", RequestID: "r1", Tool: "Bash", Title: "Run go test"})
	f.out.waitFor(t, "需要确认：Run go test\n回复 1 允许 / 2 本会话都允许 / 3 拒绝")
	if !f.tools.HasPendingApproval(f.chat) {
		t.Fatal("approval not pending")
	}
	// Another user's space can't answer it; a non-decision isn't taken.
	if f.tools.InterceptApproval(context.Background(), f.chat, "u_bob", "1") {
		t.Fatal("bob answered alice's approval")
	}
	if f.tools.InterceptApproval(context.Background(), f.chat, "u_alice", "ok") {
		t.Fatal("non-decision intercepted")
	}
	if !f.tools.InterceptApproval(context.Background(), f.chat, "u_alice", "1") {
		t.Fatal("decision not intercepted")
	}
	if got := f.link.calls("session.approve"); len(got) != 1 || !strings.Contains(got[0], `"decision":"allow"`) || !strings.Contains(got[0], `"request_id":"r1"`) {
		t.Fatalf("approve = %v", got)
	}
	f.out.waitFor(t, "已允许。")
	// A second 1 has nothing to answer.
	if f.tools.InterceptApproval(context.Background(), f.chat, "u_alice", "1") {
		t.Fatal("answered twice")
	}
	f.out.waitFor(t, "Claude Code 正在你的电脑上处理…")

	f.ev(AgentEvent{Kind: "text_delta", Text: "thinking", Phase: "commentary"})
	f.ev(AgentEvent{Kind: "text_delta", Text: "All "})
	f.ev(AgentEvent{Kind: "text_delta", Text: "green."})
	f.ev(AgentEvent{Kind: "turn_ended", TurnID: turn, Outcome: "completed"})
	out, err := <-res, <-errc
	if err != nil || !strings.Contains(out, "All green.") || strings.Contains(out, "thinking") || !strings.Contains(out, "Claude Code on MacBook") {
		t.Fatalf("result = %q, %v", out, err)
	}
	if f.tools.HasPendingApproval(f.chat) {
		t.Fatal("approval left pending")
	}
}

func TestToolsRunOffline(t *testing.T) {
	f := newToolsFixture(t)
	f.link.online = false
	res, errc := f.run(ToolRun{Agent: "claude", Task: "x"})
	out, err := <-res, <-errc
	var te *ToolError
	if !errors.As(err, &te) || !strings.Contains(te.Msg, "offline") || out != "" {
		t.Fatalf("offline: %q %v", out, err)
	}
	if len(f.link.calls("session.create")) != 0 {
		t.Fatal("offline machine got a session")
	}
}

func TestToolsRunRefusals(t *testing.T) {
	f := newToolsFixture(t)
	for _, c := range []struct {
		r    ToolRun
		want string
	}{
		{ToolRun{Agent: "codex", Task: "x"}, "No linked computer has"},          // unavailable agent
		{ToolRun{Agent: "claude", Task: "x", Machine: "Desktop"}, "No computer"}, // allowAgents=false
		{ToolRun{Agent: "claude", Task: "x", Machine: "BobMac"}, "No computer"},  // someone else's
		{ToolRun{Agent: "claude", Task: "x", UserID: "u_carol"}, "no computer linked"},
		{ToolRun{Agent: "claude"}, "required"},
	} {
		res, errc := f.run(c.r)
		<-res
		err := <-errc
		var te *ToolError
		if !errors.As(err, &te) || !strings.Contains(te.Msg, c.want) {
			t.Errorf("%+v: %v, want %q", c.r, err, c.want)
		}
	}
	if len(f.link.calls("session.create")) != 0 {
		t.Fatal("a refused run reached the machine")
	}
}

func TestToolsRunTimeoutAborts(t *testing.T) {
	f := newToolsFixture(t)
	f.tools.Timeout = 150 * time.Millisecond
	res, errc := f.run(ToolRun{Agent: "claude", Task: "x"})
	turn := f.promptTurn(t)
	f.ev(AgentEvent{Kind: "turn_started", TurnID: turn})
	f.ev(AgentEvent{Kind: "text_delta", Text: "half"})
	out, err := <-res, <-errc
	var te *ToolError
	if !errors.As(err, &te) || !strings.Contains(te.Msg, "did not finish") || !strings.Contains(out, "half") {
		t.Fatalf("timeout: %q %v", out, err)
	}
	if len(f.link.calls("session.abort")) != 1 {
		t.Fatal("timed-out turn not aborted")
	}
}

func TestToolsRunCancelAndFailure(t *testing.T) {
	f := newToolsFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := f.tools.Run(ctx, ToolRun{UserID: "u_alice", Chat: f.chat, Agent: "claude", Task: "x"})
		errc <- err
	}()
	f.promptTurn(t)
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if len(f.link.calls("session.abort")) != 1 {
		t.Fatal("cancelled turn not aborted")
	}

	g := newToolsFixture(t)
	res, errc2 := g.run(ToolRun{Agent: "claude", Task: "x"})
	turn := g.promptTurn(t)
	g.ev(AgentEvent{Kind: "turn_started", TurnID: turn})
	g.ev(AgentEvent{Kind: "turn_ended", TurnID: turn, Outcome: "failed", Error: "rate limited"})
	<-res
	var te *ToolError
	if err := <-errc2; !errors.As(err, &te) || !strings.Contains(te.Msg, "rate limited") {
		t.Fatalf("failed outcome: %v", err)
	}
}

func TestCapResult(t *testing.T) {
	s := strings.Repeat("界", MaxToolResultBytes)
	got := capResult(s)
	if len(got) > MaxToolResultBytes+100 || !strings.Contains(got, "output truncated") || !strings.HasPrefix(got, "界") {
		t.Fatalf("cap: %d bytes", len(got))
	}
	if capResult("short") != "short" {
		t.Fatal("short text changed")
	}
}
