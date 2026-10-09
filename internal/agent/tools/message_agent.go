package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// MessageAgentFunc delivers message to the agent named by ref and returns
// the tool result text (including the recipient's reply).
type MessageAgentFunc func(ctx context.Context, ref, message string) (string, error)

// RegisterMessageAgent adds message_agent: a real private message to
// another of the owner's agents. The recipient answers in its own direct
// chat (where a copy is kept, labelled as a private message from the
// caller) and its reply comes back as working context.
func RegisterMessageAgent(r *Registry, send MessageAgentFunc) {
	if r == nil || send == nil {
		return
	}
	r.Register(
		"message_agent",
		"Send a real private message to another of the user's agents. Use it when the user asks you to contact, ask, tell or send something to another agent, "+
			"or to consult a specialist agent. The recipient runs in its own chat, where a copy is kept, and its answer is returned to you. "+
			"Answer the user in your own voice, attributing the findings to the recipient; do not paste its reply verbatim. "+
			"Never pretend to have contacted an agent without calling this tool, and do not ask the user to relay the message. "+
			"Only reaches FastClaw agents: command-line coding tools on the host such as codex or claude (Claude Code) are not agents — run them with exec (see the local-coding-agents skill).",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"agent": map[string]interface{}{
					"type":        "string",
					"description": "The exact name or id of the target agent.",
				},
				"message": map[string]interface{}{
					"type":        "string",
					"description": "A self-contained question or task for the target agent.",
				},
			},
			"required": []string{"agent", "message"},
		},
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var p struct {
				Agent   string `json:"agent"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return "", err
			}
			if p.Agent == "" {
				return "", fmt.Errorf("agent is required")
			}
			return send(ctx, p.Agent, p.Message)
		},
	)
}
