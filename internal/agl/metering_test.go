package agl

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/billing"
)

// A finished local-agent run is one `local_agent` usage event; a failed
// one records nothing.
func TestToolsRunRecordsLocalAgentEvent(t *testing.T) {
	f := newToolsFixture(t)
	turn := billing.NewService(f.st).NewTurn("u_alice", "agt_persona", "sess", "wechat")
	ctx := billing.WithTurn(context.Background(), turn)

	prompts := 0
	run := func(outcome string) {
		errc := make(chan error, 1)
		go func() {
			_, err := f.tools.Run(ctx, ToolRun{UserID: "u_alice", Chat: f.chat, Agent: "Claude", Task: "run the tests"})
			errc <- err
		}()
		n := prompts
		waitFor(t, "prompt", func() bool { return len(f.link.calls("session.prompt")) > n })
		prompts++
		calls := f.link.calls("session.prompt")
		var p struct {
			TurnID string `json:"turn_id"`
		}
		_ = json.Unmarshal([]byte(calls[len(calls)-1]), &p)
		f.ev(AgentEvent{Kind: "turn_started", TurnID: p.TurnID})
		f.ev(AgentEvent{Kind: "text_delta", Text: "done"})
		f.ev(AgentEvent{Kind: "turn_ended", TurnID: p.TurnID, Outcome: outcome})
		<-errc
	}
	run("completed")
	run("failed")

	rows, err := f.st.DB().Query(`SELECT kind, provider, model, units, unit, detail, turn_id, user_id, agent_id, channel FROM usage_events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
		var kind, prov, model, unit, detail, turnID, uid, agentID, channel string
		var units float64
		if err := rows.Scan(&kind, &prov, &model, &units, &unit, &detail, &turnID, &uid, &agentID, &channel); err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		_ = json.Unmarshal([]byte(detail), &d)
		if kind != "local_agent" || prov != "agl" || model != "claude" || units != 1 || unit != "run" ||
			d["machine"] != "mch_a1" || d["duration_ms"] == nil || turnID != turn.ID || uid != "u_alice" ||
			agentID != "agt_persona" || channel != "wechat" {
			t.Fatalf("event = %s %s %s %v %s %s %s %s %s %s", kind, prov, model, units, unit, detail, turnID, uid, agentID, channel)
		}
	}
	if n != 1 {
		t.Fatalf("local_agent events = %d, want 1 (the failed run records nothing)", n)
	}
}
