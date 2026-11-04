package agent

import (
	"context"
	"log/slog"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/usage"
)

type meterSessionKey struct{}

// withMeterSession records the turn's session key so model calls made
// below the turn loop (delegate_task sub-agents) meter against it.
func withMeterSession(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, meterSessionKey{}, key)
}

func meterSessionFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	key, _ := ctx.Value(meterSessionKey{}).(string)
	return key
}

// meterProviderModel splits the model a call ran on into the provider and
// model columns. A bare model id (an auto-persist / skills-learner model
// set without a prefix) ran on the agent's own provider.
func (a *Agent) meterProviderModel(model string) (prov, id string) {
	prov, id = provider.SplitProviderModel(model)
	if prov != "" {
		if _, known := a.providers[prov]; known || len(a.providers) == 0 {
			return prov, id
		}
	}
	// No prefix, or one that names no provider (a router-style id such as
	// "anthropic/claude-…"): the whole string ran on the agent's provider.
	cur, _ := provider.SplitProviderModel(a.model)
	return cur, model
}

// modelPrice resolves the unit price of a model as this agent runs it:
// the provider's model entry when the admin priced it, else the built-in
// default table. false means no price is known — the call's cost is
// unknown, never 0.
func (a *Agent) modelPrice(model string) (usage.Price, bool) {
	prov, id := a.meterProviderModel(model)
	if pc, ok := a.providers[prov]; ok {
		if p, ok := priceInModels(pc.Models, id); ok {
			return p, true
		}
	}
	return usage.DefaultPrice(id)
}

func priceInModels(models []config.ModelEntry, id string) (usage.Price, bool) {
	for _, m := range models {
		if !strings.EqualFold(m.ID, id) {
			continue
		}
		p := usage.Price{
			Input:      m.Cost.Input,
			Output:     m.Cost.Output,
			CacheRead:  m.Cost.CacheRead,
			CacheWrite: m.Cost.CacheWrite,
		}
		if p.IsSet() {
			return p, true
		}
	}
	return usage.Price{}, false
}

// meterTokens records one model call: the request's usage collector (the
// /v1 response's usage and cost), the daily buckets, and one
// token_usage_log row. model is the model the call actually ran on — the
// turn model, or a compaction / memory / skills-learner model — so the
// cost uses that model's price. Safe with zero usage (still counts the
// request). Errors are logged, never propagated: metering must not break
// the chat path. durationMs is 0 when the call wasn't timed.
func (a *Agent) meterTokens(ctx context.Context, sessionKey, model string, u provider.Usage, durationMs int64) {
	t := usage.Tokens{
		Input:         u.InputTokens,
		Output:        u.OutputTokens,
		CacheRead:     u.CacheReadTokens,
		CacheCreation: u.CacheCreationTokens,
	}
	var cost *float64
	if price, ok := a.modelPrice(model); ok {
		c := price.Cost(t)
		cost = &c
	}
	if cost != nil {
		usage.CollectorFrom(ctx).Add(t, *cost, true)
	} else {
		usage.CollectorFrom(ctx).Add(t, 0, false)
	}
	if a.meter == nil {
		return
	}
	prov, mdl := a.meterProviderModel(model)
	if err := a.meter.RecordTokens(ctx, a.ownerUserID, a.agentID, sessionKey, prov, mdl, t); err != nil {
		slog.Warn("meter record failed", "agent", a.name, "error", err)
	}
	if err := a.meter.RecordTokenLog(ctx, a.ownerUserID, a.agentID, sessionKey, prov, mdl, t, cost, durationMs); err != nil {
		slog.Warn("meter log failed", "agent", a.name, "error", err)
	}
}

// detachCollector keeps a background call (post-turn memory extraction,
// skill learning) out of the request's usage collector: it finishes after
// the response was sent, so it is billed as its own usage event only.
func detachCollector(ctx context.Context) context.Context {
	return usage.WithCollector(ctx, nil)
}

// meterBackground meters a background call that has no turn of its own
// (skills learner); the session comes from ctx when the turn set one.
func (a *Agent) meterBackground(ctx context.Context, model string, u provider.Usage, durationMs int64) {
	a.meterTokens(ctx, meterSessionFrom(ctx), model, u, durationMs)
}
