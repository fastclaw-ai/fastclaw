package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// readUserScopeAgentDefaults must distinguish "user has no row" from
// "user explicitly chose the system default". EnsureAgent relies on the
// returned Model being empty in case 1 (fall through to owner/agent
// overlays) and non-empty in case 2 (pin chatter's choice past the
// overlay chain) — the only way to tell apart is reading the raw row,
// not the merged Setting() view.
func TestReadUserScopeAgentDefaults(t *testing.T) {
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()

	// No row → zero value.
	got := readUserScopeAgentDefaults(ctx, db, "chatter-a")
	if got.Model != "" {
		t.Fatalf("missing row should give empty model, got %q", got.Model)
	}

	// Empty userID is a system caller — never pin.
	if got := readUserScopeAgentDefaults(ctx, db, ""); got.Model != "" {
		t.Fatalf("empty userID should give empty model, got %q", got.Model)
	}

	// Set a user-scope model → reads back.
	if err := scope.SaveSetting(ctx, db, "chatter-a", "", "agents.defaults",
		map[string]interface{}{"model": "openai/gpt-5.5"}); err != nil {
		t.Fatalf("save chatter row: %v", err)
	}
	got = readUserScopeAgentDefaults(ctx, db, "chatter-a")
	if got.Model != "openai/gpt-5.5" {
		t.Fatalf("explicit user-scope: want openai/gpt-5.5, got %q", got.Model)
	}

	// A different user with no row still returns empty — chatter pins
	// are per-user, never spill across accounts.
	if got := readUserScopeAgentDefaults(ctx, db, "chatter-b"); got.Model != "" {
		t.Fatalf("other user's row should not leak, got %q", got.Model)
	}

	// A row that exists but has no model key (chatter cleared override
	// while keeping other defaults) reads as zero — fall-through, no pin.
	if err := scope.SaveSetting(ctx, db, "chatter-a", "", "agents.defaults",
		map[string]interface{}{"maxTokens": float64(8192)}); err != nil {
		t.Fatalf("rewrite chatter row without model: %v", err)
	}
	got = readUserScopeAgentDefaults(ctx, db, "chatter-a")
	if got.Model != "" {
		t.Fatalf("row without model key should not pin, got %q", got.Model)
	}
	if got.MaxTokens != 8192 {
		t.Fatalf("other fields should still parse, got MaxTokens=%d", got.MaxTokens)
	}
}

func TestResolveChatterSeparatesIMSendersForRegularOwner(t *testing.T) {
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()

	owner := &store.UserRecord{
		ID:           "u_owner",
		Username:     "owner",
		Email:        "owner@example.com",
		PasswordHash: "x",
		Role:         users.RoleUser,
		Status:       users.StatusActive,
		AgentQuota:   -1,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	if err := db.CreateUser(ctx, owner); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	accts, err := users.NewAccounts(db)
	if err != nil {
		t.Fatalf("accounts: %v", err)
	}
	g := &Gateway{store: db, accounts: accts}

	alice := bus.InboundMessage{
		Channel:    "telegram",
		AccountID:  "bot-a",
		UserID:     "111",
		SenderName: "Alice",
	}
	bob := bus.InboundMessage{
		Channel:    "telegram",
		AccountID:  "bot-a",
		UserID:     "222",
		SenderName: "Bob",
	}
	aliceID := g.resolveChatter(ctx, owner.ID, alice)
	if aliceID == "" || aliceID == owner.ID {
		t.Fatalf("alice should resolve to app_user, got %q", aliceID)
	}
	bobID := g.resolveChatter(ctx, owner.ID, bob)
	if bobID == "" || bobID == owner.ID {
		t.Fatalf("bob should resolve to app_user, got %q", bobID)
	}
	if aliceID == bobID {
		t.Fatalf("different Telegram senders resolved to same user: %s", aliceID)
	}
	if again := g.resolveChatter(ctx, owner.ID, alice); again != aliceID {
		t.Fatalf("same sender should resolve stably: got %q want %q", again, aliceID)
	}

	aliceAccount, err := db.GetUser(ctx, aliceID)
	if err != nil {
		t.Fatalf("get alice app_user: %v", err)
	}
	if aliceAccount.OwnerUserID != owner.ID {
		t.Fatalf("unexpected owner_user_id: got %q, want %q", aliceAccount.OwnerUserID, owner.ID)
	}
	if aliceAccount.Role != users.RoleChannelUser {
		t.Fatalf("unexpected role: got %q, want %q", aliceAccount.Role, users.RoleChannelUser)
	}
	// New users get the accountID-free format so chatter identity
	// survives bot reconnections and is shared across agents.
	if aliceAccount.ExternalID != "telegram:111" {
		t.Fatalf("unexpected external id: %q", aliceAccount.ExternalID)
	}
}

// ModelFallbacks ride the same agents.defaults override row as Model.
// Pin the whole data path the loadUserSpace / EnsureAgent overlays
// rely on: row data (map[string]interface{}) → AgentDefaults's
// modelFallbacks JSON key → ResolvedAgent via MergedAgentConfig. A
// silent break anywhere here means a configured fallback chain never
// reaches the agent and the first provider outage goes user-visible
// again.
func TestAgentScopeModelFallbacksMerge(t *testing.T) {
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()

	if err := scope.SaveSetting(ctx, db, "", "agent-x", NSAgentDefaults,
		map[string]interface{}{
			"model":          "openai/gpt-5.5",
			"modelFallbacks": []interface{}{"anthropic/claude-fable-5", "openrouter/llama-4"},
		}); err != nil {
		t.Fatalf("save agent row: %v", err)
	}

	cfg, err := assembleConfig(ctx, db, "", "agent-x")
	if err != nil {
		t.Fatalf("assemble config: %v", err)
	}
	want := []string{"anthropic/claude-fable-5", "openrouter/llama-4"}
	got := cfg.Agents.Defaults.ModelFallbacks
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("defaults fallbacks = %v, want %v", got, want)
	}

	// The defaults must carry through to the per-agent resolved view —
	// that's what fallbacksForAgent reads at agent build time.
	rc := cfg.MergedAgentConfig(config.AgentEntry{ID: "agent-x"})
	got = rc.ModelFallbacks
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("resolved fallbacks = %v, want %v", got, want)
	}

	// A row without the key must not invent a chain for other agents.
	cfgOther, err := assembleConfig(ctx, db, "", "agent-y")
	if err != nil {
		t.Fatalf("assemble config (other agent): %v", err)
	}
	if n := len(cfgOther.Agents.Defaults.ModelFallbacks); n != 0 {
		t.Fatalf("agent without override row should have no fallbacks, got %d", n)
	}
}
