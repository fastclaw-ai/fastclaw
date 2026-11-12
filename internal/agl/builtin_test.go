package agl

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// builtinFixture adds Alice's agent-less Mini (agl 0.3.14) to the tools
// fixture, next to her MacBook with Claude Code.
func builtinFixture(t *testing.T) *toolsFixture {
	f := newToolsFixture(t)
	mini := store.MachineRecord{ID: "mch_mini", UserID: "u_alice", Name: "Mini", AglMachineID: "mini", TokenHash: "hm",
		AllowAgents: true, DefaultCwd: "~/work", Version: "0.3.14"}
	if err := f.st.CreateMachine(context.Background(), &mini); err != nil {
		t.Fatal(err)
	}
	return f
}

func machineByName(t *testing.T, f *toolsFixture, name string) LocalMachine {
	ms, err := f.tools.Machines(context.Background(), "u_alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no machine %s", name)
	return LocalMachine{}
}

func TestUsesBuiltinTools(t *testing.T) {
	agent := []store.MachineAgent{{ID: "claude"}}
	for _, c := range []struct {
		m    LocalMachine
		want bool
	}{
		{LocalMachine{Online: true, AglVersion: "0.3.14"}, true},
		{LocalMachine{Online: true, AglVersion: "0.4.0"}, true},
		{LocalMachine{Online: true, AglVersion: "0.3.13"}, false},
		{LocalMachine{Online: false, AglVersion: "0.3.14"}, false},
		// A coding agent doesn't hide them: the model chooses.
		{LocalMachine{Online: true, AglVersion: "0.3.14", Agents: agent}, true},
	} {
		if got := UsesBuiltinTools(c.m); got != c.want {
			t.Errorf("UsesBuiltinTools(%+v) = %v", c.m, got)
		}
	}
}

func TestBuiltinToolsListedForNewAglAndCached(t *testing.T) {
	f := builtinFixture(t)
	ctx := context.Background()
	if got := f.tools.BuiltinTools(ctx, machineByName(t, f, "MacBook")); got != nil {
		t.Fatalf("machine with an old agl offered %v", got)
	}
	mini := machineByName(t, f, "Mini")
	for i := 0; i < 2; i++ {
		got := f.tools.BuiltinTools(ctx, mini)
		if len(got) != 1 || got[0].Name != "read" {
			t.Fatalf("tools = %+v", got)
		}
	}
	if calls := f.link.calls("tools.list"); len(calls) != 1 || calls[0] != `{"approvals":true}` {
		t.Fatalf("tools.list = %v, want one call (cached) opting in to approvals", calls)
	}
}

func TestCallBuiltinReadsWithinLinkAccess(t *testing.T) {
	f := builtinFixture(t)
	ctx := context.Background()
	out, err := f.tools.CallBuiltin(ctx, BuiltinCall{UserID: "u_alice", Name: "read",
		Args: json.RawMessage(`{"path":"notes/todo.md","machine":"Mini","limit":50}`)})
	if err != nil || out != `{"ok":true}` {
		t.Fatalf("read = %q, %v", out, err)
	}
	calls := f.link.calls("tools.call")
	if len(calls) != 1 || calls[0] != `{"approvals":true,"arguments":{"limit":50,"path":"~/work/notes/todo.md"},"name":"read"}` {
		t.Fatalf("tools.call = %v", calls)
	}
	// The link offers only read: exec is refused before reaching agl.
	_, err = f.tools.CallBuiltin(ctx, BuiltinCall{UserID: "u_alice", Name: "exec", Machine: "Mini", Args: json.RawMessage(`{"command":"ls"}`)})
	var te *ToolError
	if !errors.As(err, &te) || !strings.Contains(te.Msg, "allows: read") {
		t.Fatalf("exec err = %v", err)
	}
	if len(f.link.calls("tools.call")) != 1 {
		t.Fatal("refused exec reached the computer")
	}
}

func TestCallBuiltinExecDefaults(t *testing.T) {
	f := builtinFixture(t)
	f.link.toolsList = `[{"name":"exec","description":"Run.","input_schema":{"type":"object","properties":{}}}]`
	_, err := f.tools.CallBuiltin(context.Background(), BuiltinCall{UserID: "u_alice", Name: "exec", Args: json.RawMessage(`{"command":"ls"}`)})
	if err != nil {
		t.Fatal(err)
	}
	calls := f.link.calls("tools.call")
	if len(calls) != 1 || calls[0] != `{"approvals":true,"arguments":{"command":"ls","cwd":"~/work","wait_ms":15000},"name":"exec"}` {
		t.Fatalf("tools.call = %v", calls)
	}
}

func TestCallBuiltinRefusals(t *testing.T) {
	f := builtinFixture(t)
	ctx := context.Background()
	for _, c := range []struct {
		call BuiltinCall
		why  string
	}{
		{BuiltinCall{Name: "read", Args: json.RawMessage(`{"path":"~/.ssh/id_rsa"}`)}, "Hidden"},
		{BuiltinCall{Name: "read", Args: json.RawMessage(`{"path":"/repo/.env"}`)}, "Hidden"},
		{BuiltinCall{Name: "read", Machine: "MacBook", Args: json.RawMessage(`{"path":"~/a.txt"}`)}, "run_local_agent"},
		{BuiltinCall{Name: "read", Machine: "BobMac", Args: json.RawMessage(`{"path":"~/a.txt"}`)}, "No computer"},
		{BuiltinCall{Name: "rm", Args: json.RawMessage(`{}`)}, "Unknown"},
	} {
		c.call.UserID = "u_alice"
		_, err := f.tools.CallBuiltin(ctx, c.call)
		var te *ToolError
		if !errors.As(err, &te) || !strings.Contains(te.Msg, c.why) {
			t.Errorf("%+v: err = %v, want %q", c.call, err, c.why)
		}
	}
	if n := len(f.link.calls("tools.call")); n != 0 {
		t.Fatalf("refused calls reached the computer: %d", n)
	}
}

const heldExec = `{"status":"approval_required","approval_id":"ap1","tool":"exec","title":"Run: rm old.dmg","options":["allow","allow_session","deny"]}`

func execOnMini(f *toolsFixture) {
	f.link.toolsList = `[{"name":"exec","description":"Run.","input_schema":{"type":"object","properties":{}}}]`
	f.link.toolsCall = heldExec
}

func (f *toolsFixture) callBuiltin(c BuiltinCall) (chan string, chan error) {
	res, errc := make(chan string, 1), make(chan error, 1)
	c.UserID, c.Name = "u_alice", "exec"
	if c.Chat == (ToolChat{}) {
		c.Chat = f.chat
	}
	go func() {
		out, err := f.tools.CallBuiltin(context.Background(), c)
		res <- out
		errc <- err
	}()
	return res, errc
}

func TestCallBuiltinHeldCallAskedInChat(t *testing.T) {
	f := builtinFixture(t)
	execOnMini(f)
	res, errc := f.callBuiltin(BuiltinCall{Args: json.RawMessage(`{"command":"rm old.dmg"}`)})
	f.out.waitFor(t, "Mini · Run: rm old.dmg")
	if p, ok := f.tools.PendingApproval(f.chat, "u_alice"); !ok || p.Agent != BuiltinAgentName || p.Machine != "Mini" {
		t.Fatalf("pending = %+v, %v", p, ok)
	}
	// Bob can't answer Alice's request.
	if f.tools.InterceptApproval(context.Background(), f.chat, "u_bob", "1") {
		t.Fatal("another user answered")
	}
	if !f.tools.InterceptApproval(context.Background(), f.chat, "u_alice", "2") {
		t.Fatal("reply not taken")
	}
	if out, err := <-res, <-errc; err != nil || out != `{"ok":"approved"}` {
		t.Fatalf("result = %q, %v", out, err)
	}
	approves := f.link.calls("tools.approve")
	if len(approves) != 1 || approves[0] != `{"approval_id":"ap1","decision":"allow_session"}` {
		t.Fatalf("tools.approve = %v", approves)
	}
	calls := f.link.calls("tools.call")
	if len(calls) != 1 || !strings.Contains(calls[0], `"session":"chat:`) {
		t.Fatalf("tools.call = %v", calls)
	}
	f.out.waitFor(t, approvalAck["allow_session"])
	if f.tools.HasPendingApproval(f.chat) {
		t.Fatal("approval left pending")
	}
}

func TestCallBuiltinHeldCallDenied(t *testing.T) {
	f := builtinFixture(t)
	execOnMini(f)
	_, errc := f.callBuiltin(BuiltinCall{Args: json.RawMessage(`{"command":"rm old.dmg"}`)})
	f.out.waitFor(t, "Run: rm old.dmg")
	f.tools.InterceptApproval(context.Background(), f.chat, "u_alice", "3")
	var te *ToolError
	if err := <-errc; !errors.As(err, &te) || !strings.Contains(te.Msg, "denied") {
		t.Fatalf("err = %v", err)
	}
	waitFor(t, "deny sent to agl", func() bool {
		a := f.link.calls("tools.approve")
		return len(a) == 1 && a[0] == `{"approval_id":"ap1","decision":"deny"}`
	})
}

func TestCallBuiltinHeldCallTimesOut(t *testing.T) {
	f := builtinFixture(t)
	execOnMini(f)
	old := builtinApprovalWait
	builtinApprovalWait = 50 * time.Millisecond
	defer func() { builtinApprovalWait = old }()
	_, errc := f.callBuiltin(BuiltinCall{Args: json.RawMessage(`{"command":"rm old.dmg"}`)})
	var te *ToolError
	if err := <-errc; !errors.As(err, &te) || !strings.Contains(te.Msg, "in time") {
		t.Fatalf("err = %v", err)
	}
	f.out.waitFor(t, MsgApprovalTimeout)
	if f.tools.HasPendingApproval(f.chat) {
		t.Fatal("approval left pending")
	}
}

func TestCallBuiltinHeldCallWebCard(t *testing.T) {
	f := builtinFixture(t)
	execOnMini(f)
	var mu sync.Mutex
	var notices []ToolNotice
	chat := WebToolChat("u_alice", "agt_persona", "sess1")
	res, errc := f.callBuiltin(BuiltinCall{Chat: chat, Args: json.RawMessage(`{"command":"rm old.dmg"}`), Notify: func(n ToolNotice) {
		mu.Lock()
		notices = append(notices, n)
		mu.Unlock()
	}})
	waitFor(t, "approval card", func() bool { return f.tools.HasPendingApproval(chat) })
	if found, err := f.tools.Decide(context.Background(), chat, "u_alice", "allow"); !found || err != nil {
		t.Fatalf("decide = %v, %v", found, err)
	}
	if out, err := <-res, <-errc; err != nil || out != `{"ok":"approved"}` {
		t.Fatalf("result = %q, %v", out, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notices) != 2 || notices[0].Kind != NoticeApproval || notices[1].Decision != "allow" {
		t.Fatalf("notices = %+v", notices)
	}
	if len(f.out.sent) != 0 {
		t.Fatalf("web approval went to IM: %v", f.out.sent)
	}
}

func TestCallBuiltinHeldCallWithoutChatIsDenied(t *testing.T) {
	f := builtinFixture(t)
	execOnMini(f)
	_, err := f.tools.CallBuiltin(context.Background(), BuiltinCall{UserID: "u_alice", Name: "exec", Args: json.RawMessage(`{"command":"rm x"}`)})
	var te *ToolError
	if !errors.As(err, &te) || !strings.Contains(te.Msg, "no chat") {
		t.Fatalf("err = %v", err)
	}
}

func TestCapBuiltin(t *testing.T) {
	long := strings.Repeat("é", MaxBuiltinResultBytes)
	got := capBuiltin(long)
	if len(got) > MaxBuiltinResultBytes+64 || !strings.HasSuffix(got, "read a smaller range]") {
		t.Fatalf("capped len %d", len(got))
	}
}
