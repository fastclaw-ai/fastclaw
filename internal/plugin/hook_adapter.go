package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// hookPointName maps HookPoint constants to snake_case protocol names.
var hookPointName = map[agent.HookPoint]string{
	agent.BeforeModelCall: "before_model_call",
	agent.AfterModelCall:  "after_model_call",
	agent.BeforeToolCall:  "before_tool_call",
	agent.AfterToolCall:   "after_tool_call",
	agent.PostTurn:        "post_turn",
}

// hookPointFromName maps protocol snake_case names to HookPoint constants.
var hookPointFromName = map[string]agent.HookPoint{
	"before_model_call": agent.BeforeModelCall,
	"after_model_call":  agent.AfterModelCall,
	"before_tool_call":  agent.BeforeToolCall,
	"after_tool_call":   agent.AfterToolCall,
	"post_turn":         agent.PostTurn,
}

// syncHookPoints are hook points where we wait for the plugin response.
var syncHookPoints = map[agent.HookPoint]bool{
	agent.BeforeModelCall: true,
	agent.BeforeToolCall:  true,
}

const hookCallTimeout = 10 * time.Second

// RegisterPluginHooks queries a hook plugin for its desired hook points and
// registers HookFuncs in the agent's hook registry that forward events to the plugin.
func RegisterPluginHooks(ctx context.Context, mgr *Manager, pluginID string, registry *agent.HookRegistry, agentName string) error {
	inst := mgr.Plugin(pluginID)
	if inst == nil || inst.Process == nil || !inst.Process.IsRunning() {
		return fmt.Errorf("plugin %s not running", pluginID)
	}

	// Ask the plugin which hook points it wants
	result, err := inst.Process.Call(ctx, MethodHookRegister, nil)
	if err != nil {
		return fmt.Errorf("hook.register call to %s: %w", pluginID, err)
	}

	var reg HookRegisterResult
	if err := json.Unmarshal(result, &reg); err != nil {
		return fmt.Errorf("parse hook.register response from %s: %w", pluginID, err)
	}

	for _, pointName := range reg.Points {
		hp, ok := hookPointFromName[pointName]
		if !ok {
			slog.Warn("plugin: unknown hook point", "plugin", pluginID, "point", pointName)
			continue
		}

		// Capture loop variables
		capturedHP := hp
		capturedPointName := pointName
		proc := inst.Process

		registry.Register(capturedHP, func(ctx context.Context, hc *agent.HookContext) {
			params := buildHookFireParams(capturedPointName, hc)

			if syncHookPoints[capturedHP] {
				// Synchronous: call and wait for modified messages
				callCtx, cancel := context.WithTimeout(ctx, hookCallTimeout)
				defer cancel()

				raw, err := proc.Call(callCtx, MethodHookFire, params)
				if err != nil {
					slog.Warn("plugin: hook.fire call failed",
						"plugin", pluginID, "point", capturedPointName, "error", err)
					return
				}

				var fireResult HookFireResult
				if err := json.Unmarshal(raw, &fireResult); err != nil {
					slog.Warn("plugin: hook.fire result parse failed",
						"plugin", pluginID, "point", capturedPointName, "error", err)
					return
				}

				// If the plugin returned modified messages, apply them
				if len(fireResult.Messages) > 0 {
					hc.Messages = hookMessagesToProvider(fireResult.Messages)
				}
			} else {
				// Asynchronous: fire and forget
				if err := proc.Notify(MethodHookFire, params); err != nil {
					slog.Warn("plugin: hook.fire notify failed",
						"plugin", pluginID, "point", capturedPointName, "error", err)
				}
			}
		})

		slog.Info("plugin: registered hook",
			"plugin", pluginID, "point", capturedPointName, "agent", agentName)
	}

	return nil
}

// buildHookFireParams constructs HookFireParams from a HookContext.
func buildHookFireParams(pointName string, hc *agent.HookContext) HookFireParams {
	params := HookFireParams{
		Point:      pointName,
		AgentName:  hc.AgentName,
		ChatID:     hc.ChatID,
		UserID:     hc.UserID,
		ToolName:   hc.ToolName,
		ToolArgs:   hc.ToolArgs,
		ToolResult: hc.ToolResult,
	}

	// Serialize messages
	if len(hc.Messages) > 0 {
		msgs := make([]HookMessage, 0, len(hc.Messages))
		for _, m := range hc.Messages {
			hm := HookMessage{
				Role:       m.Role,
				Content:    m.Content,
				ToolCallID: m.ToolCallID,
				Name:       m.Name,
			}
			if len(m.ToolCalls) > 0 {
				if tc, err := json.Marshal(m.ToolCalls); err == nil {
					hm.ToolCalls = tc
				}
			}
			msgs = append(msgs, hm)
		}
		params.Messages = msgs
	}

	// Serialize response
	if hc.Response != nil {
		params.Response = &HookResponseData{
			Content:  hc.Response.Content,
			HasTools: hc.Response.HasToolCalls(),
		}
	}

	return params
}

// hookMessagesToProvider converts HookMessages back to provider.Messages.
func hookMessagesToProvider(msgs []HookMessage) []provider.Message {
	result := make([]provider.Message, 0, len(msgs))
	for _, hm := range msgs {
		pm := provider.Message{
			Role:       hm.Role,
			Content:    hm.Content,
			ToolCallID: hm.ToolCallID,
			Name:       hm.Name,
		}
		if len(hm.ToolCalls) > 0 {
			var tcs []provider.ToolCall
			if err := json.Unmarshal(hm.ToolCalls, &tcs); err == nil {
				pm.ToolCalls = tcs
			}
		}
		result = append(result, pm)
	}
	return result
}
