package channels

import (
	"context"
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

func waitLifecycle(t *testing.T, ch <-chan struct{}, action string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", action)
	}
}
