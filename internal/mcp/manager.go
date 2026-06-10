package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/buildinfo"
	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// Manager manages connections to multiple MCP servers.
type Manager struct {
	servers map[string]Client // serverName -> client
	// toolMap maps prefixed tool name -> (serverName, originalToolName)
	toolMap map[string]toolRoute
}

type toolRoute struct {
	serverName   string
	originalName string
}

// NewManager creates an MCP manager and connects to all configured servers.
// Servers that fail to connect are logged as warnings but don't block startup.
func NewManager(servers map[string]config.MCPServerConfig) *Manager {
	m := &Manager{
		servers: make(map[string]Client),
		toolMap: make(map[string]toolRoute),
	}

	for name, cfg := range servers {
		var client Client
		switch cfg.Type {
		case "http":
			if buildinfo.IsHostedDeploy() && isBlockedHostedHTTPMCPURL(cfg.URL) {
				slog.Warn("HTTP MCP server target disabled on hosted deployment, skipping", "server", name, "url", cfg.URL)
				continue
			}
			client = NewHTTPClient(cfg.URL, cfg.Headers)
		case "stdio":
			if buildinfo.IsHostedDeploy() {
				slog.Warn("stdio MCP server disabled on hosted deployment, skipping", "server", name)
				continue
			}
			client = NewStdioClient(cfg.Command, cfg.Args, cfg.Env)
		default:
			slog.Warn("unknown MCP server type, skipping", "server", name, "type", cfg.Type)
			continue
		}

		if err := client.Connect(); err != nil {
			slog.Warn("failed to connect to MCP server, skipping", "server", name, "error", err)
			continue
		}

		tools, err := client.ListTools()
		if err != nil {
			slog.Warn("failed to list MCP tools, skipping", "server", name, "error", err)
			client.Close()
			continue
		}

		m.servers[name] = client

		for _, t := range tools {
			prefixed := prefixToolName(name, t.Name)
			m.toolMap[prefixed] = toolRoute{
				serverName:   name,
				originalName: t.Name,
			}
		}

		slog.Info("connected to MCP server", "server", name, "tools", len(tools))
	}

	return m
}

// ToolDefs returns tool definitions for all MCP tools, with prefixed names.
func (m *Manager) ToolDefs() []ToolDef {
	var defs []ToolDef
	for name, cfg := range m.servers {
		tools, err := cfg.ListTools()
		if err != nil {
			slog.Warn("failed to list tools from MCP server", "server", name, "error", err)
			continue
		}
		for _, t := range tools {
			defs = append(defs, ToolDef{
				Name:        prefixToolName(name, t.Name),
				Description: t.Description,
				InputSchema: t.InputSchema,
			})
		}
	}
	return defs
}

// CallTool routes a prefixed tool call to the correct MCP server.
func (m *Manager) CallTool(_ context.Context, prefixedName string, args json.RawMessage) (string, error) {
	route, ok := m.toolMap[prefixedName]
	if !ok {
		return "", fmt.Errorf("unknown MCP tool: %s", prefixedName)
	}

	client, ok := m.servers[route.serverName]
	if !ok {
		return "", fmt.Errorf("MCP server not connected: %s", route.serverName)
	}

	return client.CallTool(route.originalName, args)
}

// HasTools returns true if any MCP tools are available.
func (m *Manager) HasTools() bool {
	return len(m.toolMap) > 0
}

// Close shuts down all MCP server connections.
func (m *Manager) Close() {
	for name, client := range m.servers {
		if err := client.Close(); err != nil {
			slog.Warn("error closing MCP server", "server", name, "error", err)
		}
	}
}

func isBlockedHostedHTTPMCPURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return true
	}
	host := strings.TrimSpace(strings.ToLower(u.Hostname()))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

func prefixToolName(serverName, toolName string) string {
	// Sanitize server name: replace non-alphanumeric with _
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, serverName)
	return "mcp_" + safe + "_" + toolName
}
