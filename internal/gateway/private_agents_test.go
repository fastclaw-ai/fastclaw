package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// privateAgentFixture: account "app" with a platform agent, end-users
// alice and bob, each owning private agents (agents.user_id = app_user).
func privateAgentFixture(t *testing.T) store.Store {
	t.Helper()
	st := newOnDemandStore(t)
	mustUser(t, st, "app", users.RoleUser, "")
	mustUser(t, st, "u_alice", users.RoleAppUser, "app")
	mustUser(t, st, "u_bob", users.RoleAppUser, "app")
	ctx := context.Background()
	for _, a := range []store.AgentRecord{
		{ID: "agt_plat", UserID: "app", Name: "Support"},
		{ID: "agt_plat2", UserID: "app", Name: "Billing"},
		{ID: "agt_a1", UserID: "u_alice", Name: "Writer"},
		{ID: "agt_a2", UserID: "u_alice", Name: "Reviewer"},
		{ID: "agt_b1", UserID: "u_bob", Name: "Spy"},
	} {
		a := a
		if err := st.SaveAgent(ctx, &a); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

// message_agent from a private agent reaches only its owner's other
// agents: never another end-user's, never the account's platform agents;
// and platform agents running in an end-user's space reach none of the
// end-user's private agents.
func TestMessageAgentStaysWithinOwner(t *testing.T) {
	st := privateAgentFixture(t)
	ctx := context.Background()
	mb := bus.New()

	alice, err := loadUserSpace(ctx, "u_alice", mb, st, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !alice.Agents.Has("agt_a1") || !alice.Agents.Has("agt_a2") || alice.Agents.Has("agt_b1") {
		t.Fatalf("alice's space loaded %v, want exactly her private agents", alice.Agents.Names())
	}
	resolve := messageAgentResolver(func() *UserSpace { return alice }, st, mb, nil, "u_alice", "app")

	for _, ref := range []string{"agt_a2", "reviewer"} {
		ag, err := resolve(ctx, "u_alice", ref)
		if err != nil || ag == nil || ag.Name() != "agt_a2" {
			t.Fatalf("alice's agent → %q: %v %v", ref, ag, err)
		}
	}
	for _, c := range []struct{ caller, ref, why string }{
		{"u_alice", "agt_b1", "another end-user's agent by id"},
		{"u_alice", "Spy", "another end-user's agent by name"},
		{"u_alice", "agt_plat", "a platform agent by id"},
		{"u_alice", "Support", "a platform agent by name"},
		{"app", "agt_a2", "platform agent in alice's space → her private agent"},
		{"app", "agt_plat2", "platform agent in alice's space → another platform agent"},
		{"u_bob", "agt_a1", "bob's agent → alice's agent"},
	} {
		if ag, err := resolve(ctx, c.caller, c.ref); err == nil {
			t.Fatalf("%s resolved to %v", c.why, ag.Name())
		}
	}
	if err := alice.EnsureOwnedAgent(ctx, st, mb, nil, "agt_b1"); err == nil {
		t.Fatal("alice's space must not attach bob's private agent")
	}

	// The account's own space is unchanged: platform agents reach each
	// other and never an end-user's private agent.
	app, err := loadUserSpace(ctx, "app", mb, st, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if app.Agents.Has("agt_a1") || app.Agents.Has("agt_b1") {
		t.Fatalf("account space loaded private agents: %v", app.Agents.Names())
	}
	resolveApp := messageAgentResolver(func() *UserSpace { return app }, st, mb, nil, "app", "")
	if ag, err := resolveApp(ctx, "app", "Billing"); err != nil || ag.Name() != "agt_plat2" {
		t.Fatalf("platform → platform: %v %v", ag, err)
	}
	for _, ref := range []string{"agt_a1", "Writer", "agt_b1"} {
		if ag, err := resolveApp(ctx, "app", ref); err == nil {
			t.Fatalf("platform agent reached private agent %s", ag.Name())
		}
	}
}

// Cron jobs and channels attached to a private agent belong to its owner:
// they live in the owner's (app_user) space and keep the agent loaded
// there, never in the account's.
func TestPrivateAgentBackgroundWorkBelongsToOwner(t *testing.T) {
	st := privateAgentFixture(t)
	ctx := context.Background()
	t.Setenv("FASTCLAW_EAGER_AGENT_LIMIT", "0")
	next := time.Now().Add(time.Hour)
	if err := st.SaveCronJob(ctx, &store.CronJobRecord{
		ID: "job_a", UserID: "u_alice", AgentID: "agt_a1", Name: "daily", Type: "cron",
		Schedule: "0 9 * * *", Message: "report", Channel: "web", Enabled: true, NextRun: &next,
	}); err != nil {
		t.Fatal(err)
	}
	mb := bus.New()
	alice, err := loadUserSpace(ctx, "u_alice", mb, st, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !alice.IsPinned("agt_a1") || !alice.Agents.Has("agt_a1") {
		t.Fatalf("cron-bound private agent must stay loaded in its owner's space: pinned=%v loaded=%v",
			alice.IsPinned("agt_a1"), alice.Agents.Names())
	}
	app, err := loadUserSpace(ctx, "app", mb, st, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if app.IsPinned("agt_a1") || app.Agents.Has("agt_a1") {
		t.Fatal("the account's space must not run its end-users' private agents")
	}
}
