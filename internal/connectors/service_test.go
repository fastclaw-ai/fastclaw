package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// fakeConnany is an in-memory Connany: connections per subject, sessions
// that the test completes, and tools with read_only flags.
type fakeConnany struct {
	mu       sync.Mutex
	conns    map[string][]Connection // subject → connections
	sessions map[string]*Session
	calls    []string
}

func newFake(t *testing.T) (*fakeConnany, *httptest.Server) {
	f := &fakeConnany{conns: map[string][]Connection{}, sessions: map[string]*Session{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			w.WriteHeader(401)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		subject := r.URL.Query().Get("external_user_id")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if s, ok := body["external_user_id"].(string); ok {
			subject = s
		}
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		p := r.URL.Path
		switch {
		case p == "/v1/connectors":
			write(map[string]any{
				"categories": []map[string]string{{"name": "collab", "title": "Collaboration"}},
				"data": []map[string]any{
					{"name": "notion", "title": "Notion", "category": "collab", "avatar_url": "/a.svg"},
					{"name": "github", "title": "GitHub", "category": "collab", "avatar_url": "/b.svg"},
				},
			})
		case p == "/v1/connections" && r.Method == "GET":
			list := f.conns[subject]
			if list == nil {
				list = []Connection{}
			}
			write(map[string]any{"data": list, "next_cursor": nil})
		case strings.HasSuffix(p, "/sessions") && r.Method == "POST":
			connector := strings.Split(p, "/")[3]
			id := "sess" + string(rune('a'+len(f.sessions)))
			s := &Session{ID: id, Connector: connector, Status: "pending",
				ExpiresAt: time.Now().Add(10 * time.Minute).Format(time.RFC3339), ConnectURL: "https://connect.example/" + id}
			f.sessions[id] = s
			f.calls = append(f.calls, "create:"+connector+":"+body["return_url"].(string))
			write(s)
		case strings.Contains(p, "/sessions/") && r.Method == "GET":
			id := p[strings.LastIndex(p, "/")+1:]
			s := f.sessions[id]
			if s == nil {
				w.WriteHeader(404)
				write(map[string]any{"error": map[string]string{"code": "not_found"}})
				return
			}
			write(s)
		case strings.HasSuffix(p, "/tools") && r.Method == "GET":
			readOnly := r.URL.Query().Get("read_only") == "true"
			tools := []ToolDefinition{
				{Name: "notion.search", Connector: "notion", ReadOnly: true, Description: "Search"},
				{Name: "notion.create_page", Connector: "notion", ReadOnly: false, Description: "Create"},
			}
			var out []ToolDefinition
			for _, t := range tools {
				if !readOnly || t.ReadOnly {
					out = append(out, t)
				}
			}
			write(map[string]any{"data": out, "next_offset": nil})
		case strings.HasSuffix(p, "/call"):
			f.calls = append(f.calls, "call:"+p)
			write(map[string]any{"data": map[string]any{"content": []map[string]string{{"type": "text", "text": `{"results":["page A"]}`}}}})
		default:
			w.WriteHeader(404)
			write(map[string]any{"error": map[string]string{"code": "not_found"}})
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeConnany) connect(subject string, c Connection) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conns[subject] = append(f.conns[subject], c)
}

func newTestService(t *testing.T) (*Service, *fakeConnany) {
	t.Helper()
	f, srv := newFake(t)
	st, err := store.NewDBStore("sqlite", "file:"+filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	client, err := NewClient(srv.URL, "key")
	if err != nil {
		t.Fatal(err)
	}
	ss, err := newSQLStore(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	return &Service{client: client, store: ss, catalog: map[string]catalogEntry{}, conns: map[string]connsEntry{}}, f
}

func conn(id, connector, status, account string) Connection {
	c := Connection{ID: id, Connector: connector, Status: status}
	c.Identity.AccountName = account
	return c
}

func TestDefaultAccountRules(t *testing.T) {
	svc, f := newTestService(t)
	ctx := context.Background()
	subj := Subject("u1")

	f.connect(subj, conn("c1", "notion", "connected", "Work"))
	accounts, _ := svc.Accounts(ctx, "u1", true)
	if len(accounts) != 1 || !accounts[0].IsDefault {
		t.Fatalf("the only connected account must be the default: %+v", accounts)
	}

	// A second account and no stored choice: no default — never the first.
	f.connect(subj, conn("c2", "notion", "connected", "Home"))
	_, _ = svc.Accounts(ctx, "u1", true) // past the 20s list cache
	if _, err := svc.ResolveAccount(ctx, "u1", "notion", ""); Code(err) != "account_ambiguous" {
		t.Fatalf("two accounts, none chosen: got %v", err)
	}
	if a, err := svc.ResolveAccount(ctx, "u1", "notion", "Home"); err != nil || a.ID != "c2" {
		t.Fatalf("by name: %v %v", a, err)
	}
	if _, err := svc.ResolveAccount(ctx, "u1", "notion", "Nope"); Code(err) != "account_not_found" {
		t.Fatalf("unknown name: %v", err)
	}
	if err := svc.SetDefault(ctx, "u1", "c2"); err != nil {
		t.Fatal(err)
	}
	if a, err := svc.ResolveAccount(ctx, "u1", "notion", ""); err != nil || a.ID != "c2" {
		t.Fatalf("stored default: %v %v", a, err)
	}
	if _, err := svc.ResolveAccount(ctx, "u1", "github", ""); Code(err) != "no_account" {
		t.Fatalf("no account: %v", err)
	}
	// Another person's ids never resolve.
	if err := svc.SetDefault(ctx, "u2", "c1"); Code(err) != "not_found" {
		t.Fatalf("foreign connection: %v", err)
	}
}

func TestAuthorizationWakesConversationOnce(t *testing.T) {
	svc, f := newTestService(t)
	ctx := context.Background()
	var mu sync.Mutex
	var woke []string
	svc.SetWaker(func(userID, agentID, sessionID, text string) {
		mu.Lock()
		woke = append(woke, userID+"|"+agentID+"|"+sessionID+"|"+text)
		mu.Unlock()
	})

	req, err := svc.CreateRequest(ctx, "u1", "notion", "connect", "", ChatRef{AgentID: "agt1", SessionID: "s1"})
	if err != nil || req.Status != "requested" {
		t.Fatalf("create: %v %v", req, err)
	}
	link, view, err := svc.Open(ctx, "u1", OpenTarget{RequestID: req.ID}, "http://localhost:18955/connectors/done/")
	if err != nil || link != "https://connect.example/sessa" || view.Status != "pending" {
		t.Fatalf("open: %q %v %v", link, view, err)
	}
	if !strings.HasSuffix(f.calls[0], ":http://localhost:18955/connectors/done/") {
		t.Fatalf("return url: %v", f.calls)
	}
	// Someone else can't open or confirm it.
	if _, _, err := svc.Open(ctx, "u2", OpenTarget{RequestID: req.ID}, ""); Code(err) != "not_found" {
		t.Fatalf("foreign open: %v", err)
	}

	// Still pending at Connany.
	if v, _ := svc.Confirm(ctx, "u1", req.ID, "", true); v.Status != "pending" {
		t.Fatalf("pending: %v", v)
	}
	// The person finishes: the account exists and the session says so.
	f.connect(Subject("u1"), conn("c9", "notion", "connected", "Work"))
	f.mu.Lock()
	f.sessions["sessa"].Status = "connected"
	f.sessions["sessa"].ConnectionID = "c9"
	f.mu.Unlock()

	// The return page and the card confirm at the same time: once.
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.Confirm(ctx, "u1", "", "sessa", true)
		}()
	}
	wg.Wait()
	if v, _ := svc.Status(ctx, "u1", req.ID); v.Status != "connected" {
		t.Fatalf("status: %v", v)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(woke) != 1 || !strings.HasPrefix(woke[0], "u1|agt1|s1|[connector] The person connected their Notion account \"Work\"") {
		t.Fatalf("woke = %v", woke)
	}
	// The new account became the default.
	if a, err := svc.ResolveAccount(ctx, "u1", "notion", ""); err != nil || a.ID != "c9" {
		t.Fatalf("default after connect: %v %v", a, err)
	}
	// A finished request can't be opened again.
	if _, _, err := svc.Open(ctx, "u1", OpenTarget{RequestID: req.ID}, ""); Code(err) != "already_done" {
		t.Fatalf("reopen: %v", err)
	}
}

func TestCancelNotifiesAndSettingsRequestsDoNot(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	var woke []string
	var mu sync.Mutex
	svc.SetWaker(func(_, _, _, text string) { mu.Lock(); woke = append(woke, text); mu.Unlock() })

	req, _ := svc.CreateRequest(ctx, "u1", "github", "connect", "", ChatRef{AgentID: "a", SessionID: "s"})
	if v, err := svc.Cancel(ctx, "u1", req.ID); err != nil || v.Status != "cancelled" {
		t.Fatalf("cancel: %v %v", v, err)
	}
	_, _ = svc.Cancel(ctx, "u1", req.ID) // twice: still one note

	// Connecting from Settings has no conversation to wake.
	_, view, err := svc.Open(ctx, "u1", OpenTarget{Connector: "notion"}, "http://localhost/x")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = svc.Cancel(ctx, "u1", view.ID)
	if _, _, err := svc.Open(ctx, "u1", OpenTarget{Connector: "slack"}, ""); Code(err) != "connector_not_found" {
		t.Fatalf("unknown connector: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(woke) != 1 || !strings.Contains(woke[0], "cancelled the GitHub connection") {
		t.Fatalf("woke = %v", woke)
	}
}

func TestToolsAreReadOnly(t *testing.T) {
	svc, f := newTestService(t)
	ctx := context.Background()
	f.connect(Subject("u1"), conn("c1", "notion", "connected", "Work"))
	acct, err := svc.ResolveAccount(ctx, "u1", "notion", "")
	if err != nil {
		t.Fatal(err)
	}
	list, _, err := svc.ListTools(ctx, "u1", acct, "", 0)
	if err != nil || len(list) != 1 || list[0].Name != "notion.search" {
		t.Fatalf("list: %+v %v", list, err)
	}
	// The service's definition decides, whatever the model claims.
	if _, err := svc.CallTool(ctx, "u1", acct, "notion.create_page", nil); Code(err) != "write_not_allowed" {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.CallTool(ctx, "u1", acct, "github.search", nil); Code(err) != "connector_mismatch" {
		t.Fatalf("mismatch: %v", err)
	}
	data, err := svc.CallTool(ctx, "u1", acct, "notion.search", map[string]any{"query": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := RenderResult(data); got != `{"results":["page A"]}` {
		t.Fatalf("render = %q", got)
	}
}

func TestRenderResultCaps(t *testing.T) {
	big, _ := json.Marshal(map[string]string{"x": strings.Repeat("a", MaxResultChars+10)})
	out := RenderResult(big)
	if len(out) < MaxResultChars || !strings.Contains(out, "[truncated:") {
		t.Fatalf("not capped: %d", len(out))
	}
}

func TestNewClientRejectsUnsafeURLs(t *testing.T) {
	for _, bad := range []string{"http://connany.example", "https://u:p@x.example", "https://x.example/path", "ftp://x"} {
		if _, err := NewClient(bad, "k"); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	for _, good := range []string{"https://connany.example", "http://localhost:3200", "http://127.0.0.1:3200/"} {
		if _, err := NewClient(good, "k"); err != nil {
			t.Errorf("%s rejected: %v", good, err)
		}
	}
}
