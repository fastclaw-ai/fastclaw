package store

import (
	"context"
	"testing"
	"time"
)

// TestSessionChannelTriple covers the (channel, account_id, chat_id)
// columns end-to-end: a fresh row carries the triple, ListSessions
// surfaces it, and ResolveActiveSessionKey picks the most-recent row
// for a triple — which is what powers "/new" support in IM channels.
func TestSessionChannelTriple(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	const userID = "u-test"
	const agentID = "agt-test"

	// ListSessions now scopes results to rows whose user_id exists in
	// the users table (the query joins sessions → users to surface a
	// caller's own + their app_users' threads). Seed the owner so the
	// sessions saved below are visible. (Upstream's version of this test
	// omitted this and fails the final ListSessions assertion.)
	if err := db.CreateUser(ctx, &UserRecord{ID: userID, Username: userID, Email: userID + "@test.local", Role: "user"}); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// Two sessions sharing the same wechat (account, openid) triple —
	// the older "v1" thread plus a newer "v2" minted after a /new.
	older := &SessionRecord{
		Channel:   "wechat",
		AccountID: "wxbot1",
		ChatID:    "openid-A",
		Messages:  []SessionMessage{{Role: "user", Content: "hi v1"}},
	}
	if err := db.SaveSession(ctx, userID, agentID, "s-old", older); err != nil {
		t.Fatalf("save older: %v", err)
	}
	// Force older's updated_at into the past so the active lookup has a
	// clear winner. SaveSession stamps now() — sleep is the simplest
	// portable way to space them on sqlite.
	time.Sleep(15 * time.Millisecond)
	newer := &SessionRecord{
		Channel:   "wechat",
		AccountID: "wxbot1",
		ChatID:    "openid-A",
		Messages:  []SessionMessage{{Role: "user", Content: "hi v2"}},
	}
	if err := db.SaveSession(ctx, userID, agentID, "s-new", newer); err != nil {
		t.Fatalf("save newer: %v", err)
	}
	// And one unrelated row under a *different* bot's account — must
	// not leak into the lookup for wxbot1.
	other := &SessionRecord{
		Channel:   "wechat",
		AccountID: "wxbot2",
		ChatID:    "openid-A",
		Messages:  []SessionMessage{{Role: "user", Content: "different bot"}},
	}
	if err := db.SaveSession(ctx, userID, agentID, "s-other", other); err != nil {
		t.Fatalf("save other: %v", err)
	}

	// Active lookup should pick the newer row (max updated_at) for
	// (wechat, wxbot1, openid-A).
	got, err := db.ResolveActiveSessionKey(ctx, userID, agentID, "wechat", "wxbot1", "openid-A")
	if err != nil {
		t.Fatalf("resolve active: %v", err)
	}
	if got != "s-new" {
		t.Fatalf("active session_key = %q; want %q", got, "s-new")
	}

	// And the unrelated bot's lookup yields its own row.
	gotOther, err := db.ResolveActiveSessionKey(ctx, userID, agentID, "wechat", "wxbot2", "openid-A")
	if err != nil {
		t.Fatalf("resolve other: %v", err)
	}
	if gotOther != "s-other" {
		t.Fatalf("other bot session_key = %q; want %q", gotOther, "s-other")
	}

	// SaveSession upsert path: re-saving "s-old" with a different
	// triple must NOT overwrite the original triple (row identity is
	// the PK, not the triple — the columns are write-once on insert).
	mistaken := &SessionRecord{
		Channel:   "telegram", // wrong on purpose
		AccountID: "tg-bot",
		ChatID:    "tg-chat",
		Messages:  []SessionMessage{{Role: "user", Content: "added later"}},
	}
	if err := db.SaveSession(ctx, userID, agentID, "s-old", mistaken); err != nil {
		t.Fatalf("re-save older: %v", err)
	}
	rec, err := db.GetSession(ctx, userID, agentID, "s-old")
	if err != nil {
		t.Fatalf("get older: %v", err)
	}
	if rec.Channel != "wechat" || rec.AccountID != "wxbot1" || rec.ChatID != "openid-A" {
		t.Fatalf("triple was overwritten: got (%s, %s, %s); want (wechat, wxbot1, openid-A)",
			rec.Channel, rec.AccountID, rec.ChatID)
	}

	// ListSessions surfaces the columns (powers the dashboard's "this
	// session belongs to which conversation" rendering).
	metas, err := db.ListSessions(ctx, userID, agentID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(metas) != 3 {
		t.Fatalf("list len = %d; want 3", len(metas))
	}
	for _, m := range metas {
		if m.Channel == "" || m.ChatID == "" {
			t.Errorf("meta missing triple: %+v", m)
		}
	}
}

// TestSessionTripleBackfill simulates an installed-base upgrade: a
// pre-feature schema with no channel/account_id/chat_id columns whose
// rows store legacy `<channel>_<chatID>` keys. After Migrate runs, the
// new columns must be populated by parsing the existing keys.
func TestSessionTripleBackfill(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// Drop the new columns + index to fake the pre-feature schema.
	// The composite index references the columns, so it has to go first.
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_sessions_chat_active`,
		`ALTER TABLE sessions DROP COLUMN channel`,
		`ALTER TABLE sessions DROP COLUMN account_id`,
		`ALTER TABLE sessions DROP COLUMN chat_id`,
	} {
		if _, err := db.db.ExecContext(ctx, stmt); err != nil {
			t.Skipf("sqlite version doesn't support pre-feature simulation: %v", err)
		}
	}
	// Insert legacy-format rows directly via the bare INSERT (the
	// column-aware SaveSession would refuse without the new cols).
	for _, key := range []string{"web_s-1234-abcd", "wechat_openid-XYZ", "telegram_555"} {
		if _, err := db.db.ExecContext(ctx,
			`INSERT INTO sessions (user_id, agent_id, session_key, messages, message_count, updated_at)
				VALUES (?, ?, ?, '[]', 0, CURRENT_TIMESTAMP)`,
			"u-back", "agt-back", key); err != nil {
			t.Fatalf("seed legacy row %s: %v", key, err)
		}
	}

	// Re-run Migrate — should add the columns and backfill from the keys.
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate after legacy seed: %v", err)
	}

	cases := []struct {
		key       string
		wantChan  string
		wantChat  string
	}{
		{"web_s-1234-abcd", "web", "s-1234-abcd"},
		{"wechat_openid-XYZ", "wechat", "openid-XYZ"},
		{"telegram_555", "telegram", "555"},
	}
	for _, tc := range cases {
		rec, err := db.GetSession(ctx, "u-back", "agt-back", tc.key)
		if err != nil {
			t.Fatalf("get %s: %v", tc.key, err)
		}
		if rec.Channel != tc.wantChan {
			t.Errorf("%s: channel = %q; want %q", tc.key, rec.Channel, tc.wantChan)
		}
		if rec.ChatID != tc.wantChat {
			t.Errorf("%s: chat_id = %q; want %q", tc.key, rec.ChatID, tc.wantChat)
		}
		// account_id is unrecoverable from legacy keys — must be empty.
		if rec.AccountID != "" {
			t.Errorf("%s: account_id = %q; want empty", tc.key, rec.AccountID)
		}
	}

	// Resolution by triple must find the backfilled rows so existing
	// IM conversations don't lose their history after the upgrade.
	got, err := db.ResolveActiveSessionKey(ctx, "u-back", "agt-back", "wechat", "", "openid-XYZ")
	if err != nil {
		t.Fatalf("resolve backfilled: %v", err)
	}
	if got != "wechat_openid-XYZ" {
		t.Fatalf("backfilled active key = %q; want %q", got, "wechat_openid-XYZ")
	}
}
