package bus

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	targetedOutboundClaimIdle = 15 * time.Second
	targetedOutboundReadBlock = 5 * time.Second
)

func targetedOutboundKey(prefix, channel, accountID string) string {
	sum := sha256.Sum256([]byte(accountID))
	return fmt.Sprintf("%s:bus:outbound:target:%s:%x", strings.Trim(prefix, ":"), channel, sum[:12])
}

func targetedOutboundChannel(channel string) bool {
	return channel == "wecom"
}

// HasTargetedOutbound reports whether this bus can route a persistent
// channel's outbound traffic to the replica that currently owns its lease.
func (b *MessageBus) HasTargetedOutbound() bool {
	return b != nil && b.redis != nil && b.redis.client != nil
}

// ConsumeTargetedOutbound consumes one channel account's durable Redis
// stream. The handler runs synchronously and an entry is acknowledged only
// after it succeeds, providing at-least-once delivery across lease failover.
func (b *MessageBus) ConsumeTargetedOutbound(
	ctx context.Context,
	channel, accountID string,
	handle func(OutboundMessage) error,
) error {
	if !targetedOutboundChannel(channel) {
		return fmt.Errorf("channel %q does not use targeted outbound", channel)
	}
	if accountID == "" {
		return errors.New("targeted outbound requires account ID")
	}
	if handle == nil {
		return errors.New("targeted outbound requires handler")
	}
	if !b.HasTargetedOutbound() {
		return errors.New("targeted outbound requires Redis bus")
	}

	r := b.redis
	stream := targetedOutboundKey(r.prefix, channel, accountID)
	if err := createStreamGroup(ctx, r.client, stream, r.group); err != nil {
		return err
	}

	for ctx.Err() == nil {
		if err := r.claimTargetedPending(ctx, stream, handle); err != nil && ctx.Err() == nil {
			slog.Warn("redis targeted pending claim failed", "channel", channel, "account", accountID, "error", err)
		}
		if ctx.Err() != nil {
			break
		}

		streams, err := r.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    r.group,
			Consumer: r.consumer,
			Streams:  []string{stream, ">"},
			Count:    10,
			Block:    targetedOutboundReadBlock,
		}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) || ctx.Err() != nil {
				continue
			}
			slog.Warn("redis targeted outbound read failed", "channel", channel, "account", accountID, "error", err)
			continue
		}
		for _, xs := range streams {
			r.handleTargetedMessages(ctx, stream, xs.Messages, handle)
		}
	}
	return nil
}

func createStreamGroup(ctx context.Context, client *redis.Client, stream, group string) error {
	if err := client.XGroupCreateMkStream(ctx, stream, group, "0").Err(); err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("create redis targeted stream group: %w", err)
	}
	return nil
}

func (r *redisBridge) claimTargetedPending(
	ctx context.Context,
	stream string,
	handle func(OutboundMessage) error,
) error {
	start := "0-0"
	for ctx.Err() == nil {
		messages, next, err := r.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   stream,
			Group:    r.group,
			Consumer: r.consumer,
			MinIdle:  targetedOutboundClaimIdle,
			Start:    start,
			Count:    10,
		}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		r.handleTargetedMessages(ctx, stream, messages, handle)
		if next == "0-0" || next == start {
			return nil
		}
		start = next
	}
	return nil
}

func (r *redisBridge) handleTargetedMessages(
	ctx context.Context,
	stream string,
	messages []redis.XMessage,
	handle func(OutboundMessage) error,
) {
	for _, xm := range messages {
		raw, ok := xm.Values["payload"]
		if !ok {
			slog.Error("redis targeted outbound missing payload", "stream", stream, "id", xm.ID)
			continue
		}
		payload, ok := raw.(string)
		if !ok {
			slog.Error("redis targeted outbound payload has invalid type", "stream", stream, "id", xm.ID)
			continue
		}
		var msg OutboundMessage
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			slog.Error("redis targeted outbound decode failed", "stream", stream, "id", xm.ID, "error", err)
			continue
		}
		if err := handle(msg); err != nil {
			slog.Error("redis targeted outbound handler failed", "channel", msg.Channel, "account", msg.AccountID, "id", xm.ID, "error", err)
			continue
		}
		if err := r.client.XAck(ctx, stream, r.group, xm.ID).Err(); err != nil && ctx.Err() == nil {
			slog.Warn("redis targeted outbound ack failed", "channel", msg.Channel, "account", msg.AccountID, "id", xm.ID, "error", err)
		}
	}
}
