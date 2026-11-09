package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/agl"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// fakeLocal is a linked computer whose coding agent asks for permission
// and waits for the decision.
type fakeLocal struct {
	mu       sync.Mutex
	machines map[string][]agl.LocalMachine
	runs     []agl.ToolRun
	pending  map[string]chan string // chat key → decision
}

func (f *fakeLocal) Machines(_ context.Context, uid string) ([]agl.LocalMachine, error) {
	return f.machines[uid], nil
}

func (f *fakeLocal) Run(ctx context.Context, r agl.ToolRun) (string, error) {
	f.mu.Lock()
	f.runs = append(f.runs, r)
	ch := make(chan string, 1)
	f.pending[chatKey(r.Chat)] = ch
	f.mu.Unlock()
	r.Notify(agl.ToolNotice{Kind: agl.NoticeApproval, Agent: "Codex", Machine: "MacBook", Text: "Run: say joke"})
	select {
	case d := <-ch:
		r.Notify(agl.ToolNotice{Kind: agl.NoticeApprovalResolved, Agent: "Codex", Machine: "MacBook", Decision: d})
		r.Notify(agl.ToolNotice{Kind: agl.NoticeDone, Agent: "Codex", Machine: "MacBook"})
		if d == "deny" {
			return "denied", nil
		}
		return "Why do programmers prefer dark mode? Light attracts bugs.", nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(5 * time.Second):
		return "", context.DeadlineExceeded
	}
}

func chatKey(c agl.ToolChat) string { return c.Channel + "|" + c.AccountID + "|" + c.ChatID }

func (f *fakeLocal) PendingApproval(chat agl.ToolChat, userID string) (agl.PendingToolApproval, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.pending[chatKey(chat)]; !ok || chat.AccountID != userID {
		return agl.PendingToolApproval{}, false
	}
	return agl.PendingToolApproval{Agent: "Codex", Machine: "MacBook", Title: "Run: say joke"}, true
}

func (f *fakeLocal) Decide(_ context.Context, chat agl.ToolChat, userID, decision string) (bool, error) {
	f.mu.Lock()
	ch, ok := f.pending[chatKey(chat)]
	if ok && chat.AccountID == userID {
		delete(f.pending, chatKey(chat))
	}
	f.mu.Unlock()
	if !ok || chat.AccountID != userID {
		return false, nil
	}
	ch <- decision
	return true, nil
}

func TestLocalAgentsOverV1(t *testing.T) {
	h := newV1Harness(t)
	id := h.createAgent(h.keyA, "Persona", nil)
	// Mint alice's end user and give her a computer.
	if code, out := h.do("POST", "/v1/users", h.keyA, map[string]any{"external_id": "user:alice"}); code != 200 {
		t.Fatalf("mint: %d %v", code, out)
	}
	alice := h.appUserID(h.appA, "user:alice")
	fake := &fakeLocal{machines: map[string][]agl.LocalMachine{alice: {{ID: "mch_1", Name: "MacBook", Online: true}}}, pending: map[string]chan string{}}
	agent.SetLocalAgents(fake)
	t.Cleanup(func() { agent.SetLocalAgents(nil) })

	script := func() {
		h.rt.prov.mu.Lock()
		h.rt.prov.script = []provider.Response{
			{ToolCalls: []provider.ToolCall{{ID: "call_1", Type: "function", Function: provider.FunctionCall{Name: "run_local_agent", Arguments: `{"agent":"codex","task":"tell a joke"}`}}}},
			{Content: "Codex says: light attracts bugs."},
		}
		h.rt.prov.mu.Unlock()
	}
	chat := func(localAgents bool, sessionKey string) *httptest.ResponseRecorder {
		blob, _ := json.Marshal(map[string]any{
			"agent_id": id, "stream": true, "local_agents": localAgents,
			"messages": []map[string]string{{"role": "user", "content": "让 codex 讲个笑话"}},
		})
		req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(blob))
		req.Header.Set("Authorization", "Bearer "+h.keyA)
		req.Header.Set("X-Fastclaw-End-User", "user:alice")
		if sessionKey != "" {
			req.Header.Set("x-fastclaw-session-key", sessionKey)
		}
		rec := httptest.NewRecorder()
		h.mux.ServeHTTP(rec, req)
		return rec
	}

	// Without local_agents the tool isn't offered: the model's call fails
	// and the computer is never reached.
	script()
	chat(false, "imc:acc_1:wx")
	if len(fake.runs) != 0 {
		t.Fatal("run_local_agent must not run without local_agents")
	}

	// With it: the approval streams out, the caller answers, the turn goes on.
	script()
	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- chat(true, "imc:acc_1:wx") }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, out := h.do("GET", "/v1/local-agents/approval?agent_id="+id+"&session_key=imc:acc_1:wx", h.keyA, nil, "X-Fastclaw-End-User", "user:alice")
		if code == 200 && out["pending"] == true {
			if out["title"] != "Run: say joke" || out["machine"] != "MacBook" {
				t.Fatalf("pending: %v", out)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no pending approval")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Another end user can't see or answer it.
	if _, out := h.do("GET", "/v1/local-agents/approval?agent_id="+id+"&session_key=imc:acc_1:wx", h.keyA, nil, "X-Fastclaw-End-User", "user:bob"); out["pending"] != false {
		t.Fatalf("bob sees alice's approval: %v", out)
	}
	if code, _ := h.do("POST", "/v1/local-agents/approval", h.keyA, map[string]any{"agent_id": id, "session_key": "imc:acc_1:wx", "decision": "allow"}, "X-Fastclaw-End-User", "user:bob"); code != 404 {
		t.Fatalf("bob decides: %d", code)
	}
	if code, out := h.do("POST", "/v1/local-agents/approval", h.keyA, map[string]any{"agent_id": id, "session_key": "imc:acc_1:wx", "decision": "nope"}, "X-Fastclaw-End-User", "user:alice"); code != 400 {
		t.Fatalf("bad decision: %d %v", code, out)
	}
	if code, out := h.do("POST", "/v1/local-agents/approval", h.keyA, map[string]any{"agent_id": id, "session_key": "imc:acc_1:wx", "decision": "allow"}, "X-Fastclaw-End-User", "user:alice"); code != 200 {
		t.Fatalf("decide: %d %v", code, out)
	}
	var rec *httptest.ResponseRecorder
	select {
	case rec = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("turn didn't finish")
	}

	var kinds []string
	var text strings.Builder
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct{ Content string } `json:"delta"`
			} `json:"choices"`
			LocalAgent map[string]any `json:"local_agent"`
		}
		_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk)
		if len(chunk.Choices) > 0 {
			text.WriteString(chunk.Choices[0].Delta.Content)
		}
		if chunk.LocalAgent != nil {
			k, _ := chunk.LocalAgent["kind"].(string)
			if k == "approval" && chunk.LocalAgent["text"] != "Run: say joke" {
				t.Fatalf("approval chunk: %v", chunk.LocalAgent)
			}
			kinds = append(kinds, k)
		}
	}
	if strings.Join(kinds, ",") != "approval,approval_resolved,done" {
		t.Fatalf("local_agent chunks = %v\n%s", kinds, rec.Body.String())
	}
	if !strings.Contains(text.String(), "light attracts bugs") {
		t.Fatalf("reply = %q", text.String())
	}
	run := fake.runs[0]
	if run.UserID != alice || run.Chat != agl.WebToolChat(alice, id, "imc:acc_1:wx") {
		t.Fatalf("run = %+v", run)
	}

	// No session key → ignored (approvals couldn't be addressed).
	script()
	chat(true, "")
	if len(fake.runs) != 1 {
		t.Fatal("local_agents without a session key must not reach the computer")
	}
}
