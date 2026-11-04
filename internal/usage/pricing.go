package usage

import "strings"

// Price is a model's unit price in USD per 1M tokens — the same shape as
// config.ModelCost, which is where admins set it per provider model.
type Price struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// IsSet reports whether any of the four prices is filled. An all-zero
// price is "not filled in" (the console's default), never "free": a model
// without a price makes cost unknown rather than 0.
func (p Price) IsSet() bool {
	return p.Input > 0 || p.Output > 0 || p.CacheRead > 0 || p.CacheWrite > 0
}

// Cost is the USD cost of one call's tokens at p. Input, CacheRead and
// CacheCreation are disjoint counts, each billed at its own price. A cache
// price left at 0 while the call used cache tokens falls back to the input
// price, so an admin who filled only input/output never under-costs a
// provider that caches automatically.
func (p Price) Cost(t Tokens) float64 {
	cacheRead, cacheWrite := p.CacheRead, p.CacheWrite
	if cacheRead == 0 {
		cacheRead = p.Input
	}
	if cacheWrite == 0 {
		cacheWrite = p.Input
	}
	return (float64(t.Input)*p.Input +
		float64(t.Output)*p.Output +
		float64(t.CacheRead)*cacheRead +
		float64(t.CacheCreation)*cacheWrite) / 1e6
}

// DefaultPrice returns the built-in list price for a model id, used only
// for models whose provider entry has no price. The id may carry a
// router's vendor prefix ("anthropic/claude-sonnet-4-5") or a date
// suffix ("claude-sonnet-4-5-20250929"); both are tolerated.
func DefaultPrice(modelID string) (Price, bool) {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	id = strings.ReplaceAll(id, ".", "-")
	if p, ok := defaultPrices[id]; ok {
		return p, true
	}
	// Dated snapshot ids: "<id>-20250929" / "<id>-2024-08-06".
	for base, p := range defaultPrices {
		if rest, ok := strings.CutPrefix(id, base+"-"); ok && isDateSuffix(rest) {
			return p, true
		}
	}
	return Price{}, false
}

func isDateSuffix(s string) bool {
	digits := strings.ReplaceAll(s, "-", "")
	if len(digits) != 8 {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// defaultPrices holds public list prices (USD / 1M tokens) for common
// models, keyed by lower-case id with "." written as "-". Only the
// version-pinned ids are listed: an alias whose price changes over time
// would silently mis-cost. Admin-entered prices always win.
var defaultPrices = map[string]Price{
	// Anthropic — cache write is the 5-minute TTL price.
	"claude-opus-4-5":   {Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25},
	"claude-opus-4-1":   {Input: 15, Output: 75, CacheRead: 1.5, CacheWrite: 18.75},
	"claude-opus-4":     {Input: 15, Output: 75, CacheRead: 1.5, CacheWrite: 18.75},
	"claude-sonnet-4-5": {Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75},
	"claude-sonnet-4":   {Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75},
	"claude-3-7-sonnet": {Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75},
	"claude-haiku-4-5":  {Input: 1, Output: 5, CacheRead: 0.1, CacheWrite: 1.25},
	"claude-3-5-haiku":  {Input: 0.8, Output: 4, CacheRead: 0.08, CacheWrite: 1},

	// OpenAI — no separate cache-write price; writes bill as input.
	"gpt-5":        {Input: 1.25, Output: 10, CacheRead: 0.125, CacheWrite: 1.25},
	"gpt-5-mini":   {Input: 0.25, Output: 2, CacheRead: 0.025, CacheWrite: 0.25},
	"gpt-5-nano":   {Input: 0.05, Output: 0.4, CacheRead: 0.005, CacheWrite: 0.05},
	"gpt-4-1":      {Input: 2, Output: 8, CacheRead: 0.5, CacheWrite: 2},
	"gpt-4-1-mini": {Input: 0.4, Output: 1.6, CacheRead: 0.1, CacheWrite: 0.4},
	"gpt-4-1-nano": {Input: 0.1, Output: 0.4, CacheRead: 0.025, CacheWrite: 0.1},
	"gpt-4o":       {Input: 2.5, Output: 10, CacheRead: 1.25, CacheWrite: 2.5},
	"gpt-4o-mini":  {Input: 0.15, Output: 0.6, CacheRead: 0.075, CacheWrite: 0.15},
	"o3":           {Input: 2, Output: 8, CacheRead: 0.5, CacheWrite: 2},
	"o4-mini":      {Input: 1.1, Output: 4.4, CacheRead: 0.275, CacheWrite: 1.1},
}
