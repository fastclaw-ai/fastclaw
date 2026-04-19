package gateway

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// chatKey returns the serialization key for a chat.
func chatKey(channel, chatID string) string {
	return channel + ":" + chatID
}

func (g *Gateway) processInbound(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-g.bus.Inbound:
			// When the message targets a specific user (e.g. from a cron job
			// in cloud mode), resolve that user's agent manager for routing.
			if msg.OwnerUserID != "" && msg.OwnerUserID != config.DefaultUserID {
				g.routeToUserSpace(ctx, msg)
				continue
			}

			// For DMs, use existing binding-based routing
			if msg.PeerKind != "group" {
				g.routeDM(ctx, msg)
				continue
			}

			// Deduplicate group messages (multiple bots receive the same message)
			if g.isDuplicate(msg) {
				slog.Info("dropping duplicate group message",
					"channel", msg.Channel,
					"chat_id", msg.ChatID,
					"message_id", msg.MessageID,
				)
				continue
			}

			// Group message handling
			slog.Info("group message accepted", "message_id", msg.MessageID, "account", msg.AccountID, "chat_id", msg.ChatID, "is_bot", msg.IsBotMessage)
			g.routeGroup(ctx, msg)
		}
	}
}

// routeDM handles direct message routing (existing behavior).
func (g *Gateway) routeDM(ctx context.Context, msg bus.InboundMessage) {
	ag := g.matchAgent(msg)
	if ag == nil {
		slog.Warn("no agent matched for DM, dropping",
			"channel", msg.Channel,
			"account", msg.AccountID,
			"chat_id", msg.ChatID,
		)
		return
	}

	slog.Info("routing DM",
		"channel", msg.Channel,
		"account", msg.AccountID,
		"chat_id", msg.ChatID,
		"agent", ag.Name(),
	)

	g.taskQueue.Submit(ag.Name(), chatKey(msg.Channel, msg.ChatID), msg, msg.AccountID)
}

// routeGroup handles group message routing with mention-based and team-aware logic.
func (g *Gateway) routeGroup(ctx context.Context, msg bus.InboundMessage) {
	// Find all agents bound to this group chat
	boundAgents := g.agentsBoundToMessage(msg)

	if len(boundAgents) == 0 {
		slog.Warn("no agents bound for group message, dropping",
			"channel", msg.Channel,
			"chat_id", msg.ChatID,
		)
		return
	}

	// If message is from a bot, inject into all agents for awareness,
	// and also trigger any @mentioned agent to respond.
	if msg.IsBotMessage {
		slog.Info("processing bot message in group",
			"sender", msg.SenderName,
			"chat_id", msg.ChatID,
			"mentions", msg.Mentions,
			"agents_count", len(boundAgents),
		)

		// Inject into all agents for awareness
		for _, ag := range boundAgents {
			ag.InjectGroupMessage(ctx, msg)
		}

		// If this bot message @mentions another agent, trigger that agent to respond
		if len(msg.Mentions) > 0 {
			target := g.agentByMention(msg.Mentions, boundAgents)
			if target != nil {
				slog.Info("bot message triggers mentioned agent",
					"sender", msg.SenderName,
					"target", target.Name(),
					"chat_id", msg.ChatID,
				)

				// Build a trigger message with the sender bot's name as context
				triggerMsg := msg
				triggerMsg.Text = fmt.Sprintf("[%s]: %s", msg.SenderName, msg.Text)
				triggerMsg.IsBotMessage = false // treat as actionable for HandleMessage

				g.taskQueue.Submit(target.Name(), chatKey(triggerMsg.Channel, triggerMsg.ChatID), triggerMsg, g.accountIDForAgent(target.Name(), triggerMsg.Channel))
			}
		}
		return
	}

	// If message has @mentions, only route to the mentioned agent
	if len(msg.Mentions) > 0 {
		target := g.agentByMention(msg.Mentions, boundAgents)
		if target != nil {
			slog.Info("routing group message by @mention",
				"chat_id", msg.ChatID,
				"agent", target.Name(),
				"mentions", msg.Mentions,
			)

			// Inject into other agents for awareness (without triggering reply)
			for _, ag := range boundAgents {
				if ag.Name() != target.Name() {
					ag.InjectGroupMessage(ctx, msg)
				}
			}

			g.taskQueue.Submit(target.Name(), chatKey(msg.Channel, msg.ChatID), msg, g.accountIDForAgent(target.Name(), msg.Channel))
			return
		}
		// Mentioned username doesn't match any agent — fall through to default behavior
	}

	// No @mention: use team groupBehavior
	behavior, defaultAgentID := g.groupBehaviorFor(boundAgents)

	switch behavior {
	case "default-agent":
		target := g.agents.AgentByID(defaultAgentID)
		if target == nil {
			// Fallback: use first bound agent
			target = boundAgents[0]
		}

		slog.Info("routing group message to default agent",
			"chat_id", msg.ChatID,
			"agent", target.Name(),
		)

		// Inject into other agents for awareness
		for _, ag := range boundAgents {
			if ag.Name() != target.Name() {
				ag.InjectGroupMessage(ctx, msg)
			}
		}

		g.taskQueue.Submit(target.Name(), chatKey(msg.Channel, msg.ChatID), msg, g.accountIDForAgent(target.Name(), msg.Channel))

	default: // "mention-only"
		// No @mention and behavior is mention-only: inject into all agents for awareness, but no reply
		slog.Info("group message without mention (mention-only mode), injecting for awareness",
			"chat_id", msg.ChatID,
			"agents_count", len(boundAgents),
		)
		for _, ag := range boundAgents {
			ag.InjectGroupMessage(ctx, msg)
		}
	}
}

// routeToUserSpace dispatches a message to a non-local user's agent manager.
// Used when cron jobs or webhooks target a specific user in cloud mode.
func (g *Gateway) routeToUserSpace(ctx context.Context, msg bus.InboundMessage) {
	space, err := g.users.getOrLoad(msg.OwnerUserID)
	if err != nil {
		slog.Warn("failed to load user space for routing",
			"user", msg.OwnerUserID, "error", err)
		return
	}
	mgr := space.Agents

	// Find agent: try binding-level agentID in channel (cron sets it),
	// then default or first.
	var ag *agent.Agent
	if def := mgr.DefaultAgent(); def != nil {
		ag = def
	} else if all := mgr.All(); len(all) > 0 {
		ag = all[0]
	}
	if ag == nil {
		slog.Warn("user has no agents", "user", msg.OwnerUserID)
		return
	}
	slog.Info("routing to user space",
		"user", msg.OwnerUserID, "agent", ag.Name(), "channel", msg.Channel)
	g.taskQueue.Submit(ag.Name(), chatKey(msg.Channel, msg.ChatID), msg, msg.AccountID)
}

// matchAgent evaluates bindings top-to-bottom and returns the first matching agent.
// Falls back to the default agent if no bindings are defined.
func (g *Gateway) matchAgent(msg bus.InboundMessage) *agent.Agent {
	if len(g.bindings) == 0 {
		return g.agents.DefaultAgent()
	}

	for _, b := range g.bindings {
		if !matchBinding(b.Match, msg) {
			continue
		}
		ag := g.agents.AgentByID(b.AgentID)
		if ag != nil {
			return ag
		}
		slog.Warn("binding references unknown agent", "agentId", b.AgentID)
	}

	// No binding matched — fall back to default
	return g.agents.DefaultAgent()
}

func matchBinding(m config.Match, msg bus.InboundMessage) bool {
	if m.Channel != "" && m.Channel != msg.Channel {
		return false
	}
	if m.AccountID != "" && m.AccountID != msg.AccountID {
		return false
	}
	if m.Peer != nil {
		if m.Peer.Kind != "" && m.Peer.Kind != msg.PeerKind {
			return false
		}
		if m.Peer.ID != "" && m.Peer.ID != msg.ChatID {
			return false
		}
	}
	return true
}

// agentsBoundToMessage returns all agents whose bindings match this message.
func (g *Gateway) agentsBoundToMessage(msg bus.InboundMessage) []*agent.Agent {
	if len(g.bindings) == 0 {
		if def := g.agents.DefaultAgent(); def != nil {
			return []*agent.Agent{def}
		}
		return nil
	}

	seen := make(map[string]bool)
	var result []*agent.Agent
	for _, b := range g.bindings {
		if !matchBinding(b.Match, msg) {
			continue
		}
		if seen[b.AgentID] {
			continue
		}
		ag := g.agents.AgentByID(b.AgentID)
		if ag != nil {
			seen[b.AgentID] = true
			result = append(result, ag)
		}
	}
	return result
}

// agentByMention finds the agent whose bot username matches one of the @mentions.
func (g *Gateway) agentByMention(mentions []string, candidates []*agent.Agent) *agent.Agent {
	for _, mention := range mentions {
		for _, ag := range candidates {
			botUsername, ok := g.botUsernames[ag.Name()]
			if ok && botUsername == mention {
				return ag
			}
		}
	}
	return nil
}

// gatewaySubAgentSpawner implements tools.SubAgentSpawner.
type gatewaySubAgentSpawner struct {
	agents *agent.Manager
}

func (s *gatewaySubAgentSpawner) SpawnSubAgent(ctx context.Context, agentID string, msg bus.InboundMessage) string {
	ag := s.agents.AgentByID(agentID)
	if ag == nil {
		return fmt.Sprintf("Error: agent %q not found", agentID)
	}
	return ag.HandleMessage(ctx, msg)
}

// Ensure gatewaySubAgentSpawner satisfies the interface.
var _ tools.SubAgentSpawner = (*gatewaySubAgentSpawner)(nil)

// webhookAgentHandler implements webhook.AgentHandler.
type webhookAgentHandler struct {
	agents *agent.Manager
}

func (h *webhookAgentHandler) HandleMessage(ctx context.Context, agentID string, msg bus.InboundMessage) (string, error) {
	ag := h.agents.AgentByID(agentID)
	if ag == nil {
		return "", fmt.Errorf("agent %q not found", agentID)
	}
	reply := ag.HandleMessage(ctx, msg)
	return reply, nil
}
