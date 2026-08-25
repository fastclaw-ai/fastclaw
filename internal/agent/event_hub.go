package agent

import (
	"context"
	"sync"
	"time"
)

// EventEnvelope is a ChatEvent stamped with the persistent seq the
// store assigned at append time. Subscribers use Seq to dedup against
// events they've already replayed via ListSessionEventsSince.
type EventEnvelope struct {
	Seq   int64
	Event ChatEvent
}

// EventHub is the in-process pub/sub for live chat events. Subscribers
// (the SSE chat-subscribe handler) register per (userID, agentID,
// sessionKey); publishers (emitEvent on the agent loop, fanned out to
// the hub) push envelopes that include the persisted seq so reconnect
// resume can stitch back together cleanly.
//
// In-memory only — multi-pod deploys need to swap this for redis
// pub/sub or similar (same shape as the WebChannel limitation called
// out elsewhere).
type EventHub struct {
	mu   sync.RWMutex
	subs map[string][]chan EventEnvelope
	// turns tracks the in-flight turn per (user, agent, session):
	// whether one is running and the partial text of the current
	// round accumulated from content_delta events. content_delta is
	// deliberately never persisted (see emitEvent), so a client that
	// attaches mid-round has no way to recover the half-generated
	// text from session_events — this buffer is what the subscribe
	// handler snapshots into a synthetic `content_snapshot` event on
	// attach. Entries are dropped on `done`; turnStaleAfter guards
	// against turns that died without one (process kill, timeout path).
	turns map[string]*turnState
}

type turnState struct {
	partial   string
	lastEvent time.Time
}

// turnStaleAfter bounds how long a turn with no terminal `done` event
// still counts as active. Slightly above the chat handler's 45-minute
// agentTurnTimeout so a legitimately long turn is never declared dead
// while it can still emit.
const turnStaleAfter = 50 * time.Minute

// NewEventHub returns an empty hub.
func NewEventHub() *EventHub {
	return &EventHub{
		subs:  make(map[string][]chan EventEnvelope),
		turns: make(map[string]*turnState),
	}
}

// Subscribe registers a buffered channel for one (user, agent,
// session) tuple. The cleanup func MUST be deferred — without it the
// hub leaks goroutines and channels on reconnect churn.
func (h *EventHub) Subscribe(userID, agentID, sessionKey string) (<-chan EventEnvelope, func()) {
	key := hubKey(userID, agentID, sessionKey)
	ch := make(chan EventEnvelope, 32)
	h.mu.Lock()
	h.subs[key] = append(h.subs[key], ch)
	h.mu.Unlock()
	cleanup := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		list := h.subs[key]
		for i, c := range list {
			if c == ch {
				h.subs[key] = append(list[:i], list[i+1:]...)
				close(ch)
				break
			}
		}
		if len(h.subs[key]) == 0 {
			delete(h.subs, key)
		}
	}
	return ch, cleanup
}

// Publish fans an envelope out to every current subscriber. Slow
// consumers (full buffer) are skipped, not blocked — a stuck client
// can't stall the agent loop.
//
// The send runs UNDER the write lock, not the read lock over a snapshot.
// That closes a send-on-closed-channel race: the old code RLocked, copied
// the slice, unlocked, then sent — so a concurrent unsubscribe (which
// takes the write lock and close()s the channel) could slot in between the
// copy and the send, making Publish send on an already-closed channel and
// panic the process. SSE churns subscriptions constantly (every refresh /
// reconnect), so that window is not hypothetical. Because every send is
// non-blocking (`default` arm), holding the write lock here never blocks on
// a slow consumer; the critical section stays O(subscribers) of buffered
// enqueues. unsubscribe's close() and this send are now mutually excluded
// by h.mu, so a closed channel is always already absent from h.subs[key].
func (h *EventHub) Publish(userID, agentID, sessionKey string, env EventEnvelope) {
	key := hubKey(userID, agentID, sessionKey)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.trackTurnLocked(key, env.Event)
	for _, ch := range h.subs[key] {
		select {
		case ch <- env:
		default:
		}
	}
}

// trackTurnLocked updates the in-flight turn state for one published
// event. Caller must hold h.mu.
func (h *EventHub) trackTurnLocked(key string, evt ChatEvent) {
	if evt.Type == "done" {
		delete(h.turns, key)
		return
	}
	st := h.turns[key]
	if st == nil {
		st = &turnState{}
		h.turns[key] = st
	}
	st.lastEvent = time.Now()
	switch evt.Type {
	case "content_delta":
		if d, _ := evt.Data["delta"].(string); d != "" {
			st.partial += d
		}
	case "content":
		// Round sealed — the full text is persisted now, so the
		// partial buffer would only duplicate it on the next attach.
		st.partial = ""
	}
}

// TurnSnapshot reports whether a turn is currently in flight for the
// (user, agent, session) tuple and the partial text of its current
// round (accumulated content_delta chunks not yet sealed by a
// `content` event). Used by the subscribe handler to give a client
// that attaches mid-turn the half-generated text plus a "something is
// running" signal, neither of which exists in session_events.
func (h *EventHub) TurnSnapshot(userID, agentID, sessionKey string) (partial string, active bool) {
	key := hubKey(userID, agentID, sessionKey)
	h.mu.RLock()
	defer h.mu.RUnlock()
	st := h.turns[key]
	if st == nil || time.Since(st.lastEvent) > turnStaleAfter {
		return "", false
	}
	return st.partial, true
}

func hubKey(userID, agentID, sessionKey string) string {
	return userID + "/" + agentID + "/" + sessionKey
}

// EventSink is the persistence side of the chat-events pipeline. The
// store.Store interface's AppendSessionEvent satisfies this exactly, so
// the gateway can pass its store as-is.
type EventSink interface {
	AppendSessionEvent(ctx context.Context, userID, agentID, sessionKey, eventType string, data []byte) (int64, error)
}

// streamCtx carries the per-turn handles emitEvent reaches for:
// the legacy in-memory ChatEvent channel (consumed by handleChatStream
// while the client is connected), the persistent sink, the hub, and
// the address keys (userID, agentID, sessionKey) — these last three
// can't be derived from the agent struct because the agent runs on
// behalf of the chatter, not its owner.
type streamCtx struct {
	channel    chan<- ChatEvent
	sink       EventSink
	hub        *EventHub
	userID     string
	agentID    string
	sessionKey string
}

type streamCtxKey struct{}

// ContextWithStream attaches the streaming pipeline to ctx. emitEvent
// reads it and persists / publishes / forwards to the legacy channel
// in one place.
func ContextWithStream(ctx context.Context, channel chan<- ChatEvent, sink EventSink, hub *EventHub, userID, agentID, sessionKey string) context.Context {
	return context.WithValue(ctx, streamCtxKey{}, &streamCtx{
		channel:    channel,
		sink:       sink,
		hub:        hub,
		userID:     userID,
		agentID:    agentID,
		sessionKey: sessionKey,
	})
}

func streamFromContext(ctx context.Context) *streamCtx {
	s, _ := ctx.Value(streamCtxKey{}).(*streamCtx)
	return s
}
