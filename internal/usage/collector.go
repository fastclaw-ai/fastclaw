package usage

import (
	"context"
	"sync"
)

// Collector sums the model calls made while serving one request — every
// provider call of an agent turn, tool loops included — so the API can
// report the turn's usage in its response.
type Collector struct {
	mu     sync.Mutex
	tokens Tokens
	calls  int
	// costUSD sums the priced calls; costUnknown latches once any call's
	// model had no price, making the whole request's cost unknown.
	costUSD     float64
	costUnknown bool
}

type collectorKey struct{}

// WithCollector attaches c to ctx; the agent adds each model call to it.
func WithCollector(ctx context.Context, c *Collector) context.Context {
	return context.WithValue(ctx, collectorKey{}, c)
}

// CollectorFrom returns the collector attached to ctx, or nil.
func CollectorFrom(ctx context.Context) *Collector {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(collectorKey{}).(*Collector)
	return c
}

// Add records one model call and its USD cost; known is false when the
// call's model has no price.
func (c *Collector) Add(t Tokens, costUSD float64, known bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if known {
		c.costUSD += costUSD
	} else {
		c.costUnknown = true
	}
	c.tokens.Input += t.Input
	c.tokens.Output += t.Output
	c.tokens.CacheRead += t.CacheRead
	c.tokens.CacheCreation += t.CacheCreation
	c.calls++
	c.mu.Unlock()
}

// Totals returns the summed tokens and the number of model calls.
func (c *Collector) Totals() (Tokens, int) {
	if c == nil {
		return Tokens{}, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens, c.calls
}

// CostUSD returns the summed USD cost of the recorded calls, or nil when
// any call's model had no price — an unknown cost is never reported as 0.
func (c *Collector) CostUSD() *float64 {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.costUnknown {
		return nil
	}
	v := c.costUSD
	return &v
}
