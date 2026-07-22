package channels

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// Manager manages all channel instances and routes outbound messages.
type Manager struct {
	mu       sync.Mutex
	channels map[string]Channel // key: "channel:accountID"
	// singleton tracks which registered channels are gated by the
	// Leaser (one process at a time per (channel, accountID)). Set by
	// RegisterSingleton; non-singleton channels (webhook adapters,
	// Web fanout, plugin channels) are not present in the map and run
	// their Start unconditionally on every replica.
	singleton map[string]struct{}
	// tgTokens tracks Telegram bot tokens already claimed by this
	// process so we never start two pollers on the same token (they'd
	// fight over the long-poll lock and spam 409 Conflict forever).
	// Sticky for the process lifetime — Unregister doesn't release,
	// because the underlying GetUpdatesChan goroutine can't be cancelled
	// mid-poll (see Unregister).
	tgTokens map[string]struct{}
	bus      *bus.MessageBus
	// leaser + holderID drive the cross-process singleton gate. nil
	// leaser (or NopLeaser) reduces RegisterSingleton to plain Register.
	// holderID is the per-process identifier persisted into
	// channel_leases.holder_id and must stay stable across renewals.
	leaser   Leaser
	holderID string
	// Captured by Start so RegisterAndStart can hot-launch goroutines for
	// channels added after the initial bootstrap. nil until Start runs.
	rootCtx context.Context
	// runCancels owns the child context for the currently running adapter at
	// each channel key. Replacing or unregistering a persistent adapter must
	// cancel that child without stopping the rest of the manager.
	runCancels     map[string]context.CancelFunc
	runDones       map[string]chan struct{}
	runGenerations map[string]uint64
	nextGeneration uint64
}

// NewManager creates a new channel manager with no cross-process
// singleton support — all singleton-marked channels reduce to plain
// channels (Start on every replica). Use NewManagerWithLeaser when
// running multi-instance to gate polling adapters.
func NewManager(mb *bus.MessageBus) *Manager {
	return NewManagerWithLeaser(mb, NopLeaser{}, "")
}

// NewManagerWithLeaser wires a cross-process Leaser. `holderID` must be
// unique per process (typically a UUID minted at boot) and stable for
// the process lifetime so RenewChannelLease keeps matching the row.
func NewManagerWithLeaser(mb *bus.MessageBus, leaser Leaser, holderID string) *Manager {
	if leaser == nil {
		leaser = NopLeaser{}
	}
	return &Manager{
		channels:       make(map[string]Channel),
		singleton:      make(map[string]struct{}),
		tgTokens:       make(map[string]struct{}),
		bus:            mb,
		leaser:         leaser,
		holderID:       holderID,
		runCancels:     make(map[string]context.CancelFunc),
		runDones:       make(map[string]chan struct{}),
		runGenerations: make(map[string]uint64),
	}
}

// ClaimTelegramToken returns true if the caller is the first to claim
// this token in this process, false if another adapter already holds
// it. Callers should skip registration when this returns false.
// Empty tokens are not tracked (NewTelegram will fail loudly on them).
func (m *Manager) ClaimTelegramToken(token string) bool {
	if token == "" {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.tgTokens[token]; exists {
		return false
	}
	m.tgTokens[token] = struct{}{}
	return true
}

// Register adds a channel to the manager keyed by channel:accountID.
// Use this BEFORE Start; for hot-add after Start, use RegisterAndStart.
func (m *Manager) Register(ch Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := channelKey(ch.Name(), ch.AccountID())
	m.channels[key] = ch
}

// RegisterSingleton is like Register but marks the channel as needing
// cross-process leader election. Only one replica's Start runs at a
// time per (channel, accountID); peers wait on the Leaser until the
// active holder dies. Use for polling / persistent-connection adapters
// (Telegram long-poll, WeChat iLink long-poll, Discord WS, Slack
// Socket Mode, Feishu long-conn) — anything that would deliver inbound
// twice if two processes spoke the same upstream protocol at once.
func (m *Manager) RegisterSingleton(ch Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := channelKey(ch.Name(), ch.AccountID())
	m.channels[key] = ch
	m.singleton[key] = struct{}{}
}

// RegisterAndStart adds a channel AND, if Start has already run, kicks
// off its polling goroutine immediately. Used by the dashboard's
// channel-config handlers so a freshly-saved Telegram bot starts
// receiving updates without a process restart.
//
// Safe to call before Start too — falls back to plain Register in that
// case (Start picks it up like any other entry).
func (m *Manager) RegisterAndStart(ch Channel) {
	m.registerAndStart(ch, false)
}

// RegisterSingletonAndStart is the hot-add path for singleton-gated
// adapters. Same shape as RegisterAndStart, but the launched goroutine
// goes through the Leaser instead of calling ch.Start directly.
func (m *Manager) RegisterSingletonAndStart(ch Channel) {
	m.registerAndStart(ch, true)
}

func (m *Manager) registerAndStart(ch Channel, singleton bool) {
	m.mu.Lock()
	key := channelKey(ch.Name(), ch.AccountID())
	m.channels[key] = ch
	if singleton {
		m.singleton[key] = struct{}{}
	} else {
		delete(m.singleton, key)
	}
	ctx := m.rootCtx
	var generation uint64
	if ctx != nil {
		m.nextGeneration++
		generation = m.nextGeneration
		m.runGenerations[key] = generation
	}
	m.mu.Unlock()
	if ctx == nil {
		return
	}
	go m.startChannel(ctx, key, ch, singleton, generation, "hot-starting channel")
}

// Unregister removes a channel from the routing table and cancels its active
// adapter. Telegram token claims intentionally remain sticky for the process
// lifetime because the Telegram polling implementation cannot safely reuse a
// token after an interrupted poll.
func (m *Manager) Unregister(channelType, accountID string) {
	m.mu.Lock()
	key := channelKey(channelType, accountID)
	delete(m.channels, key)
	delete(m.singleton, key)
	cancel := m.runCancels[key]
	delete(m.runCancels, key)
	delete(m.runGenerations, key)
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Start launches all channels and the outbound message router.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	m.rootCtx = ctx
	chans := make(map[string]Channel, len(m.channels))
	singletons := make(map[string]bool, len(m.channels))
	generations := make(map[string]uint64, len(m.channels))
	for k, v := range m.channels {
		chans[k] = v
		_, singletons[k] = m.singleton[k]
		m.nextGeneration++
		generations[k] = m.nextGeneration
		m.runGenerations[k] = m.nextGeneration
	}
	m.mu.Unlock()

	var wg sync.WaitGroup

	// Start outbound router
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.routeOutbound(ctx)
	}()

	// Start each channel
	for key, ch := range chans {
		singleton := singletons[key]
		wg.Add(1)
		go func(k string, c Channel, s bool, generation uint64) {
			defer wg.Done()
			m.startChannel(ctx, k, c, s, generation, "starting channel")
		}(key, ch, singleton, generations[key])
	}

	wg.Wait()
}

func (m *Manager) startChannel(parent context.Context, key string, ch Channel, singleton bool, generation uint64, logMessage string) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})

	m.mu.Lock()
	if m.runGenerations[key] != generation {
		m.mu.Unlock()
		cancel()
		return
	}
	previous := m.runCancels[key]
	previousDone := m.runDones[key]
	m.runCancels[key] = cancel
	m.runDones[key] = done
	leaser := m.leaser
	holderID := m.holderID
	m.mu.Unlock()

	if previous != nil {
		previous()
	}
	defer func() {
		cancel()
		m.mu.Lock()
		if m.runGenerations[key] == generation {
			delete(m.runCancels, key)
			delete(m.runGenerations, key)
		}
		if m.runDones[key] == done {
			delete(m.runDones, key)
		}
		m.mu.Unlock()
		close(done)
	}()
	if previousDone != nil {
		<-previousDone
	}
	if ctx.Err() != nil {
		return
	}

	slog.Info(logMessage, "key", key, "singleton", singleton)
	if singleton {
		runWithLease(ctx, ch, leaser, holderID)
		return
	}
	if err := ch.Start(ctx); err != nil {
		slog.Error("channel stopped with error", "key", key, "error", err)
	}
}

func (m *Manager) routeOutbound(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-m.bus.OutboundConsumer():
			key := channelKey(msg.Channel, msg.AccountID)
			m.mu.Lock()
			ch, ok := m.channels[key]
			m.mu.Unlock()
			if !ok {
				slog.Warn("unknown outbound channel", "key", key)
				continue
			}
			m.dispatchOutbound(ch, msg, key)
		}
	}
}

// dispatchOutbound handles SplitMessageMarker centrally — every channel
// adapter sees one logical message per SendMessage call, regardless of
// whether the agent decided to split.
//
//	AllowSplit && marker present  → split text by marker, send sequentially
//	                                (media + buttons attach to the LAST
//	                                chunk so they only appear once)
//	AllowSplit && no marker       → send as-is
//	!AllowSplit && marker present → collapse marker to newline first so the
//	                                raw `<|split|>` token doesn't surface
//	                                as literal text on stale system-prompt
//	                                caches
//	!AllowSplit && no marker      → send as-is
//
// Sequential dispatch is guaranteed by routeOutbound's single-goroutine
// design — chunks arrive in order at the adapter.
func (m *Manager) dispatchOutbound(ch Channel, msg bus.OutboundMessage, key string) {
	hasMarker := strings.Contains(msg.Text, SplitMessageMarker)
	if !hasMarker {
		if err := ch.SendMessage(msg); err != nil {
			slog.Error("send message failed", "key", key, "error", err)
		}
		return
	}
	if !msg.AllowSplit {
		msg.Text = strings.ReplaceAll(msg.Text, SplitMessageMarker, "\n")
		if err := ch.SendMessage(msg); err != nil {
			slog.Error("send message failed", "key", key, "error", err)
		}
		return
	}
	chunks := SplitOutboundText(msg.Text)
	for i, chunk := range chunks {
		out := msg
		out.Text = chunk
		// Attach media + buttons + ReplyToMsgID + EditMsgID only to the
		// LAST chunk. Otherwise a single attachment would either ride
		// the first bubble (looks weird with text trailing it) or get
		// re-sent on every chunk (definitely wrong).
		if i < len(chunks)-1 {
			out.MediaItems = nil
			out.MediaPaths = nil
			out.Buttons = nil
			out.ReplyToMsgID = ""
			out.EditMsgID = ""
		}
		if err := ch.SendMessage(out); err != nil {
			slog.Error("send message failed", "key", key, "chunk", i, "error", err)
			// Stop on first error — sending the remaining bubbles
			// after a failure could look like a duplicate to the
			// chatter once the platform recovers.
			return
		}
	}
}

// BotUsername returns the bot username for a given channel:accountID pair.
func (m *Manager) BotUsername(channel, accountID string) string {
	key := channelKey(channel, accountID)
	m.mu.Lock()
	defer m.mu.Unlock()
	ch, ok := m.channels[key]
	if !ok {
		return ""
	}
	return ch.BotUsername()
}

// SendTyping sends a typing indicator for the given channel and chat.
func (m *Manager) SendTyping(channel, accountID, chatID string) {
	key := channelKey(channel, accountID)
	m.mu.Lock()
	ch, ok := m.channels[key]
	m.mu.Unlock()
	if !ok {
		return
	}
	if err := ch.SendTyping(chatID); err != nil {
		slog.Debug("send typing failed", "key", key, "error", err)
	}
}

// Has returns true when a channel with the given key is registered.
// Used by handlers to short-circuit redundant hot-starts.
func (m *Manager) Has(channel, accountID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.channels[channelKey(channel, accountID)]
	return ok
}

// Get returns the registered adapter for (channel, accountID), or nil.
// Used by the Feishu webhook handler to find the adapter that should
// dispatch an incoming event — the HTTP route receives the raw POST
// and needs to call the right Feishu instance's HandleWebhook based on
// the {accountId} (Feishu App ID) in the URL path.
func (m *Manager) Get(channel, accountID string) Channel {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.channels[channelKey(channel, accountID)]
}

func channelKey(channel, accountID string) string {
	if accountID == "" {
		return channel + ":"
	}
	return channel + ":" + accountID
}
