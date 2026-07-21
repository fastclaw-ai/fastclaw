package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Source identifies what produced an InboundMessage. The default
// (empty string) means an end-user typed it through a chat surface
// (IM, web, OpenAI-compat API, webhook, plugin) — that's the most
// common case and the existing call sites don't have to be touched.
//
// Non-user sources are the cases the agent runtime needs to
// distinguish: cron self-fires, heartbeat ticks, sub-agent spawns,
// and runtime-injected goal continuations. The /goal feature in
// particular uses this to decide whether a turn just finished
// because of a real user message (and should trigger a continuation
// probe) or because of one of these synthetic ones (which should not,
// or we'd loop).
const (
	SourceUser        = "" // default — keep "" so existing producers stay correct without edits
	SourceCron        = "cron"
	SourceHeartbeat   = "heartbeat"
	SourceSubAgent    = "subagent"
	SourceGoalContext = "goal_context"
)

// InboundMessage represents a message received from a channel.
type InboundMessage struct {
	Channel   string // channel type, e.g. "telegram"
	AccountID string // account within the channel (e.g. which bot)
	ChatID    string // unique chat identifier within the channel
	// ProjectID, when set, names the per-(user, agent) project the chat
	// belongs to. Empty = loose chat (legacy behavior). Stamped on
	// inbound messages by the chat handlers after they've resolved the
	// session row, so the agent runtime can route workspace IO to
	// projects/<id>/ instead of sessions/<chat>/.
	ProjectID   string
	UserID      string // user identifier
	OwnerUserID string // fastclaw user that owns the agent (for multi-user routing)
	// AgentID is an *explicit* agent target. Non-empty when the source
	// of the message already knows which agent should handle it (cron
	// jobs, web chat, sub-agent spawns) — bypasses binding lookup +
	// default-agent fallback in routeDM. Empty for IM-channel messages
	// where the gateway has to figure out the agent from bindings.
	AgentID    string
	MessageID  string // unique message identifier within the chat
	Text       string // message text
	PeerKind   string // "group" or "dm"
	SenderName string // display name of the sender
	// SenderAvatarURL is the platform-side avatar URL for the message
	// sender, when the channel can provide one (Discord serves
	// `cdn.discordapp.com/avatars/<user_id>/<hash>.png`; Telegram/Slack
	// need a separate API hit so the bridges leave this empty for now).
	// Stored on the session_message row as UI-only metadata — the LLM
	// never sees it — so the web chat panel can render an avatar +
	// nickname header on each IM-routed user bubble.
	SenderAvatarURL string
	Mentions        []string    // @usernames mentioned in the message
	IsBotMessage    bool        // true if the message was sent by a bot
	PhotoURL        string      // URL of attached photo (if any) — single-image legacy field
	PhotoURLs       []string    // URLs of attached photos. Independent of PhotoURL so old single-image callers (Telegram bridge etc.) keep working untouched; new web-chat path uses this for multi-image attachments.
	MediaItems      []MediaItem // downloaded inbound files/images to materialize into the session workspace
	ReplyToMsgID    string      // message ID being replied to
	// Params is a freeform structured-parameter blob supplied by the
	// calling client (typically a third-party app via the chat
	// completions API's `params` field). The agent loop renders it as
	// a per-turn system message so the LLM can honor it when calling
	// tools. Scope is per-request — not stored in session history,
	// next turn ships its own params (or none). nil / empty when the
	// inbound source doesn't supply params (IM channels, web chat).
	Params map[string]any
	// Source distinguishes user-originated messages from runtime-
	// originated ones (cron, heartbeat, sub-agent, goal continuations).
	// Empty means "user". See the Source* constants. Read this on the
	// agent-loop side to decide whether the turn should fire downstream
	// reactions that are only valid for genuine user input.
	Source string
	// SharedIdentity is set by the gateway when the channel has
	// shared_identity enabled. The agent loop uses this to resolve
	// sessions by owner identity instead of by channel triple, so
	// conversations are shared across all channels.
	SharedIdentity bool
}

// SessionTriple returns the (channel, accountID, chatID) used for session
// resolution. When SharedIdentity is enabled, all channels converge on a
// single virtual triple so they share one session. The real Channel /
// AccountID / ChatID remain intact on the message for outbound routing.
func (m *InboundMessage) SessionTriple() (channel, accountID, chatID string) {
	if m.SharedIdentity {
		return "shared", "", m.UserID
	}
	return m.Channel, m.AccountID, m.ChatID
}

// OutboundButton represents a button in an inline keyboard.
type OutboundButton struct {
	Text         string
	CallbackData string
	URL          string
}

// MediaItem is an attachment whose bytes are already resolved (read
// from workspace.Store / sandbox snapshot / wherever) by the time the
// message lands on the bus. Channels that can't access the host
// filesystem (e2b path) still need to upload to Telegram/Discord/etc.,
// so we ship the bytes inline rather than asking each channel adapter
// to hold a workspace.Store reference.
type MediaItem struct {
	Filename    string // for content-type sniffing + display in IM
	ContentType string // optional override; channels can sniff if empty
	Bytes       []byte
}

// StreamState describes the lifecycle of a transport-neutral streaming
// response. The zero value keeps existing final-only channel adapters
// backwards compatible.
type StreamState string

const (
	StreamNone   StreamState = ""
	StreamStart  StreamState = "start"
	StreamUpdate StreamState = "update"
	StreamFinish StreamState = "finish"
)

// OutboundMessage represents a message to be sent to a channel.
type OutboundMessage struct {
	Channel      string             // target channel type
	AccountID    string             // target account within the channel
	AgentID      string             // originating agent — used by the WebChannel to route SSE events to the right (agent, session) pair; harmless for IM channels (which key on AccountID).
	ChatID       string             // target chat identifier
	Text         string             // message text
	ReplyToMsgID string             // reply to specific message
	ParseMode    string             // "MarkdownV2", "HTML", ""
	Buttons      [][]OutboundButton // inline keyboard rows
	EditMsgID    string             // edit existing message instead of sending new
	MediaPaths   []string           // file paths to attach (from MEDIA: protocol; host-mounted backends only)
	MediaItems   []MediaItem        // pre-resolved attachments — channel uploads bytes directly
	StreamID     string             // stable identifier for one incremental response
	StreamState  StreamState        // start/update/finish; empty means a final-only response
	// AllowSplit, when true on a WeChat-bound message, lets the adapter
	// honor SplitMessageMarker and emit multiple bubbles. False (default)
	// collapses the marker to a newline so a stray marker doesn't leak
	// as literal text. Stamped by the originating Agent based on its
	// effective splitReplies setting (per-agent override OR system
	// default) — see internal/agent/loop.go. Harmless on non-WeChat
	// channels: they ignore the field.
	AllowSplit bool
}

// MessageBus is an async message queue. By default it is backed by Go
// channels. When constructed with Redis, producers still write to Inbound /
// Outbound, but non-local traffic is bridged through Redis Streams so multiple
// FastClaw replicas share one delivery center.
type MessageBus struct {
	Inbound  chan InboundMessage
	Outbound chan OutboundMessage

	inboundConsume  chan InboundMessage
	outboundConsume chan OutboundMessage
	redis           *redisBridge
}

// New creates a new MessageBus with buffered channels.
func New() *MessageBus {
	inbound := make(chan InboundMessage, 100)
	outbound := make(chan OutboundMessage, 100)
	return &MessageBus{
		Inbound:         inbound,
		Outbound:        outbound,
		inboundConsume:  inbound,
		outboundConsume: outbound,
	}
}

type RedisConfig struct {
	Client   *redis.Client
	Prefix   string
	Group    string
	Consumer string
}

// NewRedis creates a MessageBus that uses Redis Streams for shared inbound and
// outbound delivery. Web/API traffic stays local because browser SSE and
// HTTP callers are tied to the current process.
func NewRedis(cfg RedisConfig) *MessageBus {
	inbound := make(chan InboundMessage, 100)
	outbound := make(chan OutboundMessage, 100)
	inboundConsume := make(chan InboundMessage, 100)
	outboundConsume := make(chan OutboundMessage, 100)
	prefix := strings.Trim(cfg.Prefix, ":")
	if prefix == "" {
		prefix = "fastclaw"
	}
	group := cfg.Group
	if group == "" {
		group = "fastclaw"
	}
	consumer := cfg.Consumer
	if consumer == "" {
		consumer = fmt.Sprintf("consumer-%d", time.Now().UnixNano())
	}
	return &MessageBus{
		Inbound:         inbound,
		Outbound:        outbound,
		inboundConsume:  inboundConsume,
		outboundConsume: outboundConsume,
		redis: &redisBridge{
			client:         cfg.Client,
			prefix:         prefix,
			group:          group,
			consumer:       consumer,
			inboundKey:     prefix + ":bus:inbound",
			outboundKey:    prefix + ":bus:outbound",
			inboundLocal:   inboundConsume,
			outboundLocal:  outboundConsume,
			inboundSource:  inbound,
			outboundSource: outbound,
		},
	}
}

func (b *MessageBus) InboundConsumer() <-chan InboundMessage {
	if b == nil || b.inboundConsume == nil {
		return nil
	}
	return b.inboundConsume
}

func (b *MessageBus) OutboundConsumer() <-chan OutboundMessage {
	if b == nil || b.outboundConsume == nil {
		return nil
	}
	return b.outboundConsume
}

func (b *MessageBus) Start(ctx context.Context) error {
	if b == nil || b.redis == nil {
		return nil
	}
	return b.redis.start(ctx)
}

type redisBridge struct {
	client         *redis.Client
	prefix         string
	group          string
	consumer       string
	inboundKey     string
	outboundKey    string
	inboundLocal   chan InboundMessage
	outboundLocal  chan OutboundMessage
	inboundSource  chan InboundMessage
	outboundSource chan OutboundMessage
}

func (r *redisBridge) start(ctx context.Context) error {
	if r.client == nil {
		return errors.New("redis bus requires client")
	}
	for _, key := range []string{r.inboundKey, r.outboundKey} {
		if err := r.client.XGroupCreateMkStream(ctx, key, r.group, "0").Err(); err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
			return fmt.Errorf("create redis stream group %s: %w", key, err)
		}
	}
	go r.publishInbound(ctx)
	go r.publishOutbound(ctx)
	go r.consumeInbound(ctx)
	go r.consumeOutbound(ctx)
	slog.Info("redis message bus enabled", "prefix", r.prefix, "group", r.group, "consumer", r.consumer)
	return nil
}

func (r *redisBridge) publishInbound(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-r.inboundSource:
			if localOnly(msg.Channel) {
				deliver(ctx, r.inboundLocal, msg)
				continue
			}
			if err := r.xadd(ctx, r.inboundKey, msg); err != nil {
				slog.Error("redis inbound enqueue failed", "channel", msg.Channel, "account", msg.AccountID, "error", err)
			}
		}
	}
}

func (r *redisBridge) publishOutbound(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-r.outboundSource:
			if localOnly(msg.Channel) {
				deliver(ctx, r.outboundLocal, msg)
				continue
			}
			if err := r.xadd(ctx, r.outboundKey, msg); err != nil {
				slog.Error("redis outbound enqueue failed", "channel", msg.Channel, "account", msg.AccountID, "error", err)
			}
		}
	}
}

func (r *redisBridge) xadd(ctx context.Context, key string, msg any) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return r.client.XAdd(ctx, &redis.XAddArgs{
		Stream: key,
		MaxLen: 10000,
		Approx: true,
		Values: map[string]any{"payload": payload},
	}).Err()
}

func (r *redisBridge) consumeInbound(ctx context.Context) {
	r.consume(ctx, r.inboundKey, func(payload []byte) error {
		var msg InboundMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			return err
		}
		deliver(ctx, r.inboundLocal, msg)
		return nil
	})
}

func (r *redisBridge) consumeOutbound(ctx context.Context) {
	r.consume(ctx, r.outboundKey, func(payload []byte) error {
		var msg OutboundMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			return err
		}
		deliver(ctx, r.outboundLocal, msg)
		return nil
	})
}

func (r *redisBridge) consume(ctx context.Context, stream string, handle func([]byte) error) {
	for ctx.Err() == nil {
		res, err := r.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    r.group,
			Consumer: r.consumer,
			Streams:  []string{stream, ">"},
			Count:    10,
			Block:    5 * time.Second,
		}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) || ctx.Err() != nil {
				continue
			}
			slog.Warn("redis stream read failed", "stream", stream, "error", err)
			continue
		}
		for _, xs := range res {
			for _, xm := range xs.Messages {
				raw, ok := xm.Values["payload"]
				if !ok {
					_ = r.client.XAck(ctx, stream, r.group, xm.ID).Err()
					continue
				}
				payload, ok := raw.(string)
				if !ok {
					_ = r.client.XAck(ctx, stream, r.group, xm.ID).Err()
					continue
				}
				if err := handle([]byte(payload)); err != nil {
					slog.Error("redis stream payload failed", "stream", stream, "id", xm.ID, "error", err)
					continue
				}
				if err := r.client.XAck(ctx, stream, r.group, xm.ID).Err(); err != nil {
					slog.Warn("redis stream ack failed", "stream", stream, "id", xm.ID, "error", err)
				}
			}
		}
	}
}

func deliver[T any](ctx context.Context, ch chan T, msg T) {
	select {
	case ch <- msg:
	case <-ctx.Done():
	}
}

func localOnly(channel string) bool {
	return channel == "" || channel == "web" || channel == "api"
}
