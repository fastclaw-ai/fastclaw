package api

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/users"
)

func chatBody(agentID string) map[string]any {
	return map[string]any{"agent_id": agentID, "messages": []map[string]string{{"role": "user", "content": "hi"}}}
}

// createPrivate creates an agent owned by endUser (body `user`).
func (h *v1Harness) createPrivate(key, endUser, name string, extra map[string]any) map[string]any {
	h.t.Helper()
	body := map[string]any{"name": name, "user": endUser}
	for k, v := range extra {
		body[k] = v
	}
	code, out := h.do("POST", "/v1/agents", key, body)
	if code != http.StatusOK {
		h.t.Fatalf("create private agent for %s: %d %v", endUser, code, out)
	}
	return out["agent"].(map[string]any)
}

func (h *v1Harness) appUserID(account, endUser string) string {
	h.t.Helper()
	u, err := h.st.GetUserByExternal(context.Background(), account, endUser)
	if err != nil {
		h.t.Fatalf("end-user %s not minted: %v", endUser, err)
	}
	return u.ID
}

func listIDs(out map[string]any) map[string]map[string]any {
	ids := map[string]map[string]any{}
	list, _ := out["agents"].([]any)
	for _, a := range list {
		m := a.(map[string]any)
		ids[m["id"].(string)] = m
	}
	return ids
}

// Who owns a created agent: the header alone never makes an agent private
// (callers that always send it keep creating platform agents); body
// `user` or owner "end_user" does.
func TestCreateAgentOwner(t *testing.T) {
	h := newV1Harness(t)
	ctx := context.Background()

	// No end-user: platform agent, as before.
	code, out := h.do("POST", "/v1/agents", h.keyA, map[string]any{"name": "plain"})
	if ag := out["agent"].(map[string]any); code != http.StatusOK || ag["owner"] != "account" || ag["end_user"] != nil {
		t.Fatalf("plain create: %d %v", code, out)
	}
	// Header only: still a platform agent.
	code, out = h.do("POST", "/v1/agents", h.keyA, map[string]any{"name": "hdr"}, "X-Fastclaw-End-User", "alice")
	hdr := out["agent"].(map[string]any)
	if rec, _ := h.st.GetAgent(ctx, hdr["id"].(string)); code != http.StatusOK || hdr["owner"] != "account" || rec.UserID != h.appA {
		t.Fatalf("header-only create must stay a platform agent: %d %v", code, out)
	}
	// Body user: private to alice.
	ag := h.createPrivate(h.keyA, "alice", "bot", map[string]any{"instructions": "You are alice's bot."})
	alice := h.appUserID(h.appA, "alice")
	rec, _ := h.st.GetAgent(ctx, ag["id"].(string))
	if ag["owner"] != "end_user" || ag["end_user"] != "alice" || rec.UserID != alice {
		t.Fatalf("body-user create: view %v, owner %s want %s", ag, rec.UserID, alice)
	}
	if soul, err := h.st.GetAgentFileExact(ctx, rec.ID, alice, "SOUL.md"); err != nil || string(soul) != "You are alice's bot." {
		t.Fatalf("SOUL.md under the owner = %q %v", soul, err)
	}
	// Header + owner end_user: private to the header's end-user.
	code, out = h.do("POST", "/v1/agents", h.keyA, map[string]any{"name": "bob-bot", "owner": "end_user"}, "X-Fastclaw-End-User", "bob")
	if ag := out["agent"].(map[string]any); code != http.StatusOK || ag["owner"] != "end_user" || ag["end_user"] != "bob" {
		t.Fatalf("header + owner=end_user: %d %v", code, out)
	}
	// owner account wins over user.
	code, out = h.do("POST", "/v1/agents", h.keyA, map[string]any{"name": "acct", "user": "alice", "owner": "account"})
	if ag := out["agent"].(map[string]any); code != http.StatusOK || ag["owner"] != "account" {
		t.Fatalf("owner=account with user: %d %v", code, out)
	}
	// Errors.
	if code, out = h.do("POST", "/v1/agents", h.keyA, map[string]any{"name": "x", "owner": "end_user"}); code != http.StatusBadRequest {
		t.Fatalf("owner=end_user without an end-user: %d %v", code, out)
	}
	if code, out = h.do("POST", "/v1/agents", h.keyA, map[string]any{"name": "x", "owner": "someone"}); code != http.StatusBadRequest {
		t.Fatalf("bad owner: %d %v", code, out)
	}
}

// End-user A can never reach end-user B's private agent through /v1 —
// read, write, files or chat — and gets the same 404 as for an agent that
// doesn't exist.
func TestPrivateAgentIsolation(t *testing.T) {
	h := newV1Harness(t)
	ctx := context.Background()
	platform := h.createAgent(h.keyA, "Support", nil)
	aliceBot := h.createPrivate(h.keyA, "alice", "Alice bot", nil)["id"].(string)
	bobBot := h.createPrivate(h.keyA, "bob", "Bob bot", nil)["id"].(string)

	// Alice chats with her own bot, in her namespace.
	if code, out := h.do("POST", "/v1/chat/completions", h.keyA, chatBody(aliceBot), "X-Fastclaw-End-User", "alice", "X-Fastclaw-Session-Key", "s-alice"); code != http.StatusOK {
		t.Fatalf("alice → her bot: %d %v", code, out)
	}
	if got := h.rt.ensured[len(h.rt.ensured)-1]; got != h.appUserID(h.appA, "alice")+"/"+aliceBot {
		t.Fatalf("alice's bot ran in %s", got)
	}
	// ... and with the platform agent.
	if code, out := h.do("POST", "/v1/chat/completions", h.keyA, chatBody(platform), "X-Fastclaw-End-User", "alice"); code != http.StatusOK {
		t.Fatalf("alice → platform agent: %d %v", code, out)
	}

	notFound := func(what string, code int, out map[string]any) {
		t.Helper()
		if code != http.StatusNotFound || errCode(out) != "agent_not_found" {
			t.Fatalf("%s: %d %v", what, code, out)
		}
	}
	ensuredBefore := len(h.rt.ensured)
	for _, who := range []struct {
		name    string
		key     string
		headers []string
		body    map[string]any
	}{
		{"bob via header", h.keyA, []string{"X-Fastclaw-End-User", "bob"}, nil},
		{"bob via body user", h.keyA, nil, map[string]any{"user": "bob"}},
		{"alice of another account", h.keyB, []string{"X-Fastclaw-End-User", "alice"}, nil},
		{"another account's key", h.keyB, nil, nil},
	} {
		// Body `user` only names the end-user on chat; elsewhere such a
		// request is the account's key, which manages every private agent.
		for _, c := range []struct{ method, path string }{
			{"GET", "/v1/agents/" + aliceBot},
			{"PATCH", "/v1/agents/" + aliceBot},
			{"DELETE", "/v1/agents/" + aliceBot},
			{"PUT", "/v1/agents/" + aliceBot + "/system-files/SOUL.md"},
			{"GET", "/v1/agents/" + aliceBot + "/sessions/s-alice/files/x.txt"},
		} {
			if who.body != nil {
				break
			}
			code, out := h.do(c.method, c.path, who.key, map[string]any{"name": "pwned", "content": "pwned"}, who.headers...)
			notFound(who.name+" "+c.method+" "+c.path, code, out)
		}
		body := chatBody(aliceBot)
		for k, v := range who.body {
			body[k] = v
		}
		code, out := h.do("POST", "/v1/chat/completions", who.key, body, who.headers...)
		notFound(who.name+" chat", code, out)
	}
	if len(h.rt.ensured) != ensuredBefore {
		t.Fatalf("refused requests attached agents: %v", h.rt.ensured[ensuredBefore:])
	}
	if rec, _ := h.st.GetAgent(ctx, aliceBot); rec == nil || rec.Name != "Alice bot" {
		t.Fatalf("alice's bot was modified: %+v", rec)
	}
	if soul, _ := h.st.GetAgentFile(ctx, aliceBot, h.appUserID(h.appA, "alice"), "SOUL.md"); string(soul) == "pwned" {
		t.Fatal("alice's SOUL.md was overwritten")
	}

	// Lists: each end-user sees platform agents + their own, tagged.
	_, out := h.do("GET", "/v1/agents", h.keyA, nil, "X-Fastclaw-End-User", "alice")
	ids := listIDs(out)
	if len(ids) != 2 || ids[platform]["owner"] != "account" || ids[aliceBot]["owner"] != "end_user" || ids[aliceBot]["end_user"] != "alice" {
		t.Fatalf("alice's list = %v", ids)
	}
	_, out = h.do("GET", "/v1/agents?owner=end_user", h.keyA, nil, "X-Fastclaw-End-User", "bob")
	if ids := listIDs(out); len(ids) != 1 || ids[bobBot] == nil {
		t.Fatalf("bob's private list = %v", ids)
	}
	_, out = h.do("GET", "/v1/agents", h.keyB, nil, "X-Fastclaw-End-User", "alice")
	if ids := listIDs(out); len(ids) != 0 {
		t.Fatalf("another account's alice sees %v", ids)
	}

	// An agent-scoped key never reaches private agents.
	_, agentKey, err := h.apikeys.Create(ctx, h.appA, "scoped", users.APIKeyTypeAgent, []string{platform})
	if err != nil {
		t.Fatal(err)
	}
	code, out := h.do("GET", "/v1/agents/"+aliceBot, agentKey, nil, "X-Fastclaw-End-User", "alice")
	notFound("agent key → private agent", code, out)
	_, out = h.do("GET", "/v1/agents", agentKey, nil, "X-Fastclaw-End-User", "alice")
	if ids := listIDs(out); len(ids) != 1 || ids[platform] == nil {
		t.Fatalf("agent key list = %v", ids)
	}
}

// The account's key without an end-user manages every private agent of
// its end-users, but cannot chat as them.
func TestAccountKeyManagesPrivateAgents(t *testing.T) {
	h := newV1Harness(t)
	platform := h.createAgent(h.keyA, "Support", nil)
	aliceBot := h.createPrivate(h.keyA, "alice", "Alice bot", nil)["id"].(string)
	bobBot := h.createPrivate(h.keyA, "bob", "Bob bot", nil)["id"].(string)

	// Default list is unchanged: platform agents only.
	_, out := h.do("GET", "/v1/agents", h.keyA, nil)
	if ids := listIDs(out); len(ids) != 1 || ids[platform] == nil {
		t.Fatalf("default list = %v", ids)
	}
	_, out = h.do("GET", "/v1/agents?owner=end_user", h.keyA, nil)
	ids := listIDs(out)
	if len(ids) != 2 || ids[aliceBot]["end_user"] != "alice" || ids[bobBot]["end_user"] != "bob" {
		t.Fatalf("private list = %v", ids)
	}
	_, out = h.do("GET", "/v1/agents?owner=all", h.keyA, nil)
	if ids := listIDs(out); len(ids) != 3 {
		t.Fatalf("all list = %v", ids)
	}

	code, out := h.do("PATCH", "/v1/agents/"+aliceBot, h.keyA, map[string]any{"name": "Renamed"})
	if code != http.StatusOK || out["agent"].(map[string]any)["display_name"] != "Renamed" {
		t.Fatalf("account patches private agent: %d %v", code, out)
	}
	code, out = h.do("POST", "/v1/chat/completions", h.keyA, chatBody(aliceBot))
	if code != http.StatusBadRequest {
		t.Fatalf("account chatting with a private agent without its owner: %d %v", code, out)
	}
	if code, _ := h.do("DELETE", "/v1/agents/"+bobBot, h.keyA, nil); code != http.StatusOK {
		t.Fatalf("account deletes private agent: %d", code)
	}
	// Another account's key still can't.
	if code, out := h.do("GET", "/v1/agents/"+aliceBot, h.keyB, nil); code != http.StatusNotFound {
		t.Fatalf("other account → private agent: %d %v", code, out)
	}
	_, out = h.do("GET", "/v1/agents?owner=end_user", h.keyB, nil)
	if ids := listIDs(out); len(ids) != 0 {
		t.Fatalf("other account lists %v", ids)
	}
}

// external_ref makes create idempotent per owner, including under
// concurrent creates.
func TestCreateAgentExternalRefIdempotent(t *testing.T) {
	h := newV1Harness(t)
	ctx := context.Background()
	// Mint alice up front so the race is on the agent, not the app_user.
	h.do("GET", "/v1/agents", h.keyA, nil, "X-Fastclaw-End-User", "alice")

	const n = 20
	ids := make([]string, n)
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, out := h.do("POST", "/v1/agents", h.keyA, map[string]any{
				"name": "bot", "user": "alice", "external_ref": "workany:bot:alice",
			})
			codes[i] = code
			if ag, ok := out["agent"].(map[string]any); ok {
				ids[i], _ = ag["id"].(string)
			}
		}(i)
	}
	wg.Wait()
	for i := range ids {
		if codes[i] != http.StatusOK || ids[i] == "" || ids[i] != ids[0] {
			t.Fatalf("concurrent create %d: code %d id %q (first %q)", i, codes[i], ids[i], ids[0])
		}
	}
	recs, err := h.st.ListAgents(ctx, h.appUserID(h.appA, "alice"))
	if err != nil || len(recs) != 1 || recs[0].ExternalRef != "workany:bot:alice" {
		t.Fatalf("alice owns %d agents (%v), want exactly one", len(recs), err)
	}

	// Sequential repeat: same agent, reported as not created, unchanged.
	code, out := h.do("POST", "/v1/agents", h.keyA, map[string]any{
		"name": "different", "user": "alice", "external_ref": "workany:bot:alice",
	})
	if ag := out["agent"].(map[string]any); code != http.StatusOK || ag["id"] != ids[0] || out["created"] != false || ag["display_name"] != "bot" || ag["external_ref"] != "workany:bot:alice" {
		t.Fatalf("repeat create: %d %v", code, out)
	}
	// The ref is per owner: bob and the account get their own agents.
	bob := h.createPrivate(h.keyA, "bob", "bot", map[string]any{"external_ref": "workany:bot:alice"})["id"]
	code, out = h.do("POST", "/v1/agents", h.keyA, map[string]any{"name": "bot", "external_ref": "workany:bot:alice"})
	acct := out["agent"].(map[string]any)["id"]
	if code != http.StatusOK || bob == ids[0] || acct == ids[0] || acct == bob {
		t.Fatalf("refs must be scoped per owner: alice %s bob %v account %v", ids[0], bob, acct)
	}
}

// The account's agent quota covers its end-users' private agents too.
func TestAgentQuotaCountsPrivateAgents(t *testing.T) {
	h := newV1Harness(t)
	ctx := context.Background()
	u, err := h.st.GetUser(ctx, h.appA)
	if err != nil {
		t.Fatal(err)
	}
	u.AgentQuota = 2
	if err := h.st.UpdateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	h.createAgent(h.keyA, "platform", nil)
	h.createPrivate(h.keyA, "alice", "bot", nil)
	code, out := h.do("POST", "/v1/agents", h.keyA, map[string]any{"name": "one more", "user": "bob"})
	if code != http.StatusForbidden || errCode(out) != "agent_quota_exceeded" {
		t.Fatalf("third agent over a quota of 2: %d %v", code, out)
	}
}

// The request's `model` runs the turn when the agent can reach it; values
// it can't (OpenAI SDK placeholders, unknown providers) are ignored, and
// the response names the model that actually ran.
func TestChatRequestModel(t *testing.T) {
	h := newV1Harness(t)
	id := h.createAgent(h.keyA, "Picker", nil)
	for _, c := range []struct{ requested, want string }{
		{"", "test/model"},
		{"test/bigger", "test/bigger"},
		{"gpt-4o", "test/model"},
		{"unknown/model", "test/model"},
	} {
		body := chatBody(id)
		body["model"] = c.requested
		code, out := h.do("POST", "/v1/chat/completions", h.keyA, body)
		if code != http.StatusOK {
			t.Fatalf("model %q: %d %v", c.requested, code, out)
		}
		_, wantID := splitModel(c.want)
		if got := h.rt.prov.lastModel(); got != c.want && got != wantID {
			t.Fatalf("model %q ran %q, want %q", c.requested, got, c.want)
		}
		if out["model"] != c.want {
			t.Fatalf("model %q: response names %v, want %q", c.requested, out["model"], c.want)
		}
	}
}

func splitModel(s string) (string, string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return s[:i], s[i+1:]
		}
	}
	return "", s
}
