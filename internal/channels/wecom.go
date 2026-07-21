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
		pending: make(map[string]chan weComAck),
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

	err = w.readLoop(connCtx, conn)
	cancel()
	if ctx.Err() != nil {
		return true, ctx.Err()
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

func (w *WeCom) handleCallbackFrame(context.Context, weComFrame) error {
	// Message/event mapping is implemented separately from the connection
	// lifecycle so malformed callbacks cannot interfere with ack handling.
	return nil
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
			if _, err := w.sendRequest(ctx, reqID, weComCmdPing, nil); err != nil && ctx.Err() == nil {
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
	body := map[string]any{
		"chatid":   msg.ChatID,
		"msgtype":  "markdown",
		"markdown": map[string]string{"content": msg.Text},
	}
	ctx, cancel := context.WithTimeout(context.Background(), w.requestTimeout)
	defer cancel()
	_, err := w.sendRequest(ctx, w.requestID(weComCmdSend), weComCmdSend, body)
	return err
}

func (w *WeCom) SendTyping(string) error { return nil }

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
