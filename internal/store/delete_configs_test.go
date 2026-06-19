package store

import (
	"context"
	"testing"
)

// TestDeleteAgentRemovesScopedConfigs and TestDeleteUserRemovesScopedConfigs
// guard the delete paths against the configs refactor that replaced the
// (user_id, agent_id) columns with (scope, scope_id). Before the fix the
// DELETE statements still referenced the dropped columns and failed at
// runtime with "no such column" on any migrated database. These tests
// reproduce that failure (DeleteAgent/DeleteUser returning an error) and
// assert the scoped config rows are actually removed.

func TestDeleteAgentRemovesScopedConfigs(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	const ownerID = "u-owner"
	const otherID = "u-other"
	const agentID = "agt-x"

	for _, u := range []string{ownerID, otherID} {
		if err := db.CreateUser(ctx, &UserRecord{ID: u, Username: u, Email: u + "@test.local", Role: "user"}); err != nil {
			t.Fatalf("seed user %s: %v", u, err)
		}
	}
	if err := db.SaveAgent(ctx, &AgentRecord{ID: agentID, UserID: ownerID, Name: "x"}); err != nil {
		t.Fatalf("save agent: %v", err)
	}

	// Official agent row (scope='agent') + a per-user overlay authored by
	// a different user (scope='user-agent', scope_id='u-other/agt-x').
	mustSaveConfig(t, db, &ConfigRecord{Kind: "setting", AgentID: agentID, Name: "model", Enabled: true})
	mustSaveConfig(t, db, &ConfigRecord{Kind: "setting", UserID: otherID, AgentID: agentID, Name: "model", Enabled: true})
	// An unrelated user-scoped row that must survive.
	mustSaveConfig(t, db, &ConfigRecord{Kind: "setting", UserID: otherID, Name: "theme", Enabled: true})

	if err := db.DeleteAgent(ctx, agentID); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}

	assertNoConfig(t, db, "setting", "", agentID, "model")      // official agent row gone
	assertNoConfig(t, db, "setting", otherID, agentID, "model") // overlay gone
	assertConfig(t, db, "setting", otherID, "", "theme")        // unrelated row survives
}

func TestDeleteUserRemovesScopedConfigs(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	const userID = "u-self"
	const keepID = "u-keep"
	const otherAgent = "agt-other"

	for _, u := range []string{userID, keepID} {
		if err := db.CreateUser(ctx, &UserRecord{ID: u, Username: u, Email: u + "@test.local", Role: "user"}); err != nil {
			t.Fatalf("seed user %s: %v", u, err)
		}
	}
	if err := db.SaveAgent(ctx, &AgentRecord{ID: otherAgent, UserID: keepID, Name: "o"}); err != nil {
		t.Fatalf("save agent: %v", err)
	}

	// The user's own row (scope='user') + an overlay they authored on
	// someone else's agent (scope='user-agent', scope_id='u-self/agt-other').
	mustSaveConfig(t, db, &ConfigRecord{Kind: "setting", UserID: userID, Name: "theme", Enabled: true})
	mustSaveConfig(t, db, &ConfigRecord{Kind: "setting", UserID: userID, AgentID: otherAgent, Name: "model", Enabled: true})
	// Another user's row that must survive.
	mustSaveConfig(t, db, &ConfigRecord{Kind: "setting", UserID: keepID, Name: "theme", Enabled: true})

	if err := db.DeleteUser(ctx, userID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	assertNoConfig(t, db, "setting", userID, "", "theme")         // own row gone
	assertNoConfig(t, db, "setting", userID, otherAgent, "model") // overlay gone
	assertConfig(t, db, "setting", keepID, "", "theme")           // other user survives
}

func mustSaveConfig(t *testing.T, db *DBStore, c *ConfigRecord) {
	t.Helper()
	if err := db.SaveConfig(context.Background(), c); err != nil {
		t.Fatalf("SaveConfig %+v: %v", c, err)
	}
}

func assertConfig(t *testing.T, db *DBStore, kind, userID, agentID, name string) {
	t.Helper()
	if got := getConfigByName(t, db, kind, userID, agentID, name); got == nil {
		t.Fatalf("expected config (kind=%s user=%s agent=%s name=%s) to exist", kind, userID, agentID, name)
	}
}

func assertNoConfig(t *testing.T, db *DBStore, kind, userID, agentID, name string) {
	t.Helper()
	if got := getConfigByName(t, db, kind, userID, agentID, name); got != nil {
		t.Fatalf("expected config (kind=%s user=%s agent=%s name=%s) to be deleted, still present", kind, userID, agentID, name)
	}
}

func getConfigByName(t *testing.T, db *DBStore, kind, userID, agentID, name string) *ConfigRecord {
	t.Helper()
	recs, err := db.ListConfigs(context.Background(), kind, userID, agentID)
	if err != nil {
		t.Fatalf("ListConfigs: %v", err)
	}
	for i := range recs {
		if recs[i].Name == name {
			return &recs[i]
		}
	}
	return nil
}
