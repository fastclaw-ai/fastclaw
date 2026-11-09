package agent

import (
	"context"

	"github.com/fastclaw-ai/fastclaw/internal/billing"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// turnChannel is the usage-event channel of a turn: "cron" for a
// scheduled job, else the channel the message arrived on ("api" for /v1),
// "task" for a queued turn that names none.
func turnChannel(msg bus.InboundMessage) string {
	if msg.Source == bus.SourceCron {
		return "cron"
	}
	if msg.Channel != "" {
		return msg.Channel
	}
	return "task"
}

// beginBillingTurn starts the turn's metering scope and asks the paying
// account's billing hook whether the turn may run. A turn nested in
// another (sub-agent, message_agent, a /v1 turn authorized up front by
// AuthorizeTurn) keeps the outer turn's id and isn't asked again. The
// returned ctx carries the turn; a non-empty string is the refusal to
// show the user instead of running the turn.
func (a *Agent) beginBillingTurn(ctx context.Context, msg bus.InboundMessage) (context.Context, string) {
	return a.beginBillingTurnKeyed(ctx, msg, "")
}

// beginBillingTurnKeyed is beginBillingTurn with the session key events
// carry: sessionKey, or "" for the agent's own key of the session.
func (a *Agent) beginBillingTurnKeyed(ctx context.Context, msg bus.InboundMessage, sessionKey string) (context.Context, string) {
	if parent := billing.TurnFrom(ctx); parent != nil {
		if parent.AgentID == a.agentID && parent.UserID == a.ownerUserID && parent.Prepared {
			// This very turn, authorized before the loop (AuthorizeTurn).
			return ctx, ""
		}
		return billing.WithTurn(ctx, parent.Child(a.billing, a.ownerUserID, a.agentID, a.billingSessionKey(msg), turnChannel(msg))), ""
	}
	if sessionKey == "" {
		sessionKey = a.billingSessionKey(msg)
	}
	channel := turnChannel(msg)
	turn := a.billing.NewTurn(a.ownerUserID, a.agentID, sessionKey, channel)
	ctx = billing.WithTurn(ctx, turn)
	prov, mdl := a.meterProviderModel(a.model)
	return ctx, turn.AuthorizeTurn(ctx, prov, mdl)
}

// AuthorizeTurn runs the turn-stage billing authorization for msg before
// the turn starts, for callers that must answer a refusal differently
// from a reply (/v1 returns 402). It returns ctx carrying the authorized
// turn — pass it to HandleMessage, which then won't ask again — the
// turn's id, and the refusal message ("" when allowed).
func (a *Agent) AuthorizeTurn(ctx context.Context, msg bus.InboundMessage) (context.Context, string, string) {
	t := *a // shallow: only the turn model changes
	t.applyTurnModel(ctx)
	// Over /v1 the session is the caller's (X-Fastclaw-Session-Key), so
	// that's the key its events carry.
	ctx, deny := t.beginBillingTurnKeyed(ctx, msg, msg.ChatID)
	if turn := billing.TurnFrom(ctx); turn != nil {
		turn.Prepared = true
	}
	return ctx, billing.TurnID(ctx), deny
}

// billingSessionKey is the agent's own key of msg's session.
func (a *Agent) billingSessionKey(msg bus.InboundMessage) string {
	if a.sessions == nil {
		return ""
	}
	if sess := a.sessions.Get(sessionTriple(msg, msg.ProjectID)); sess != nil {
		return sess.Key()
	}
	return ""
}

// meterLLMEvent records one model call as an `llm` usage event: against
// the turn in ctx, or — for calls outside a turn (slash commands,
// background work) — against this agent with no turn id.
func (a *Agent) meterLLMEvent(ctx context.Context, sessionKey, prov, model string, u provider.Usage, cost *float64) {
	ev := billing.Event{
		Kind: billing.KindLLM, Provider: prov, Model: model,
		InputTokens: int64(u.InputTokens), OutputTokens: int64(u.OutputTokens),
		CacheReadTokens: int64(u.CacheReadTokens), CacheCreationTokens: int64(u.CacheCreationTokens),
		CostUSD: cost, Detail: map[string]any{},
	}
	if t := billing.TurnFrom(ctx); t != nil {
		t.Record(ctx, ev)
		return
	}
	a.billing.Record(ctx, billing.Who{
		UserID: a.ownerUserID, AgentID: a.agentID, SessionKey: sessionKey,
		Channel: store.ChannelFromContext(ctx),
	}, ev)
}
