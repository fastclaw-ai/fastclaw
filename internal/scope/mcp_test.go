package scope

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func TestAgentScopeMCPServersReturnsEnabledAgentRows(t *testing.T) {
	db := newMCPTestStore(t)
	ctx := context.Background()
	agentID := "agent-mcp"

	saveMCPRow(t, ctx, db, agentID, "github", true, config.MCPServerConfig{
		Type:    "http",
		URL:     "https://example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer token"},
	})
	saveMCPRow(t, ctx, db, agentID, "filesystem", true, config.MCPServerConfig{
		Type:    "stdio",
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/workspace"},
		Env:     map[string]string{"API_TOKEN": "secret"},
	})

	got, err := AgentScopeMCPServers(ctx, db, agentID)
	if err != nil {
		t.Fatalf("AgentScopeMCPServers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 MCP servers, got %d: %#v", len(got), got)
	}
	if got["github"].URL != "https://example.com/mcp" {
		t.Fatalf("github URL: want https://example.com/mcp, got %q", got["github"].URL)
	}
	if got["filesystem"].Command != "npx" {
		t.Fatalf("filesystem command: want npx, got %q", got["filesystem"].Command)
	}
}

func TestAgentScopeMCPServersSkipsDisabledRows(t *testing.T) {
	db := newMCPTestStore(t)
	ctx := context.Background()
	agentID := "agent-mcp"

	saveMCPRow(t, ctx, db, agentID, "enabled", true, config.MCPServerConfig{Type: "http", URL: "https://enabled.example/mcp"})
	saveMCPRow(t, ctx, db, agentID, "disabled", false, config.MCPServerConfig{Type: "http", URL: "https://disabled.example/mcp"})

	got, err := AgentScopeMCPServers(ctx, db, agentID)
	if err != nil {
		t.Fatalf("AgentScopeMCPServers: %v", err)
	}
	if _, ok := got["disabled"]; ok {
		t.Fatalf("disabled row should be omitted: %#v", got)
	}
	if _, ok := got["enabled"]; !ok {
		t.Fatalf("enabled row missing: %#v", got)
	}
}

func TestAgentScopeMCPServersEmptyAgentIDReturnsEmptyMap(t *testing.T) {
	db := newMCPTestStore(t)
	got, err := AgentScopeMCPServers(context.Background(), db, "")
	if err != nil {
		t.Fatalf("AgentScopeMCPServers: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty map, got %#v", got)
	}
}

func TestAgentScopeMCPServersReturnsErrorForMalformedConfig(t *testing.T) {
	db := newMCPTestStore(t)
	ctx := context.Background()
	err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind:    store.KindMCP,
		AgentID: "agent-mcp",
		Name:    "broken",
		Enabled: true,
		Data: map[string]interface{}{
			"type": 123,
		},
	})
	if err != nil {
		t.Fatalf("save malformed row: %v", err)
	}

	_, err = AgentScopeMCPServers(ctx, db, "agent-mcp")
	if err == nil {
		t.Fatal("want malformed config error, got nil")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Fatalf("error should name broken row, got %q", err.Error())
	}
}

func TestSystemScopeMCPServersReturnsEnabledSystemRows(t *testing.T) {
	db := newMCPTestStore(t)
	ctx := context.Background()

	saveMCPRow(t, ctx, db, "", "shared", true, config.MCPServerConfig{Type: "http", URL: "https://shared.example/mcp"})
	saveMCPRow(t, ctx, db, "", "off", false, config.MCPServerConfig{Type: "http", URL: "https://off.example/mcp"})
	// An agent-scoped row must not leak into the system view.
	saveMCPRow(t, ctx, db, "agent-mcp", "agentonly", true, config.MCPServerConfig{Type: "http", URL: "https://agentonly.example/mcp"})

	got, err := SystemScopeMCPServers(ctx, db)
	if err != nil {
		t.Fatalf("SystemScopeMCPServers: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 system MCP server, got %d: %#v", len(got), got)
	}
	if got["shared"].URL != "https://shared.example/mcp" {
		t.Fatalf("shared URL: want https://shared.example/mcp, got %q", got["shared"].URL)
	}
	if _, ok := got["off"]; ok {
		t.Fatalf("disabled system row should be omitted: %#v", got)
	}
	if _, ok := got["agentonly"]; ok {
		t.Fatalf("agent-scoped row leaked into system view: %#v", got)
	}
}

func TestSystemScopeMCPServersNilStoreReturnsEmptyMap(t *testing.T) {
	got, err := SystemScopeMCPServers(context.Background(), nil)
	if err != nil {
		t.Fatalf("SystemScopeMCPServers: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty map, got %#v", got)
	}
}

func newMCPTestStore(t *testing.T) *store.DBStore {
	t.Helper()
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func saveMCPRow(t *testing.T, ctx context.Context, db *store.DBStore, agentID, name string, enabled bool, cfg config.MCPServerConfig) {
	t.Helper()
	data := map[string]interface{}{
		"type": cfg.Type,
	}
	if cfg.URL != "" {
		data["url"] = cfg.URL
	}
	if cfg.Headers != nil {
		data["headers"] = cfg.Headers
	}
	if cfg.Command != "" {
		data["command"] = cfg.Command
	}
	if cfg.Args != nil {
		data["args"] = cfg.Args
	}
	if cfg.Env != nil {
		data["env"] = cfg.Env
	}
	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind:    store.KindMCP,
		AgentID: agentID,
		Name:    name,
		Enabled: enabled,
		Data:    data,
	}); err != nil {
		t.Fatalf("save MCP row %s: %v", name, err)
	}
}
