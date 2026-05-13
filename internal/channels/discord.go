package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
)

var discordMentionRe = regexp.MustCompile(`<@!?(\d+)>`)

// Discord implements the Channel interface for Discord bots.
type Discord struct {
	session     *discordgo.Session
	bus         *bus.MessageBus
	accountID   string
	botUserID   string
	botUsername string

	// Thread tracking: thread IDs the bot has participated in.
	// Follow-up messages in these threads don't require @mention.
	participatedThreads map[string]struct{}
	threadsMu           sync.RWMutex

	// Whether to auto-create threads on @mention.
	autoThread bool
}

// NewDiscord creates a new Discord channel instance.
func NewDiscord(botToken string, accountID string, mb *bus.MessageBus) (*Discord, error) {
	dg, err := discordgo.New("Bot " + botToken)
	if err != nil {
		return nil, err
	}

	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent

	autoThread := os.Getenv("FASTCLAW_DISCORD_AUTO_THREAD") != "false"

	d := &Discord{
		session:             dg,
		bus:                 mb,
		accountID:           accountID,
		autoThread:          autoThread,
		participatedThreads: make(map[string]struct{}),
	}

	dg.AddHandler(d.onMessageCreate)

	return d, nil
}

func (d *Discord) Name() string {
	return "discord"
}

func (d *Discord) AccountID() string {
	return d.accountID
}

func (d *Discord) BotUsername() string {
	return d.botUsername
}

// Start connects to Discord gateway and blocks until ctx is cancelled.
func (d *Discord) Start(ctx context.Context) error {
	if err := d.session.Open(); err != nil {
		return err
	}
	defer d.session.Close()

	// Cache bot user info
	d.botUserID = d.session.State.User.ID
	d.botUsername = d.session.State.User.Username

	// Load persisted thread tracking from disk
	d.loadParticipatedThreads()

	slog.Info("discord bot connected",
		"username", d.botUsername,
		"user_id", d.botUserID,
		"account", d.accountID,
		"auto_thread", d.autoThread,
		"tracked_threads", len(d.participatedThreads),
	)

	<-ctx.Done()
	return nil
}

// Send sends a message to a Discord channel.
func (d *Discord) Send(chatID string, text string) error {
	// Discord has a 2000 char limit; split if needed
	for len(text) > 0 {
		chunk := text
		if len(chunk) > 2000 {
			chunk = text[:2000]
			text = text[2000:]
		} else {
			text = ""
		}
		if _, err := d.session.ChannelMessageSend(chatID, chunk); err != nil {
			return err
		}
	}
	return nil
}

// SendMessage delivers text + any pre-resolved MediaItems to Discord.
// Discord renders standard markdown natively (bold/italic/code/lists),
// so msg.Text goes through unchanged. MediaItems upload as message
// attachments — Discord auto-renders images inline. Single
// ChannelMessageSendComplex call carries both, but if there's a long
// body that needs chunking we send the body chunked first and the
// files on the last chunk.
func (d *Discord) SendMessage(msg bus.OutboundMessage) error {
	if msg.Text != "" {
		// Discord 2000-char per-message limit. Send N-1 chunks
		// without files, then the final chunk with files attached so
		// the embedded preview lands at the end of the conversation.
		chunks := splitDiscordMessage(msg.Text)
		for i, chunk := range chunks {
			if i < len(chunks)-1 || len(msg.MediaItems) == 0 {
				if _, err := d.session.ChannelMessageSend(msg.ChatID, chunk); err != nil {
					slog.Warn("discord chunk send failed", "i", i, "error", err)
				}
				continue
			}
			if err := d.sendWithFiles(msg.ChatID, chunk, msg.MediaItems); err != nil {
				slog.Warn("discord final chunk+files failed", "error", err)
			}
		}
		return nil
	}
	if len(msg.MediaItems) > 0 {
		return d.sendWithFiles(msg.ChatID, "", msg.MediaItems)
	}
	return nil
}

func (d *Discord) sendWithFiles(chatID, text string, items []bus.MediaItem) error {
	files := make([]*discordgo.File, 0, len(items))
	for _, it := range items {
		ct := it.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		files = append(files, &discordgo.File{
			Name:        it.Filename,
			ContentType: ct,
			Reader:      bytes.NewReader(it.Bytes),
		})
	}
	_, err := d.session.ChannelMessageSendComplex(chatID, &discordgo.MessageSend{
		Content: text,
		Files:   files,
	})
	return err
}

func splitDiscordMessage(text string) []string {
	if len(text) <= 2000 {
		return []string{text}
	}
	var out []string
	for len(text) > 0 {
		if len(text) <= 2000 {
			out = append(out, text)
			break
		}
		// Prefer a paragraph break so we don't tear sentences apart.
		cut := strings.LastIndex(text[:2000], "\n\n")
		if cut < 1000 {
			cut = strings.LastIndex(text[:2000], "\n")
		}
		if cut < 1000 {
			cut = 2000
		}
		out = append(out, text[:cut])
		text = strings.TrimLeft(text[cut:], "\n")
	}
	return out
}

// SendTyping sends a typing indicator to the Discord channel.
func (d *Discord) SendTyping(chatID string) error {
	return d.session.ChannelTyping(chatID)
}

func (d *Discord) onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignore own messages
	if m.Author.ID == d.botUserID {
		return
	}

	// Determine peer kind
	peerKind := "dm"
	if m.GuildID != "" {
		peerKind = "group"
	}

	// Determine if we're already in a thread
	isThread := false
	if ch, err := s.State.Channel(m.ChannelID); err == nil && ch != nil {
		isThread = ch.Type == discordgo.ChannelTypeGuildPublicThread ||
			ch.Type == discordgo.ChannelTypeGuildPrivateThread
	}

	// Check if bot is mentioned
	botMentioned := false
	for _, u := range m.Mentions {
		if u.ID == d.botUserID {
			botMentioned = true
			break
		}
	}

	// In a participated thread, treat every message as implicitly mentioned.
	// In group channels, only respond to @mentions.
	if peerKind == "group" && !botMentioned {
		if isThread && d.isParticipatedThread(m.ChannelID) {
			// Thread we've participated in — respond without explicit @mention
		} else {
			// Not mentioned, not in a participated thread — inject to agent
			// history but don't trigger a response
			d.bus.Inbound <- bus.InboundMessage{
				Channel:      "discord",
				AccountID:    d.accountID,
				ChatID:       m.ChannelID,
				UserID:       m.Author.ID,
				MessageID:    m.ID,
				Text:         m.Content,
				PeerKind:     peerKind,
				SenderName:   m.Author.Username,
				IsBotMessage: m.Author.Bot,
			}
			return
		}
	}

	// Parse @mentions
	var mentions []string
	for _, u := range m.Mentions {
		mentions = append(mentions, u.Username)
	}

	// Check if sender is a bot
	isBot := m.Author.Bot

	// Clean message text: replace <@ID> mentions with @username
	text := m.Content
	for _, u := range m.Mentions {
		text = strings.ReplaceAll(text, "<@"+u.ID+">", "@"+u.Username)
		text = strings.ReplaceAll(text, "<@!"+u.ID+">", "@"+u.Username)
	}

	// Target channel for routing (default: current channel)
	targetChatID := m.ChannelID

	// Auto-thread: create a thread if this is a guild channel (not DM, not already a thread)
	// and the bot was @mentioned and autoThread is enabled.
	if d.autoThread && peerKind == "group" && !isThread && botMentioned {
		threadName := d.deriveThreadName(m.Content)
		thread, err := s.MessageThreadStart(m.ChannelID, m.ID, threadName, 1440)
		if err != nil {
			// Fallback: try creating thread without attaching to the message
			slog.Warn("discord MessageThreadStart failed, trying fallback",
				"channel", m.ChannelID, "error", err)
			// Send a seed message and thread from it
			seedMsg, seedErr := s.ChannelMessageSend(m.ChannelID,
				"🧵 **"+threadName+"**")
			if seedErr == nil {
				thread, threadErr := s.MessageThreadStart(m.ChannelID, seedMsg.ID, threadName, 1440)
				if threadErr == nil {
					targetChatID = thread.ID
					d.markParticipatedThread(thread.ID)
					slog.Info("discord auto-thread created (fallback)",
						"thread_id", thread.ID,
						"name", threadName,
						"channel", m.ChannelID,
					)
				}
			}
		} else {
			targetChatID = thread.ID
			d.markParticipatedThread(thread.ID)
			slog.Info("discord auto-thread created",
				"thread_id", thread.ID,
				"name", threadName,
				"channel", m.ChannelID,
			)
		}
	} else if isThread {
		d.markParticipatedThread(m.ChannelID)
	}

	slog.Info("discord message received",
		"from", m.Author.Username,
		"channel_id", m.ChannelID,
		"target_chat_id", targetChatID,
		"guild_id", m.GuildID,
		"peer_kind", peerKind,
		"is_bot", isBot,
		"is_thread", isThread,
		"auto_thread", targetChatID != m.ChannelID,
	)

	d.bus.Inbound <- bus.InboundMessage{
		Channel:      "discord",
		AccountID:    d.accountID,
		ChatID:       targetChatID,
		UserID:       m.Author.ID,
		MessageID:    m.ID,
		Text:         text,
		PeerKind:     peerKind,
		SenderName:   m.Author.Username,
		Mentions:     mentions,
		IsBotMessage: isBot,
	}
}

// deriveThreadName creates a thread name from message content, stripping
// @mentions and truncating to 80 characters.
func (d *Discord) deriveThreadName(content string) string {
	// Remove bot mentions
	content = discordMentionRe.ReplaceAllString(content, "")
	content = strings.TrimSpace(content)

	if content == "" {
		return "Conversation"
	}

	// Truncate to 80 chars, preferring word boundaries
	const maxLen = 80
	runes := []rune(content)
	if len(runes) <= maxLen {
		return content
	}
	// Try to break at last space before maxLen
	truncated := string(runes[:maxLen])
	if lastSpace := strings.LastIndex(truncated, " "); lastSpace > maxLen/2 {
		return truncated[:lastSpace]
	}
	return truncated
}

// --- Thread participation tracking ---

func (d *Discord) threadsPath() string {
	home, _ := config.HomeDir()
	return filepath.Join(home, "discord_threads.json")
}

func (d *Discord) loadParticipatedThreads() {
	path := d.threadsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var threads []string
	if err := json.Unmarshal(data, &threads); err != nil {
		return
	}
	d.threadsMu.Lock()
	defer d.threadsMu.Unlock()
	for _, id := range threads {
		d.participatedThreads[id] = struct{}{}
	}
}

func (d *Discord) saveParticipatedThreads() {
	d.threadsMu.RLock()
	threads := make([]string, 0, len(d.participatedThreads))
	for id := range d.participatedThreads {
		threads = append(threads, id)
	}
	d.threadsMu.RUnlock()

	// Cap at 500 threads
	const maxTracked = 500
	if len(threads) > maxTracked {
		threads = threads[len(threads)-maxTracked:]
	}

	data, err := json.Marshal(threads)
	if err != nil {
		slog.Warn("discord failed to marshal thread list", "error", err)
		return
	}
	if err := os.WriteFile(d.threadsPath(), data, 0644); err != nil {
		slog.Warn("discord failed to save thread list", "error", err)
	}
}

func (d *Discord) markParticipatedThread(threadID string) {
	d.threadsMu.Lock()
	if _, ok := d.participatedThreads[threadID]; ok {
		d.threadsMu.Unlock()
		return
	}
	d.participatedThreads[threadID] = struct{}{}
	d.threadsMu.Unlock()
	d.saveParticipatedThreads()
}

func (d *Discord) isParticipatedThread(threadID string) bool {
	d.threadsMu.RLock()
	defer d.threadsMu.RUnlock()
	_, ok := d.participatedThreads[threadID]
	return ok
}
