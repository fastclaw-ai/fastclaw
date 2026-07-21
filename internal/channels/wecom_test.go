package channels

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/gorilla/websocket"
)

func TestNewWeComRequiresBotIDAndSecret(t *testing.T) {
	for i, opts := range []WeComOptions{
		{Secret: "fixture-secret"},
		{BotID: "bot-1"},
	} {
		if _, err := NewWeCom(opts, bus.New()); err == nil {
			t.Fatalf("NewWeCom credential validation case %d succeeded", i)
		}
	}
}

func TestWeComAuthenticatesWithSubscribeFrame(t *testing.T) {
	frames := make(chan weComFrame, 1)
	wsURL := newWeComWSServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var frame weComFrame
		if err := conn.ReadJSON(&frame); err != nil {
			return
		}
		frames <- frame
		_ = conn.WriteJSON(weComFrame{Headers: frame.Headers, ErrCode: 0, ErrMsg: "ok"})
	})

	err := validateWeComCredentials(context.Background(), WeComOptions{
		BotID: "bot-1", Secret: "fixture-secret", WSURL: wsURL,
		RequestTimeout: time.Second,
		RequestID:      func(string) string { return "aibot_subscribe-test-1" },
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-frames:
		if got.Cmd != weComCmdSubscribe || got.Headers.ReqID != "aibot_subscribe-test-1" {
			t.Fatalf("subscribe frame metadata = %#v", got)
		}
		var body weComSubscribeBody
		if err := json.Unmarshal(got.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.BotID != "bot-1" || body.Secret != "fixture-secret" {
			t.Fatalf("subscribe body did not match credentials")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscribe frame")
	}
}

func TestWeComAuthenticationErrorPreservesCodeWithoutSecret(t *testing.T) {
	const secret = "fixture-secret-never-print"
	wsURL := newWeComWSServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var frame weComFrame
		if err := conn.ReadJSON(&frame); err != nil {
			return
		}
		_ = conn.WriteJSON(weComFrame{
			Headers: frame.Headers,
			ErrCode: 40001,
			ErrMsg:  "invalid credential fixture-secret-never-print",
		})
	})

	err := validateWeComCredentials(context.Background(), WeComOptions{
		BotID: "bot-1", Secret: secret, WSURL: wsURL, RequestTimeout: time.Second,
	})
	var protocolErr *WeComProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if protocolErr.ErrCode != 40001 || !strings.Contains(protocolErr.ErrMsg, "invalid credential") {
		t.Fatalf("protocol error = %#v", protocolErr)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(protocolErr.ErrMsg, secret) {
		t.Fatalf("authentication error exposed the credential")
	}
}

func TestWeComCredentialValidationTimesOutWithoutPersistingState(t *testing.T) {
	wsURL := newWeComWSServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var frame weComFrame
		_ = conn.ReadJSON(&frame)
		<-time.After(200 * time.Millisecond)
	})

	err := validateWeComCredentials(context.Background(), WeComOptions{
		BotID: "bot-1", Secret: "fixture-secret", WSURL: wsURL,
		RequestTimeout: 30 * time.Millisecond,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("validation error = %v, want deadline exceeded", err)
	}
}

func TestWeComHeartbeatUsesPingFrame(t *testing.T) {
	heartbeat := make(chan weComFrame, 1)
	wsURL := newWeComWSServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var auth weComFrame
		if err := conn.ReadJSON(&auth); err != nil {
			return
		}
		if err := conn.WriteJSON(weComFrame{Headers: auth.Headers, ErrCode: 0, ErrMsg: "ok"}); err != nil {
			return
		}
		var ping weComFrame
		if err := conn.ReadJSON(&ping); err == nil {
			heartbeat <- ping
		}
	})

	w := newTestWeCom(t, wsURL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()
	select {
	case got := <-heartbeat:
		if got.Cmd != weComCmdPing || !strings.HasPrefix(got.Headers.ReqID, weComCmdPing) {
			t.Fatalf("heartbeat = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for heartbeat")
	}
	cancel()
	waitWeComDone(t, done)
}

func TestWeComContextCancellationClosesSocket(t *testing.T) {
	authenticated := make(chan struct{}, 1)
	closed := make(chan struct{}, 1)
	wsURL := newWeComWSServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var auth weComFrame
		if err := conn.ReadJSON(&auth); err != nil {
			return
		}
		if err := conn.WriteJSON(weComFrame{Headers: auth.Headers, ErrCode: 0, ErrMsg: "ok"}); err != nil {
			return
		}
		authenticated <- struct{}{}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				closed <- struct{}{}
				return
			}
		}
	})

	w := newTestWeCom(t, wsURL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()
	waitWeComSignal(t, authenticated, "authentication")
	cancel()
	waitWeComSignal(t, closed, "socket close")
	waitWeComDone(t, done)
}

func TestWeComRecoverableDisconnectReconnects(t *testing.T) {
	var connections atomic.Int32
	reconnected := make(chan struct{}, 1)
	wsURL := newWeComWSServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		attempt := connections.Add(1)
		var auth weComFrame
		if err := conn.ReadJSON(&auth); err != nil {
			return
		}
		if err := conn.WriteJSON(weComFrame{Headers: auth.Headers, ErrCode: 0, ErrMsg: "ok"}); err != nil {
			return
		}
		if attempt == 1 {
			return
		}
		reconnected <- struct{}{}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	w := newTestWeCom(t, wsURL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()
	waitWeComSignal(t, reconnected, "reconnection")
	if connections.Load() < 2 {
		t.Fatalf("connections = %d", connections.Load())
	}
	cancel()
	waitWeComDone(t, done)
}

func TestWeComDisconnectedEventIsTerminal(t *testing.T) {
	var connections atomic.Int32
	closed := make(chan struct{}, 1)
	wsURL := newWeComWSServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		connections.Add(1)
		var auth weComFrame
		if err := conn.ReadJSON(&auth); err != nil {
			return
		}
		if err := conn.WriteJSON(weComFrame{Headers: auth.Headers, ErrCode: 0, ErrMsg: "ok"}); err != nil {
			return
		}
		body, _ := json.Marshal(map[string]any{"event": map[string]any{"eventtype": "disconnected_event"}})
		_ = conn.WriteJSON(weComFrame{
			Cmd: weComCmdEventCallback, Headers: weComHeaders{ReqID: "event-1"}, Body: body,
		})
		if _, _, err := conn.ReadMessage(); err != nil {
			closed <- struct{}{}
		}
	})

	w := newTestWeCom(t, wsURL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()
	waitWeComSignal(t, closed, "terminal socket close")
	time.Sleep(80 * time.Millisecond)
	if got := connections.Load(); got != 1 {
		t.Fatalf("terminal event reconnected %d times", got)
	}
	cancel()
	if err := waitWeComDone(t, done); !errors.Is(err, ErrWeComDisconnected) {
		t.Fatalf("Start error = %v", err)
	}
}

func TestWeComRevokedCredentialsStopAuthRetry(t *testing.T) {
	var connections atomic.Int32
	responded := make(chan struct{}, 1)
	wsURL := newWeComWSServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		connections.Add(1)
		var auth weComFrame
		if err := conn.ReadJSON(&auth); err != nil {
			return
		}
		_ = conn.WriteJSON(weComFrame{Headers: auth.Headers, ErrCode: 40001, ErrMsg: "credential revoked"})
		responded <- struct{}{}
	})

	w := newTestWeCom(t, wsURL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()
	waitWeComSignal(t, responded, "authentication rejection")
	time.Sleep(80 * time.Millisecond)
	if got := connections.Load(); got != 1 {
		t.Fatalf("authentication rejection retried %d times", got)
	}
	cancel()
	var protocolErr *WeComProtocolError
	if err := waitWeComDone(t, done); !errors.As(err, &protocolErr) || protocolErr.ErrCode != 40001 {
		t.Fatalf("Start error = %T %v", err, err)
	}
}

func newTestWeCom(t *testing.T, wsURL string) *WeCom {
	t.Helper()
	w, err := NewWeCom(WeComOptions{
		BotID: "bot-1", Secret: "fixture-secret", AccountID: "bot-1", WSURL: wsURL,
		HeartbeatInterval:  10 * time.Millisecond,
		RequestTimeout:     100 * time.Millisecond,
		ReconnectBaseDelay: 10 * time.Millisecond,
		ReconnectMaxDelay:  20 * time.Millisecond,
		ReconnectJitter:    func(d time.Duration) time.Duration { return d },
	}, bus.New())
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func newWeComWSServer(t *testing.T, handle func(*websocket.Conn)) string {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		handle(conn)
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func waitWeComSignal(t *testing.T, ch <-chan struct{}, action string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", action)
	}
}

func waitWeComDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for WeCom adapter to stop")
		return nil
	}
}
