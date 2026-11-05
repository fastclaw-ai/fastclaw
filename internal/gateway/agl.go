package gateway

import (
	"context"
	"log/slog"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/agl"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Runtime "agl" agents: IM messages for them never reach the LLM loop.
// They are handed to the agl bridge, which runs the turn on the owner's
// own computer (docs/agl-machines.md).

// aglSender delivers bridge replies through the bus, in order, and drives
// the channel's typing indicator.
type aglSender struct{ g *Gateway }

func (s aglSender) Send(ctx context.Context, out bus.OutboundMessage) {
	select {
	case s.g.bus.Outbound <- out:
	case <-ctx.Done():
		slog.Warn("agl: reply dropped: outbound busy", "channel", out.Channel, "chat_id", out.ChatID)
	}
}

func (s aglSender) Typing(channel, accountID, chatID string) {
	if s.g.chanMgr != nil {
		s.g.chanMgr.SendTyping(channel, accountID, chatID)
	}
}

// EnableAgl routes IM messages of runtime "agl" agents to machines
// linked through hub. Call before Run.
func (g *Gateway) EnableAgl(hub *agl.Hub) {
	ms, _ := g.store.(store.MachineStore)
	if hub == nil || ms == nil {
		return
	}
	g.aglBridge = agl.NewBridge(hub, ms, aglSender{g: g})
	// Tool mode: LLM agents dispatch to the chatter's own computer.
	g.aglTools = agl.NewTools(hub, ms, aglSender{g: g})
	agent.SetLocalAgents(g.aglTools)
}

// interceptToolApproval takes a 1/2/3 reply that answers a permission
// request raised by a running run_local_agent call in this chat. It runs
// before routing, so the reply never starts, joins or steers an LLM turn
// — the turn that asked is still blocked in the tool call.
func (g *Gateway) interceptToolApproval(ctx context.Context, msg bus.InboundMessage) bool {
	if g.aglTools == nil || msg.PeerKind == "group" || msg.Source != bus.SourceUser || msg.IsBotMessage {
		return false
	}
	chat := agl.ToolChat{Channel: msg.Channel, AccountID: msg.AccountID, ChatID: msg.ChatID}
	if _, ok := agl.ApprovalDecision(msg.Text); !ok || !g.aglTools.HasPendingApproval(chat) {
		return false
	}
	if g.store != nil && !g.aglSenderAllowed(ctx, msg) {
		return false
	}
	return g.aglTools.InterceptApproval(ctx, chat, msg.OwnerUserID, msg.Text)
}

// machineUserFor returns whose linked computers an LLM turn for msg may
// dispatch to (bus.InboundMessage.MachineUserID), or "": a 1:1 IM
// message from a person typing, in a channel that belongs to the user
// space it was routed to (never a channel inherited from an agent's
// owner), from the channel's bound sender when the channel knows one.
// The agent then offers the tools only when that user has a machine
// that allows agents.
func (g *Gateway) machineUserFor(ctx context.Context, msg bus.InboundMessage) string {
	if g.aglTools == nil || g.store == nil || msg.OwnerUserID == "" || msg.PeerKind == "group" ||
		msg.Source != bus.SourceUser || msg.IsBotMessage || msg.SharedIdentity || msg.Channel == "" || msg.Channel == "web" {
		return ""
	}
	ch, err := g.store.LookupChannel(ctx, msg.Channel, msg.AccountID)
	if err != nil || ch == nil || ch.UserID == "" || ch.UserID != msg.OwnerUserID {
		return ""
	}
	if ch.BoundUserID != "" && !msg.FromChannelOwner {
		return ""
	}
	return msg.OwnerUserID
}

// aglAgentFor returns the agent msg would be routed to in a DM when that
// agent has runtime "agl" — the explicit target, else the first matching
// binding (matchAgent's order) — or nil.
func (g *Gateway) aglAgentFor(ctx context.Context, space *UserSpace, msg bus.InboundMessage) *store.AgentRecord {
	if g.aglBridge == nil || g.store == nil || space == nil {
		return nil
	}
	agentID := msg.AgentID
	if agentID == "" && space.Config != nil {
		for _, b := range space.Config.Bindings {
			if matchBinding(b.Match, msg) {
				agentID = b.AgentID
				break
			}
		}
	}
	if agentID == "" {
		return nil
	}
	rec, err := g.store.GetAgent(ctx, agentID)
	if err != nil || !agl.IsAglAgent(rec) {
		return nil
	}
	return rec
}

// routeAgl hands a DM for a runtime "agl" agent to the bridge. It
// returns true when msg was taken (run, refused or dropped) and must not
// reach the LLM.
func (g *Gateway) routeAgl(ctx context.Context, space *UserSpace, msg bus.InboundMessage) bool {
	rec := g.aglAgentFor(ctx, space, msg)
	if rec == nil {
		return false
	}
	// Only people typing in an IM chat start agl turns: no web chat,
	// cron, heartbeat or sub-agent turns.
	if msg.Source != bus.SourceUser || msg.Channel == "web" || msg.Channel == "" {
		slog.Info("agl: non-IM message for agl agent dropped",
			"agent", rec.ID, "channel", msg.Channel, "source", msg.Source)
		return true
	}
	g.aglBridge.Handle(agl.Inbound{
		Agent:         rec,
		SpaceUserID:   msg.OwnerUserID,
		Channel:       msg.Channel,
		AccountID:     msg.AccountID,
		ChatID:        msg.ChatID,
		MessageID:     msg.MessageID,
		Text:          msg.Text,
		PeerKind:      msg.PeerKind,
		SenderAllowed: g.aglSenderAllowed(ctx, msg),
	})
	return true
}

// aglSenderAllowed: when the channel knows who connected it (the WeChat
// QR scanner, or the /pair sender), only that person may drive the
// computer.
func (g *Gateway) aglSenderAllowed(ctx context.Context, msg bus.InboundMessage) bool {
	ch, err := g.store.LookupChannel(ctx, msg.Channel, msg.AccountID)
	if err != nil || ch == nil || ch.BoundUserID == "" {
		return true
	}
	return msg.FromChannelOwner
}

// withoutAglAgents drops runtime "agl" agents from a group's candidates:
// they only run in 1:1 chats. refused reports whether one was dropped.
func (g *Gateway) withoutAglAgents(ctx context.Context, agents []*agent.Agent) (kept []*agent.Agent, refused bool) {
	if g.aglBridge == nil || g.store == nil {
		return agents, false
	}
	kept = agents[:0:0]
	for _, ag := range agents {
		if rec, err := g.store.GetAgent(ctx, ag.Name()); err == nil && agl.IsAglAgent(rec) {
			refused = true
			continue
		}
		kept = append(kept, ag)
	}
	return kept, refused
}

// aglGroupRefusal answers a group message that addressed an agl agent.
func (g *Gateway) aglGroupRefusal(ctx context.Context, msg bus.InboundMessage) {
	if msg.IsBotMessage || len(msg.Mentions) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	g.replyInbound(ctx, msg, agl.MsgDMOnly)
}
