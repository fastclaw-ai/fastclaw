package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/usage"
	"github.com/fastclaw-ai/fastclaw/internal/users"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// recordingProvider answers every turn with a fixed reply and keeps the
// system prompt text of the last call.
type recordingProvider struct {
	mu       sync.Mutex
	system   string
	lastUser string
	// last is every message of the latest call.
	last []provider.Message
	// reply overrides "pong"; onChat runs during the model call (a test
	// stands in for the agent's tools writing files).
	reply  string
	onChat func()
	usage  provider.Usage
	// script, when set, answers the next calls in order instead (a
	// round that calls tools, then the final answer).
	script []provider.Response
	// err, when set, fails every model call with it.
	err error
	// model is the model id of the latest call.
	model string
}

func (p *recordingProvider) next() provider.Response {
	p.mu.Lock()
	if len(p.script) > 0 {
		r := p.script[0]
		p.script = p.script[1:]
		p.mu.Unlock()
		return r
	}
	p.mu.Unlock()
	return provider.Response{Content: p.answer(), Usage: p.usage}
}

func (p *recordingProvider) answer() string {
	p.mu.Lock()
	hook, reply := p.onChat, p.reply
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	if reply != "" {
		return reply
	}
	return "pong"
}

func (p *recordingProvider) record(messages []provider.Message) {
	var b strings.Builder
	lastUser := ""
	for _, m := range messages {
		if m.Role == "system" {
			b.WriteString(m.Content)
			b.WriteString("\n")
		}
		if m.Role == "user" {
			lastUser = m.Content
			for _, part := range m.ContentParts {
				lastUser += part.Text + "|" + part.Type + "|"
			}
		}
	}
	p.mu.Lock()
	p.system = b.String()
	p.lastUser = lastUser
	p.last = messages
	p.mu.Unlock()
}

func (p *recordingProvider) lastSystem() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.system
}

func (p *recordingProvider) Chat(_ context.Context, messages []provider.Message, _ []provider.Tool, model string, _ int, _ float64) (*provider.Response, error) {
	p.record(messages)
	p.setModel(model)
	if err := p.failure(); err != nil {
		return nil, err
	}
	r := p.next()
	return &r, nil
}

func (p *recordingProvider) setModel(m string) {
	p.mu.Lock()
	p.model = m
	p.mu.Unlock()
}

func (p *recordingProvider) lastModel() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.model
}

func (p *recordingProvider) failure() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *recordingProvider) ChatStream(_ context.Context, messages []provider.Message, _ []provider.Tool, model string, _ int, _ float64) (*provider.StreamReader, error) {
	p.record(messages)
	p.setModel(model)
	if err := p.failure(); err != nil {
		return nil, err
	}
	r := p.next()
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{Content: r.Content, ToolCalls: r.ToolCalls, Done: true, Usage: r.Usage, Truncated: r.Truncated}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

// fakeRuntime is a UserResolver whose spaces start empty and attach
// agents from the store on EnsureAgent — the shape of an on-demand
// gateway UserSpace.
type fakeRuntime struct {
	t      *testing.T
	st     store.Store
	prov   *recordingProvider
	mb     *bus.MessageBus
	mu     sync.Mutex
	spaces map[string]*agent.Manager
	// ensured records "<namespace user>/<agent>" for every attach.
	ensured []string
	// providers are handed to every attached agent (model prices).
	providers map[string]config.ProviderConfig
	// meter, when set, meters every attached agent's model calls.
	meter usage.Meter
}

func (f *fakeRuntime) manager(uid string) *agent.Manager {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.spaces[uid]; ok {
		return m
	}
	opts := []agent.ManagerOption{agent.WithUserID(uid)}
	if f.meter != nil {
		opts = append(opts, agent.WithMeter(f.meter))
	}
	m, err := agent.NewManager(nil, f.prov, f.mb, opts...)
	if err != nil {
		f.t.Fatalf("NewManager: %v", err)
	}
	f.spaces[uid] = m
	return m
}

func (f *fakeRuntime) UserSpaceFor(uid string) (*UserSpaceView, error) {
	return &UserSpaceView{UserID: uid, Agents: f.manager(uid), Config: &config.Config{}}, nil
}
func (f *fakeRuntime) LocalAgentManager() *agent.Manager { return nil }
func (f *fakeRuntime) IsCloudMode() bool                 { return true }

func (f *fakeRuntime) EnsureAgent(ctx context.Context, uid, agentID string) error {
	mgr := f.manager(uid)
	if mgr.Has(agentID) {
		return nil
	}
	rec, err := f.st.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.ensured = append(f.ensured, uid+"/"+agentID)
	f.mu.Unlock()
	dir := f.t.TempDir()
	return mgr.AddAgent(config.ResolvedAgent{
		ID: rec.ID, Home: dir, Workspace: filepath.Join(dir, "workspace"),
		Model: "test/model", MaxTokens: 256, MaxToolIterations: 2,
		Providers: f.providers,
	}, f.prov, f.mb)
}

func (f *fakeRuntime) InvalidateUser(string)  {}
func (f *fakeRuntime) InvalidateAgent(string) {}

type v1Harness struct {
	t       *testing.T
	st      *store.DBStore
	ws      *workspace.LocalFS
	rt      *fakeRuntime
	mux     *http.ServeMux
	appA    string
	keyA    string
	appB    string
	keyB    string
	accts   *users.Accounts
	apikeys *users.APIKeys
}

func newV1Harness(t *testing.T) *v1Harness {
	t.Helper()
	t.Setenv("FASTCLAW_HOME", t.TempDir())
	st, err := store.NewDBStore("sqlite", "file:"+filepath.Join(t.TempDir(), "v1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	accts, _ := users.NewAccounts(st)
	apikeys, _ := users.NewAPIKeys(st)
	authResolver, err := auth.NewResolver(st)
	if err != nil {
		t.Fatal(err)
	}
	h := &v1Harness{t: t, st: st, accts: accts, apikeys: apikeys}
	h.appA, h.keyA = h.newApp("douchat")
	h.appB, h.keyB = h.newApp("weclaw")

	h.rt = &fakeRuntime{t: t, st: st, prov: &recordingProvider{}, mb: bus.New(), spaces: map[string]*agent.Manager{}}
	srv := NewServer(h.rt, authResolver, nil)
	srv.SetStore(st)
	srv.SetMeter(usage.NewSQLMeter(st.DB(), "sqlite"))
	h.ws = workspace.NewLocalFS(t.TempDir())
	srv.SetWorkspaceStore(h.ws)
	h.mux = http.NewServeMux()
	srv.RegisterRoutes(h.mux)
	return h
}

func (h *v1Harness) newApp(name string) (string, string) {
	ctx := context.Background()
	acc, err := h.accts.Create(ctx, users.CreateInput{Username: name, Email: name + "@example.com", Password: "password-" + name})
	if err != nil {
		h.t.Fatal(err)
	}
	_, token, err := h.apikeys.Create(ctx, acc.ID, name+"-key", users.APIKeyTypeUser, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	return acc.ID, token
}

func (h *v1Harness) do(method, path, key string, body any, headers ...string) (int, map[string]any) {
	h.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		blob, _ := json.Marshal(body)
		rdr = bytes.NewReader(blob)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (h *v1Harness) createAgent(key, name string, md map[string]string) string {
	h.t.Helper()
	code, out := h.do("POST", "/v1/agents", key, map[string]any{"name": name, "metadata": md})
	if code != http.StatusOK {
		h.t.Fatalf("create agent: %d %v", code, out)
	}
	return out["agent"].(map[string]any)["id"].(string)
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func TestV1AgentLifecycle(t *testing.T) {
	h := newV1Harness(t)
	ctx := context.Background()

	code, out := h.do("POST", "/v1/agents", h.keyA, map[string]any{
		"name":         "周报助手",
		"description":  "每周五汇总团队进展",
		"instructions": "You write weekly reports.",
		"model":        "anthropic/claude-sonnet-5-5",
		"metadata":     map[string]string{"app_user": "douchat-user-123"},
	})
	if code != http.StatusOK {
		t.Fatalf("create: %d %v", code, out)
	}
	ag := out["agent"].(map[string]any)
	id := ag["id"].(string)
	if !strings.HasPrefix(id, "agt_") || ag["display_name"] != "周报助手" || ag["model"] != "anthropic/claude-sonnet-5-5" {
		t.Fatalf("unexpected agent view: %v", ag)
	}
	if md := ag["metadata"].(map[string]any); md["app_user"] != "douchat-user-123" {
		t.Fatalf("metadata = %v", md)
	}
	rec, err := h.st.GetAgent(ctx, id)
	if err != nil || rec.UserID != h.appA {
		t.Fatalf("agent must belong to the app: %+v %v", rec, err)
	}
	if soul, err := h.st.GetAgentFileExact(ctx, id, h.appA, "SOUL.md"); err != nil || string(soul) != "You write weekly reports." {
		t.Fatalf("SOUL.md = %q %v", soul, err)
	}

	other := h.createAgent(h.keyA, "Other", map[string]string{"app_user": "douchat-user-456"})

	// Metadata filter, both spellings.
	for _, q := range []string{"metadata[app_user]=douchat-user-123", "metadata.app_user=douchat-user-123"} {
		code, out = h.do("GET", "/v1/agents?"+q, h.keyA, nil)
		list := out["agents"].([]any)
		if code != http.StatusOK || len(list) != 1 || list[0].(map[string]any)["id"] != id {
			t.Fatalf("filter %s: %d %v", q, code, out)
		}
	}

	// Pagination walks every agent exactly once.
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 5; page++ {
		path := "/v1/agents?limit=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		_, out = h.do("GET", path, h.keyA, nil)
		for _, a := range out["agents"].([]any) {
			seen[a.(map[string]any)["id"].(string)] = true
		}
		if out["has_more"] != true {
			break
		}
		cursor = out["next_cursor"].(string)
	}
	if len(seen) != 2 || !seen[id] || !seen[other] {
		t.Fatalf("paginated ids = %v", seen)
	}

	// PATCH: rename, drop a metadata key, clear the model.
	code, out = h.do("PATCH", "/v1/agents/"+id, h.keyA, map[string]any{
		"name": "Weekly", "model": "", "metadata": map[string]any{"app_user": nil, "team": "t1"},
	})
	ag = out["agent"].(map[string]any)
	if code != http.StatusOK || ag["display_name"] != "Weekly" || ag["model"] != "" {
		t.Fatalf("patch: %d %v", code, out)
	}
	if md := ag["metadata"].(map[string]any); len(md) != 1 || md["team"] != "t1" {
		t.Fatalf("patched metadata = %v", md)
	}

	code, _ = h.do("PUT", "/v1/agents/"+id+"/system-files/IDENTITY.md", h.keyA, map[string]any{"content": "Name: Weekly"})
	if code != http.StatusOK {
		t.Fatalf("put system file: %d", code)
	}
	if code, out = h.do("PUT", "/v1/agents/"+id+"/system-files/USER.md", h.keyA, map[string]any{"content": "x"}); code != http.StatusBadRequest {
		t.Fatalf("per-user files are not writable through /v1: %d %v", code, out)
	}

	if code, _ = h.do("DELETE", "/v1/agents/"+id, h.keyA, nil); code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
	code, out = h.do("GET", "/v1/agents/"+id, h.keyA, nil)
	if code != http.StatusNotFound || errCode(out) != "agent_not_found" {
		t.Fatalf("get deleted: %d %v", code, out)
	}
	code, out = h.do("DELETE", "/v1/agents/"+id, h.keyA, nil)
	if code != http.StatusNotFound || errCode(out) != "agent_not_found" {
		t.Fatalf("second delete: %d %v", code, out)
	}
}

func TestV1AgentMetadataLimits(t *testing.T) {
	h := newV1Harness(t)
	tooMany := map[string]string{}
	for i := 0; i < maxMetadataKeys+1; i++ {
		tooMany[fmt.Sprintf("k%d", i)] = "v"
	}
	for name, md := range map[string]map[string]string{
		"too many keys": tooMany,
		"long key":      {strings.Repeat("k", maxMetadataKeyLen+1): "v"},
		"long value":    {"k": strings.Repeat("v", maxMetadataValueLen+1)},
	} {
		code, out := h.do("POST", "/v1/agents", h.keyA, map[string]any{"name": "x", "metadata": md})
		if code != http.StatusBadRequest || errCode(out) != "invalid_request" {
			t.Fatalf("%s: %d %v", name, code, out)
		}
	}
}

func TestV1AppsAreIsolated(t *testing.T) {
	h := newV1Harness(t)
	id := h.createAgent(h.keyA, "A's agent", nil)

	_, out := h.do("GET", "/v1/agents", h.keyB, nil)
	if list := out["agents"].([]any); len(list) != 0 {
		t.Fatalf("app B sees app A's agents: %v", list)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/agents/" + id},
		{"PATCH", "/v1/agents/" + id},
		{"DELETE", "/v1/agents/" + id},
		{"PUT", "/v1/agents/" + id + "/system-files/SOUL.md"},
	} {
		code, out := h.do(c.method, c.path, h.keyB, map[string]any{"name": "pwned", "content": "pwned"})
		if code != http.StatusNotFound || errCode(out) != "agent_not_found" {
			t.Fatalf("%s %s from app B: %d %v", c.method, c.path, code, out)
		}
	}
	code, out := h.do("POST", "/v1/chat/completions", h.keyB, map[string]any{
		"agent_id": id, "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if code != http.StatusNotFound || errCode(out) != "agent_not_found" {
		t.Fatalf("chat with another app's agent: %d %v", code, out)
	}
}

func TestChatStrictAgentID(t *testing.T) {
	h := newV1Harness(t)
	h.createAgent(h.keyA, "Only", nil)

	// An unknown agent_id must 404 — never fall back to another agent —
	// whether it comes in the body or the header.
	code, out := h.do("POST", "/v1/chat/completions", h.keyA, map[string]any{
		"agent_id": "agt_doesnotexist", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if code != http.StatusNotFound || errCode(out) != "agent_not_found" {
		t.Fatalf("unknown agent_id: %d %v", code, out)
	}
	code, out = h.do("POST", "/v1/chat/completions", h.keyA, map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}, "X-Fastclaw-Agent-ID", "agt_doesnotexist")
	if code != http.StatusNotFound || errCode(out) != "agent_not_found" {
		t.Fatalf("unknown agent header: %d %v", code, out)
	}
	if len(h.rt.ensured) != 0 {
		t.Fatalf("no agent may be attached for an unknown id: %v", h.rt.ensured)
	}
}

func TestChatWithEndUserRunsAppAgentInEndUserNamespace(t *testing.T) {
	h := newV1Harness(t)
	id := h.createAgent(h.keyA, "Shared", nil)

	for _, endUser := range []string{"alice", "bob"} {
		code, out := h.do("POST", "/v1/chat/completions", h.keyA, map[string]any{
			"agent_id": id, "messages": []map[string]string{{"role": "user", "content": "hi"}},
		}, "X-Fastclaw-End-User", endUser, "X-Fastclaw-Session-Key", "douchat:dm:"+endUser)
		if code != http.StatusOK {
			t.Fatalf("chat as %s: %d %v", endUser, code, out)
		}
	}
	// Body `user` names the end-user the same way.
	code, out := h.do("POST", "/v1/chat/completions", h.keyA, map[string]any{
		"agent_id": id, "user": "carol", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if code != http.StatusOK {
		t.Fatalf("chat with body user: %d %v", code, out)
	}

	ctx := context.Background()
	namespaces := map[string]bool{}
	for _, ext := range []string{"alice", "bob", "carol"} {
		u, err := h.st.GetUserByExternal(ctx, h.appA, ext)
		if err != nil {
			t.Fatalf("end-user %s was not minted under the app: %v", ext, err)
		}
		namespaces[u.ID+"/"+id] = true
	}
	if len(h.rt.ensured) != 3 {
		t.Fatalf("ensured = %v", h.rt.ensured)
	}
	for _, e := range h.rt.ensured {
		if !namespaces[e] {
			t.Fatalf("agent attached outside an end-user namespace: %s (want one of %v)", e, namespaces)
		}
	}
}

func TestChatSpeakerIsTurnContextNotClientParam(t *testing.T) {
	h := newV1Harness(t)
	id := h.createAgent(h.keyA, "Group", nil)
	code, out := h.do("POST", "/v1/chat/completions", h.keyA, map[string]any{
		"agent_id": id,
		"messages": []map[string]string{{"role": "user", "content": "summarize"}},
		"params":   map[string]any{"speaker": map[string]any{"id": "douchat-user-456", "name": "Bob"}, "lang": "en"},
	}, "X-Fastclaw-Session-Key", "douchat:group_123:topic_9")
	if code != http.StatusOK {
		t.Fatalf("chat: %d %v", code, out)
	}
	sys := h.rt.prov.lastSystem()
	if !strings.Contains(sys, "## Current Speaker") || !strings.Contains(sys, "Bob") || !strings.Contains(sys, "douchat-user-456") {
		t.Fatalf("speaker missing from turn context:\n%s", sys)
	}
	if strings.Contains(sys, `"speaker"`) || strings.Contains(sys, "__fastclawSpeaker") {
		t.Fatalf("speaker leaked into client parameters:\n%s", sys)
	}
	if !strings.Contains(sys, `"lang": "en"`) {
		t.Fatalf("other params must still reach the agent:\n%s", sys)
	}
}

func TestUsageByEndUserAndApp(t *testing.T) {
	h := newV1Harness(t)
	ctx := context.Background()
	alice, err := h.accts.EnsureAppUser(ctx, h.appA, "alice", "", "")
	if err != nil {
		t.Fatal(err)
	}
	agt1 := h.createAgent(h.keyA, "one", nil)
	agt2 := h.createAgent(h.keyA, "two", nil)
	agt3 := h.createAgent(h.keyB, "three", nil)
	meter := usage.NewSQLMeter(h.st.DB(), "sqlite")
	_ = meter.RecordTokens(ctx, alice.ID, agt1, "s1", "", "m", usage.Tokens{Input: 10, Output: 5})
	_ = meter.RecordTokens(ctx, h.appA, agt2, "s2", "", "m", usage.Tokens{Input: 100})
	_ = meter.RecordTokens(ctx, h.appB, agt3, "s3", "", "m", usage.Tokens{Input: 1000})

	totalIn := func(out map[string]any) float64 {
		return out["totals"].(map[string]any)["inputTokens"].(float64)
	}
	code, out := h.do("GET", "/v1/usage?end_user=alice", h.keyA, nil)
	daily := out["daily"].([]any)
	if code != http.StatusOK || totalIn(out) != 10 || len(daily) != 1 || daily[0].(map[string]any)["endUser"] != "alice" {
		t.Fatalf("end_user usage: %d %v", code, out)
	}
	if _, out = h.do("GET", "/v1/usage?scope=app", h.keyA, nil); totalIn(out) != 110 {
		t.Fatalf("app usage must cover the app and its end-users only: %v", out)
	}
	if _, out = h.do("GET", "/v1/usage?scope=app&agent_id="+agt2, h.keyA, nil); totalIn(out) != 100 {
		t.Fatalf("agent filter: %v", out)
	}
	if _, out = h.do("GET", "/v1/usage", h.keyA, nil); totalIn(out) != 100 {
		t.Fatalf("default usage stays the caller's own namespace: %v", out)
	}
	if _, out = h.do("GET", "/v1/usage?end_user=nobody", h.keyA, nil); totalIn(out) != 0 {
		t.Fatalf("unknown end_user: %v", out)
	}
	if _, err := h.st.GetUserByExternal(ctx, h.appA, "nobody"); err == nil {
		t.Fatal("querying usage must not mint end-users")
	}
	code, out = h.do("GET", "/v1/usage?user_id="+h.appA, h.keyB, nil)
	if code != http.StatusForbidden || errCode(out) != "forbidden" {
		t.Fatalf("app B read app A's usage: %d %v", code, out)
	}
}

func TestV1UnauthorizedUsesUnifiedError(t *testing.T) {
	h := newV1Harness(t)
	code, out := h.do("GET", "/v1/agents", "fc_bogus", nil)
	if code != http.StatusUnauthorized || errCode(out) != "unauthorized" {
		t.Fatalf("bad key: %d %v", code, out)
	}
}

// An "agent" key uses exactly the agents granted to it; with a single
// grant, requests that name no agent go to that one.
func TestAgentKeyUsesItsGrants(t *testing.T) {
	h := newV1Harness(t)
	ctx := context.Background()
	editor := h.createAgent(h.keyA, "Snapok-Image-Editor", nil)
	other := h.createAgent(h.keyA, "Private", nil)
	_, key, err := h.apikeys.Create(ctx, h.appA, "snapok", users.APIKeyTypeAgent, []string{editor})
	if err != nil {
		t.Fatal(err)
	}

	_, out := h.do("GET", "/v1/agents", key, nil)
	if list := out["agents"].([]any); len(list) != 1 || list[0].(map[string]any)["id"] != editor {
		t.Fatalf("agent key lists %v, want only the granted agent", list)
	}
	if code, out := h.do("GET", "/v1/agents/"+other, key, nil); code != http.StatusNotFound {
		t.Fatalf("ungranted agent visible: %d %v", code, out)
	}
	for _, body := range []map[string]any{
		{"agent_id": editor, "messages": []map[string]string{{"role": "user", "content": "hi"}}},
		{"messages": []map[string]string{{"role": "user", "content": "hi"}}},
	} {
		if code, out := h.do("POST", "/v1/chat/completions", key, body); code != http.StatusOK {
			t.Fatalf("chat %v: %d %v", body, code, out)
		}
	}
	if got := h.rt.ensured[len(h.rt.ensured)-1]; got != h.appA+"/"+editor {
		t.Fatalf("default agent resolved to %s, want the sole grant", got)
	}
	code, out := h.do("POST", "/v1/chat/completions", key, map[string]any{
		"agent_id": other, "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if code != http.StatusNotFound || errCode(out) != "agent_not_found" {
		t.Fatalf("ungranted chat: %d %v", code, out)
	}
}

// Files the agent writes into the conversation workspace come back with
// the reply — linked ones first, uploads excluded — and download through
// /v1 only within the caller's namespace.
func TestChatReturnsProducedFiles(t *testing.T) {
	h := newV1Harness(t)
	ctx := context.Background()
	id := h.createAgent(h.keyA, "Editor", nil)
	const session = "snapok:edit:1"
	png := []byte("\x89PNG fake image")
	h.rt.prov.mu.Lock()
	h.rt.prov.reply = "Done: ![edited](/workspace/out/edited.png)"
	h.rt.prov.onChat = func() {
		_ = h.ws.Put(ctx, id, "", session, "out/edited.png", bytes.NewReader(png), int64(len(png)), "image/png")
	}
	h.rt.prov.mu.Unlock()

	upload := "data:image/png;base64,iVBORw0KGgo="
	code, out := h.do("POST", "/v1/chat/completions", h.keyA, map[string]any{
		"agent_id": id, "images": []string{upload}, "return_files": "inline",
		"messages": []map[string]string{{"role": "user", "content": "make the background blue"}},
	}, "X-Fastclaw-Session-Key", session, "X-Fastclaw-End-User", "alice")
	if code != http.StatusOK {
		t.Fatalf("chat: %d %v", code, out)
	}
	files, _ := out["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("files = %v, want just the produced file (not the upload)", out["files"])
	}
	f := files[0].(map[string]any)
	wantURL := "http://example.com/v1/agents/" + id + "/sessions/snapok:edit:1/files/out/edited.png"
	if f["path"] != "out/edited.png" || f["content_type"] != "image/png" || f["url"] != wantURL {
		t.Fatalf("file = %v", f)
	}
	if f["data_url"] != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("inline data_url = %v", f["data_url"])
	}

	// The conversation is alice's: record it the way the session store does.
	alice, _ := h.st.GetUserByExternal(ctx, h.appA, "alice")
	if _, err := h.st.DB().ExecContext(ctx,
		`INSERT INTO sessions (user_id, agent_id, session_key, channel, chat_id) VALUES (?, ?, 's-1', 'api', ?)`,
		alice.ID, id, session); err != nil {
		t.Fatal(err)
	}
	get := func(path string, headers ...string) (int, string) {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+h.keyA)
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		rec := httptest.NewRecorder()
		h.mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	filePath := "/v1/agents/" + id + "/sessions/snapok:edit:1/files/out/edited.png"
	if code, body := get(filePath, "X-Fastclaw-End-User", "alice"); code != http.StatusOK || body != string(png) {
		t.Fatalf("download as alice: %d %q", code, body)
	}
	for name, hdr := range map[string][]string{
		"another end-user":  {"X-Fastclaw-End-User", "bob"},
		"the app namespace": {},
	} {
		if code, _ := get(filePath, hdr...); code != http.StatusNotFound {
			t.Fatalf("download as %s: %d, want 404", name, code)
		}
	}
	// The mux cleans literal ".." (redirect); an encoded one reaches the
	// handler, which keeps it inside the conversation's workspace.
	for _, p := range []string{"files/../../../etc/passwd", "files/..%2F..%2F..%2Fsessions%2Fother%2Fx"} {
		if code, _ := get("/v1/agents/"+id+"/sessions/snapok:edit:1/"+p, "X-Fastclaw-End-User", "alice"); code == http.StatusOK {
			t.Fatalf("path traversal %s: %d", p, code)
		}
	}
}

func TestStreamedChatReturnsFilesOnFinalChunk(t *testing.T) {
	h := newV1Harness(t)
	ctx := context.Background()
	id := h.createAgent(h.keyA, "Editor", nil)
	h.rt.prov.mu.Lock()
	h.rt.prov.onChat = func() {
		_ = h.ws.Put(ctx, id, "", "s:1", "result.png", bytes.NewReader([]byte("img")), 3, "image/png")
	}
	h.rt.prov.mu.Unlock()
	blob, _ := json.Marshal(map[string]any{
		"agent_id": id, "stream": true,
		"messages": []map[string]string{{"role": "user", "content": "go"}},
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(blob))
	req.Header.Set("Authorization", "Bearer "+h.keyA)
	req.Header.Set("X-Fastclaw-Session-Key", "s:1")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	var final map[string]any
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, "data: {") && strings.Contains(line, `"finish_reason":"stop"`) {
			_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &final)
		}
	}
	files, _ := final["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["path"] != "result.png" {
		t.Fatalf("final chunk files = %v\n%s", final["files"], rec.Body.String())
	}
}

// A streamed turn relays each round's text as it comes and reports
// every tool call starting and finishing; the text adds up to the
// non-streamed reply.
func TestStreamedChatReportsToolSteps(t *testing.T) {
	h := newV1Harness(t)
	id := h.createAgent(h.keyA, "Steps", nil)
	h.rt.prov.mu.Lock()
	h.rt.prov.script = []provider.Response{
		{Content: "Looking.", ToolCalls: []provider.ToolCall{{ID: "call_1", Type: "function", Function: provider.FunctionCall{Name: "read_file", Arguments: `{"path":"missing.txt"}`}}}},
		{Content: "Done."},
	}
	h.rt.prov.mu.Unlock()
	blob, _ := json.Marshal(map[string]any{
		"agent_id": id, "stream": true,
		"messages": []map[string]string{{"role": "user", "content": "go"}},
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(blob))
	req.Header.Set("Authorization", "Bearer "+h.keyA)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)

	var text strings.Builder
	var steps []string
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Tool *struct{ ID, Name, Status string } `json:"tool"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatal(err)
		}
		text.WriteString(chunk.Choices[0].Delta.Content)
		if chunk.Tool != nil {
			steps = append(steps, chunk.Tool.ID+" "+chunk.Tool.Name+" "+chunk.Tool.Status+" after "+strconv.Quote(text.String()))
		}
	}
	want := []string{`call_1 read_file running after "Looking."`, `call_1 read_file done after "Looking."`}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("tool steps = %q, want %q\n%s", steps, want, rec.Body.String())
	}
	if text.String() != "Looking.\n\nDone." {
		t.Fatalf("streamed text = %q\n%s", text.String(), rec.Body.String())
	}
}

func TestMessageContentParts(t *testing.T) {
	var msgs []chatMessage
	err := json.Unmarshal([]byte(`[
		{"role": "user", "content": "plain"},
		{"role": "user", "content": [
			{"type": "text", "text": "make it blue"},
			{"type": "image_url", "image_url": {"url": "data:image/png;base64,AAAA"}},
			{"type": "image_url", "image_url": "https://example.com/a.jpg"}
		]},
		{"role": "user", "content": [
			{"type": "image", "source": {"type": "base64", "media_type": "image/jpeg", "data": "BBBB"}},
			{"type": "text", "text": "what is this"}
		]}
	]`), &msgs)
	if err != nil {
		t.Fatal(err)
	}
	if msgs[0].Content.Text != "plain" || len(msgs[0].Content.Images) != 0 {
		t.Fatalf("string content: %+v", msgs[0].Content)
	}
	if msgs[1].Content.Text != "make it blue" ||
		len(msgs[1].Content.Images) != 2 || msgs[1].Content.Images[1] != "https://example.com/a.jpg" {
		t.Fatalf("openai parts: %+v", msgs[1].Content)
	}
	if msgs[2].Content.Text != "what is this" || msgs[2].Content.Images[0] != "data:image/jpeg;base64,BBBB" {
		t.Fatalf("anthropic parts: %+v", msgs[2].Content)
	}
	out, _ := json.Marshal(chatMessage{Role: "assistant", Content: messageContent{Text: "hi"}})
	if string(out) != `{"role":"assistant","content":"hi"}` {
		t.Fatalf("marshal: %s", out)
	}
}

// An OpenAI-style image part reaches the agent like the `images` field.
func TestChatAcceptsImageContentParts(t *testing.T) {
	h := newV1Harness(t)
	id := h.createAgent(h.keyA, "Vision", nil)
	code, out := h.do("POST", "/v1/chat/completions", h.keyA, map[string]any{
		"agent_id": id,
		"messages": []map[string]any{{"role": "user", "content": []map[string]any{
			{"type": "text", "text": "describe"},
			{"type": "image_url", "image_url": map[string]string{"url": "data:image/png;base64,iVBORw0KGgo="}},
		}}},
	}, "X-Fastclaw-Session-Key", "parts:1")
	if code != http.StatusOK {
		t.Fatalf("chat: %d %v", code, out)
	}
	if msg := out["choices"].([]any)[0].(map[string]any)["message"].(map[string]any); msg["content"] != "pong" {
		t.Fatalf("reply content must stay a string: %v", msg["content"])
	}
	h.rt.prov.mu.Lock()
	got := h.rt.prov.lastUser
	h.rt.prov.mu.Unlock()
	if !strings.Contains(got, "[Attached: /workspace/") || !strings.Contains(got, ".png]") || !strings.Contains(got, "describe") {
		t.Fatalf("image part not attached to the turn: %q", got)
	}
	// Image-only messages are fine too.
	code, out = h.do("POST", "/v1/chat/completions", h.keyA, map[string]any{
		"agent_id": id,
		"messages": []map[string]any{{"role": "user", "content": []map[string]any{
			{"type": "image_url", "image_url": map[string]string{"url": "data:image/png;base64,iVBORw0KGgo="}},
		}}},
	})
	if code != http.StatusOK {
		t.Fatalf("image-only message: %d %v", code, out)
	}
}

// The response reports the turn's usage: every model call it took.
func TestChatReportsTurnUsage(t *testing.T) {
	h := newV1Harness(t)
	id := h.createAgent(h.keyA, "Meter", nil)
	h.rt.prov.mu.Lock()
	h.rt.prov.usage = provider.Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 300, CacheCreationTokens: 5}
	h.rt.prov.mu.Unlock()
	body := map[string]any{"agent_id": id, "messages": []map[string]string{{"role": "user", "content": "hi"}}}

	// Each model call reports the same usage; the response sums them.
	check := func(u map[string]any, calls float64) {
		t.Helper()
		want := map[string]float64{
			"prompt_tokens": 405 * calls, "completion_tokens": 20 * calls, "total_tokens": 425 * calls,
			"input_tokens": 100 * calls, "cache_read_tokens": 300 * calls, "cache_creation_tokens": 5 * calls,
			"model_calls": calls,
		}
		for k, v := range want {
			if u[k] != v {
				t.Fatalf("usage[%s] = %v, want %v (%v)", k, u[k], v, u)
			}
		}
		if d := u["prompt_tokens_details"].(map[string]any); d["cached_tokens"] != 300*calls {
			t.Fatalf("cached_tokens = %v", d)
		}
	}
	code, out := h.do("POST", "/v1/chat/completions", h.keyA, body)
	if code != http.StatusOK {
		t.Fatalf("chat: %d %v", code, out)
	}
	check(out["usage"].(map[string]any), 1)

	body["stream"] = true
	blob, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(blob))
	req.Header.Set("Authorization", "Bearer "+h.keyA)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	var final map[string]any
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, "data: {") && strings.Contains(line, `"finish_reason":"stop"`) {
			_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &final)
		}
	}
	u, _ := final["usage"].(map[string]any)
	if u == nil {
		t.Fatalf("final chunk has no usage:\n%s", rec.Body.String())
	}
	// Streaming runs the same loop: one model call.
	check(u, 1)
}

// The response's usage carries the turn's USD cost at the model's unit
// price — on the non-streaming body and the final stream chunk — and each
// token_usage_log row stores its call's cost so pulled events agree.
func TestChatReportsTurnCost(t *testing.T) {
	h := newV1Harness(t)
	h.rt.meter = usage.NewSQLMeter(h.st.DB(), "sqlite")
	h.rt.providers = map[string]config.ProviderConfig{
		"test": {Models: []config.ModelEntry{
			{ID: "model", Cost: config.ModelCost{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}},
			{ID: "bigger"}, // no price
		}},
	}
	id := h.createAgent(h.keyA, "Costed", nil)
	h.rt.prov.mu.Lock()
	h.rt.prov.usage = provider.Usage{InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 4000, CacheCreationTokens: 100}
	h.rt.prov.mu.Unlock()
	want := (1000*3.0 + 200*15.0 + 4000*0.3 + 100*3.75) / 1e6

	body := map[string]any{"agent_id": id, "messages": []map[string]string{{"role": "user", "content": "hi"}}}
	code, out := h.do("POST", "/v1/chat/completions", h.keyA, body)
	if code != http.StatusOK {
		t.Fatalf("chat: %d %v", code, out)
	}
	if got, ok := out["usage"].(map[string]any)["cost_usd"].(float64); !ok || math.Abs(got-want) > 1e-12 {
		t.Fatalf("cost_usd = %v, want %v", out["usage"], want)
	}

	body["stream"] = true
	blob, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(blob))
	req.Header.Set("Authorization", "Bearer "+h.keyA)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	var final map[string]any
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, "data: {") && strings.Contains(line, `"usage"`) {
			_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &final)
		}
	}
	u, _ := final["usage"].(map[string]any)
	if got, ok := u["cost_usd"].(float64); !ok || math.Abs(got-want) > 1e-12 {
		t.Fatalf("stream cost_usd = %v, want %v", u, want)
	}

	// The unpriced model reports null — present, never 0, never omitted.
	delete(body, "stream")
	body["model"] = "test/bigger"
	code, out = h.do("POST", "/v1/chat/completions", h.keyA, body)
	if code != http.StatusOK {
		t.Fatalf("chat: %d %v", code, out)
	}
	if v, present := out["usage"].(map[string]any)["cost_usd"]; !present || v != nil {
		t.Fatalf("unpriced model: cost_usd = %v (present=%v), want null", v, present)
	}

	rows, err := h.st.DB().Query(`SELECT model, cost_usd FROM token_usage_log ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var model string
		var cost sql.NullFloat64
		if err := rows.Scan(&model, &cost); err != nil {
			t.Fatal(err)
		}
		switch {
		case !cost.Valid:
			got = append(got, model+"=null")
		case math.Abs(cost.Float64-want) < 1e-12:
			got = append(got, model+"=want")
		default:
			got = append(got, fmt.Sprintf("%s=%v", model, cost.Float64))
		}
	}
	if strings.Join(got, ",") != "model=want,model=want,bigger=null" {
		t.Fatalf("token_usage_log costs: %v", got)
	}
}

func TestChatRefusedOnBillingHold(t *testing.T) {
	h := newV1Harness(t)
	id := h.createAgent(h.keyA, "Held", nil)
	if err := h.st.SetBillingHold(context.Background(), h.appA, true, "balance 0"); err != nil {
		t.Fatal(err)
	}
	code, out := h.do("POST", "/v1/chat/completions", h.keyA, map[string]any{
		"agent_id": id, "messages": []map[string]string{{"role": "user", "content": "hi"}},
	}, "X-Fastclaw-End-User", "alice")
	if code != http.StatusPaymentRequired || errCode(out) != "payment_required" {
		t.Fatalf("held account: %d %v", code, out)
	}
	if strings.Contains(fmt.Sprint(out), "balance 0") {
		t.Fatal("the billing system's note must not reach API callers")
	}
}
