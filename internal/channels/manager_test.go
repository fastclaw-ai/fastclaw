package channels

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

type lifecycleChannel struct {
	name    string
	account string
	started chan struct{}
	stopped chan struct{}
}

type delayedStopChannel struct {
	*lifecycleChannel
	canceled  chan struct{}
	allowStop chan struct{}
	stopOnce  sync.Once
}

func (c *delayedStopChannel) permitStop() {
	c.stopOnce.Do(func() { close(c.allowStop) })
}

func newDelayedStopChannel(name, account string) *delayedStopChannel {
	return &delayedStopChannel{
		lifecycleChannel: newLifecycleChannel(name, account),
		canceled:         make(chan struct{}),
		allowStop:        make(chan struct{}),
	}
}

func (c *delayedStopChannel) Start(ctx context.Context) error {
	close(c.started)
	<-ctx.Done()
	close(c.canceled)
	<-c.allowStop
	close(c.stopped)
	return nil
}

type lifecycleLeaser struct {
	events chan string
}

func newLifecycleLeaser() *lifecycleLeaser {
	return &lifecycleLeaser{events: make(chan string, 8)}
}

func (l *lifecycleLeaser) Acquire(context.Context, string, string, string, time.Duration) (bool, error) {
	l.events <- "acquire"
	return true, nil
}

func (l *lifecycleLeaser) Renew(context.Context, string, string, string, time.Duration) (bool, error) {
	return true, nil
}

func (l *lifecycleLeaser) Release(context.Context, string, string, string) error {
	l.events <- "release"
	return nil
}

func newLifecycleChannel(name, account string) *lifecycleChannel {
	return &lifecycleChannel{
		name: name, account: account,
		started: make(chan struct{}), stopped: make(chan struct{}),
	}
}

func (c *lifecycleChannel) Name() string        { return c.name }
func (c *lifecycleChannel) AccountID() string   { return c.account }
func (c *lifecycleChannel) BotUsername() string { return c.account }
func (c *lifecycleChannel) Start(ctx context.Context) error {
	close(c.started)
	<-ctx.Done()
	close(c.stopped)
	return nil
}
func (c *lifecycleChannel) Send(string, string) error             { return nil }
func (c *lifecycleChannel) SendMessage(bus.OutboundMessage) error { return nil }
func (c *lifecycleChannel) SendTyping(string) error               { return nil }

func TestManagerUnregisterCancelsRunningChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManager(bus.New())
	ch := newLifecycleChannel("test", "account-1")
	m.Register(ch)
	go m.Start(ctx)
	waitLifecycle(t, ch.started, "channel start")

	m.Unregister("test", "account-1")
	waitLifecycle(t, ch.stopped, "channel stop after unregister")
}

func TestManagerHotReplaceCancelsPreviousChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManager(bus.New())
	first := newLifecycleChannel("test", "account-1")
	m.Register(first)
	go m.Start(ctx)
	waitLifecycle(t, first.started, "first channel start")

	replacement := newLifecycleChannel("test", "account-1")
	m.RegisterAndStart(replacement)
	waitLifecycle(t, replacement.started, "replacement channel start")
	waitLifecycle(t, first.stopped, "first channel stop after replacement")
}

func TestManagerUnregisterBeforeHotStartPreventsAdapterStart(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManager(bus.New())
	m.mu.Lock()
	m.rootCtx = ctx
	m.mu.Unlock()

	ch := newLifecycleChannel("test", "account-1")
	m.RegisterAndStart(ch)
	m.Unregister("test", "account-1")
	runtime.Gosched()

	select {
	case <-ch.started:
		t.Fatal("unregistered channel started after removal")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestManagerSingletonReplacementWaitsForPreviousLeaseRelease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	leaser := newLifecycleLeaser()
	m := NewManagerWithLeaser(bus.New(), leaser, "process-1")
	first := newDelayedStopChannel("test", "account-1")
	defer first.permitStop()
	m.RegisterSingleton(first)
	go m.Start(ctx)
	waitLifecycle(t, first.started, "first singleton start")
	waitLeaseEvent(t, leaser.events, "acquire")

	replacement := newLifecycleChannel("test", "account-1")
	m.RegisterSingletonAndStart(replacement)
	waitLifecycle(t, first.canceled, "first singleton cancellation")

	select {
	case <-replacement.started:
		t.Fatal("replacement started before previous singleton released its lease")
	case <-time.After(100 * time.Millisecond):
	}

	first.permitStop()
	waitLifecycle(t, first.stopped, "first singleton stop")
	waitLeaseEvent(t, leaser.events, "release")
	waitLifecycle(t, replacement.started, "replacement singleton start")
}

func waitLeaseEvent(t *testing.T, events <-chan string, want string) {
	t.Helper()
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("lease event = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for lease event %q", want)
	}
}

func waitLifecycle(t *testing.T, ch <-chan struct{}, action string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", action)
	}
}
