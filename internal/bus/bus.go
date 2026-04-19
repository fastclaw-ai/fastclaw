package bus

// InboundMessage represents a message received from a channel.
type InboundMessage struct {
	Channel      string   // channel type, e.g. "telegram"
	AccountID    string   // account within the channel (e.g. which bot)
	ChatID       string   // unique chat identifier within the channel
	UserID       string   // user identifier
	OwnerUserID  string   // fastclaw user that owns the agent (for multi-user routing)
	MessageID    string   // unique message identifier within the chat
	Text         string   // message text
	PeerKind     string   // "group" or "dm"
	SenderName   string   // display name of the sender
	Mentions     []string // @usernames mentioned in the message
	IsBotMessage bool     // true if the message was sent by a bot
	PhotoURL     string   // URL of attached photo (if any)
	ReplyToMsgID string   // message ID being replied to
}

// OutboundButton represents a button in an inline keyboard.
type OutboundButton struct {
	Text         string
	CallbackData string
	URL          string
}

// OutboundMessage represents a message to be sent to a channel.
type OutboundMessage struct {
	Channel      string              // target channel type
	AccountID    string              // target account within the channel
	ChatID       string              // target chat identifier
	Text         string              // message text
	ReplyToMsgID string              // reply to specific message
	ParseMode    string              // "MarkdownV2", "HTML", ""
	Buttons      [][]OutboundButton  // inline keyboard rows
	EditMsgID    string              // edit existing message instead of sending new
	MediaPaths   []string            // file paths to attach (from MEDIA: protocol)
}

// MessageBus is an async message queue backed by Go channels.
type MessageBus struct {
	Inbound  chan InboundMessage
	Outbound chan OutboundMessage
}

// New creates a new MessageBus with buffered channels.
func New() *MessageBus {
	return &MessageBus{
		Inbound:  make(chan InboundMessage, 100),
		Outbound: make(chan OutboundMessage, 100),
	}
}
