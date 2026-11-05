package setup

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// A product's key + X-Fastclaw-End-User reaches the WeChat channel
// endpoints of that end user's private agent, and another end user's
// private agent looks nonexistent (404) — even with an admin key.
func TestEndUserPrivateAgentChannelEndpoints(t *testing.T) {
	ctx := context.Background()
	s, _, _, account := newAuthTestServer(t, ctx)
	apikeys, _ := users.NewAPIKeys(s.dataStore)

	for _, keyType := range []string{users.APIKeyTypeUser, users.APIKeyTypeAdmin} {
		t.Run(keyType, func(t *testing.T) {
			key, token, err := apikeys.Create(ctx, account.ID, "weclaw-"+keyType, keyType, nil)
			if err != nil {
				t.Fatalf("create key: %v", err)
			}
			alice, err := s.accounts.EnsureAppUser(ctx, account.ID, "user:alice", "", key.ID)
			if err != nil {
				t.Fatal(err)
			}
			agentID := "agt_alice_mac_" + keyType
			if err := s.dataStore.SaveAgent(ctx, &store.AgentRecord{ID: agentID, UserID: alice.ID, Name: "Alice's Mac"}); err != nil {
				t.Fatal(err)
			}
			call := func(handler http.HandlerFunc, method, path, endUser string, pathValues ...string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, path, nil)
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("X-Fastclaw-End-User", endUser)
				for i := 0; i+1 < len(pathValues); i += 2 {
					req.SetPathValue(pathValues[i], pathValues[i+1])
				}
				rr := httptest.NewRecorder()
				s.authMiddleware(handler)(rr, req)
				return rr
			}
			id := []string{"id", agentID}

			// The owner passes the agent check (then: unknown QR session /
			// no such binding).
			rr := call(s.handleAgentWeChatLoginStatus, "GET", "/api/agents/"+agentID+"/channels/wechat/login/status?session=nope", "user:alice", id...)
			if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "session not found") {
				t.Fatalf("owner login status: %d %s", rr.Code, rr.Body)
			}
			rr = call(s.handleDisconnectAgentChannel, "DELETE", "/api/agents/"+agentID+"/channels/wechat/acc", "user:alice",
				"id", agentID, "type", "wechat", "accountId", "acc")
			if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "binding not found") {
				t.Fatalf("owner disconnect: %d %s", rr.Code, rr.Body)
			}
			if rec, _ := s.dataStore.GetAgent(ctx, agentID); rec == nil || !s.agentReadableForTest(t, token, "user:alice", rec) {
				t.Fatal("owner can't read their private agent")
			}

			// Another end user: 404 everywhere, before anything else runs.
			for name, rr := range map[string]*httptest.ResponseRecorder{
				"login":  call(s.handleStartAgentWeChatLogin, "POST", "/api/agents/"+agentID+"/channels/wechat/login", "user:bob", id...),
				"status": call(s.handleAgentWeChatLoginStatus, "GET", "/api/agents/"+agentID+"/channels/wechat/login/status?session=nope", "user:bob", id...),
				"disconnect": call(s.handleDisconnectAgentChannel, "DELETE", "/api/agents/"+agentID+"/channels/wechat/acc", "user:bob",
					"id", agentID, "type", "wechat", "accountId", "acc"),
				"avatar": call(s.handleAgentFile, "GET", "/api/agents/"+agentID+"/files/avatar.png", "user:bob",
					"id", agentID, "path", "avatar.png"),
			} {
				if rr.Code != http.StatusNotFound || strings.Contains(rr.Body.String(), "session not found") || strings.Contains(rr.Body.String(), "binding") {
					t.Errorf("bob %s: %d %s", name, rr.Code, rr.Body)
				}
			}
		})
	}
}

// agentReadableForTest runs agentReadable for a key + end-user request.
func (s *Server) agentReadableForTest(t *testing.T, token, endUser string, rec *store.AgentRecord) bool {
	t.Helper()
	var ok bool
	req := httptest.NewRequest("GET", "/api/agents/"+rec.ID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Fastclaw-End-User", endUser)
	s.authMiddleware(func(_ http.ResponseWriter, r *http.Request) { ok = s.agentReadable(r, rec) })(httptest.NewRecorder(), req)
	return ok
}
