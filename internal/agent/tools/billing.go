package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/usage"
)

// RegisterBillingTools exposes a read-only billing/usage lookup to the
// agent. It never accepts a user_id argument: the target is derived from
// the in-flight registry context so a user cannot ask the agent to inspect
// someone else's quota.
func RegisterBillingTools(r *Registry, meter usage.Meter, quotaStore usage.QuotaStore) {
	if r == nil || meter == nil {
		return
	}
	r.Register("get_billing_usage",
		"Get the current user's token usage and remaining quota for billing. Use this when the user asks how many tokens they used, how much quota remains, whether they are over limit, when quota resets, or what plan allowance they have. This is read-only and only returns the current billing account; it cannot inspect other users.",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		makeGetBillingUsage(meter, quotaStore, r),
	)
}

func makeGetBillingUsage(meter usage.Meter, quotaStore usage.QuotaStore, r *Registry) ToolFunc {
	return func(ctx context.Context, _ json.RawMessage) (string, error) {
		// Billing is charged to the account owner (boot-stable r.userID);
		// the per-turn chatter (resolved from ctx) is reported for
		// attribution. On IM channels with per-sender app_users these two
		// differ, which is exactly why billing must NOT race on a shared
		// per-turn field — each turn resolves its own chatter from ctx.
		billingUserID := r.OwnerUserID()
		chatterUserID := r.chatterFromCtx(ctx)
		if billingUserID == "" {
			billingUserID = chatterUserID
		}
		if billingUserID == "" {
			return "", fmt.Errorf("billing user is not available in this chat context")
		}
		out := map[string]any{
			"billingUserId": billingUserID,
			"chatterUserId": chatterUserID,
		}

		if quotaStore != nil {
			if _, qerr := quotaStore.GetQuota(ctx, billingUserID); qerr == nil {
				status, err := usage.CheckQuota(ctx, quotaStore, meter, billingUserID)
				if err != nil {
					return "", fmt.Errorf("check quota: %w", err)
				}
				out["status"] = status
				out["remaining"] = quotaRemaining(status)
				return marshalBilling(out)
			}
		}

		// No quota store (or no quota row) means unlimited. Still return a
		// useful recent usage snapshot so the user can see consumption.
		totals, err := meter.TotalsForUser(ctx, billingUserID, usage.LastN(30))
		if err != nil {
			return "", fmt.Errorf("get usage totals: %w", err)
		}
		tokensUsed := totals.Input + totals.Output + totals.CacheRead + totals.CacheCreation
		out["status"] = map[string]any{
			"allowed":             true,
			"monthlyTokenLimit":   int64(0),
			"monthlyRequestLimit": int64(0),
			"tokensUsed":          tokensUsed,
			"requestsUsed":        totals.Requests,
			"resetsAt":            "",
			"quota":               "unlimited",
			"usageWindowFallback": "last_30_days",
			"inputTokens":         totals.Input,
			"outputTokens":        totals.Output,
			"cacheReadTokens":     totals.CacheRead,
			"cacheCreationTokens": totals.CacheCreation,
			"generatedAt":         time.Now().Format(time.RFC3339),
		}
		out["remaining"] = map[string]any{
			"tokens":   "unlimited",
			"requests": "unlimited",
		}
		return marshalBilling(out)
	}
}

func quotaRemaining(status *usage.QuotaStatus) map[string]any {
	remaining := map[string]any{}
	if status.MonthlyTokenLimit > 0 {
		left := status.MonthlyTokenLimit - status.TokensUsed
		if left < 0 {
			left = 0
		}
		remaining["tokens"] = left
	} else {
		remaining["tokens"] = "unlimited"
	}
	if status.MonthlyRequestLimit > 0 {
		left := status.MonthlyRequestLimit - status.RequestsUsed
		if left < 0 {
			left = 0
		}
		remaining["requests"] = left
	} else {
		remaining["requests"] = "unlimited"
	}
	return remaining
}

func marshalBilling(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
