package gateway

import (
	"reflect"
	"testing"
	"time"
)

func TestWorkersWaitAllStopped(t *testing.T) {
	var w workers
	w.Go("a", func() {})
	w.Go("b", func() { time.Sleep(10 * time.Millisecond) })
	if stuck := w.Wait(time.Second); stuck != nil {
		t.Fatalf("stuck = %v, want none", stuck)
	}
}

func TestWorkersWaitNamesStuck(t *testing.T) {
	var w workers
	block := make(chan struct{})
	defer close(block)
	w.Go("quick", func() {})
	w.Go("channels", func() { <-block })
	start := time.Now()
	stuck := w.Wait(50 * time.Millisecond)
	if !reflect.DeepEqual(stuck, []string{"channels"}) {
		t.Fatalf("stuck = %v, want [channels]", stuck)
	}
	if time.Since(start) > time.Second {
		t.Fatal("Wait did not give up at its deadline")
	}
}
