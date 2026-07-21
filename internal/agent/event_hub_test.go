package agent

import (
	"sync"
	"testing"
	"time"
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

// TestTurnSnapshotLifecycle covers the in-flight turn tracking that
// backs the subscribe handler's content_snapshot event: deltas
// accumulate, a content seal clears the partial, done ends the turn.
func TestTurnSnapshotLifecycle(t *testing.T) {
	hub := NewEventHub()

	// No events yet → no active turn.
	if _, active := hub.TurnSnapshot("u", "a", "s"); active {
		t.Fatal("expected inactive turn before any publish")
	}

	pub := func(evt ChatEvent) { hub.Publish("u", "a", "s", EventEnvelope{Seq: -1, Event: evt}) }

	pub(ChatEvent{Type: "content_delta", Data: map[string]any{"delta": "Hel"}})
	pub(ChatEvent{Type: "content_delta", Data: map[string]any{"delta": "lo"}})
	partial, active := hub.TurnSnapshot("u", "a", "s")
	if !active || partial != "Hello" {
		t.Fatalf("expected active turn with partial %q, got active=%v partial=%q", "Hello", active, partial)
	}

	// Round seal clears the partial but the turn stays active.
	pub(ChatEvent{Type: "content", Data: map[string]any{"content": "Hello"}})
	partial, active = hub.TurnSnapshot("u", "a", "s")
	if !active || partial != "" {
		t.Fatalf("expected active turn with empty partial after seal, got active=%v partial=%q", active, partial)
	}

	// Tool activity keeps the turn active with no partial change.
	pub(ChatEvent{Type: "tool_call", Data: map[string]any{"name": "exec"}})
	if _, active = hub.TurnSnapshot("u", "a", "s"); !active {
		t.Fatal("expected turn active during tool round")
	}

	// done terminates the turn and drops the state entry.
	pub(ChatEvent{Type: "done"})
	if _, active = hub.TurnSnapshot("u", "a", "s"); active {
		t.Fatal("expected inactive turn after done")
	}
	hub.mu.RLock()
	_, leaked := hub.turns["u/a/s"]
	hub.mu.RUnlock()
	if leaked {
		t.Fatal("expected turn state entry removed after done")
	}
}

// TestTurnSnapshotStaleness: a turn whose last event is older than
// turnStaleAfter no longer reports active (crash / timeout without a
// terminal done).
func TestTurnSnapshotStaleness(t *testing.T) {
	hub := NewEventHub()
	hub.Publish("u", "a", "s", EventEnvelope{Seq: -1, Event: ChatEvent{Type: "content_delta", Data: map[string]any{"delta": "x"}}})
	hub.mu.Lock()
	hub.turns["u/a/s"].lastEvent = time.Now().Add(-turnStaleAfter - time.Minute)
	hub.mu.Unlock()
	if _, active := hub.TurnSnapshot("u", "a", "s"); active {
		t.Fatal("expected stale turn to report inactive")
	}
}
