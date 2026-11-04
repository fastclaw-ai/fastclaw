package usage

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

func TestPriceCost(t *testing.T) {
	p := Price{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}
	// Single model: tokens × unit price / 1M, each kind at its own price.
	got := p.Cost(Tokens{Input: 1000, Output: 200})
	if want := (1000*3.0 + 200*15.0) / 1e6; !near(got, want) {
		t.Fatalf("cost = %v, want %v", got, want)
	}
	// Cache read and cache write each bill at their own price, not input's.
	got = p.Cost(Tokens{Input: 100, Output: 10, CacheRead: 10000, CacheCreation: 2000})
	if want := (100*3.0 + 10*15.0 + 10000*0.3 + 2000*3.75) / 1e6; !near(got, want) {
		t.Fatalf("cache cost = %v, want %v", got, want)
	}
	// A cache price left blank bills cache tokens at the input price
	// rather than for free.
	blank := Price{Input: 2, Output: 8}
	if got, want := blank.Cost(Tokens{CacheRead: 1000, CacheCreation: 500}), (1000*2.0+500*2.0)/1e6; !near(got, want) {
		t.Fatalf("blank cache price: %v, want %v", got, want)
	}
	if (Price{}).IsSet() || !blank.IsSet() {
		t.Fatal("IsSet")
	}
}

func TestDefaultPrice(t *testing.T) {
	sonnet := Price{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}
	for _, id := range []string{
		"claude-sonnet-4-5", "claude-sonnet-4.5", "anthropic/claude-sonnet-4.5",
		"claude-sonnet-4-5-20250929", "Claude-Sonnet-4-5",
	} {
		if p, ok := DefaultPrice(id); !ok || p != sonnet {
			t.Fatalf("DefaultPrice(%q) = %v %v", id, p, ok)
		}
	}
	if p, ok := DefaultPrice("gpt-4o-mini"); !ok || p.Input != 0.15 {
		t.Fatalf("gpt-4o-mini resolved to %v %v (must not fall back to gpt-4o)", p, ok)
	}
	if p, ok := DefaultPrice("gpt-4o-2024-08-06"); !ok || p.Input != 2.5 {
		t.Fatalf("dated gpt-4o: %v %v", p, ok)
	}
	for _, id := range []string{"", "my-local-llama", "gpt-4o-custom", "claude-sonnet-4-5-latest"} {
		if _, ok := DefaultPrice(id); ok {
			t.Fatalf("DefaultPrice(%q) must be unknown", id)
		}
	}
}

func TestCollectorCost(t *testing.T) {
	var c Collector
	if v := c.CostUSD(); v == nil || *v != 0 {
		t.Fatalf("no calls: %v", v)
	}
	c.Add(Tokens{Input: 10}, 0.25, true)
	c.Add(Tokens{Output: 10}, 0.5, true)
	if v := c.CostUSD(); v == nil || !near(*v, 0.75) {
		t.Fatalf("two priced calls: %v", v)
	}
	c.Add(Tokens{Input: 1}, 0, false)
	c.Add(Tokens{Input: 1}, 1, true)
	if v := c.CostUSD(); v != nil {
		t.Fatalf("an unpriced call must make the cost unknown, got %v", *v)
	}
	if tok, calls := c.Totals(); calls != 4 || tok.Input != 12 {
		t.Fatalf("totals: %+v %d", tok, calls)
	}
	var nilC *Collector
	nilC.Add(Tokens{}, 1, true)
	if nilC.CostUSD() != nil {
		t.Fatal("nil collector")
	}
}
