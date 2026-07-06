package agent

import (
	"sync"
	"testing"
)

// TestEventHubPublishUnsubscribeRace hammers Publish concurrently with
// Subscribe/unsubscribe churn on the same key. Before Publish took the
// write lock and sent under it, a publisher could copy the subscriber
// slice, unlock, and then send on a channel that a concurrent unsubscribe
// had already close()d — a "send on closed channel" panic that takes down
// the whole process. SSE reconnect churn makes that window real. This test
// would panic (and flag under -race) against the old snapshot-then-send
// Publish; it must stay clean.
func TestEventHubPublishUnsubscribeRace(t *testing.T) {
	hub := NewEventHub()
	const (
		publishers = 8
		churners   = 8
		iterations = 500
	)
	env := EventEnvelope{Seq: 1, Event: ChatEvent{Type: "content"}}

	var wg sync.WaitGroup

	// Publishers: fan out to the shared key as fast as they can.
	for p := 0; p < publishers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				hub.Publish("u", "a", "s", env)
			}
		}()
	}

	// Churners: subscribe then immediately unsubscribe, closing channels
	// under the write lock while publishers race to send.
	for c := 0; c < churners; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				ch, cleanup := hub.Subscribe("u", "a", "s")
				// Drain opportunistically so buffered sends don't wedge,
				// then tear the subscription down.
				select {
				case <-ch:
				default:
				}
				cleanup()
			}
		}()
	}

	wg.Wait()

	// After all churn, the key must be fully cleaned up (no leaked subs).
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if got := len(hub.subs["u/a/s"]); got != 0 {
		t.Fatalf("expected no lingering subscribers, got %d", got)
	}
}
