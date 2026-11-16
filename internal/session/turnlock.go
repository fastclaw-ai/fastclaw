package session

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Turns on one conversation run one at a time. Overlapping turns (a
// burst of forwarded messages, a scheduled task firing while the person
// is chatting) each loaded the history, then appended their own
// messages to it as they went: results landed after another turn's
// calls, a tool call lost its result, and the provider rejected every
// later turn of the session.
//
// The lock is per process, which matches one fastclaw-cloud replica.
// It is taken before the session is loaded, so a waiting turn reads the
// history the previous turn left behind.

// maxTurnLockWait bounds the wait behind a turn that never ends; after
// it the turn runs anyway rather than stall the conversation for good.
const maxTurnLockWait = 30 * time.Minute

type turnLock struct {
	ch   chan struct{} // holds one token while a turn runs
	refs int           // holders + waiters; the entry is removed at 0
}

var turnLocks = struct {
	sync.Mutex
	m map[string]*turnLock
}{m: map[string]*turnLock{}}

type heldTurnLocksKey struct{}

// LockTurn waits until no other turn runs on the conversation (channel,
// accountID, chatID) of this manager's user and agent, and holds it
// until unlock is called. Waiters go in arrival order.
//
// The returned context records the held lock: a turn started from inside
// this one (an agent asked through message_agent who asks back) passes
// it and runs without waiting, so the chain can't deadlock on itself.
// err is the context's error when it ended while waiting.
func (m *Manager) LockTurn(ctx context.Context, channel, accountID, chatID string) (context.Context, func(), error) {
	key := m.userID + "\x00" + m.agentID + "\x00" + channel + "\x00" + accountID + "\x00" + chatID
	return lockTurnKey(ctx, key, maxTurnLockWait)
}

func lockTurnKey(ctx context.Context, key string, maxWait time.Duration) (context.Context, func(), error) {
	held, _ := ctx.Value(heldTurnLocksKey{}).(map[string]bool)
	if held[key] {
		return ctx, func() {}, nil
	}

	turnLocks.Lock()
	l := turnLocks.m[key]
	if l == nil {
		l = &turnLock{ch: make(chan struct{}, 1)}
		turnLocks.m[key] = l
	}
	l.refs++
	turnLocks.Unlock()

	release := func() {
		turnLocks.Lock()
		l.refs--
		if l.refs == 0 {
			delete(turnLocks.m, key)
		}
		turnLocks.Unlock()
	}

	acquired := false
	select {
	case l.ch <- struct{}{}:
		acquired = true
	default:
		start := time.Now()
		timer := time.NewTimer(maxWait)
		select {
		case l.ch <- struct{}{}:
			acquired = true
			timer.Stop()
			slog.Info("turn waited for the previous turn on its conversation",
				"wait", time.Since(start).Round(time.Millisecond))
		case <-ctx.Done():
			timer.Stop()
			release()
			return ctx, nil, ctx.Err()
		case <-timer.C:
			slog.Warn("previous turn on this conversation is still running; starting anyway",
				"waited", maxWait)
		}
	}

	next := make(map[string]bool, len(held)+1)
	for k := range held {
		next[k] = true
	}
	next[key] = true
	ctx = context.WithValue(ctx, heldTurnLocksKey{}, next)

	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			if acquired {
				<-l.ch
			}
			release()
		})
	}, nil
}
