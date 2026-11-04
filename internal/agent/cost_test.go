package agent

import (
	"context"
	"math"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/usage"
)

// logMeter captures the token_usage_log rows the agent writes.
type logMeter struct {
	usage.Meter
	mu   sync.Mutex
	rows []loggedCall
}

type loggedCall struct {
	session, prov, model string
	t                    usage.Tokens
	cost                 *float64
}

func (m *logMeter) RecordTokens(context.Context, string, string, string, string, string, usage.Tokens) error {
	return nil
}

func (m *logMeter) RecordTokenLog(_ context.Context, _, _, session, prov, model string, t usage.Tokens, cost *float64, _ int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, loggedCall{session, prov, model, t, cost})
	return nil
}

func pricedModel(id string, in, out, cacheRead, cacheWrite float64) config.ModelEntry {
	return config.ModelEntry{ID: id, Name: id, Cost: config.ModelCost{Input: in, Output: out, CacheRead: cacheRead, CacheWrite: cacheWrite}}
}

func costAgent(m *logMeter) *Agent {
	return &Agent{
		name: "a", agentID: "agt_cost", model: "main/big", meter: m,
		providers: map[string]config.ProviderConfig{
			"main": {APIKey: "k", Models: []config.ModelEntry{
				pricedModel("big", 3, 15, 0.3, 3.75),
				pricedModel("small", 1, 5, 0.1, 1.25),
				{ID: "free-text", Name: "unpriced"}, // admin left the price blank
			}},
			// Admin price beats the built-in table for the same id.
			"openai": {APIKey: "k", Models: []config.ModelEntry{
				pricedModel("gpt-4o", 10, 10, 10, 10),
				{ID: "gpt-4o-mini"}, // blank → built-in default fills it
			}},
		},
	}
}

func usd(in, out, cacheRead, cacheWrite int, p usage.Price) float64 {
	return (float64(in)*p.Input + float64(out)*p.Output + float64(cacheRead)*p.CacheRead + float64(cacheWrite)*p.CacheWrite) / 1e6
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

func TestTurnCostSingleModel(t *testing.T) {
	m := &logMeter{}
	a := costAgent(m)
	c := &usage.Collector{}
	ctx := usage.WithCollector(context.Background(), c)
	u := provider.Usage{InputTokens: 1200, OutputTokens: 300, CacheReadTokens: 5000, CacheCreationTokens: 800}
	a.meterTokens(ctx, "s1", a.model, u, 0)
	a.meterTokens(ctx, "s1", a.model, u, 0)

	big := usage.Price{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}
	per := usd(1200, 300, 5000, 800, big)
	got := c.CostUSD()
	if got == nil || !approx(*got, 2*per) {
		t.Fatalf("turn cost = %v, want %v", got, 2*per)
	}
	if len(m.rows) != 2 || m.rows[0].cost == nil || !approx(*m.rows[0].cost, per) ||
		m.rows[0].prov != "main" || m.rows[0].model != "big" || m.rows[0].session != "s1" {
		t.Fatalf("log rows: %+v", m.rows)
	}
}

// One turn calling two models (e.g. a compaction / memory model beside the
// turn model) costs each call at its own model's price.
func TestTurnCostTwoModels(t *testing.T) {
	m := &logMeter{}
	a := costAgent(m)
	c := &usage.Collector{}
	ctx := usage.WithCollector(context.Background(), c)
	a.meterTokens(ctx, "s", "main/big", provider.Usage{InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 2000}, 0)
	a.meterTokens(ctx, "s", "main/small", provider.Usage{InputTokens: 4000, OutputTokens: 400, CacheCreationTokens: 1000}, 0)
	// A bare model id (auto-persist model set without a prefix) ran on
	// the agent's own provider and is priced there.
	a.meterTokens(ctx, "s", "small", provider.Usage{InputTokens: 10}, 0)

	big := usage.Price{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}
	small := usage.Price{Input: 1, Output: 5, CacheRead: 0.1, CacheWrite: 1.25}
	want := usd(1000, 100, 2000, 0, big) + usd(4000, 400, 0, 1000, small) + usd(10, 0, 0, 0, small)
	if got := c.CostUSD(); got == nil || !approx(*got, want) {
		t.Fatalf("turn cost = %v, want %v", got, want)
	}
	if r := m.rows[1]; r.model != "small" || !approx(*r.cost, usd(4000, 400, 0, 1000, small)) {
		t.Fatalf("second call: %+v", r)
	}
	if r := m.rows[2]; r.prov != "main" || r.model != "small" {
		t.Fatalf("bare model metered as %s/%s", r.prov, r.model)
	}
}

func TestTurnCostUnknownWhenAnyModelUnpriced(t *testing.T) {
	for _, unpriced := range []string{"main/free-text", "main/not-listed", "other/whatever"} {
		m := &logMeter{}
		a := costAgent(m)
		c := &usage.Collector{}
		ctx := usage.WithCollector(context.Background(), c)
		a.meterTokens(ctx, "s", "main/big", provider.Usage{InputTokens: 100, OutputTokens: 10}, 0)
		a.meterTokens(ctx, "s", unpriced, provider.Usage{InputTokens: 100, OutputTokens: 10}, 0)
		a.meterTokens(ctx, "s", "main/small", provider.Usage{InputTokens: 100}, 0)
		if got := c.CostUSD(); got != nil {
			t.Fatalf("%s: turn cost = %v, want unknown (nil), never 0", unpriced, *got)
		}
		if m.rows[0].cost == nil || m.rows[1].cost != nil || m.rows[2].cost == nil {
			t.Fatalf("%s: per-call costs must stay per call: %+v", unpriced, m.rows)
		}
	}
}

func TestModelPriceAdminBeatsDefault(t *testing.T) {
	a := costAgent(&logMeter{})
	if p, ok := a.modelPrice("openai/gpt-4o"); !ok || p.Input != 10 {
		t.Fatalf("admin price must win over the built-in table: %v %v", p, ok)
	}
	if p, ok := a.modelPrice("openai/gpt-4o-mini"); !ok || p.Input != 0.15 {
		t.Fatalf("blank admin price must fall back to the built-in table: %v %v", p, ok)
	}
	if _, ok := a.modelPrice("main/free-text"); ok {
		t.Fatal("blank price with no default must be unknown")
	}
}
