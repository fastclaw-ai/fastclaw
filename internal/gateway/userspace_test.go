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
func TestOverlayAgentScopeMCPDisabledRowPreservesExistingServer(t *testing.T) {
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()

	rc := &config.ResolvedAgent{
		ID: "agent-mcp",
		MCPServers: map[string]config.MCPServerConfig{
			"legacy": {Type: "http", URL: "https://legacy.example/mcp"},
		},
	}
	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind:    store.KindMCP,
		AgentID: rc.ID,
		Name:    "legacy",
		Enabled: false,
		Data: map[string]interface{}{
			"type": "http",
			"url":  "https://disabled.example/mcp",
		},
	}); err != nil {
		t.Fatalf("save disabled MCP row: %v", err)
	}

	if err := overlayAgentScopeMCP(ctx, db, rc); err != nil {
		t.Fatalf("overlayAgentScopeMCP: %v", err)
	}
	if got := rc.MCPServers["legacy"].URL; got != "https://legacy.example/mcp" {
		t.Fatalf("disabled DB row should preserve same-name legacy MCP server, got %q", got)
	}
}

func TestOverlayAgentScopeMCPOverlaysDBRows(t *testing.T) {
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()

	rc := &config.ResolvedAgent{
		ID: "agent-mcp",
		MCPServers: map[string]config.MCPServerConfig{
			"existing": {Type: "http", URL: "https://existing.example/mcp"},
			"db":       {Type: "http", URL: "https://old.example/mcp"},
		},
	}
	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind:    store.KindMCP,
		AgentID: rc.ID,
		Name:    "db",
		Enabled: true,
		Data: map[string]interface{}{
			"type": "http",
			"url":  "https://db.example/mcp",
		},
	}); err != nil {
		t.Fatalf("save db MCP row: %v", err)
	}
	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind:    store.KindMCP,
		AgentID: rc.ID,
		Name:    "disabled",
		Enabled: false,
		Data: map[string]interface{}{
			"type": "http",
			"url":  "https://disabled.example/mcp",
		},
	}); err != nil {
		t.Fatalf("save disabled MCP row: %v", err)
	}

	if err := overlayAgentScopeMCP(ctx, db, rc); err != nil {
		t.Fatalf("overlayAgentScopeMCP: %v", err)
	}
	if got := rc.MCPServers["existing"].URL; got != "https://existing.example/mcp" {
		t.Fatalf("existing MCP should be preserved, got %q", got)
	}
	if got := rc.MCPServers["db"].URL; got != "https://db.example/mcp" {
		t.Fatalf("db MCP should overlay existing entry, got %q", got)
	}
	if _, ok := rc.MCPServers["disabled"]; ok {
		t.Fatalf("disabled MCP row should not be overlaid: %#v", rc.MCPServers)
	}
}

func TestOverlayAgentScopeMCPMergesSystemAndAgentLayers(t *testing.T) {
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()

	rc := &config.ResolvedAgent{ID: "agent-mcp"}

	// System base layer: "shared" (also overridden by agent) + "sysonly".
	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind: store.KindMCP, Name: "shared", Enabled: true,
		Data: map[string]interface{}{"type": "http", "url": "https://system.example/mcp"},
	}); err != nil {
		t.Fatalf("save system shared row: %v", err)
	}
	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind: store.KindMCP, Name: "sysonly", Enabled: true,
		Data: map[string]interface{}{"type": "http", "url": "https://sysonly.example/mcp"},
	}); err != nil {
		t.Fatalf("save system sysonly row: %v", err)
	}
	// Agent layer: same-name "shared" must win; "agentonly" coexists.
	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind: store.KindMCP, AgentID: rc.ID, Name: "shared", Enabled: true,
		Data: map[string]interface{}{"type": "http", "url": "https://agent.example/mcp"},
	}); err != nil {
		t.Fatalf("save agent shared row: %v", err)
	}
	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind: store.KindMCP, AgentID: rc.ID, Name: "agentonly", Enabled: true,
		Data: map[string]interface{}{"type": "http", "url": "https://agentonly.example/mcp"},
	}); err != nil {
		t.Fatalf("save agent agentonly row: %v", err)
	}

	if err := overlayAgentScopeMCP(ctx, db, rc); err != nil {
		t.Fatalf("overlayAgentScopeMCP: %v", err)
	}
	if got := rc.MCPServers["sysonly"].URL; got != "https://sysonly.example/mcp" {
		t.Fatalf("system row should be injected, got %q", got)
	}
	if got := rc.MCPServers["agentonly"].URL; got != "https://agentonly.example/mcp" {
		t.Fatalf("agent row should be injected, got %q", got)
	}
	if got := rc.MCPServers["shared"].URL; got != "https://agent.example/mcp" {
		t.Fatalf("agent row should shadow same-name system row, got %q", got)
	}
}

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
	if aliceAccount.APIKeyID != "owner:"+owner.ID {
		t.Fatalf("unexpected namespace: %q", aliceAccount.APIKeyID)
	}
	if aliceAccount.ExternalID != "telegram:bot-a:111" {
		t.Fatalf("unexpected external id: %q", aliceAccount.ExternalID)
	}
}
