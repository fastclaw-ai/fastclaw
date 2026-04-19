package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
)

// RegisterPluginTools queries a tool plugin for its tools and registers them
// in the given tool registry. If a plugin tool has the same name as a built-in
// tool, the plugin version overrides the built-in. Otherwise, the tool is
// registered with a qualified name (e.g. "echo.echo_tool").
func RegisterPluginTools(ctx context.Context, mgr *Manager, pluginID string, registry *tools.Registry) error {
	toolDefs, err := mgr.ListTools(ctx, pluginID)
	if err != nil {
		return fmt.Errorf("list tools from plugin %s: %w", pluginID, err)
	}

	for _, td := range toolDefs {
		desc := td.Description
		params := td.Parameters
		toolName := td.Name

		fn := func(ctx context.Context, args json.RawMessage) (string, error) {
			var argsMap map[string]interface{}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &argsMap); err != nil {
					return "", fmt.Errorf("parse tool args: %w", err)
				}
			}
			if argsMap == nil {
				argsMap = make(map[string]interface{})
			}
			return mgr.ExecuteTool(ctx, pluginID, toolName, argsMap)
		}

		// If the plugin provides a tool with the same name as a built-in,
		// override the built-in with the plugin version.
		if registry.HasBuiltin(toolName) {
			registry.RegisterFrom(toolName, desc, params, fn, tools.SourcePlugin)
			slog.Info("plugin: overriding built-in tool", "plugin", pluginID, "tool", toolName)
		} else {
			qualifiedName := pluginID + "." + toolName
			registry.RegisterFrom(qualifiedName, desc, params, fn, tools.SourcePlugin)
			slog.Info("plugin: registered tool", "plugin", pluginID, "tool", qualifiedName)
		}
	}

	return nil
}
