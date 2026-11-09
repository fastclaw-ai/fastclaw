package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// memStore is an in-memory Store.
type memStore struct {
	mu     sync.Mutex
	events []store.MeteredEvent
	target store.BillingTarget
	err    error
}

func (m *memStore) InsertMeteredEvent(_ context.Context, e *store.MeteredEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.events = append(m.events, *e)
	return nil
}

func (m *memStore) BillingTarget(_ context.Context, userID string) (store.BillingTarget, error) {
	t := m.target
	if t.AccountID == "" {
		t.AccountID = userID
	}
	return t, nil
}

// hookServer verifies each request the way an integrating app must and
// answers with answer(req).
type hookServer struct {
	*httptest.Server
	secret string
	mu     sync.Mutex
	bodies []string
	reqs   []AuthorizeRequest
}

func newHookServer(t *testing.T, secret string, answer func(AuthorizeRequest) (int, string)) *hookServer {
	t.Helper()
	h := &hookServer{secret: secret}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("method %s content-type %q", r.Method, r.Header.Get("Content-Type"))
		}
		// X-Fastclaw-Signature: t=<unix>,v1=<hex hmac-sha256(secret, t + "." + body)>
		sig := r.Header.Get(SignatureHeader)
		parts := strings.Split(sig, ",")
		if len(parts) != 2 || !strings.HasPrefix(parts[0], "t=") || !strings.HasPrefix(parts[1], "v1=") {
			t.Errorf("signature header %q", sig)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		ts, err := strconv.ParseInt(strings.TrimPrefix(parts[0], "t="), 10, 64)
		if err != nil || time.Since(time.Unix(ts, 0)).Abs() > 5*time.Minute {
			t.Errorf("stale or bad t in %q", sig)
		}
		mac := hmac.New(sha256.New, []byte(h.secret))
		mac.Write([]byte(strings.TrimPrefix(parts[0], "t=") + "." + string(body)))
		if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(strings.TrimPrefix(parts[1], "v1="))) {
			t.Errorf("signature mismatch for %q", sig)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req AuthorizeRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("body: %v", err)
		}
		h.mu.Lock()
		h.bodies = append(h.bodies, string(body))
		h.reqs = append(h.reqs, req)
		h.mu.Unlock()
		code, out := answer(req)
		w.WriteHeader(code)
		_, _ = io.WriteString(w, out)
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *hookServer) requests() []AuthorizeRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]AuthorizeRequest(nil), h.reqs...)
}

const testSecret = "s3cr3t-s3cr3t-s3cr3t-s3cr3t-s3cr3t"

func newTestTurn(st *memStore) *Turn {
	return NewService(st).NewTurn("u_end", "agt_1", "imc:acc_1:o9cq", "cron")
}

func TestAuthorizeSignsAndCarriesTheTurn(t *testing.T) {
	t.Setenv(EnvAllowPrivate, "true")
	hook := newHookServer(t, testSecret, func(r AuthorizeRequest) (int, string) {
		if r.Stage == StageTool {
			return 200, `{"allow": false, "message": "Out of image credits."}`
		}
		return 200, `{"allow": true}`
	})
	st := &memStore{target: store.BillingTarget{AccountID: "u_acct", EndUser: "user:42", HookURL: hook.URL + "/authorize", HookSecret: testSecret}}
	turn := newTestTurn(st)
	ctx := WithTurn(context.Background(), turn)

	if msg := turn.AuthorizeTurn(ctx, "openrouter", "autojev/fast"); msg != "" {
		t.Fatalf("turn denied: %q", msg)
	}
	err := AuthorizeTool(ctx, KindImage, "fal", "flux-pro", Estimate{Units: 2, Unit: "image"})
	var denied *Denied
	if !errors.As(err, &denied) || denied.Message != "Out of image credits." {
		t.Fatalf("tool authorize = %v", err)
	}

	reqs := hook.requests()
	if len(reqs) != 2 {
		t.Fatalf("hook calls = %d", len(reqs))
	}
	turnReq, toolReq := reqs[0], reqs[1]
	if !strings.HasPrefix(turnReq.ID, "authz_") || turnReq.Stage != "turn" || turnReq.Kind != "turn" ||
		turnReq.Estimate != nil || turnReq.Provider != "openrouter" || turnReq.Model != "autojev/fast" ||
		turnReq.EndUser != "user:42" || turnReq.AgentID != "agt_1" || turnReq.SessionKey != "imc:acc_1:o9cq" ||
		turnReq.Channel != "cron" || turnReq.TurnID != turn.ID || !strings.HasPrefix(turn.ID, "trn_") {
		t.Fatalf("turn request = %+v", turnReq)
	}
	if toolReq.Stage != "tool" || toolReq.Kind != "image" || toolReq.Provider != "fal" || toolReq.Model != "flux-pro" ||
		toolReq.Estimate == nil || toolReq.Estimate.Units != 2 || toolReq.Estimate.Unit != "image" || toolReq.TurnID != turn.ID ||
		toolReq.ID == turnReq.ID {
		t.Fatalf("tool request = %+v", toolReq)
	}
	// The exact wire shape (keys in the documented order).
	t.Logf("authorize body: %s", hook.bodies[1])
	for _, key := range []string{`"id":`, `"stage":`, `"kind":`, `"provider":`, `"model":`, `"estimate":{"units":2,"unit":"image"}`,
		`"end_user":"user:42"`, `"agent_id":`, `"session_key":`, `"channel":"cron"`, `"turn_id":`} {
		if !strings.Contains(hook.bodies[1], key) {
			t.Fatalf("body %s lacks %s", hook.bodies[1], key)
		}
	}
	if strings.Contains(hook.bodies[0], `"estimate"`) {
		t.Fatalf("turn stage must not carry an estimate: %s", hook.bodies[0])
	}
}

func TestAuthorizeDenialWithoutMessageGetsDefault(t *testing.T) {
	t.Setenv(EnvAllowPrivate, "true")
	hook := newHookServer(t, testSecret, func(AuthorizeRequest) (int, string) { return 200, `{"allow": false}` })
	st := &memStore{target: store.BillingTarget{HookURL: hook.URL, HookSecret: testSecret}}
	turn := newTestTurn(st)
	if msg := turn.AuthorizeTurn(context.Background(), "", ""); msg != defaultDenyMessage {
		t.Fatalf("deny message = %q", msg)
	}
}

func TestAuthorizeFailOpenAndFailClosed(t *testing.T) {
	t.Setenv(EnvAllowPrivate, "true")
	broken := map[string]func(AuthorizeRequest) (int, string){
		"500":        func(AuthorizeRequest) (int, string) { return 500, `{"allow": false, "message": "no"}` },
		"not json":   func(AuthorizeRequest) (int, string) { return 200, `ok` },
		"no allow":   func(AuthorizeRequest) (int, string) { return 200, `{"message": "?"}` },
		"redirected": func(AuthorizeRequest) (int, string) { return 302, `` },
	}
	for name, answer := range broken {
		hook := newHookServer(t, testSecret, answer)
		st := &memStore{target: store.BillingTarget{HookURL: hook.URL, HookSecret: testSecret}}
		t.Setenv(EnvFailClosed, "")
		if msg := newTestTurn(st).AuthorizeTurn(context.Background(), "", ""); msg != "" {
			t.Fatalf("%s: fail-open must allow, got %q", name, msg)
		}
		t.Setenv(EnvFailClosed, "true")
		if msg := newTestTurn(st).AuthorizeTurn(context.Background(), "", ""); msg != failClosedMessage {
			t.Fatalf("%s: fail-closed must deny, got %q", name, msg)
		}
	}

	// Unreachable.
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	st := &memStore{target: store.BillingTarget{HookURL: url, HookSecret: testSecret}}
	t.Setenv(EnvFailClosed, "")
	if msg := newTestTurn(st).AuthorizeTurn(context.Background(), "", ""); msg != "" {
		t.Fatalf("unreachable, fail-open: %q", msg)
	}
	t.Setenv(EnvFailClosed, "true")
	if err := AuthorizeTool(WithTurn(context.Background(), newTestTurn(st)), KindTTS, "openai", "", Estimate{Units: 5, Unit: "char"}); err == nil || err.Error() != failClosedMessage {
		t.Fatalf("unreachable, fail-closed tool: %v", err)
	}
}

func TestHookTimeoutFailsOpen(t *testing.T) {
	t.Setenv(EnvAllowPrivate, "true")
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); slow.Close() })
	svc := NewService(&memStore{target: store.BillingTarget{HookURL: slow.URL, HookSecret: testSecret}})
	svc.hook = newHookClient()
	svc.hook.http.Timeout = 100 * time.Millisecond
	start := time.Now()
	if msg := svc.NewTurn("u", "a", "s", "web").AuthorizeTurn(context.Background(), "", ""); msg != "" {
		t.Fatalf("timeout must fail open: %q", msg)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("hook call not bounded by its timeout")
	}
}

func TestNoHookOrNoTurnAllows(t *testing.T) {
	st := &memStore{}
	if msg := newTestTurn(st).AuthorizeTurn(context.Background(), "", ""); msg != "" {
		t.Fatalf("no hook: %q", msg)
	}
	if err := AuthorizeTool(context.Background(), KindImage, "fal", "", Estimate{}); err != nil {
		t.Fatalf("no turn: %v", err)
	}
	var nilSvc *Service
	if msg := nilSvc.NewTurn("u", "a", "s", "web").AuthorizeTurn(context.Background(), "", ""); msg != "" {
		t.Fatalf("nil service: %q", msg)
	}
}

func TestPrivateHookRefusedWithoutOptIn(t *testing.T) {
	t.Setenv(EnvAllowPrivate, "")
	t.Setenv(EnvFailClosed, "true")
	hook := newHookServer(t, testSecret, func(AuthorizeRequest) (int, string) { return 200, `{"allow": true}` })
	// http:// is refused before dialing.
	st := &memStore{target: store.BillingTarget{HookURL: hook.URL, HookSecret: testSecret}}
	if msg := newTestTurn(st).AuthorizeTurn(context.Background(), "", ""); msg != failClosedMessage {
		t.Fatalf("http hook must not be called: %q", msg)
	}
	if len(hook.requests()) != 0 {
		t.Fatal("hook was reached")
	}
	// The address is checked when dialing, not only at registration.
	addr := strings.TrimPrefix(hook.URL, "http://")
	if _, err := guardedDial(context.Background(), "tcp", addr); err == nil || !strings.Contains(err.Error(), "blocked address") {
		t.Fatalf("dial to loopback = %v", err)
	}
	t.Setenv(EnvAllowPrivate, "true")
	conn, err := guardedDial(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("opted-in dial: %v", err)
	}
	conn.Close()
}

func TestValidateHookURL(t *testing.T) {
	ctx := context.Background()
	t.Setenv(EnvAllowPrivate, "")
	for _, bad := range []string{
		"", "not a url", "/relative", "http://93.184.216.34/hook", "ftp://93.184.216.34/x",
		"https://127.0.0.1/hook", "https://10.0.0.5/hook", "https://169.254.169.254/latest", "https://[::1]/hook",
		"https://192.168.1.10/x", "https://100.64.0.1/x", "https://localhost/hook", "https://user:pw@93.184.216.34/x",
	} {
		if err := ValidateHookURL(ctx, bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if err := ValidateHookURL(ctx, "https://93.184.216.34/api/fastclaw/authorize"); err != nil {
		t.Fatalf("public https: %v", err)
	}
	t.Setenv(EnvAllowPrivate, "true")
	for _, ok := range []string{"http://127.0.0.1:3000/hook", "https://localhost/hook"} {
		if err := ValidateHookURL(ctx, ok); err != nil {
			t.Fatalf("%q with private allowed: %v", ok, err)
		}
	}
}

func TestRecordFillsTheTurnAndNeverFails(t *testing.T) {
	st := &memStore{}
	turn := newTestTurn(st)
	ctx := WithTurn(context.Background(), turn)
	Record(ctx, Event{Kind: KindSandbox, Provider: "docker", Units: 1.23456, Unit: "second"})
	if len(st.events) != 1 {
		t.Fatalf("events = %d", len(st.events))
	}
	e := st.events[0]
	if e.UserID != "u_end" || e.AgentID != "agt_1" || e.SessionKey != "imc:acc_1:o9cq" || e.Channel != "cron" ||
		e.TurnID != turn.ID || e.Units != 1.235 || e.Kind != "sandbox" {
		t.Fatalf("event = %+v", e)
	}
	// Children share the id.
	child := turn.Child(nil, "u_end", "agt_2", "sub", "subagent")
	if child.ID != turn.ID {
		t.Fatal("child turn must share the id")
	}
	// No turn: nothing. Store failure: logged, not raised.
	Record(context.Background(), Event{Kind: KindFetch})
	st.err = errors.New("db down")
	Record(ctx, Event{Kind: KindFetch})
	if len(st.events) != 1 {
		t.Fatalf("events = %d", len(st.events))
	}
}
