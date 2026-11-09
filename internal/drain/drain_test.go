package drain

import (
	"context"
	"testing"
	"time"
)

func TestWaitReturnsWhenTurnsEnd(t *testing.T) {
	done1, done2 := Track(), Track()
	if Active() != 2 {
		t.Fatalf("active = %d", Active())
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		done1()
		done1() // idempotent
		time.Sleep(20 * time.Millisecond)
		done2()
	}()
	if !Wait(context.Background(), time.Second) {
		t.Fatal("Wait timed out with every turn ended")
	}
	if Active() != 0 {
		t.Fatalf("active = %d after Wait", Active())
	}
}

func TestWaitTimesOutOnAStuckTurn(t *testing.T) {
	done := Track()
	defer done()
	start := time.Now()
	if Wait(context.Background(), 30*time.Millisecond) {
		t.Fatal("Wait reported idle with a turn running")
	}
	if time.Since(start) < 30*time.Millisecond {
		t.Fatal("Wait returned before its timeout")
	}
}

func TestWaitWithNothingRunning(t *testing.T) {
	if !Wait(context.Background(), time.Second) {
		t.Fatal("Wait should return at once when idle")
	}
}

func TestDrainingFlag(t *testing.T) {
	if Draining() {
		t.Fatal("draining before Start")
	}
	Start()
	if !Draining() {
		t.Fatal("not draining after Start")
	}
	draining.Store(false)
}
