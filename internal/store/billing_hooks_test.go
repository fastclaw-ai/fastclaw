package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openBillingStore(t *testing.T) *DBStore {
	t.Helper()
	st, err := NewDBStore("sqlite", "file:"+filepath.Join(t.TempDir(), "billing.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestListUsageEventsResolvesPayingAccount(t *testing.T) {
	ctx := context.Background()
	st := openBillingStore(t)
	for _, u := range []*UserRecord{
		{ID: "u_acct", Username: "acct", Email: "a@x", Role: "user", Status: "active"},
		{ID: "u_end", Username: "end", Email: "e@x", Role: "app_user", Status: "active", OwnerUserID: "u_acct", ExternalID: "alice"},
		{ID: "u_chat", Username: "chat", Email: "c@x", Role: "channel_user", Status: "active", OwnerUserID: "u_end", ExternalID: "wx_123"},
	} {
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().UTC().Add(-time.Minute).Format("2006-01-02 15:04:05")
	for _, uid := range []string{"u_acct", "u_end", "u_chat"} {
		if _, err := st.DB().ExecContext(ctx,
			`INSERT INTO token_usage_log (user_id, agent_id, model, input_tokens, output_tokens, created_at) VALUES (?, 'agt_1', 'm', 10, 5, ?)`,
			uid, old); err != nil {
			t.Fatal(err)
		}
	}
	// Too fresh to export yet.
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO token_usage_log (user_id, agent_id, model, input_tokens) VALUES ('u_acct', 'agt_1', 'm', 1)`); err != nil {
		t.Fatal(err)
	}

	events, err := st.ListUsageEvents(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3 settled rows", len(events))
	}
	for i, want := range []struct{ user, endUser string }{{"u_acct", ""}, {"u_end", "alice"}, {"u_chat", ""}} {
		e := events[i]
		if e.UserID != want.user || e.AccountID != "u_acct" || e.EndUser != want.endUser || e.InputTokens != 10 {
			t.Fatalf("event %d = %+v", i, e)
		}
	}
	if events[0].ID >= events[1].ID {
		t.Fatal("ids must increase")
	}
	rest, err := st.ListUsageEvents(ctx, events[1].ID, 100)
	if err != nil || len(rest) != 1 || rest[0].UserID != "u_chat" {
		t.Fatalf("after cursor: %+v %v", rest, err)
	}
}

func TestBillingHoldAndLoginTokens(t *testing.T) {
	ctx := context.Background()
	st := openBillingStore(t)
	if err := st.CreateUser(ctx, &UserRecord{ID: "u_1", Username: "one", Email: "1@x", Role: "user", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if hold, _, err := st.GetBillingHold(ctx, "u_1"); err != nil || hold {
		t.Fatalf("default hold = %v %v", hold, err)
	}
	if err := st.SetBillingHold(ctx, "u_1", true, "balance exhausted"); err != nil {
		t.Fatal(err)
	}
	if hold, reason, _ := st.GetBillingHold(ctx, "u_1"); !hold || reason != "balance exhausted" {
		t.Fatalf("hold = %v %q", hold, reason)
	}
	if err := st.SetBillingHold(ctx, "u_nope", true, ""); err != ErrNotFound {
		t.Fatalf("unknown user: %v", err)
	}

	if err := st.CreateLoginToken(ctx, "h1", "u_1", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if uid, err := st.ConsumeLoginToken(ctx, "h1"); err != nil || uid != "u_1" {
		t.Fatalf("consume: %q %v", uid, err)
	}
	if _, err := st.ConsumeLoginToken(ctx, "h1"); err != ErrNotFound {
		t.Fatalf("second use: %v", err)
	}
	if err := st.CreateLoginToken(ctx, "h2", "u_1", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConsumeLoginToken(ctx, "h2"); err != ErrNotFound {
		t.Fatalf("expired token: %v", err)
	}
}

// A token_usage_log created before cost tracking gains a nullable
// cost_usd column; existing rows read back as unknown (null), not 0.
func TestMigrateAddsTokenUsageCost(t *testing.T) {
	ctx := context.Background()
	st, err := NewDBStore("sqlite", "file:"+filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.DB().ExecContext(ctx, `CREATE TABLE token_usage_log (
		user_id TEXT NOT NULL DEFAULT '', agent_id TEXT NOT NULL DEFAULT '',
		session_key TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '', input_tokens BIGINT NOT NULL DEFAULT 0,
		output_tokens BIGINT NOT NULL DEFAULT 0, cache_read_tokens BIGINT NOT NULL DEFAULT 0,
		cache_create_tokens BIGINT NOT NULL DEFAULT 0, duration_ms BIGINT NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-time.Minute).Format("2006-01-02 15:04:05")
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO token_usage_log (user_id, model, input_tokens, created_at) VALUES ('u', 'm', 1, ?)`, old); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := st.ListUsageEvents(ctx, 0, 10)
	if err != nil || len(events) != 1 || events[0].CostUSD != nil {
		t.Fatalf("old row after migration: %+v %v", events, err)
	}
}
