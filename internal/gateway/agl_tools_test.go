package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agl"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/taskqueue"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// toolLink is an always-online machine that records requests.
type toolLink struct {
	mu       sync.Mutex
	requests []string
	events   chan agl.Event
}

func (l *toolLink) Online(string) bool { return true }
func (l *toolLink) Request(_ context.Context, _ string, method string, params any) (json.RawMessage, error) {
	blob, _ := json.Marshal(params)
	l.mu.Lock()
	l.requests = append(l.requests, method+" "+string(blob))
	l.mu.Unlock()
	if method == "session.create" {
		return json.RawMessage(`{"id":"s1","access":"ask"}`), nil
	}
	return json.RawMessage(`{}`), nil
}
func (l *toolLink) Subscribe(string) (<-chan agl.Event, func()) { return l.events, func() {} }
func (l *toolLink) calls(method string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, r := range l.requests {
		if strings.HasPrefix(r, method+" ") {
			out = append(out, strings.TrimPrefix(r, method+" "))
		}
	}
	return out
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func saveChannel(t *testing.T, st *store.DBStore, ch store.ChannelRecord) {
	t.Helper()
	ctx := context.Background()
	bound := ch.BoundUserID
	if err := st.SaveChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	if bound != "" {
		saved, err := st.LookupChannel(ctx, ch.Type, ch.AccountID)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetChannelBinding(ctx, saved.ID, bound, ""); err != nil {
			t.Fatal(err)
		}
	}
}

// Whose computers a DM's LLM turn may use: the user space the channel
// belongs to, only in 1:1 messages typed by its bound sender.
func TestMachineUserFor(t *testing.T) {
	st := newOnDemandStore(t)
	saveChannel(t, st, store.ChannelRecord{UserID: "u_alice", AgentID: "agt_plat", Type: "wechat", AccountID: "bot-a", Enabled: true, BoundUserID: "wx_alice"})
	saveChannel(t, st, store.ChannelRecord{UserID: "u_carol", AgentID: "agt_plat", Type: "telegram", AccountID: "bot-c", Enabled: true})
	// A channel with no user: its owner is resolved from the agent's
	// owner — never someone whose computer a stranger may drive.
	saveChannel(t, st, store.ChannelRecord{AgentID: "agt_plat", Type: "telegram", AccountID: "bot-sys", Enabled: true})
	g := &Gateway{store: st, aglTools: agl.NewTools(&toolLink{}, st, nil)}
	ctx := context.Background()
	base := bus.InboundMessage{Channel: "wechat", AccountID: "bot-a", ChatID: "wx_alice", UserID: "u_chatter",
		OwnerUserID: "u_alice", PeerKind: "dm", FromChannelOwner: true, Text: "hi"}
	for _, c := range []struct {
		name string
		edit func(*bus.InboundMessage)
		want string
	}{
		{"bound sender in 1:1", func(*bus.InboundMessage) {}, "u_alice"},
		{"someone else messaging the bot", func(m *bus.InboundMessage) { m.FromChannelOwner = false }, ""},
		{"group", func(m *bus.InboundMessage) { m.PeerKind = "group" }, ""},
		{"shared identity", func(m *bus.InboundMessage) { m.SharedIdentity = true }, ""},
		{"cron", func(m *bus.InboundMessage) { m.Source = bus.SourceCron }, ""},
		{"web", func(m *bus.InboundMessage) { m.Channel = "web" }, ""},
		{"routed to another space", func(m *bus.InboundMessage) { m.OwnerUserID = "app" }, ""},
		{"channel without a bound sender", func(m *bus.InboundMessage) {
			m.Channel, m.AccountID, m.OwnerUserID, m.FromChannelOwner = "telegram", "bot-c", "u_carol", false
		}, "u_carol"},
		{"system channel owned through its agent", func(m *bus.InboundMessage) {
			m.Channel, m.AccountID, m.OwnerUserID = "telegram", "bot-sys", "app"
		}, ""},
	} {
		msg := base
		c.edit(&msg)
		if got := g.machineUserFor(ctx, msg); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// No agl hub → never.
	if got := (&Gateway{store: st}).machineUserFor(ctx, base); got != "" {
		t.Fatalf("without agl: %q", got)
	}
}

// A 1/2/3 while the chat's LLM turn is blocked in run_local_agent goes
// to session.approve and is acknowledged; it never reaches the agent
// (neither a new queued turn nor the running one). Other text still
// routes normally.
func TestToolApprovalInterceptedWhileTurnRuns(t *testing.T) {
	st := newOnDemandStore(t)
	ctx := context.Background()
	mustUser(t, st, "u_alice", users.RoleUser, "")
	mustAgent(t, st, "agt_only", "u_alice")
	saveChannel(t, st, store.ChannelRecord{UserID: "u_alice", AgentID: "agt_only", Type: "wechat", AccountID: "bot-a", Enabled: true, BoundUserID: "wx_alice"})
	if err := st.CreateMachine(ctx, &store.MachineRecord{ID: "mch_a", UserID: "u_alice", Name: "MacBook", TokenHash: "h",
		AllowAgents: true, DefaultAccess: agl.AccessAsk,
		Agents: []store.MachineAgent{{ID: "claude", Name: "Claude Code", Available: true}}}); err != nil {
		t.Fatal(err)
	}

	mb := bus.New()
	link := &toolLink{events: make(chan agl.Event, 16)}
	g := &Gateway{store: st, bus: mb, users: newUserSpaceRegistry(mb, st, nil, nil, nil, nil, nil)}
	g.aglTools = agl.NewTools(link, st, aglSender{g: g})

	// The "LLM turn": the queue handler is what an agent turn would be;
	// on the first message it calls run_local_agent and blocks in it.
	var mu sync.Mutex
	var seen []bus.InboundMessage
	toolResult := make(chan string, 1)
	g.taskQueue = taskqueue.NewQueue(4, time.Minute, func(ctx context.Context, task *taskqueue.Task) (string, error) {
		mu.Lock()
		seen = append(seen, task.Message)
		first := len(seen) == 1
		mu.Unlock()
		if first {
			m := task.Message
			out, err := g.aglTools.Run(ctx, agl.ToolRun{UserID: m.MachineUserID,
				Chat:  agl.ToolChat{Channel: m.Channel, AccountID: m.AccountID, ChatID: m.ChatID, AgentID: task.AgentID},
				Agent: "claude", Task: m.Text})
			if err != nil {
				out = "error: " + err.Error()
			}
			toolResult <- out
		}
		return "", nil
	})
	t.Cleanup(g.taskQueue.Stop)

	dm := func(text string) bus.InboundMessage {
		return bus.InboundMessage{Channel: "wechat", AccountID: "bot-a", ChatID: "wx_alice", UserID: "u_chatter",
			OwnerUserID: "u_alice", PeerKind: "dm", FromChannelOwner: true, Text: text, MessageID: text + time.Now().String()}
	}
	g.routeDM(ctx, dm("fix the build"))
	waitUntil(t, "prompt", func() bool { return len(link.calls("session.prompt")) == 1 })
	mu.Lock()
	if seen[0].MachineUserID != "u_alice" {
		t.Fatalf("turn MachineUserID = %q", seen[0].MachineUserID)
	}
	mu.Unlock()
	var prompt struct {
		TurnID string `json:"turn_id"`
	}
	_ = json.Unmarshal([]byte(link.calls("session.prompt")[0]), &prompt)
	link.events <- agl.Event{SessionID: "s1", Event: agl.AgentEvent{Kind: "turn_started", TurnID: prompt.TurnID}}
	link.events <- agl.Event{SessionID: "s1", Event: agl.AgentEvent{Kind: "permission_request", RequestID: "r1", Title: "Run make"}}

	readOut := func(want string) {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for {
			select {
			case out := <-mb.Outbound:
				if strings.Contains(out.Text, want) {
					if out.Channel != "wechat" || out.ChatID != "wx_alice" {
						t.Fatalf("sent to %s/%s", out.Channel, out.ChatID)
					}
					return
				}
			case <-deadline:
				t.Fatalf("no outbound %q", want)
			}
		}
	}
	readOut("需要确认：Run make")

	// Someone other than the bound sender can't answer it (their "1"
	// routes on as a normal message).
	other := dm("1")
	other.FromChannelOwner = false
	if g.interceptToolApproval(ctx, other) {
		t.Fatal("stranger answered the approval")
	}

	g.routeDM(ctx, dm("2"))
	if got := link.calls("session.approve"); len(got) != 1 || !strings.Contains(got[0], `"decision":"allow_session"`) {
		t.Fatalf("approve = %v", got)
	}
	readOut("已允许（本会话内不再询问）。")

	link.events <- agl.Event{SessionID: "s1", Event: agl.AgentEvent{Kind: "text_delta", Text: "Build fixed."}}
	link.events <- agl.Event{SessionID: "s1", Event: agl.AgentEvent{Kind: "turn_ended", TurnID: prompt.TurnID, Outcome: "completed"}}
	select {
	case out := <-toolResult:
		if !strings.Contains(out, "Build fixed.") {
			t.Fatalf("tool result = %q", out)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tool run did not finish")
	}

	// Ordinary text still reaches the agent; the "2" never did.
	g.routeDM(ctx, dm("thanks"))
	waitUntil(t, "second turn", func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) == 2 })
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	for _, m := range seen {
		if m.Text == "2" {
			t.Fatal("approval reply reached the LLM turn")
		}
	}
	if seen[1].Text != "thanks" {
		t.Fatalf("turns = %+v", seen)
	}
}
