package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/agl"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// fakeLocalAgents: machines per user (already filtered to allowAgents,
// as *agl.Tools does); records runs.
type fakeLocalAgents struct {
	byUser map[string][]agl.LocalMachine
	runs   []agl.ToolRun
}

func (f *fakeLocalAgents) Machines(_ context.Context, uid string) ([]agl.LocalMachine, error) {
	return f.byUser[uid], nil
}

func (f *fakeLocalAgents) Run(_ context.Context, r agl.ToolRun) (string, error) {
	f.runs = append(f.runs, r)
	return "done", nil
}

func withLocalAgents(t *testing.T, svc tools.LocalAgentRunner) {
	prev := localAgents()
	SetLocalAgents(svc)
	t.Cleanup(func() { SetLocalAgents(prev) })
}

func hasTool(r *tools.Registry, name string) bool {
	for _, d := range r.DefinitionsForMode([]string{}) { // chatbot-style: no built-ins
		if d.Function.Name == name {
			return true
		}
	}
	return false
}

// The tools go to the CHATTER (msg.MachineUserID, stamped by the gateway)
// with an allowed machine — never the agent owner, never groups, web,
// shared sessions or runtime turns, never someone without a machine.
func TestLocalAgentToolsOfferedOnlyToChatterWithMachine(t *testing.T) {
	svc := &fakeLocalAgents{byUser: map[string][]agl.LocalMachine{
		"u_alice": {{ID: "mch_a", Name: "MacBook", Online: true, Agents: nil}},
		// The platform agent's owner has a machine too: never used.
		"app": {{ID: "mch_owner", Name: "OwnerMac", Online: true}},
	}}
	withLocalAgents(t, svc)
	dm := bus.InboundMessage{Channel: "wechat", AccountID: "bot", ChatID: "wx_alice", PeerKind: "dm", UserID: "u_chatter", MachineUserID: "u_alice"}

	cases := []struct {
		name string
		edit func(m *bus.InboundMessage)
		want bool
	}{
		{"chatter with machine", func(*bus.InboundMessage) {}, true},
		{"no machine user (not the bound sender / not 1:1)", func(m *bus.InboundMessage) { m.MachineUserID = "" }, false},
		{"other user without machines", func(m *bus.InboundMessage) { m.MachineUserID = "u_bob" }, false},
		{"group", func(m *bus.InboundMessage) { m.PeerKind = "group" }, false},
		{"shared session", func(m *bus.InboundMessage) { m.SharedIdentity = true }, false},
		{"web", func(m *bus.InboundMessage) { m.Channel = "web" }, false},
		{"cron", func(m *bus.InboundMessage) { m.Source = bus.SourceCron }, false},
	}
	for _, c := range cases {
		a := &Agent{name: "agt_persona", ownerUserID: "app", agentOwnerID: "app", registry: tools.NewRegistry("", "")}
		msg := dm
		c.edit(&msg)
		note := a.mountLocalAgents(context.Background(), msg)
		got := hasTool(a.registry, tools.RunLocalAgentToolName) && hasTool(a.registry, tools.LocalAgentsToolName)
		if got != c.want || (note != "") != c.want {
			t.Errorf("%s: tools=%v note=%q, want %v", c.name, got, note, c.want)
		}
		if c.want && (!strings.Contains(note, "MacBook") || strings.Contains(note, "OwnerMac")) {
			t.Errorf("%s: note = %q", c.name, note)
		}
	}

	// The model can't pick whose computer: the owner and chat are bound.
	a := &Agent{name: "agt_persona", ownerUserID: "app", registry: tools.NewRegistry("", "")}
	a.mountLocalAgents(context.Background(), dm)
	args, _ := json.Marshal(map[string]any{"agent": "claude", "task": "ls", "machine": "OwnerMac", "userId": "app"})
	if _, err := a.registry.Execute(context.Background(), tools.RunLocalAgentToolName, string(args)); err != nil {
		t.Fatal(err)
	}
	r := svc.runs[len(svc.runs)-1]
	if r.UserID != "u_alice" || r.Chat.ChatID != "wx_alice" || r.Chat.AgentID != "agt_persona" || r.Agent != "claude" {
		t.Fatalf("run = %+v", r)
	}

	// Off when no runner is installed (no agl hub).
	SetLocalAgents(nil)
	b := &Agent{name: "x", registry: tools.NewRegistry("", "")}
	if b.mountLocalAgents(context.Background(), dm) != "" || hasTool(b.registry, tools.RunLocalAgentToolName) {
		t.Fatal("tools mounted without a runner")
	}
}

// A dashboard chat stream marks its turn with the logged-in person: the
// tools go to them, bound to the web chat, and updates become stream
// events. Without the mark (team chat, message_agent, plain /api/chat)
// web turns get nothing.
func TestLocalAgentToolsOnWebChat(t *testing.T) {
	svc := &fakeLocalAgents{byUser: map[string][]agl.LocalMachine{
		"u_alice": {{ID: "mch_a", Name: "MacBook", Online: true}},
	}}
	withLocalAgents(t, svc)
	web := bus.InboundMessage{Channel: "web", ChatID: "sess1", PeerKind: "dm", UserID: "u_alice"}
	streamed := ContextWithStream(context.Background(), nil, nil, nil, "u_alice", "agt_persona", "sess1")

	a := &Agent{name: "agt_persona", ownerUserID: "app", registry: tools.NewRegistry("", "")}
	if a.mountLocalAgents(streamed, web) != "" {
		t.Fatal("unmarked web turn got the tools")
	}
	ctx := ContextWithLocalAgentsUser(streamed, "u_alice")
	a = &Agent{name: "agt_persona", ownerUserID: "app", registry: tools.NewRegistry("", "")}
	if note := a.mountLocalAgents(ctx, web); !strings.Contains(note, "MacBook") {
		t.Fatalf("note = %q", note)
	}
	if a.mountLocalAgents(ContextWithLocalAgentsUser(context.Background(), "u_alice"), web) != "" {
		// no stream to report approvals to
		t.Fatal("web turn without a stream got the tools")
	}
	args, _ := json.Marshal(map[string]any{"agent": "claude", "task": "ls"})
	if _, err := a.registry.Execute(ctx, tools.RunLocalAgentToolName, string(args)); err != nil {
		t.Fatal(err)
	}
	r := svc.runs[len(svc.runs)-1]
	if r.UserID != "u_alice" || r.Chat != agl.WebToolChat("u_alice", "agt_persona", "sess1") || r.Notify == nil {
		t.Fatalf("run = %+v", r)
	}
}
