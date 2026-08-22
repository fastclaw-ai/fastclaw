package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

func TestPumpWeComStreamStartsImmediatelyAndUsesStableID(t *testing.T) {
	chunks := make(chan provider.StreamChunk)
	sr := provider.NewStreamReader(chunks)
	events := make(chan bus.OutboundMessage, 4)
	done := make(chan error, 1)
	go func() {
		_, err := pumpWeComStream(context.Background(), sr, bus.OutboundMessage{
			Channel: "wecom", ChatID: "chat-1", ReplyToMsgID: "msg-1", StreamID: "stable-1",
		}, 10*time.Millisecond, func(msg bus.OutboundMessage) error {
			events <- msg
			return nil
		})
		done <- err
	}()

	start := waitWeComStreamEvent(t, events)
	if start.StreamState != bus.StreamStart || start.StreamID != "stable-1" || start.Text != "…" {
		t.Fatalf("start = %#v", start)
	}
	chunks <- provider.StreamChunk{Content: "hello", Done: true}
	close(chunks)
	finish := waitWeComStreamEvent(t, events)
	if finish.StreamState != bus.StreamFinish || finish.StreamID != "stable-1" || finish.Text != "hello" {
		t.Fatalf("finish = %#v", finish)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPumpWeComStreamEmitsThrottledCumulativeUpdates(t *testing.T) {
	chunks := make(chan provider.StreamChunk, 4)
	sr := provider.NewStreamReader(chunks)
	events := make(chan bus.OutboundMessage, 8)
	done := make(chan error, 1)
	go func() {
		_, err := pumpWeComStream(context.Background(), sr, bus.OutboundMessage{StreamID: "stable-1"}, 20*time.Millisecond, func(msg bus.OutboundMessage) error {
			events <- msg
			return nil
		})
		done <- err
	}()
	_ = waitWeComStreamEvent(t, events)
	chunks <- provider.StreamChunk{Content: "first "}
	chunks <- provider.StreamChunk{Content: "second"}
	update := waitWeComStreamEvent(t, events)
	if update.StreamState != bus.StreamUpdate || update.Text != "first second" {
		t.Fatalf("update = %#v", update)
	}
	select {
	case extra := <-events:
		t.Fatalf("unthrottled extra update = %#v", extra)
	case <-time.After(5 * time.Millisecond):
	}
	chunks <- provider.StreamChunk{Done: true}
	close(chunks)
	finish := waitWeComStreamEvent(t, events)
	if finish.StreamState != bus.StreamFinish || finish.Text != "first second" {
		t.Fatalf("finish = %#v", finish)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPumpWeComStreamAlwaysFinishes(t *testing.T) {
	chunks := make(chan provider.StreamChunk, 1)
	chunks <- provider.StreamChunk{Content: "complete", Done: true}
	close(chunks)
	sr := provider.NewStreamReader(chunks)
	var mu sync.Mutex
	var events []bus.OutboundMessage
	reply, err := pumpWeComStream(context.Background(), sr, bus.OutboundMessage{StreamID: "stable-1"}, time.Millisecond, func(msg bus.OutboundMessage) error {
		mu.Lock()
		events = append(events, msg)
		mu.Unlock()
		return nil
	})
	if err != nil || reply != "complete" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
	finishCount := 0
	for _, event := range events {
		if event.StreamState == bus.StreamFinish {
			finishCount++
		}
	}
	if finishCount != 1 {
		t.Fatalf("finish count = %d, events=%#v", finishCount, events)
	}
}

func TestPumpWeComStreamFinishesWithError(t *testing.T) {
	chunks := make(chan provider.StreamChunk)
	close(chunks)
	sr := provider.NewStreamReader(chunks)
	streamErr := errors.New("fixture provider failure")
	sr.SetErr(streamErr)
	var events []bus.OutboundMessage
	reply, err := pumpWeComStream(context.Background(), sr, bus.OutboundMessage{StreamID: "stable-1"}, time.Millisecond, func(msg bus.OutboundMessage) error {
		events = append(events, msg)
		return nil
	})
	if !errors.Is(err, streamErr) {
		t.Fatalf("error = %v", err)
	}
	if len(events) != 2 || events[1].StreamState != bus.StreamFinish || !strings.Contains(events[1].Text, "couldn't complete") {
		t.Fatalf("events = %#v", events)
	}
	if reply != events[1].Text {
		t.Fatalf("reply=%q finish=%q", reply, events[1].Text)
	}
}

func TestPumpWeComStreamHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	chunks := make(chan provider.StreamChunk)
	sr := provider.NewStreamReader(chunks)
	events := make(chan bus.OutboundMessage, 4)
	done := make(chan error, 1)
	go func() {
		_, err := pumpWeComStream(ctx, sr, bus.OutboundMessage{StreamID: "stable-1"}, time.Hour, func(msg bus.OutboundMessage) error {
			events <- msg
			return nil
		})
		done <- err
	}()
	_ = waitWeComStreamEvent(t, events)
	cancel()
	finish := waitWeComStreamEvent(t, events)
	if finish.StreamState != bus.StreamFinish || !strings.Contains(finish.Text, "interrupted") {
		t.Fatalf("finish = %#v", finish)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	close(chunks)
}

func waitWeComStreamEvent(t *testing.T, events <-chan bus.OutboundMessage) bus.OutboundMessage {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for WeCom stream event")
		return bus.OutboundMessage{}
	}
}
