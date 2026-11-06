package agl

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func newTestStore(t *testing.T) *store.DBStore {
	t.Helper()
	st, err := store.NewDBStore("sqlite", "file:"+filepath.Join(t.TempDir(), "agl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

type testHub struct {
	t   *testing.T
	st  *store.DBStore
	hub *Hub
	srv *httptest.Server
	mu  sync.Mutex
	now time.Time
}

func newTestHub(t *testing.T) *testHub {
	th := &testHub{t: t, st: newTestStore(t), now: time.Now().UTC()}
	th.hub = NewHub(th.st, "")
	th.hub.now = func() time.Time { th.mu.Lock(); defer th.mu.Unlock(); return th.now }
	mux := http.NewServeMux()
	th.hub.RegisterRoutes(mux)
	th.srv = httptest.NewServer(mux)
	t.Cleanup(th.srv.Close)
	return th
}

func (th *testHub) advance(d time.Duration) {
	th.mu.Lock()
	th.now = th.now.Add(d)
	th.mu.Unlock()
}

func (th *testHub) wsURL(query string) string {
	return "ws" + strings.TrimPrefix(th.srv.URL, "http") + "/agl/ws/machine?" + query
}

// dial returns the socket, or the HTTP status of a refused upgrade.
func (th *testHub) dial(query string) (*websocket.Conn, int) {
	th.t.Helper()
	c, resp, err := websocket.DefaultDialer.Dial(th.wsURL(query), nil)
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode
		}
		th.t.Fatalf("dial: %v", err)
	}
	return c, 101
}

func (th *testHub) pair(code, aglID string) string {
	th.t.Helper()
	c, status := th.dial("code=" + code + "&machine_id=" + aglID)
	if c == nil {
		th.t.Fatalf("pair with %s: status %d", code, status)
	}
	defer c.Close()
	var msg struct{ Type, Token string }
	if err := c.ReadJSON(&msg); err != nil || msg.Type != "paired" || !strings.HasPrefix(msg.Token, "aglm_") {
		th.t.Fatalf("paired reply: %+v %v", msg, err)
	}
	return msg.Token
}

// connect links a fake machine and answers requests with respond.
func (th *testHub) connect(token, aglID string, respond func(method string, params json.RawMessage) any) *websocket.Conn {
	th.t.Helper()
	c, status := th.dial("token=" + token + "&machine_id=" + aglID)
	if c == nil {
		th.t.Fatalf("connect: status %d", status)
	}
	hello := map[string]any{"type": "hello", "machine": map[string]any{
		"machine_id": aglID, "hostname": "MacBook-Pro", "os": "darwin", "arch": "arm64", "version": "0.3.3", "protocol": 1,
		"agents": []map[string]any{{"id": "claude", "name": "Claude Code", "available": true, "features": []string{"steer"}}},
	}}
	if err := c.WriteJSON(hello); err != nil {
		th.t.Fatal(err)
	}
	go func() {
		for {
			var req struct {
				Type, ID, Method string
				Params           json.RawMessage
			}
			if err := c.ReadJSON(&req); err != nil {
				return
			}
			if req.Type == "request" && respond != nil {
				_ = c.WriteJSON(map[string]any{"type": "response", "id": req.ID, "result": respond(req.Method, req.Params)})
			}
		}
	}()
	return c
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPairingCodeSingleUse(t *testing.T) {
	th := newTestHub(t)
	ctx := context.Background()
	code, exp, err := th.hub.CreatePairing(ctx, "u_alice")
	if err != nil {
		t.Fatal(err)
	}
	if d := exp.Sub(th.now); d != PairingTTL {
		t.Fatalf("ttl = %v", d)
	}
	// Only the hash is stored.
	if _, err := th.st.GetMachinePairing(ctx, code); err == nil {
		t.Fatal("code stored in clear")
	}
	if st, _, err := th.hub.PairingStatus(ctx, "u_alice", code); err != nil || st != PairingPending {
		t.Fatalf("status before pairing = %q %v", st, err)
	}
	// The installer is served while the code is unused.
	resp, err := http.Get(th.srv.URL + "/agl/install.sh?code=" + code)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), th.srv.URL+"/agl") || !strings.Contains(string(body), "--code "+code) {
		t.Fatalf("install.sh: %d\n%.300s", resp.StatusCode, body)
	}

	token := th.pair(code, "agl-1")
	// Second use of the same code: refused before the upgrade (401).
	if c, status := th.dial("code=" + code + "&machine_id=agl-2"); c != nil || status != http.StatusUnauthorized {
		t.Fatalf("reused code: status %d", status)
	}
	resp, _ = http.Get(th.srv.URL + "/agl/install.sh?code=" + code)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("install.sh with a used code: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Paired but no hello yet: still pending; after hello: connected.
	if st, _, _ := th.hub.PairingStatus(ctx, "u_alice", code); st != PairingPending {
		t.Fatalf("status before hello = %q", st)
	}
	c := th.connect(token, "agl-1", nil)
	defer c.Close()
	ms, _ := th.st.ListMachines(ctx, "u_alice")
	if len(ms) != 1 || !strings.HasPrefix(ms[0].ID, "mch_") {
		t.Fatalf("machines = %+v", ms)
	}
	waitFor(t, "online", func() bool { return th.hub.Online(ms[0].ID) })
	st, mid, err := th.hub.PairingStatus(ctx, "u_alice", code)
	if err != nil || st != PairingConnected || mid != ms[0].ID {
		t.Fatalf("status after hello = %q %q %v", st, mid, err)
	}
	m, _ := th.st.GetMachine(ctx, ms[0].ID)
	if m.Hostname != "MacBook-Pro" || m.OS != "macos" || len(m.Agents) != 1 || m.Agents[0].ID != "claude" || m.TokenHash == token {
		t.Fatalf("machine after hello = %+v", m)
	}
	if !m.AllowAgents || m.DefaultAccess != AccessAsk || m.DefaultCwd != "" {
		t.Fatalf("new machine settings = %+v", m)
	}
	// Another user can't read the code's status.
	if _, _, err := th.hub.PairingStatus(ctx, "u_bob", code); err != ErrPairingNotFound {
		t.Fatalf("bob reads alice's code: %v", err)
	}
}

func TestPairingCodeExpiry(t *testing.T) {
	th := newTestHub(t)
	ctx := context.Background()
	code, _, _ := th.hub.CreatePairing(ctx, "u_alice")
	th.advance(PairingTTL + time.Second)
	if st, _, _ := th.hub.PairingStatus(ctx, "u_alice", code); st != PairingExpired {
		t.Fatalf("status = %q", st)
	}
	resp, _ := http.Get(th.srv.URL + "/agl/install.sh?code=" + code)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "exit 1") {
		t.Fatalf("expired install.sh: %d %s", resp.StatusCode, body)
	}
	if c, status := th.dial("code=" + code + "&machine_id=agl-1"); c != nil || status != http.StatusUnauthorized {
		t.Fatalf("expired code: status %d", status)
	}
	// Garbage codes never reach the store.
	if c, status := th.dial("code=nope&machine_id=agl-1"); c != nil || status != http.StatusUnauthorized {
		t.Fatalf("bad code: status %d", status)
	}
}

func TestRevokeClosesLinkAndRejectsToken(t *testing.T) {
	th := newTestHub(t)
	ctx := context.Background()
	code, _, _ := th.hub.CreatePairing(ctx, "u_alice")
	token := th.pair(code, "agl-1")
	c := th.connect(token, "agl-1", func(method string, _ json.RawMessage) any { return map[string]any{"pong": method == "ping"} })
	defer c.Close()
	ms, _ := th.st.ListMachines(ctx, "u_alice")
	id := ms[0].ID
	waitFor(t, "online", func() bool { return th.hub.Online(id) })

	raw, err := th.hub.Request(ctx, id, "ping", nil)
	if err != nil || !strings.Contains(string(raw), `"pong":true`) {
		t.Fatalf("request: %s %v", raw, err)
	}
	// A token for another machine id is refused.
	if c2, status := th.dial("token=" + token + "&machine_id=agl-other"); c2 != nil || status != http.StatusUnauthorized {
		t.Fatalf("token with wrong machine id: %d", status)
	}

	events, cancel := th.hub.Subscribe(id)
	defer cancel()
	if err := th.hub.Revoke(ctx, id); err != nil {
		t.Fatal(err)
	}
	if th.hub.Online(id) {
		t.Fatal("still online after revoke")
	}
	select {
	case ev := <-events:
		if !ev.Disconnected {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("subscribers not told about the disconnect")
	}
	if _, err := th.hub.Request(ctx, id, "ping", nil); err != ErrOffline {
		t.Fatalf("request after revoke: %v", err)
	}
	if c2, status := th.dial("token=" + token + "&machine_id=agl-1"); c2 != nil || status != http.StatusUnauthorized {
		t.Fatalf("revoked token: status %d, want 401", status)
	}
	if ms, _ := th.st.ListMachines(ctx, "u_alice"); len(ms) != 0 {
		t.Fatalf("revoked machine still listed: %+v", ms)
	}
	if m, _ := th.st.GetMachine(ctx, id); m.TokenHash != "" || !m.Revoked() {
		t.Fatalf("revoked row = %+v", m)
	}
}

func TestRepairSameMachineKeepsID(t *testing.T) {
	th := newTestHub(t)
	ctx := context.Background()
	code1, _, _ := th.hub.CreatePairing(ctx, "u_alice")
	old := th.pair(code1, "agl-1")
	code2, _, _ := th.hub.CreatePairing(ctx, "u_alice")
	th.pair(code2, "agl-1")
	ms, _ := th.st.ListMachines(ctx, "u_alice")
	if len(ms) != 1 {
		t.Fatalf("re-pair duplicated the machine: %+v", ms)
	}
	if c, status := th.dial("token=" + old + "&machine_id=agl-1"); c != nil || status != http.StatusUnauthorized {
		t.Fatalf("old token after re-pair: %d", status)
	}
}

func TestDownloadRedirect(t *testing.T) {
	th := newTestHub(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for path, want := range map[string]string{
		"/agl/download/darwin-arm64":     DefaultReleaseURL + "/agl-darwin-arm64",
		"/agl/download/linux-x64.sha256": DefaultReleaseURL + "/agl-linux-x64.sha256",
	} {
		resp, err := client.Get(th.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != want {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
}

// The name follows the hostname until the user renames the machine; a
// rename survives later hellos.
func TestHelloKeepsUserChosenName(t *testing.T) {
	th := newTestHub(t)
	ctx := context.Background()
	if err := th.st.CreateMachine(ctx, &store.MachineRecord{ID: "mch_1", UserID: "u_alice", TokenHash: "h", AllowAgents: true}); err != nil {
		t.Fatal(err)
	}
	hello := func(host string) *store.MachineRecord {
		m, _ := th.st.GetMachine(ctx, "mch_1")
		th.hub.applyHello(ctx, m, &helloMachine{Hostname: host})
		m, _ = th.st.GetMachine(ctx, "mch_1")
		return m
	}
	if m := hello("mac-a"); m.Name != "mac-a" {
		t.Fatalf("first hello name = %q", m.Name)
	}
	if m := hello("mac-b"); m.Name != "mac-b" {
		t.Fatalf("hostname change not followed: %q", m.Name)
	}
	name := "Work laptop"
	if err := th.st.UpdateMachineSettings(ctx, "mch_1", store.MachineSettings{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if m := hello("mac-c"); m.Name != name || m.Hostname != "mac-c" {
		t.Fatalf("rename lost on hello: %+v", m)
	}
}

// dialAt dials the machine endpoint of the hub address base ("" = /agl).
func (th *testHub) dialAt(scope, query string) (*websocket.Conn, int) {
	th.t.Helper()
	path := "/agl/ws/machine?"
	if scope != "" {
		path = "/agl/p/" + scope + "/ws/machine?"
	}
	c, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(th.srv.URL, "http")+path+query, nil)
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode
		}
		th.t.Fatalf("dial: %v", err)
	}
	return c, 101
}

func (th *testHub) pairAt(scope, code, aglID string) string {
	th.t.Helper()
	c, status := th.dialAt(scope, "code="+code+"&machine_id="+aglID)
	if c == nil {
		th.t.Fatalf("pair at %q: status %d", scope, status)
	}
	defer c.Close()
	var msg struct{ Type, Token string }
	if err := c.ReadJSON(&msg); err != nil || msg.Type != "paired" {
		th.t.Fatalf("paired reply: %+v %v", msg, err)
	}
	return msg.Token
}

func TestProductScopesAreSeparateHubAddresses(t *testing.T) {
	th := newTestHub(t)
	ctx := context.Background()
	a, b := ProductScope("acct_a"), ProductScope("acct_b")
	if a == b || !scopeRE.MatchString(a) || ProductScope("acct_a") != a {
		t.Fatalf("scopes: %q %q", a, b)
	}
	code, _, err := th.hub.CreateScopedPairing(ctx, "u_alice", a)
	if err != nil {
		t.Fatal(err)
	}
	// The code pairs only at its own address, and refusing elsewhere
	// doesn't burn it.
	for _, wrong := range []string{"", b, "zz"} {
		if c, status := th.dialAt(wrong, "code="+code+"&machine_id=agl-1"); c != nil || status != http.StatusUnauthorized {
			t.Fatalf("code of %s at %q: %d", a, wrong, status)
		}
	}
	token := th.pairAt(a, code, "agl-1")
	if c, status := th.dialAt("", "token="+token+"&machine_id=agl-1"); c != nil || status != http.StatusUnauthorized {
		t.Fatalf("token of %s at /agl: %d", a, status)
	}
	c, status := th.dialAt(a, "token="+token+"&machine_id=agl-1")
	if c == nil {
		t.Fatalf("token at its own address: %d", status)
	}
	c.Close()
	if ms, _ := th.st.ListMachines(ctx, "u_alice"); len(ms) != 1 || ms[0].HubScope != a {
		t.Fatalf("machine scope: %+v", ms)
	}
}

func TestInstallScriptUsesCodeAddressAndPrivate(t *testing.T) {
	th := newTestHub(t)
	a := ProductScope("acct_a")
	code, _, _ := th.hub.CreateScopedPairing(context.Background(), "u_alice", a)
	get := func(path string) (int, string) {
		resp, err := http.Get(th.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var sb strings.Builder
		_, _ = io.Copy(&sb, resp.Body)
		return resp.StatusCode, sb.String()
	}
	if status, _ := get("/agl/install.sh?code=" + code); status != http.StatusUnauthorized {
		t.Fatalf("scoped code at /agl/install.sh: %d", status)
	}
	status, body := get("/agl/p/" + a + "/install.sh?code=" + code)
	if status != http.StatusOK || !strings.Contains(body, th.srv.URL+"/agl/p/"+a) || !strings.Contains(body, "--code "+code+" --private") {
		t.Fatalf("install.sh: %d\n%s", status, body)
	}
}

func TestPairingForAnotherAccountRevokesTheReplacedRecord(t *testing.T) {
	th := newTestHub(t)
	ctx := context.Background()
	a, b := ProductScope("acct_a"), ProductScope("acct_b")
	pair := func(user, scope string) string {
		code, _, err := th.hub.CreateScopedPairing(ctx, user, scope)
		if err != nil {
			t.Fatal(err)
		}
		return th.pairAt(scope, code, "agl-1")
	}
	aliceTok := pair("u_alice", a)
	carolTok := pair("u_carol", b) // another product: its own link, kept
	pair("u_bob", a)               // same product: replaces alice's link
	if ms, _ := th.st.ListMachines(ctx, "u_alice"); len(ms) != 0 {
		t.Fatalf("alice's replaced record still active: %+v", ms)
	}
	if c, status := th.dialAt(a, "token="+aliceTok+"&machine_id=agl-1"); c != nil || status != http.StatusUnauthorized {
		t.Fatalf("alice's token after bob paired: %d", status)
	}
	if ms, _ := th.st.ListMachines(ctx, "u_bob"); len(ms) != 1 {
		t.Fatalf("bob: %+v", ms)
	}
	if ms, _ := th.st.ListMachines(ctx, "u_carol"); len(ms) != 1 {
		t.Fatalf("carol's link in another product was revoked: %+v", ms)
	}
	c, status := th.dialAt(b, "token="+carolTok+"&machine_id=agl-1")
	if c == nil {
		t.Fatalf("carol's token: %d", status)
	}
	c.Close()
}

func TestRepairAtNewAddressMovesTheMachine(t *testing.T) {
	th := newTestHub(t)
	ctx := context.Background()
	a := ProductScope("acct_a")
	code1, _, _ := th.hub.CreatePairing(ctx, "u_alice")
	old := th.pair(code1, "agl-1")
	before, _ := th.st.ListMachines(ctx, "u_alice")
	code2, _, _ := th.hub.CreateScopedPairing(ctx, "u_alice", a)
	th.pairAt(a, code2, "agl-1")
	after, _ := th.st.ListMachines(ctx, "u_alice")
	if len(after) != 1 || after[0].ID != before[0].ID || after[0].HubScope != a {
		t.Fatalf("re-pair at a new address: before %+v after %+v", before, after)
	}
	if c, status := th.dial("token=" + old + "&machine_id=agl-1"); c != nil || status != http.StatusUnauthorized {
		t.Fatalf("old /agl link after moving: %d", status)
	}
}
