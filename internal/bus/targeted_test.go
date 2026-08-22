package bus

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisWeComOutboundIsAccountTargeted(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mb := NewRedis(RedisConfig{Client: client, Prefix: "test", Group: "workers", Consumer: "shared-1"})
	if err := mb.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !mb.HasTargetedOutbound() {
		t.Fatal("redis bus should advertise targeted outbound support")
	}

	mb.Outbound <- OutboundMessage{Channel: "wecom", AccountID: "bot-a", ChatID: "chat-a", Text: "a"}
	mb.Outbound <- OutboundMessage{Channel: "wecom", AccountID: "bot-b", ChatID: "chat-b", Text: "b"}

	keyA := targetedOutboundKey("test", "wecom", "bot-a")
	keyB := targetedOutboundKey("test", "wecom", "bot-b")
	waitRedis(t, func() bool {
		return client.XLen(ctx, keyA).Val() == 1 && client.XLen(ctx, keyB).Val() == 1
	})
	if got := client.XLen(ctx, "test:bus:outbound").Val(); got != 0 {
		t.Fatalf("shared outbound stream length = %d, want 0", got)
	}

	consumeCtx, stop := context.WithCancel(ctx)
	received := make(chan OutboundMessage, 2)
	go func() {
		_ = mb.ConsumeTargetedOutbound(consumeCtx, "wecom", "bot-a", func(msg OutboundMessage) error {
			received <- msg
			return nil
		})
	}()

	select {
	case got := <-received:
		if got.AccountID != "bot-a" || got.Text != "a" {
			t.Fatalf("received %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for bot-a targeted message")
	}
	stop()

	select {
	case got := <-received:
		t.Fatalf("bot-a consumer received another account: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTargetedOutboundAcksOnlyAfterHandlerSuccess(t *testing.T) {
	mr, client, mb, ctx, cancel := newTargetedBus(t, "consumer-1")
	defer cancel()
	_ = mr

	msg := OutboundMessage{Channel: "wecom", AccountID: "bot-a", ChatID: "chat-a", Text: "retry me"}
	mb.Outbound <- msg
	stream := targetedOutboundKey("test", "wecom", "bot-a")
	waitRedis(t, func() bool { return client.XLen(ctx, stream).Val() == 1 })

	handlerErr := errors.New("send failed")
	called := make(chan struct{}, 1)
	consumeCtx, stop := context.WithCancel(ctx)
	go func() {
		_ = mb.ConsumeTargetedOutbound(consumeCtx, "wecom", "bot-a", func(OutboundMessage) error {
			called <- struct{}{}
			return handlerErr
		})
	}()
	waitSignal(t, called, "failing targeted handler")
	stop()

	waitRedis(t, func() bool {
		pending, err := client.XPending(ctx, stream, "workers").Result()
		return err == nil && pending.Count == 1
	})
}

func TestTargetedOutboundStopsBatchAfterFirstHandlerFailure(t *testing.T) {
	_, client, mb, ctx, cancel := newTargetedBus(t, "consumer-1")
	defer cancel()

	stream := targetedOutboundKey("test", "wecom", "bot-a")
	for _, state := range []StreamState{StreamStart, StreamUpdate, StreamFinish} {
		mb.Outbound <- OutboundMessage{
			Channel:     "wecom",
			AccountID:   "bot-a",
			ChatID:      "chat-a",
			StreamID:    "stream-1",
			StreamState: state,
		}
	}
	waitRedis(t, func() bool { return client.XLen(ctx, stream).Val() == 3 })

	called := make(chan StreamState, 3)
	consumeCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		_ = mb.ConsumeTargetedOutbound(consumeCtx, "wecom", "bot-a", func(msg OutboundMessage) error {
			called <- msg.StreamState
			if msg.StreamState == StreamStart {
				return errors.New("start send failed")
			}
			return nil
		})
	}()

	select {
	case got := <-called:
		if got != StreamStart {
			t.Fatalf("first handled stream state = %q, want %q", got, StreamStart)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first targeted message")
	}
	select {
	case got := <-called:
		t.Fatalf("handled stream state %q after the first handler failure", got)
	case <-time.After(100 * time.Millisecond):
	}

	pending, err := client.XPending(ctx, stream, "workers").Result()
	if err != nil {
		t.Fatal(err)
	}
	if pending.Count != 3 {
		t.Fatalf("pending count = %d, want all 3 batch entries pending", pending.Count)
	}
}

func TestTargetedOutboundRetriesFailedPendingBeforeNewMessages(t *testing.T) {
	_, _, mb, ctx, cancel := newTargetedBus(t, "consumer-1")
	defer cancel()

	mb.Outbound <- OutboundMessage{Channel: "wecom", AccountID: "bot-a", ChatID: "chat-a", Text: "blocked"}
	firstAttempt := make(chan struct{}, 1)
	allowRetry := make(chan struct{})
	called := make(chan string, 4)
	consumeCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		_ = mb.ConsumeTargetedOutbound(consumeCtx, "wecom", "bot-a", func(msg OutboundMessage) error {
			called <- msg.Text
			if msg.Text == "blocked" {
				select {
				case <-allowRetry:
					return nil
				default:
					select {
					case firstAttempt <- struct{}{}:
					default:
					}
					return errors.New("fixture blocked send")
				}
			}
			return nil
		})
	}()
	waitSignal(t, firstAttempt, "first failed targeted attempt")
	if got := <-called; got != "blocked" {
		t.Fatalf("first handled message = %q, want blocked", got)
	}

	mb.Outbound <- OutboundMessage{Channel: "wecom", AccountID: "bot-a", ChatID: "chat-a", Text: "later"}
	select {
	case got := <-called:
		t.Fatalf("handled %q before retrying failed pending message", got)
	case <-time.After(100 * time.Millisecond):
	}

	close(allowRetry)
	for _, want := range []string{"blocked", "later"} {
		select {
		case got := <-called:
			if got != want {
				t.Fatalf("handled message = %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

func TestTargetedOutboundDoesNotClaimWhileHandlerCanStillBeActive(t *testing.T) {
	mr, client, first, ctx, cancel := newTargetedBus(t, "consumer-1")
	defer cancel()

	first.Outbound <- OutboundMessage{Channel: "wecom", AccountID: "bot-a", ChatID: "chat-a", Text: "slow send"}
	stream := targetedOutboundKey("test", "wecom", "bot-a")
	waitRedis(t, func() bool { return client.XLen(ctx, stream).Val() == 1 })

	handling := make(chan struct{}, 1)
	release := make(chan struct{})
	firstCtx, stopFirst := context.WithCancel(ctx)
	defer stopFirst()
	go func() {
		_ = first.ConsumeTargetedOutbound(firstCtx, "wecom", "bot-a", func(OutboundMessage) error {
			handling <- struct{}{}
			<-release
			return nil
		})
	}()
	waitSignal(t, handling, "slow targeted handler")

	// WeCom SendMessage may remain active for two minutes. A peer that
	// acquires the account lease during that window must not claim the entry.
	mr.SetTime(time.Now().UTC().Add(2 * time.Minute))
	second := NewRedis(RedisConfig{Client: client, Prefix: "test", Group: "workers", Consumer: "consumer-2"})
	duplicate := make(chan struct{}, 1)
	secondCtx, stopSecond := context.WithCancel(ctx)
	defer stopSecond()
	go func() {
		_ = second.ConsumeTargetedOutbound(secondCtx, "wecom", "bot-a", func(OutboundMessage) error {
			duplicate <- struct{}{}
			return nil
		})
	}()

	select {
	case <-duplicate:
		t.Fatal("active targeted message was claimed by another consumer")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	waitRedis(t, func() bool {
		pending, err := client.XPending(ctx, stream, "workers").Result()
		return err == nil && pending.Count == 0
	})
}

func TestTargetedOutboundClaimsPendingFromPreviousConsumer(t *testing.T) {
	mr, client, first, ctx, cancel := newTargetedBus(t, "consumer-1")
	defer cancel()

	first.Outbound <- OutboundMessage{Channel: "wecom", AccountID: "bot-a", ChatID: "chat-a", Text: "recover me"}
	stream := targetedOutboundKey("test", "wecom", "bot-a")
	waitRedis(t, func() bool { return client.XLen(ctx, stream).Val() == 1 })

	failed := make(chan struct{}, 1)
	firstCtx, stopFirst := context.WithCancel(ctx)
	go func() {
		_ = first.ConsumeTargetedOutbound(firstCtx, "wecom", "bot-a", func(OutboundMessage) error {
			failed <- struct{}{}
			return errors.New("leaseholder stopped before send completed")
		})
	}()
	waitSignal(t, failed, "first targeted consumer")
	stopFirst()
	waitRedis(t, func() bool {
		pending, err := client.XPending(ctx, stream, "workers").Result()
		return err == nil && pending.Count == 1
	})

	// Stream pending idle time follows Redis server time. miniredis FastForward
	// only changes key TTLs, so advance its explicit clock for XAUTOCLAIM.
	mr.SetTime(time.Now().UTC().Add(targetedOutboundClaimIdle + time.Second))
	second := NewRedis(RedisConfig{Client: client, Prefix: "test", Group: "workers", Consumer: "consumer-2"})
	recovered := make(chan OutboundMessage, 1)
	secondCtx, stopSecond := context.WithCancel(ctx)
	go func() {
		_ = second.ConsumeTargetedOutbound(secondCtx, "wecom", "bot-a", func(msg OutboundMessage) error {
			recovered <- msg
			return nil
		})
	}()

	select {
	case got := <-recovered:
		if got.Text != "recover me" {
			t.Fatalf("recovered %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pending targeted message claim")
	}
	stopSecond()
	waitRedis(t, func() bool {
		pending, err := client.XPending(ctx, stream, "workers").Result()
		return err == nil && pending.Count == 0
	})
}

func TestRedisNonWeComOutboundStillUsesSharedConsumer(t *testing.T) {
	_, _, mb, ctx, cancel := newTargetedBus(t, "consumer-1")
	defer cancel()

	want := OutboundMessage{Channel: "telegram", AccountID: "bot-a", ChatID: "chat-a", Text: "shared"}
	mb.Outbound <- want
	select {
	case got := <-mb.OutboundConsumer():
		if got.Channel != want.Channel || got.Text != want.Text {
			t.Fatalf("shared outbound = %#v", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for shared outbound message")
	}
}

func newTargetedBus(t *testing.T, consumer string) (*miniredis.Miniredis, *redis.Client, *MessageBus, context.Context, context.CancelFunc) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	mb := NewRedis(RedisConfig{Client: client, Prefix: "test", Group: "workers", Consumer: consumer})
	if err := mb.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	return mr, client, mb, ctx, cancel
}

func waitRedis(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for Redis condition")
}

func waitSignal(t *testing.T, ch <-chan struct{}, action string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal(fmt.Sprintf("timed out waiting for %s", action))
	}
}
