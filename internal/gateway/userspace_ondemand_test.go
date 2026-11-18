package gateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/usermodels"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

func newOnDemandStore(t *testing.T) *store.DBStore {
	t.Helper()
	t.Setenv("FASTCLAW_HOME", t.TempDir())
	st, err := store.NewDBStore("sqlite", "file:"+filepath.Join(t.TempDir(), "ondemand.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func mustUser(t *testing.T, st store.Store, id, role, owner string) {
	t.Helper()
	if err := st.CreateUser(context.Background(), &store.UserRecord{
		ID: id, Username: id, Email: id + "@example.com", Role: role,
		Status: users.StatusActive, OwnerUserID: owner, ExternalID: id, AgentQuota: -1,
	}); err != nil {
		t.Fatal(err)
	}
}

func mustAgent(t *testing.T, st store.Store, id, owner string) {
	t.Helper()
	if err := st.SaveAgent(context.Background(), &store.AgentRecord{ID: id, UserID: owner, Name: id}); err != nil {
		t.Fatal(err)
	}
}

// An account above the eager limit only builds its background agents
// (cron / channel bound) at load; others attach on demand and are
// evicted when idle, while background agents are never evicted.
func TestOnDemandUserSpace(t *testing.T) {
	st := newOnDemandStore(t)
	ctx := context.Background()
	t.Setenv("FASTCLAW_EAGER_AGENT_LIMIT", "1")

	mustUser(t, st, "app", users.RoleUser, "")
	mustUser(t, st, "stranger", users.RoleUser, "")
	for _, id := range []string{"agt_cron", "agt_a", "agt_b"} {
		mustAgent(t, st, id, "app")
	}
	mustAgent(t, st, "agt_foreign", "stranger")
	next := time.Now().Add(time.Hour)
	if err := st.SaveCronJob(ctx, &store.CronJobRecord{
		ID: "job1", UserID: "app", AgentID: "agt_cron", Name: "daily", Type: "cron",
		Schedule: "0 9 * * *", Message: "report", Channel: "web", Enabled: true, NextRun: &next,
	}); err != nil {
		t.Fatal(err)
	}

	mb := bus.New()
	sp, err := loadUserSpace(ctx, "app", mb, st, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !sp.OnDemand {
		t.Fatal("3 agents over a limit of 1 must load on demand")
	}
	if got := sp.Agents.Names(); len(got) != 1 || got[0] != "agt_cron" {
		t.Fatalf("eagerly loaded = %v, want only the cron agent", got)
	}
	if sp.Agents.DefaultAgent() != nil {
		t.Fatal("a lone pinned agent must not become the default of a multi-agent account")
	}

	if err := sp.EnsureOwnedAgent(ctx, st, mb, nil, "agt_a"); err != nil || !sp.Agents.Has("agt_a") {
		t.Fatalf("owned agent should attach on demand: %v", err)
	}
	if err := sp.EnsureOwnedAgent(ctx, st, mb, nil, "agt_foreign"); err == nil || sp.Agents.Has("agt_foreign") {
		t.Fatal("EnsureOwnedAgent must refuse another account's agent")
	}

	evicted := sp.Agents.EvictIdle(time.Now().Add(time.Minute), sp.IsPinned)
	if len(evicted) != 1 || evicted[0] != "agt_a" {
		t.Fatalf("evicted = %v, want [agt_a]", evicted)
	}
	if !sp.Agents.Has("agt_cron") {
		t.Fatal("pinned cron agent must survive eviction")
	}
}

// Under the limit nothing changes: every owned agent loads eagerly.
func TestEagerUserSpaceUnderLimit(t *testing.T) {
	st := newOnDemandStore(t)
	ctx := context.Background()
	mustUser(t, st, "solo", users.RoleUser, "")
	mustAgent(t, st, "agt_only", "solo")
	sp, err := loadUserSpace(ctx, "solo", bus.New(), st, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sp.OnDemand || !sp.Agents.Has("agt_only") || sp.Agents.DefaultAgent() == nil {
		t.Fatalf("small account should load eagerly with a default agent: onDemand=%v names=%v", sp.OnDemand, sp.Agents.Names())
	}
}

// An end-user (app_user) space treats the app's agents as its own.
func TestAppUserSpaceOwnsAppAgents(t *testing.T) {
	st := newOnDemandStore(t)
	ctx := context.Background()
	mustUser(t, st, "app", users.RoleUser, "")
	mustUser(t, st, "u_alice", users.RoleAppUser, "app")
	mustUser(t, st, "stranger", users.RoleUser, "")
	mustAgent(t, st, "agt_foreign", "stranger")
	// Opting out of sharing model config with chatters must not affect
	// the app's own end-users: the app's model settings always apply.
	if err := st.SaveAgent(ctx, &store.AgentRecord{ID: "agt_app", UserID: "app", Name: "agt_app",
		Config: map[string]interface{}{"shareModelConfig": false}}); err != nil {
		t.Fatal(err)
	}
	if err := scope.SaveSetting(ctx, st, "app", "", "agents.defaults",
		map[string]interface{}{"model": "owner-provider/owner-model"}); err != nil {
		t.Fatal(err)
	}

	mb := bus.New()
	sp, err := loadUserSpace(ctx, "u_alice", mb, st, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sp.AppOwnerUserID != "app" {
		t.Fatalf("AppOwnerUserID = %q", sp.AppOwnerUserID)
	}
	if err := sp.EnsureOwnedAgent(ctx, st, mb, nil, "agt_app"); err != nil {
		t.Fatalf("end-user space should attach the app's agent: %v", err)
	}
	if got := sp.Agents.AgentByID("agt_app").Model(); got != "owner-provider/owner-model" {
		t.Fatalf("end-user space runs the app's agent with model %q, want the app owner's", got)
	}
	if err := sp.EnsureOwnedAgent(ctx, st, mb, nil, "agt_foreign"); err == nil {
		t.Fatal("end-user space must not own agents of other accounts")
	}
}

func TestAppUserBYOKOverridesOwnerAndAgent(t *testing.T) {
	st := newOnDemandStore(t)
	ctx := context.Background()
	mustUser(t, st, "app", users.RoleUser, "")
	mustUser(t, st, "alice", users.RoleAppUser, "app")
	mustUser(t, st, "bob", users.RoleAppUser, "app")
	mustAgent(t, st, "agt_app", "app")
	if err := scope.SaveSetting(ctx, st, "", "agt_app", "agents.defaults", map[string]interface{}{"model": "system/owner"}); err != nil {
		t.Fatal(err)
	}
	c := usermodels.Config{Providers: []usermodels.Provider{{ID: "mine", Name: "Mine", APIBase: "https://api.example.com/v1", APIType: "openai-completions", APIKey: "test-key", Models: []string{"m"}}}, DefaultModel: "mine/m"}
	if err := usermodels.Save(ctx, st, "alice", c); err != nil {
		t.Fatal(err)
	}
	mb := bus.New()
	for _, tc := range []struct{ uid, want string }{{"alice", "byok-mine/m"}, {"bob", "system/owner"}} {
		sp, err := loadUserSpace(ctx, tc.uid, mb, st, nil, nil, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = sp.EnsureOwnedAgent(ctx, st, mb, nil, "agt_app"); err != nil {
			t.Fatal(err)
		}
		if got := sp.Agents.AgentByID("agt_app").Model(); got != tc.want {
			t.Fatalf("%s model=%s want=%s", tc.uid, got, tc.want)
		}
	}
}
