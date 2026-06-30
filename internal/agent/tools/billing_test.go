package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/usage"
)

func TestGetBillingUsageWithQuota(t *testing.T) {
	ctx := context.Background()
	meter := usage.NewMemMeter()
	quota := usage.NewMemQuotaStore()
	if err := quota.SetQuota(ctx, &usage.Quota{
		UserID:              "owner-1",
		MonthlyTokenLimit:   1000,
		MonthlyRequestLimit: 10,
		ResetDay:            1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := meter.RecordTokens(ctx, "owner-1", "agent-1", "session-1", "openai", "gpt", usage.Tokens{Input: 100, Output: 50}); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry("", "")
	r.SetOwnerUserID("owner-1")
	RegisterBillingTools(r, meter, quota)

	// Per-turn chatter now flows through TurnContext on ctx (the same
	// path the agent loop uses), not a shared registry field.
	ctx = WithTurnContext(ctx, &TurnContext{ChatterUserID: "chatter-1"})

	got, err := r.Execute(ctx, "get_billing_usage", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"billingUserId": "owner-1"`,
		`"chatterUserId": "chatter-1"`,
		`"tokensUsed": 150`,
		`"tokens": 850`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("billing output missing %s:\n%s", want, got)
		}
	}
}

func TestGetBillingUsageWithoutQuotaReturnsRecentUsage(t *testing.T) {
	ctx := context.Background()
	meter := usage.NewMemMeter()
	quota := usage.NewMemQuotaStore()
	if err := meter.RecordTokens(ctx, "owner-1", "agent-1", "session-1", "openai", "gpt", usage.Tokens{Input: 12, Output: 8}); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry("", "")
	r.SetOwnerUserID("owner-1")
	RegisterBillingTools(r, meter, quota)

	got, err := r.Execute(ctx, "get_billing_usage", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"quota": "unlimited"`,
		`"usageWindowFallback": "last_30_days"`,
		`"tokensUsed": 20`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("billing output missing %s:\n%s", want, got)
		}
	}
}
