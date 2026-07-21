package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/gorilla/websocket"
)

var ErrWeComDisconnected = errors.New("wecom connection replaced by another client")

type WeComOptions struct {
	BotID      string
	Secret     string
	AccountID  string
	WSURL      string
	Dialer     *websocket.Dialer
	HTTPClient *http.Client

	HeartbeatInterval  time.Duration
	RequestTimeout     time.Duration
	ReconnectBaseDelay time.Duration
	ReconnectMaxDelay  time.Duration
	MaxReconnects      int
	RequestID          func(string) string
	ReconnectJitter    func(time.Duration) time.Duration
}

type weComAck struct {
	frame weComFrame
	err   error
}

type weComReplyRef struct {
	ReqID     string
	ChatID    string
	ExpiresAt time.Time
}

type weComInboundMessage struct {
	MsgID    string `json:"msgid"`
	AIBotID  string `json:"aibotid"`
	ChatID   string `json:"chatid,omitempty"`
	ChatType string `json:"chattype"`
	From     struct {
		UserID string `json:"userid"`
	} `json:"from"`
	MsgType string `json:"msgtype"`
	Text    struct {
		Content string `json:"content"`
	} `json:"text,omitempty"`
	Image weComInboundMedia `json:"image,omitempty"`
	File  weComInboundMedia `json:"file,omitempty"`
	Mixed struct {
		Items []struct {
			MsgType string            `json:"msgtype"`
			Text    weComInboundText  `json:"text,omitempty"`
			Image   weComInboundMedia `json:"image,omitempty"`
		} `json:"msg_item"`
	} `json:"mixed,omitempty"`
}

type weComInboundText struct {
	Content string `json:"content"`
}

type weComInboundMedia struct {
	URL    string `json:"url"`
	AESKey string `json:"aeskey,omitempty"`
}

type WeCom struct {
	botID     string
	secret    string
	accountID string
	wsURL     string
	dialer    *websocket.Dialer
	http      *http.Client
	bus       *bus.MessageBus

	heartbeatInterval  time.Duration
	requestTimeout     time.Duration
	reconnectBaseDelay time.Duration
	reconnectMaxDelay  time.Duration
	maxReconnects      int
	requestID          func(string) string
	reconnectJitter    func(time.Duration) time.Duration

	connMu sync.RWMutex
	conn   *websocket.Conn

	writeMu   sync.Mutex
	requestMu sync.Mutex
	pendingMu sync.Mutex
	pending   map[string]chan weComAck
	// sendRequestHook is test-only injection for callback error paths. Normal
	// construction leaves it nil and request() uses the authenticated socket.
	sendRequestHook func(context.Context, string, string, any) (weComFrame, error)

	replyMu      sync.Mutex
	replyRefs    map[string]weComReplyRef
	replyRefTTL  time.Duration
	maxReplyRefs int
	now          func() time.Time
}

func NewWeCom(opts WeComOptions, mb *bus.MessageBus) (*WeCom, error) {
	botID := strings.TrimSpace(opts.BotID)
	if botID == "" {
		return nil, errors.New("wecom bot ID is required")
	}
	if strings.TrimSpace(opts.Secret) == "" {
		return nil, errors.New("wecom secret is required")
	}
	if mb == nil {
		return nil, errors.New("wecom message bus is required")
	}
	accountID := strings.TrimSpace(opts.AccountID)
	if accountID == "" {
		accountID = botID
	}
	wsURL := strings.TrimSpace(opts.WSURL)
	if wsURL == "" {
		wsURL = weComDefaultWSURL
	}
	dialer := opts.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	heartbeatInterval := opts.HeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = 30 * time.Second
	}
	requestTimeout := opts.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 10 * time.Second
	}
	reconnectBaseDelay := opts.ReconnectBaseDelay
	if reconnectBaseDelay <= 0 {
		reconnectBaseDelay = time.Second
	}
	reconnectMaxDelay := opts.ReconnectMaxDelay
	if reconnectMaxDelay <= 0 {
		reconnectMaxDelay = 30 * time.Second
	}
	maxReconnects := opts.MaxReconnects
	if maxReconnects == 0 {
		maxReconnects = 10
	}
	requestID := opts.RequestID
	if requestID == nil {
		requestID = defaultWeComRequestID
	}
	reconnectJitter := opts.ReconnectJitter
	if reconnectJitter == nil {
		reconnectJitter = func(d time.Duration) time.Duration {
			return d + time.Duration(rand.Int64N(max(int64(d/5), 1)))
		}
	}

	return &WeCom{
		botID: botID, secret: opts.Secret, accountID: accountID,
		wsURL: wsURL, dialer: dialer, http: httpClient, bus: mb,
		heartbeatInterval: heartbeatInterval, requestTimeout: requestTimeout,
		reconnectBaseDelay: reconnectBaseDelay, reconnectMaxDelay: reconnectMaxDelay,
		maxReconnects: maxReconnects, requestID: requestID, reconnectJitter: reconnectJitter,
		pending: make(map[string]chan weComAck), replyRefs: make(map[string]weComReplyRef),
		replyRefTTL: 5 * time.Minute, maxReplyRefs: 2048, now: time.Now,
	}, nil
}

func WeComValidateCredentials(ctx context.Context, botID, secret string) error {
	return validateWeComCredentials(ctx, WeComOptions{BotID: botID, Secret: secret})
}

func validateWeComCredentials(ctx context.Context, opts WeComOptions) error {
	mb := bus.New()
	w, err := NewWeCom(opts, mb)
	if err != nil {
		return err
	}
	validationCtx, cancel := context.WithTimeout(ctx, w.requestTimeout)
	defer cancel()
	conn, _, err := w.dialer.DialContext(validationCtx, w.wsURL, nil)
	if err != nil {
		if validationCtx.Err() != nil {
			return validationCtx.Err()
		}
		return fmt.Errorf("connect to wecom: %w", err)
	}
	defer conn.Close()
	return authenticateWeComConn(validationCtx, conn, w.botID, w.secret, w.requestTimeout, w.requestID)
}

func (w *WeCom) Name() string        { return "wecom" }
func (w *WeCom) AccountID() string   { return w.accountID }
func (w *WeCom) BotUsername() string { return w.botID }

func (w *WeCom) Start(ctx context.Context) error {
	reconnects := 0
	for ctx.Err() == nil {
		authenticated, err := w.runConnection(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrWeComDisconnected) || isWeComAuthenticationError(err) {
			slog.Error("wecom adapter stopped on terminal protocol error", "account", w.accountID, "error", err)
			<-ctx.Done()
			return err
		}
		if authenticated {
			reconnects = 0
		}
		reconnects++
		if w.maxReconnects >= 0 && reconnects > w.maxReconnects {
			terminal := fmt.Errorf("wecom reconnect attempts exhausted after %d attempts: %w", w.maxReconnects, err)
			slog.Error("wecom adapter stopped after reconnect exhaustion", "account", w.accountID, "error", terminal)
			<-ctx.Done()
			return terminal
		}
		delay := w.reconnectDelay(reconnects)
		if !sleepOrDone(ctx, delay) {
			return nil
		}
	}
	return nil
}

func isWeComAuthenticationError(err error) bool {
	var protocolErr *WeComProtocolError
	return errors.As(err, &protocolErr) && protocolErr.Operation == "authentication"
}

func (w *WeCom) reconnectDelay(attempt int) time.Duration {
	delay := w.reconnectBaseDelay
	for i := 1; i < attempt && delay < w.reconnectMaxDelay; i++ {
		if delay > w.reconnectMaxDelay/2 {
			delay = w.reconnectMaxDelay
			break
		}
		delay *= 2
	}
	if delay > w.reconnectMaxDelay {
		delay = w.reconnectMaxDelay
	}
	return w.reconnectJitter(delay)
}

func (w *WeCom) runConnection(ctx context.Context) (bool, error) {
	conn, _, err := w.dialer.DialContext(ctx, w.wsURL, nil)
	if err != nil {
		return false, fmt.Errorf("connect to wecom: %w", err)
	}
	if err := authenticateWeComConn(ctx, conn, w.botID, w.secret, w.requestTimeout, w.requestID); err != nil {
		_ = conn.Close()
		return false, err
	}

	connCtx, cancel := context.WithCancel(ctx)
	w.setConnection(conn)
	defer func() {
		cancel()
		w.clearConnection(conn)
		w.failPending(errors.New("wecom connection closed"))
		_ = conn.Close()
	}()

	go func() {
		<-connCtx.Done()
		_ = conn.Close()
	}()
	go w.runHeartbeat(connCtx, conn)
	targetedErrors := make(chan error, 1)
	if w.bus.HasTargetedOutbound() {
		go func() {
			err := w.bus.ConsumeTargetedOutbound(connCtx, "wecom", w.accountID, w.SendMessage)
			if err != nil && connCtx.Err() == nil {
				targetedErrors <- err
				_ = conn.Close()
			}
		}()
	}

	err = w.readLoop(connCtx, conn)
	cancel()
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	select {
	case targetedErr := <-targetedErrors:
		return true, fmt.Errorf("consume wecom targeted outbound: %w", targetedErr)
	default:
	}
	return true, err
}

func (w *WeCom) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for ctx.Err() == nil {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var frame weComFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			return fmt.Errorf("decode wecom frame: %w", err)
		}
		if frame.Cmd == weComCmdEventCallback && isWeComDisconnectedEvent(frame.Body) {
			return ErrWeComDisconnected
		}
		if frame.Cmd == weComCmdMsgCallback || frame.Cmd == weComCmdEventCallback {
			if err := w.handleCallbackFrame(ctx, frame); err != nil {
				slog.Error("wecom callback rejected", "account", w.accountID, "cmd", frame.Cmd, "error", err)
			}
			continue
		}
		w.deliverAck(frame)
	}
	return ctx.Err()
}

func isWeComDisconnectedEvent(body json.RawMessage) bool {
	var envelope struct {
		Event struct {
			EventType string `json:"eventtype"`
		} `json:"event"`
	}
	return json.Unmarshal(body, &envelope) == nil && envelope.Event.EventType == "disconnected_event"
}

func (w *WeCom) handleCallbackFrame(ctx context.Context, frame weComFrame) error {
	if frame.Cmd == weComCmdEventCallback {
		return nil
	}
	if frame.Cmd != weComCmdMsgCallback {
		return fmt.Errorf("unsupported wecom callback command %q", frame.Cmd)
	}
	if strings.TrimSpace(frame.Headers.ReqID) == "" {
		return errors.New("wecom callback missing request ID")
	}
	var callback weComInboundMessage
	if err := json.Unmarshal(frame.Body, &callback); err != nil {
		return fmt.Errorf("decode wecom message callback: %w", err)
	}
	callback.MsgID = strings.TrimSpace(callback.MsgID)
	callback.AIBotID = strings.TrimSpace(callback.AIBotID)
	callback.ChatID = strings.TrimSpace(callback.ChatID)
	callback.ChatType = strings.TrimSpace(callback.ChatType)
	callback.From.UserID = strings.TrimSpace(callback.From.UserID)
	if callback.MsgID == "" || callback.AIBotID == "" || callback.From.UserID == "" {
		return errors.New("wecom callback missing required message identity")
	}
	if callback.AIBotID != w.botID {
		return fmt.Errorf("wecom callback addressed unexpected bot %q", callback.AIBotID)
	}
	if callback.From.UserID == w.botID {
		return nil
	}

	peerKind := ""
	chatID := ""
	var mentions []string
	switch callback.ChatType {
	case "single":
		peerKind = "dm"
		chatID = callback.From.UserID
	case "group":
		if callback.ChatID == "" {
			return errors.New("wecom group callback missing chat ID")
		}
		peerKind = "group"
		chatID = callback.ChatID
		mentions = []string{w.botID}
	default:
		return fmt.Errorf("unsupported wecom chat type %q", callback.ChatType)
	}

	text, mediaItems, err := w.mapInboundContent(ctx, callback)
	if err != nil {
		if replyErr := w.respondCallbackError(ctx, frame.Headers.ReqID, callback.MsgID, err); replyErr != nil {
			return fmt.Errorf("%w; passive error reply failed: %v", err, replyErr)
		}
		return err
	}
	if strings.TrimSpace(text) == "" && len(mediaItems) == 0 {
		return errors.New("wecom callback has no supported content")
	}

	inbound := bus.InboundMessage{
		Channel: "wecom", AccountID: w.accountID, ChatID: chatID,
		UserID: callback.From.UserID, PeerKind: peerKind,
		MessageID: callback.MsgID, Text: text, Mentions: mentions, MediaItems: mediaItems,
		SharedIdentity: false,
	}
	w.storeReplyRef(callback.MsgID, weComReplyRef{
		ReqID: frame.Headers.ReqID, ChatID: chatID, ExpiresAt: w.now().Add(w.replyRefTTL),
	})
	select {
	case w.bus.Inbound <- inbound:
		return nil
	case <-ctx.Done():
		w.deleteReplyRef(callback.MsgID)
		return ctx.Err()
	}
}

func (w *WeCom) storeReplyRef(messageID string, ref weComReplyRef) {
	w.replyMu.Lock()
	defer w.replyMu.Unlock()
	now := w.now()
	for id, existing := range w.replyRefs {
		if !existing.ExpiresAt.After(now) {
			delete(w.replyRefs, id)
		}
	}
	if len(w.replyRefs) >= w.maxReplyRefs {
		var oldestID string
		var oldest time.Time
		for id, existing := range w.replyRefs {
			if oldestID == "" || existing.ExpiresAt.Before(oldest) {
				oldestID, oldest = id, existing.ExpiresAt
			}
		}
		delete(w.replyRefs, oldestID)
	}
	w.replyRefs[messageID] = ref
}

func (w *WeCom) lookupReplyRef(messageID string) (weComReplyRef, bool) {
	w.replyMu.Lock()
	defer w.replyMu.Unlock()
	ref, ok := w.replyRefs[messageID]
	if !ok {
		return weComReplyRef{}, false
	}
	if !ref.ExpiresAt.After(w.now()) {
		delete(w.replyRefs, messageID)
		return weComReplyRef{}, false
	}
	return ref, true
}

func (w *WeCom) deleteReplyRef(messageID string) {
	w.replyMu.Lock()
	delete(w.replyRefs, messageID)
	w.replyMu.Unlock()
}

func (w *WeCom) runHeartbeat(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(w.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reqID := w.requestID(weComCmdPing)
			if _, err := w.request(ctx, reqID, weComCmdPing, nil); err != nil && ctx.Err() == nil {
				slog.Warn("wecom heartbeat failed", "account", w.accountID, "error", err)
				_ = conn.Close()
				return
			}
		}
	}
}

func (w *WeCom) Send(chatID, text string) error {
	return w.SendMessage(bus.OutboundMessage{Channel: "wecom", AccountID: w.accountID, ChatID: chatID, Text: text})
}

func (w *WeCom) SendMessage(msg bus.OutboundMessage) error {
	if strings.TrimSpace(msg.ChatID) == "" {
		return errors.New("wecom outbound message requires chat ID")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if msg.StreamState != bus.StreamNone {
		return w.sendPassiveStream(ctx, msg)
	}
	return w.sendProactiveMessage(ctx, msg)
}

func (w *WeCom) SendTyping(string) error { return nil }

func splitWeComMarkdown(text string, maxBytes int) []string {
	if maxBytes <= 0 {
		return nil
	}
	if text == "" {
		return []string{""}
	}
	remaining := text
	chunks := make([]string, 0, len(text)/maxBytes+1)
	for len(remaining) > maxBytes {
		cut := maxBytes
		for cut > 0 && !utf8.RuneStart(remaining[cut]) {
			cut--
		}
		if cut == 0 {
			_, size := utf8.DecodeRuneInString(remaining)
			cut = size
		}
		if newline := strings.LastIndexByte(remaining[:cut], '\n'); newline >= cut/2 {
			cut = newline + 1
		}
		chunks = append(chunks, remaining[:cut])
		remaining = remaining[cut:]
	}
	if remaining != "" {
		chunks = append(chunks, remaining)
	}
	return chunks
}

func (w *WeCom) request(ctx context.Context, reqID, cmd string, body any) (weComFrame, error) {
	if w.sendRequestHook != nil {
		return w.sendRequestHook(ctx, reqID, cmd, body)
	}
	return w.sendRequest(ctx, reqID, cmd, body)
}

func (w *WeCom) sendRequest(ctx context.Context, reqID, cmd string, body any) (weComFrame, error) {
	w.requestMu.Lock()
	defer w.requestMu.Unlock()

	conn := w.activeConnection()
	if conn == nil {
		return weComFrame{}, errors.New("wecom websocket is not connected")
	}
	bodyJSON, err := marshalWeComBody(body)
	if err != nil {
		return weComFrame{}, err
	}
	ack := make(chan weComAck, 1)
	w.pendingMu.Lock()
	if _, exists := w.pending[reqID]; exists {
		w.pendingMu.Unlock()
		return weComFrame{}, fmt.Errorf("wecom request %q is already pending", reqID)
	}
	w.pending[reqID] = ack
	w.pendingMu.Unlock()
	defer func() {
		w.pendingMu.Lock()
		delete(w.pending, reqID)
		w.pendingMu.Unlock()
	}()

	w.writeMu.Lock()
	err = conn.WriteJSON(weComFrame{Cmd: cmd, Headers: weComHeaders{ReqID: reqID}, Body: bodyJSON})
	w.writeMu.Unlock()
	if err != nil {
		return weComFrame{}, fmt.Errorf("send wecom %s: %w", cmd, err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, w.requestTimeout)
	defer cancel()
	select {
	case result := <-ack:
		if result.err != nil {
			return weComFrame{}, result.err
		}
		if result.frame.ErrCode != 0 {
			return weComFrame{}, newWeComProtocolError(cmd, result.frame.ErrCode, result.frame.ErrMsg, w.secret)
		}
		return result.frame, nil
	case <-timeoutCtx.Done():
		return weComFrame{}, fmt.Errorf("wecom %s acknowledgement timed out: %w", cmd, timeoutCtx.Err())
	}
}

func (w *WeCom) deliverAck(frame weComFrame) {
	reqID := frame.Headers.ReqID
	if reqID == "" {
		return
	}
	w.pendingMu.Lock()
	ack := w.pending[reqID]
	w.pendingMu.Unlock()
	if ack == nil {
		return
	}
	select {
	case ack <- weComAck{frame: frame}:
	default:
	}
}

func (w *WeCom) failPending(err error) {
	w.pendingMu.Lock()
	defer w.pendingMu.Unlock()
	for _, ack := range w.pending {
		select {
		case ack <- weComAck{err: err}:
		default:
		}
	}
}

func (w *WeCom) setConnection(conn *websocket.Conn) {
	w.connMu.Lock()
	w.conn = conn
	w.connMu.Unlock()
}

func (w *WeCom) clearConnection(conn *websocket.Conn) {
	w.connMu.Lock()
	if w.conn == conn {
		w.conn = nil
	}
	w.connMu.Unlock()
}

func (w *WeCom) activeConnection() *websocket.Conn {
	w.connMu.RLock()
	defer w.connMu.RUnlock()
	return w.conn
}
