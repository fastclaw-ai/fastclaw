// Package drain lets the process finish the agent turns it is running
// before it exits. A turn (HandleMessage — API, IM channels, web chat,
// cron) calls Track and the returned func when it ends; on SIGTERM the
// gateway calls Start, so /readyz fails and the load balancer stops sending
// new requests here, then Wait until every tracked turn has ended (or the
// timeout passes) before shutting the rest down. A rolling deploy then no
// longer cuts off a long turn — one running the user's own computer can
// take minutes.
package drain

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

var (
	draining atomic.Bool
	mu       sync.Mutex
	active   int
	idle     = make(chan struct{}) // closed while active == 0
)

func init() { close(idle) }

// Track marks one turn as running; call the returned func when it ends
// (safe to call more than once).
func Track() func() {
	mu.Lock()
	if active == 0 {
		idle = make(chan struct{})
	}
	active++
	mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			mu.Lock()
			active--
			if active == 0 {
				close(idle)
			}
			mu.Unlock()
		})
	}
}

// Active is the number of turns running now.
func Active() int {
	mu.Lock()
	defer mu.Unlock()
	return active
}

// Start begins draining: Draining reports true from now on.
func Start() { draining.Store(true) }

// Draining reports whether the process is shutting down (readiness).
func Draining() bool { return draining.Load() }

// Wait blocks until no turn is running, ctx is done, or timeout passes.
// It reports whether every turn ended.
func Wait(ctx context.Context, timeout time.Duration) bool {
	t := time.NewTimer(timeout)
	defer t.Stop()
	for {
		mu.Lock()
		ch := idle
		mu.Unlock()
		select {
		case <-ch:
			// A turn may have started right as the last one ended.
			if Active() == 0 {
				return true
			}
		case <-ctx.Done():
			return false
		case <-t.C:
			return false
		}
	}
}
