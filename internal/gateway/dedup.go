package gateway

import (
	"context"
	"fmt"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

const dedupTTL = 60 * time.Second

// dedupEntry tracks when a message was first seen.
type dedupEntry struct {
	seenAt time.Time
}

// isDuplicate reports whether this inbound has already been seen within
// the dedupTTL window. Returns false (= "process it") for messages with
// no usable dedup key (web / api / webhook callers with empty MessageID
// and a non-group PeerKind).
//
// Two distinct keying strategies because the failure modes differ:
//
//   - Group: WeCom provides a stable msgid, so use the exact
//     (channel, accountID, messageID) tuple. Other platforms retain the
//     text strategy because Telegram supergroups deliver the same logical
//     message to each bot with a *different* message_id. For those, key on
//     (channel, chatID, userID, text-hash) instead.
//   - DM: every supported IM channel (wechat / telegram / discord /
//     line / feishu / slack) emits a stable per-conversation
//     message_id. Key on (channel, accountID, messageID) so the same
//     inbound being pushed onto the bus twice — by an orphaned poller,
//     a multi-replica lease race, or an IM server retry — drops the
//     second copy here instead of fanning out to a duplicate LLM
//     reply downstream.
//
// Dedup is per-process in-memory (sync.Map). Cross-replica
// coordination still relies on the channel_leases TTL keeping only
// one polling adapter live at a time; if that ever drifts (clock
// skew, hung renew), each replica still suppresses its OWN duplicate
// pulls but two replicas could each emit one reply. A shared store
// (Redis/DB unique on (channel, accountID, messageID)) is the proper
// fix for that — out of scope here.
func (g *Gateway) isDuplicate(msg bus.InboundMessage) bool {
	if msg.PeerKind == "group" {
		// Intelligent Bot callbacks provide a stable msgid in groups. Use it
		// directly so retries remain duplicates even if text normalization
		// changes, while two intentional identical messages stay distinct.
		if msg.Channel == "wecom" && msg.MessageID != "" {
			key := fmt.Sprintf("group:%s:%s:%s", msg.Channel, msg.AccountID, msg.MessageID)
			_, loaded := g.dedup.LoadOrStore(key, dedupEntry{seenAt: time.Now()})
			return loaded
		}
		key := fmt.Sprintf("group:%s:%s:%s:%x", msg.Channel, msg.ChatID, msg.UserID, hashString(msg.Text))
		_, loaded := g.dedup.LoadOrStore(key, dedupEntry{seenAt: time.Now()})
		return loaded
	}
	// DM path. Some inbound sources don't carry a stable MessageID
	// (web SSE turns, /api/chat completions, webhook fan-out): nothing
	// to key on, just let them through — those paths don't have the
	// duplicate-poll failure mode IM channels do.
	if msg.MessageID == "" {
		return false
	}
	key := fmt.Sprintf("dm:%s:%s:%s", msg.Channel, msg.AccountID, msg.MessageID)
	_, loaded := g.dedup.LoadOrStore(key, dedupEntry{seenAt: time.Now()})
	return loaded
}

// cleanupDedup periodically removes expired entries from the dedup cache.
func (g *Gateway) cleanupDedup(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			g.dedup.Range(func(key, value any) bool {
				entry := value.(dedupEntry)
				if now.Sub(entry.seenAt) > dedupTTL {
					g.dedup.Delete(key)
				}
				return true
			})
		}
	}
}

func hashString(s string) uint32 {
	var h uint32
	for _, c := range s {
		h = h*31 + uint32(c)
	}
	return h
}
