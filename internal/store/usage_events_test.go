package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func testUsageEventsRoundTrip(t *testing.T, st *DBStore) {
	ctx := context.Background()
	for _, u := range []*UserRecord{
		{ID: "u_acct", Username: "acct", Email: "a@x", Role: "user", Status: "active"},
		{ID: "u_end", Username: "end", Email: "e@x", Role: "app_user", Status: "active", OwnerUserID: "u_acct", ExternalID: "user:42"},
		{ID: "u_chat", Username: "chat", Email: "c@x", Role: "channel_user", Status: "active", OwnerUserID: "u_end", ExternalID: "wx_1"},
		{ID: "u_other", Username: "other", Email: "o@x", Role: "user", Status: "active"},
	} {
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	cost := 0.0042
	rows := []MeteredEvent{
		{UserID: "u_acct", AgentID: "agt_1", SessionKey: "s1", Channel: "api", TurnID: "trn_1", Kind: "llm", Provider: "openrouter", Model: "autojev/fast",
			InputTokens: 9213, OutputTokens: 77, CacheReadTokens: 5, CacheCreationTokens: 6, CostUSD: &cost},
		{UserID: "u_end", AgentID: "agt_1", SessionKey: "s2", Channel: "web", TurnID: "trn_2", Kind: "image", Provider: "fal", Model: "flux-pro",
			Units: 2, Unit: "image", Detail: map[string]any{"size": "1024x1024"}},
		{UserID: "u_chat", AgentID: "agt_1", SessionKey: "s3", Channel: "wechat", TurnID: "trn_3", Kind: "sandbox", Provider: "docker", Units: 1.234, Unit: "second"},
		{UserID: "u_other", AgentID: "agt_9", Kind: "search", Provider: "brave", Units: 1, Unit: "call"},
	}
	for i := range rows {
		if err := st.InsertMeteredEvent(ctx, &rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	// Too fresh to export.
	if got, err := st.ListAccountUsageEvents(ctx, "u_acct", 0, 100); err != nil || len(got) != 0 {
		t.Fatalf("fresh rows exported: %+v %v", got, err)
	}
	age := `UPDATE usage_events SET created_at = CURRENT_TIMESTAMP - INTERVAL '1 minute'`
	if st.Dialect() != "postgres" {
		age = `UPDATE usage_events SET created_at = datetime('now', '-1 minute')`
	}
	if _, err := st.DB().ExecContext(ctx, age); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListAccountUsageEvents(ctx, "u_acct", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("account events = %+v", got)
	}
	e := got[0]
	if e.Kind != "llm" || e.Provider != "openrouter" || e.Model != "autojev/fast" || e.InputTokens != 9213 || e.OutputTokens != 77 ||
		e.CacheReadTokens != 5 || e.CacheCreationTokens != 6 || e.CostUSD == nil || *e.CostUSD != cost || e.EndUser != "" ||
		e.AgentID != "agt_1" || e.SessionKey != "s1" || e.Channel != "api" || e.TurnID != "trn_1" || e.CreatedAt.IsZero() ||
		len(e.Detail) != 0 || e.Detail == nil {
		t.Fatalf("llm event = %+v", e)
	}
	if e := got[1]; e.EndUser != "user:42" || e.Units != 2 || e.Unit != "image" || e.Detail["size"] != "1024x1024" || e.CostUSD != nil {
		t.Fatalf("image event = %+v", e)
	}
	// An IM chatter under an end user is billed to that end user.
	if e := got[2]; e.EndUser != "user:42" || e.Units != 1.234 || e.Channel != "wechat" {
		t.Fatalf("sandbox event = %+v", e)
	}
	if !(got[0].ID < got[1].ID && got[1].ID < got[2].ID) {
		t.Fatal("ids must increase")
	}

	// Cursor and limit.
	page, err := st.ListAccountUsageEvents(ctx, "u_acct", 0, 2)
	if err != nil || len(page) != 2 || page[1].ID != got[1].ID {
		t.Fatalf("limit: %+v %v", page, err)
	}
	rest, err := st.ListAccountUsageEvents(ctx, "u_acct", page[1].ID, 2)
	if err != nil || len(rest) != 1 || rest[0].ID != got[2].ID {
		t.Fatalf("after cursor: %+v %v", rest, err)
	}
	// Scoped to the paying account.
	other, err := st.ListAccountUsageEvents(ctx, "u_other", 0, 100)
	if err != nil || len(other) != 1 || other[0].Kind != "search" {
		t.Fatalf("other account: %+v %v", other, err)
	}

	// Wire shape: documented keys, nothing internal.
	blob, _ := json.Marshal(got[0])
	t.Logf("exported event: %s", blob)
	for _, key := range []string{`"id":`, `"created_at":`, `"kind":"llm"`, `"cost_usd":0.0042`, `"detail":{}`, `"end_user":""`, `"turn_id":"trn_1"`, `"units":0`, `"unit":""`} {
		if !strings.Contains(string(blob), key) {
			t.Fatalf("%s lacks %s", blob, key)
		}
	}
	if strings.Contains(string(blob), "u_acct") || strings.Contains(string(blob), "user_id") {
		t.Fatalf("internal ids leaked: %s", blob)
	}

	// Billing targets and the hook.
	if err := st.SetBillingHook(ctx, "u_acct", "https://app.example.com/hook", "secret"); err != nil {
		t.Fatal(err)
	}
	for uid, want := range map[string]BillingTarget{
		"u_acct":  {AccountID: "u_acct", HookURL: "https://app.example.com/hook", HookSecret: "secret"},
		"u_end":   {AccountID: "u_acct", EndUser: "user:42", HookURL: "https://app.example.com/hook", HookSecret: "secret"},
		"u_chat":  {AccountID: "u_acct", EndUser: "user:42", HookURL: "https://app.example.com/hook", HookSecret: "secret"},
		"u_other": {AccountID: "u_other"},
		"nobody":  {AccountID: "nobody"},
	} {
		if got, err := st.BillingTarget(ctx, uid); err != nil || got != want {
			t.Fatalf("target(%s) = %+v %v", uid, got, err)
		}
	}
	if err := st.SetBillingHook(ctx, "u_acct", "", "ignored"); err != nil {
		t.Fatal(err)
	}
	if url, secret, err := st.GetBillingHook(ctx, "u_acct"); err != nil || url != "" || secret != "" {
		t.Fatalf("removed hook = %q %q %v", url, secret, err)
	}
	if err := st.SetBillingHook(ctx, "nobody", "https://x", "y"); err != ErrNotFound {
		t.Fatalf("unknown account: %v", err)
	}
}

func TestUsageEventsSQLite(t *testing.T) {
	testUsageEventsRoundTrip(t, openBillingStore(t))
}

func TestUsageEventsPostgres(t *testing.T) {
	st, cleanup := openTestPG(t)
	defer cleanup()
	testUsageEventsRoundTrip(t, st)
}
