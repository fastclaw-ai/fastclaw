package session

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func turnLockEntries() int {
	turnLocks.Lock()
	defer turnLocks.Unlock()
	return len(turnLocks.m)
}

// Many turns arriving at once on one conversation run one at a time;
// another conversation isn't held up.
func TestLockTurn_SerializesOneConversation(t *testing.T) {
	m := &Manager{userID: "u1", agentID: "a1"}
	var running, maxRunning int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, unlock, err := m.LockTurn(context.Background(), "wechat", "acc", "chat1")
			if err != nil {
				t.Error(err)
				return
			}
			n := atomic.AddInt32(&running, 1)
			for {
				old := atomic.LoadInt32(&maxRunning)
				if n <= old || atomic.CompareAndSwapInt32(&maxRunning, old, n) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt32(&running, -1)
			unlock()
		}()
	}

	// A different chat locks right away while those run.
	done := make(chan struct{})
	go func() {
		_, unlock, _ := m.LockTurn(context.Background(), "wechat", "acc", "chat2")
		unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("another conversation waited on chat1's turns")
	}

	wg.Wait()
	if maxRunning != 1 {
		t.Fatalf("max concurrent turns = %d, want 1", maxRunning)
	}
	if n := turnLockEntries(); n != 0 {
		t.Fatalf("%d lock entries left behind", n)
	}
}

// A turn started from inside the turn holding the lock (agent A asks
// agent B, who asks A back) runs instead of deadlocking.
func TestLockTurn_ReentrantThroughContext(t *testing.T) {
	m := &Manager{userID: "u1", agentID: "a1"}
	ctx, unlock, err := m.LockTurn(context.Background(), "web", "", "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	done := make(chan struct{})
	go func() {
		_, inner, err := m.LockTurn(ctx, "web", "", "s1")
		if err == nil {
			inner()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("nested turn deadlocked")
	}
}

// A waiter whose request goes away stops waiting, and the lock still
// works afterwards.
func TestLockTurn_CanceledWhileWaiting(t *testing.T) {
	m := &Manager{userID: "u1", agentID: "a1"}
	_, unlock, _ := m.LockTurn(context.Background(), "web", "", "s2")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, err := m.LockTurn(ctx, "web", "", "s2"); err == nil {
		t.Fatal("waiter should give up when its context ends")
	}
	unlock()
	_, unlock2, err := m.LockTurn(context.Background(), "web", "", "s2")
	if err != nil {
		t.Fatal(err)
	}
	unlock2()
	unlock() // a second unlock is harmless
	if n := turnLockEntries(); n != 0 {
		t.Fatalf("%d lock entries left behind", n)
	}
}

// A turn that never ends doesn't stall the conversation for good.
func TestLockTurn_StuckHolderTimesOut(t *testing.T) {
	_, unlock, _ := lockTurnKey(context.Background(), "stuck", time.Second)
	_, unlock2, err := lockTurnKey(context.Background(), "stuck", 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	unlock2()
	unlock()
	if n := turnLockEntries(); n != 0 {
		t.Fatalf("%d lock entries left behind", n)
	}
}
