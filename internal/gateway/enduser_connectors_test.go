package gateway

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// An end user's connected accounts reach only the account's agents marked
// as theirs or as the account's official ones (endUserConnectors): not other public agents (someone else's instructions), not another
// end user's, not from another chatter or outside an end user's space.
func TestEndUserConnectorGate(t *testing.T) {
	st := privateAgentFixture(t)
	ctx := context.Background()
	for _, a := range []store.AgentRecord{
		{ID: "agt_pub", UserID: "app", Name: "小微", Config: map[string]any{store.AgentCfgEndUserVisibility: store.EndUserVisibilityPublic}},
		{ID: "agt_mine", UserID: "app", Name: "Mine", Config: map[string]any{store.AgentCfgEndUserVisibility: store.EndUserVisibilityPublic, store.AgentCfgEndUserOwner: "user:alice"}},
		{ID: "agt_draft", UserID: "app", Name: "Draft", Config: map[string]any{store.AgentCfgEndUserVisibility: store.EndUserVisibilityPrivate, store.AgentCfgEndUserOwner: "user:alice"}},
		{ID: "agt_bob", UserID: "app", Name: "Bob's", Config: map[string]any{store.AgentCfgEndUserOwner: "user:bob"}},
		// The app's official agent: every end user's accounts.
		{ID: "agt_official", UserID: "app", Name: "拽姐", Config: map[string]any{store.AgentCfgEndUserVisibility: store.EndUserVisibilityPublic, store.AgentCfgEndUserOwner: "user:staff", store.AgentCfgEndUserConnectors: true}},
		// The flag only counts on the app's own agents.
		{ID: "agt_other_official", UserID: "u_bob", Name: "Other", Config: map[string]any{store.AgentCfgEndUserConnectors: true}},
	} {
		a := a
		if err := st.SaveAgent(ctx, &a); err != nil {
			t.Fatal(err)
		}
	}
	if endUserConnectorGate(st, "app", "", "") != nil {
		t.Fatal("an account's own space must get no end-user gate")
	}
	gate := endUserConnectorGate(st, "u_alice", "app", "user:alice")
	for _, c := range []struct {
		agent, chatter string
		want           bool
	}{
		{"agt_mine", "u_alice", true},
		{"agt_draft", "u_alice", true},
		{"agt_pub", "u_alice", false},
		{"agt_bob", "u_alice", false},
		{"agt_plat", "u_alice", false},
		{"agt_mine", "u_bob", false},
		{"agt_official", "u_alice", true},
		{"agt_official", "u_bob", false},
		{"agt_other_official", "u_alice", false},
		{"agt_missing", "u_alice", false},
	} {
		if got := gate(ctx, c.agent, c.chatter); got != c.want {
			t.Errorf("gate(%s, %s) = %v, want %v", c.agent, c.chatter, got, c.want)
		}
	}
}
