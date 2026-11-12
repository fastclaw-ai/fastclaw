package agl

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

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
		{LocalMachine{Online: true, AglVersion: "0.3.14", Agents: agent}, false},
	} {
		if got := UsesBuiltinTools(c.m); got != c.want {
			t.Errorf("UsesBuiltinTools(%+v) = %v", c.m, got)
		}
	}
}

func TestBuiltinToolsListedOnlyForAgentlessAndCached(t *testing.T) {
	f := builtinFixture(t)
	ctx := context.Background()
	if got := f.tools.BuiltinTools(ctx, machineByName(t, f, "MacBook")); got != nil {
		t.Fatalf("machine with an agent offered %v", got)
	}
	mini := machineByName(t, f, "Mini")
	for i := 0; i < 2; i++ {
		got := f.tools.BuiltinTools(ctx, mini)
		if len(got) != 1 || got[0].Name != "read" {
			t.Fatalf("tools = %+v", got)
		}
	}
	if n := len(f.link.calls("tools.list")); n != 1 {
		t.Fatalf("tools.list called %d times, want 1 (cached)", n)
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
	if len(calls) != 1 || calls[0] != `{"arguments":{"limit":50,"path":"~/work/notes/todo.md"},"name":"read"}` {
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
	if len(calls) != 1 || calls[0] != `{"arguments":{"command":"ls","cwd":"~/work","wait_ms":15000},"name":"exec"}` {
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

func TestCapBuiltin(t *testing.T) {
	long := strings.Repeat("é", MaxBuiltinResultBytes)
	got := capBuiltin(long)
	if len(got) > MaxBuiltinResultBytes+64 || !strings.HasSuffix(got, "read a smaller range]") {
		t.Fatalf("capped len %d", len(got))
	}
}
