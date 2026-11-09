package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/billing"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/toolproviders"
)

const hookSecret = "0123456789abcdef0123456789abcdef-hook"

// ageUsageEvents makes every recorded event old enough to export.
func (h *v1Harness) ageUsageEvents() {
	h.t.Helper()
	if _, err := h.st.DB().Exec(`UPDATE usage_events SET created_at = datetime('now', '-1 minute')`); err != nil {
		h.t.Fatal(err)
	}
}

// chatTurn runs one /v1 turn and returns the status, the reply's turn_id
// and the raw body.
func (h *v1Harness) chatTurn(key, agentID, endUser string, stream bool, sessionKey ...string) (int, string, string) {
	h.t.Helper()
	blob, _ := json.Marshal(map[string]any{
		"agent_id": agentID, "stream": stream,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(blob))
	req.Header.Set("Authorization", "Bearer "+key)
	if endUser != "" {
		req.Header.Set("X-Fastclaw-End-User", endUser)
	}
	if len(sessionKey) > 0 {
		req.Header.Set("X-Fastclaw-Session-Key", sessionKey[0])
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !stream {
		var out struct {
			TurnID string `json:"turn_id"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.TurnID, body
	}
	turnID := ""
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var chunk struct {
			TurnID  string `json:"turn_id"`
			Choices []struct {
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk)
		if chunk.TurnID != "" {
			if len(chunk.Choices) == 0 || chunk.Choices[0].FinishReason == nil {
				h.t.Fatalf("turn_id on a non-final chunk: %s", line)
			}
			turnID = chunk.TurnID
		}
	}
	return rec.Code, turnID, body
}

func eventsOf(out map[string]any) []map[string]any {
	var list []map[string]any
	for _, e := range out["events"].([]any) {
		list = append(list, e.(map[string]any))
	}
	return list
}

func TestV1UsageEventsScopedToAccount(t *testing.T) {
	h := newV1Harness(t)
	h.rt.prov.usage = provider.Usage{InputTokens: 9213, OutputTokens: 77}
	idA := h.createAgent(h.keyA, "A", nil)
	idB := h.createAgent(h.keyB, "B", nil)

	code, ownTurn, body := h.chatTurn(h.keyA, idA, "", false)
	if code != 200 || !strings.HasPrefix(ownTurn, "trn_") {
		t.Fatalf("A own turn: %d %q %s", code, ownTurn, body)
	}
	code, aliceTurn, body := h.chatTurn(h.keyA, idA, "user:42", true, "imc:acc_1:o9cq")
	if code != 200 || !strings.HasPrefix(aliceTurn, "trn_") || aliceTurn == ownTurn {
		t.Fatalf("A end-user streamed turn: %d %q %s", code, aliceTurn, body)
	}
	code, bTurn, _ := h.chatTurn(h.keyB, idB, "user:42", false)
	if code != 200 || bTurn == "" {
		t.Fatalf("B turn: %d", code)
	}

	// Fresh events aren't exported yet.
	if code, out := h.do("GET", "/v1/usage/events", h.keyA, nil); code != 200 || len(out["events"].([]any)) != 0 {
		t.Fatalf("fresh events exported: %d %v", code, out)
	}
	h.ageUsageEvents()

	code, out := h.do("GET", "/v1/usage/events", h.keyA, nil)
	if code != 200 {
		t.Fatalf("list: %d %v", code, out)
	}
	events := eventsOf(out)
	if len(events) != 2 || out["has_more"] != false || out["next_after"] != events[1]["id"] {
		t.Fatalf("A events: %v", out)
	}
	blob, _ := json.Marshal(events[0])
	t.Logf("exported event: %s", blob)
	own, alice := events[0], events[1]
	if own["kind"] != "llm" || own["turn_id"] != ownTurn || own["end_user"] != "" || own["agent_id"] != idA ||
		own["channel"] != "api" || !strings.HasPrefix(own["session_key"].(string), "api-") || own["input_tokens"] != float64(9213) || own["output_tokens"] != float64(77) ||
		own["provider"] != "test" || own["model"] != "model" || own["units"] != float64(0) || own["unit"] != "" {
		t.Fatalf("own event = %v", own)
	}
	if v, ok := own["cost_usd"]; !ok || v != nil {
		t.Fatalf("unknown cost must be null: %v", own)
	}
	if alice["turn_id"] != aliceTurn || alice["end_user"] != "user:42" || alice["session_key"] != "imc:acc_1:o9cq" {
		t.Fatalf("end-user event = %v", alice)
	}
	for _, k := range []string{"id", "created_at", "kind", "provider", "model", "input_tokens", "output_tokens",
		"cache_read_tokens", "cache_creation_tokens", "units", "unit", "cost_usd", "detail", "end_user",
		"agent_id", "session_key", "channel", "turn_id"} {
		if _, ok := own[k]; !ok {
			t.Fatalf("event lacks %q: %v", k, own)
		}
	}
	if len(own) != 18 {
		t.Fatalf("event has extra keys: %v", own)
	}

	// B sees only its own; the same end-user id under B is B's.
	_, out = h.do("GET", "/v1/usage/events", h.keyB, nil)
	bEvents := eventsOf(out)
	if len(bEvents) != 1 || bEvents[0]["turn_id"] != bTurn || bEvents[0]["agent_id"] != idB {
		t.Fatalf("B events: %v", out)
	}

	// Cursor + limit.
	_, out = h.do("GET", "/v1/usage/events?limit=1", h.keyA, nil)
	if p := eventsOf(out); len(p) != 1 || p[0]["turn_id"] != ownTurn || out["has_more"] != true {
		t.Fatalf("page 1: %v", out)
	}
	next := int64(out["next_after"].(float64))
	_, out = h.do("GET", "/v1/usage/events?limit=1&after="+itoa(next), h.keyA, nil)
	if p := eventsOf(out); len(p) != 1 || p[0]["turn_id"] != aliceTurn {
		t.Fatalf("page 2: %v", out)
	}
	_, out = h.do("GET", "/v1/usage/events?after="+itoa(int64(out["next_after"].(float64))), h.keyA, nil)
	if out["events"] == nil || len(out["events"].([]any)) != 0 || out["has_more"] != false {
		t.Fatalf("page 3: %v", out)
	}

	for _, bad := range []string{"limit=0", "limit=1001", "limit=x", "after=-1", "after=x"} {
		if code, out := h.do("GET", "/v1/usage/events?"+bad, h.keyA, nil); code != 400 || errCode(out) != "invalid_request" {
			t.Fatalf("%s: %d %v", bad, code, out)
		}
	}
	if code, out := h.do("GET", "/v1/usage/events", "sk-wrong", nil); code != 401 {
		t.Fatalf("bad key: %d %v", code, out)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestV1BillingHookRegistration(t *testing.T) {
	h := newV1Harness(t)
	t.Setenv(billing.EnvAllowPrivate, "")
	for name, body := range map[string]map[string]any{
		"http":         {"url": "http://93.184.216.34/hook", "secret": hookSecret},
		"loopback":     {"url": "https://127.0.0.1/hook", "secret": hookSecret},
		"private":      {"url": "https://10.1.2.3/hook", "secret": hookSecret},
		"metadata":     {"url": "https://169.254.169.254/latest", "secret": hookSecret},
		"localhost":    {"url": "https://localhost/hook", "secret": hookSecret},
		"short secret": {"url": "https://93.184.216.34/hook", "secret": "short"},
		"no url":       {"secret": hookSecret},
	} {
		if code, out := h.do("PUT", "/v1/billing/hook", h.keyA, body); code != 400 || errCode(out) != "invalid_request" {
			t.Fatalf("%s: %d %v", name, code, out)
		}
	}

	code, out := h.do("PUT", "/v1/billing/hook", h.keyA, map[string]any{"url": "https://93.184.216.34/api/fastclaw/authorize", "secret": hookSecret})
	if code != 200 || out["url"] != "https://93.184.216.34/api/fastclaw/authorize" || out["secret_set"] != true {
		t.Fatalf("register: %d %v", code, out)
	}
	raw := func(key string) string {
		req := httptest.NewRequest("GET", "/v1/billing/hook", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		h.mux.ServeHTTP(rec, req)
		return rec.Body.String()
	}
	if got := raw(h.keyA); strings.Contains(got, hookSecret) || !strings.Contains(got, `"secret_set":true`) ||
		!strings.Contains(got, `"url":"https://93.184.216.34/api/fastclaw/authorize"`) {
		t.Fatalf("GET hook = %s", got)
	}
	if got := raw(h.keyB); !strings.Contains(got, `"url":""`) || !strings.Contains(got, `"secret_set":false`) {
		t.Fatalf("B sees A's hook: %s", got)
	}
	if url, secret, _ := h.st.GetBillingHook(context.Background(), h.appA); url == "" || secret != hookSecret {
		t.Fatalf("stored hook = %q", url)
	}

	if code, out := h.do("PUT", "/v1/billing/hook", h.keyA, map[string]any{"url": ""}); code != 200 || out["secret_set"] != false {
		t.Fatalf("remove: %d %v", code, out)
	}
	if got := raw(h.keyA); !strings.Contains(got, `"url":""`) || !strings.Contains(got, `"secret_set":false`) {
		t.Fatalf("after remove: %s", got)
	}
}

// fakeHook is an app's authorize endpoint.
type fakeHook struct {
	*httptest.Server
	mu     sync.Mutex
	reqs   []billing.AuthorizeRequest
	decide func(billing.AuthorizeRequest) string // "" allows, else the deny message
}

func newFakeHook(t *testing.T, decide func(billing.AuthorizeRequest) string) *fakeHook {
	f := &fakeHook{decide: decide}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ts := strings.TrimPrefix(strings.Split(r.Header.Get(billing.SignatureHeader), ",")[0], "t=")
		var n int64
		_ = json.Unmarshal([]byte(ts), &n)
		if r.Header.Get(billing.SignatureHeader) != billing.Sign(hookSecret, n, body) {
			t.Errorf("bad signature %q", r.Header.Get(billing.SignatureHeader))
		}
		var req billing.AuthorizeRequest
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.reqs = append(f.reqs, req)
		f.mu.Unlock()
		if msg := f.decide(req); msg != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"allow": false, "message": msg})
			return
		}
		_, _ = io.WriteString(w, `{"allow": true}`)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeHook) requests() []billing.AuthorizeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]billing.AuthorizeRequest(nil), f.reqs...)
}

func (h *v1Harness) eventKinds() []string {
	h.t.Helper()
	rows, err := h.st.DB().Query(`SELECT kind FROM usage_events ORDER BY id`)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		out = append(out, k)
	}
	return out
}

func TestV1TurnDeniedByBillingHook(t *testing.T) {
	h := newV1Harness(t)
	t.Setenv(billing.EnvAllowPrivate, "true")
	const deny = "Your balance is used up. Top up at app.example.com to keep chatting."
	hook := newFakeHook(t, func(r billing.AuthorizeRequest) string {
		if r.Stage == "turn" {
			return deny
		}
		return ""
	})
	if err := h.st.SetBillingHook(context.Background(), h.appA, hook.URL+"/authorize", hookSecret); err != nil {
		t.Fatal(err)
	}
	id := h.createAgent(h.keyA, "Persona", nil)

	for _, stream := range []bool{false, true} {
		code, turnID, body := h.chatTurn(h.keyA, id, "user:42", stream)
		var out map[string]any
		_ = json.Unmarshal([]byte(body), &out)
		e, _ := out["error"].(map[string]any)
		if code != http.StatusPaymentRequired || errCode(out) != "payment_required" || e["message"] != deny || turnID != "" {
			t.Fatalf("stream=%v: %d %s", stream, code, body)
		}
	}
	if h.rt.prov.lastModel() != "" {
		t.Fatal("the model ran for a refused turn")
	}
	reqs := hook.requests()
	if len(reqs) != 2 || reqs[0].Stage != "turn" || reqs[0].Kind != "turn" || reqs[0].Channel != "api" ||
		reqs[0].EndUser != "user:42" || reqs[0].AgentID != id || !strings.HasPrefix(reqs[0].TurnID, "trn_") ||
		reqs[0].Provider != "test" || reqs[0].Model != "model" || !strings.HasPrefix(reqs[0].SessionKey, "api-") {
		t.Fatalf("hook requests = %+v", reqs)
	}

	// Any other channel: the refusal is the reply.
	if err := h.rt.EnsureAgent(context.Background(), h.appA, id); err != nil {
		t.Fatal(err)
	}
	ag := h.rt.manager(h.appA).AgentByID(id)
	if ag == nil {
		t.Fatal("agent not attached")
	}
	reply := ag.HandleMessage(context.Background(), bus.InboundMessage{Channel: "wechat", AccountID: "bot1", ChatID: "wx_1", UserID: "chatter", Text: "hi", PeerKind: "dm"})
	if reply != deny {
		t.Fatalf("wechat reply = %q", reply)
	}
	reply = ag.HandleMessage(context.Background(), bus.InboundMessage{Channel: "wechat", AccountID: "bot1", ChatID: "wx_1", UserID: "chatter", Text: "daily report", Source: bus.SourceCron})
	if reply != deny {
		t.Fatalf("cron reply = %q", reply)
	}
	reqs = hook.requests()
	if len(reqs) != 4 || reqs[2].Channel != "wechat" || reqs[3].Channel != "cron" || reqs[2].EndUser != "" {
		t.Fatalf("hook requests = %+v", reqs)
	}
	if kinds := h.eventKinds(); len(kinds) != 0 {
		t.Fatalf("refused turns recorded usage: %v", kinds)
	}

	// Allowed again: one hook call per turn, the turn runs and is metered.
	hook.decide = func(billing.AuthorizeRequest) string { return "" }
	code, turnID, body := h.chatTurn(h.keyA, id, "", false)
	if code != 200 || turnID == "" {
		t.Fatalf("allowed turn: %d %s", code, body)
	}
	if reqs = hook.requests(); len(reqs) != 5 || reqs[4].TurnID != turnID || reqs[4].EndUser != "" {
		t.Fatalf("allowed turn asked %+v", reqs)
	}
	if kinds := h.eventKinds(); len(kinds) != 1 || kinds[0] != "llm" {
		t.Fatalf("events = %v", kinds)
	}
}

// fakeImages is an image_gen provider.
type fakeImages struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeImages) Category() string     { return "image_gen" }
func (f *fakeImages) Name() string         { return "fal" }
func (f *fakeImages) CredentialFree() bool { return true }
func (f *fakeImages) Execute(context.Context, toolproviders.Request) (toolproviders.Response, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return toolproviders.Response{Text: "Generated 1 image(s) for: cat\n\n1. ![image 1](https://img.example/1.png)\n"}, nil
}

func TestV1ImageGenDeniedByBillingHook(t *testing.T) {
	h := newV1Harness(t)
	t.Setenv(billing.EnvAllowPrivate, "true")
	const deny = "No image credits left — tell the user to top up."
	hook := newFakeHook(t, func(r billing.AuthorizeRequest) string {
		if r.Stage == "tool" {
			return deny
		}
		return ""
	})
	if err := h.st.SetBillingHook(context.Background(), h.appA, hook.URL, hookSecret); err != nil {
		t.Fatal(err)
	}
	id := h.createAgent(h.keyA, "Painter", nil)
	if err := h.rt.EnsureAgent(context.Background(), h.appA, id); err != nil {
		t.Fatal(err)
	}
	images := &fakeImages{}
	reg := toolproviders.NewRegistry()
	reg.Register(images)
	h.rt.manager(h.appA).AgentByID(id).RegisterImageGenChain(&toolproviders.Chain{
		Category: "image_gen", Order: []string{"fal/flux-pro"}, Registry: reg,
		GetConfig: func(string) toolproviders.ProviderConfig { return toolproviders.ProviderConfig{} },
	})
	script := func() {
		h.rt.prov.mu.Lock()
		h.rt.prov.script = []provider.Response{
			{ToolCalls: []provider.ToolCall{{ID: "call_1", Type: "function", Function: provider.FunctionCall{Name: "image_gen", Arguments: `{"prompt":"a cat","n":1,"size":"1024x1024"}`}}}},
			{Content: "done"},
		}
		h.rt.prov.mu.Unlock()
	}

	script()
	code, turnID, body := h.chatTurn(h.keyA, id, "", false)
	if code != 200 {
		t.Fatalf("turn: %d %s", code, body)
	}
	if images.calls != 0 {
		t.Fatal("the image provider ran despite the denial")
	}
	h.rt.prov.mu.Lock()
	var toolResult string
	for _, m := range h.rt.prov.last {
		if m.Role == "tool" {
			toolResult = m.Content
		}
	}
	h.rt.prov.mu.Unlock()
	if !strings.Contains(toolResult, deny) {
		t.Fatalf("the model didn't get the hook's message as the tool error: %q", toolResult)
	}
	reqs := hook.requests()
	if len(reqs) != 2 || reqs[1].Stage != "tool" || reqs[1].Kind != "image" || reqs[1].Provider != "fal" ||
		reqs[1].Model != "flux-pro" || reqs[1].Estimate == nil || reqs[1].Estimate.Units != 1 || reqs[1].TurnID != turnID {
		t.Fatalf("hook requests = %+v", reqs)
	}
	if kinds := h.eventKinds(); strings.Join(kinds, ",") != "llm,llm" {
		t.Fatalf("events after a denied image = %v", kinds)
	}

	// Allowed: the image is metered on the same turn as its model calls.
	hook.decide = func(billing.AuthorizeRequest) string { return "" }
	script()
	code, turnID, _ = h.chatTurn(h.keyA, id, "", false)
	if code != 200 || images.calls != 1 {
		t.Fatalf("allowed image turn: %d calls=%d", code, images.calls)
	}
	h.ageUsageEvents()
	_, out := h.do("GET", "/v1/usage/events?after=2", h.keyA, nil)
	events := eventsOf(out)
	if len(events) != 3 {
		t.Fatalf("events = %v", out)
	}
	var img map[string]any
	for _, e := range events {
		if e["turn_id"] != turnID {
			t.Fatalf("event of another turn: %v", e)
		}
		if e["kind"] == "image" {
			img = e
		}
	}
	if img == nil || img["provider"] != "fal" || img["model"] != "flux-pro" || img["units"] != float64(1) ||
		img["unit"] != "image" || img["detail"].(map[string]any)["size"] != "1024x1024" || img["cost_usd"] != nil {
		t.Fatalf("image event = %v", img)
	}
}
