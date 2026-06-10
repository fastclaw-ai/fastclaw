package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/api"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

func TestAgentMCPServersCRUDMasksAndPreservesSecrets(t *testing.T) {
	ctx := context.Background()
	s, resolver, _, owner := newAuthTestServer(t, ctx)
	agentID := createMCPTestAgent(t, ctx, s.dataStore, owner.ID)
	invalidator := &mcpTestResolver{}
	s.SetUserResolver(invalidator)

	createBody := `{"name":"github","type":"http","enabled":true,"url":"https://example.com/mcp","headers":{"Authorization":"Bearer secret","X-Trace":"visible"}}`
	createReq := mcpAuthRequest(t, ctx, resolver, http.MethodPost, "/api/agents/"+agentID+"/mcp", owner.ID, createBody)
	createReq.SetPathValue("id", agentID)
	createRR := httptest.NewRecorder()
	s.authMiddleware(s.handleCreateAgentMCPServer)(createRR, createReq)
	if createRR.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", createRR.Code, createRR.Body.String())
	}
	if len(invalidator.agents) != 1 || invalidator.agents[0] != agentID {
		t.Fatalf("create should invalidate %s, got %#v", agentID, invalidator.agents)
	}

	listReq := mcpAuthRequest(t, ctx, resolver, http.MethodGet, "/api/agents/"+agentID+"/mcp", owner.ID, "")
	listReq.SetPathValue("id", agentID)
	listRR := httptest.NewRecorder()
	s.authMiddleware(s.handleListAgentMCPServers)(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", listRR.Code, listRR.Body.String())
	}
	var listResp struct {
		Servers []agentMCPServerOut `json:"servers"`
	}
	if err := json.Unmarshal(listRR.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.Servers) != 1 {
		t.Fatalf("want 1 server, got %#v", listResp.Servers)
	}
	if got := listResp.Servers[0].Headers["Authorization"]; got == "Bearer secret" || got == "" {
		t.Fatalf("Authorization header should be masked, got %q", got)
	}
	if got := listResp.Servers[0].Headers["X-Trace"]; got != "visible" {
		t.Fatalf("non-secret header should remain visible, got %q", got)
	}

	updateBody := `{"name":"github","type":"http","enabled":false,"url":"https://example.com/mcp2","headers":{"Authorization":"****","X-Trace":"changed"}}`
	updateReq := mcpAuthRequest(t, ctx, resolver, http.MethodPut, "/api/agents/"+agentID+"/mcp/github", owner.ID, updateBody)
	updateReq.SetPathValue("id", agentID)
	updateReq.SetPathValue("name", "github")
	updateRR := httptest.NewRecorder()
	s.authMiddleware(s.handleUpdateAgentMCPServer)(updateRR, updateReq)
	if updateRR.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", updateRR.Code, updateRR.Body.String())
	}
	if len(invalidator.agents) != 2 || invalidator.agents[1] != agentID {
		t.Fatalf("update should invalidate %s, got %#v", agentID, invalidator.agents)
	}
	rec, err := s.dataStore.GetConfigByName(ctx, store.KindMCP, "", agentID, "github")
	if err != nil || rec == nil {
		t.Fatalf("read MCP config: %v", err)
	}
	storedHeaders, _ := rec.Data["headers"].(map[string]any)
	if got := storedHeaders["Authorization"]; got != "Bearer secret" {
		t.Fatalf("masked update should preserve stored secret, got %#v", got)
	}
	if got := storedHeaders["X-Trace"]; got != "changed" {
		t.Fatalf("plain header should update, got %#v", got)
	}
	if rec.Enabled {
		t.Fatal("enabled should update to false")
	}

	deleteReq := mcpAuthRequest(t, ctx, resolver, http.MethodDelete, "/api/agents/"+agentID+"/mcp/github", owner.ID, "")
	deleteReq.SetPathValue("id", agentID)
	deleteReq.SetPathValue("name", "github")
	deleteRR := httptest.NewRecorder()
	s.authMiddleware(s.handleDeleteAgentMCPServer)(deleteRR, deleteReq)
	if deleteRR.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body=%s", deleteRR.Code, deleteRR.Body.String())
	}
	if len(invalidator.agents) != 3 || invalidator.agents[2] != agentID {
		t.Fatalf("delete should invalidate %s, got %#v", agentID, invalidator.agents)
	}
	if rec, err := s.dataStore.GetConfigByName(ctx, store.KindMCP, "", agentID, "github"); err == nil && rec != nil {
		t.Fatalf("MCP config should be deleted: %#v", rec)
	}
}

func TestAgentMCPServersRejectDuplicateCreate(t *testing.T) {
	ctx := context.Background()
	s, resolver, _, owner := newAuthTestServer(t, ctx)
	agentID := createMCPTestAgent(t, ctx, s.dataStore, owner.ID)
	body := `{"name":"github","type":"http","url":"https://example.com/mcp"}`

	createReq := mcpAuthRequest(t, ctx, resolver, http.MethodPost, "/api/agents/"+agentID+"/mcp", owner.ID, body)
	createReq.SetPathValue("id", agentID)
	createRR := httptest.NewRecorder()
	s.authMiddleware(s.handleCreateAgentMCPServer)(createRR, createReq)
	if createRR.Code != http.StatusOK {
		t.Fatalf("initial create status = %d, body=%s", createRR.Code, createRR.Body.String())
	}

	dupReq := mcpAuthRequest(t, ctx, resolver, http.MethodPost, "/api/agents/"+agentID+"/mcp", owner.ID, body)
	dupReq.SetPathValue("id", agentID)
	dupRR := httptest.NewRecorder()
	s.authMiddleware(s.handleCreateAgentMCPServer)(dupRR, dupReq)
	if dupRR.Code != http.StatusConflict {
		t.Fatalf("duplicate create status = %d, want 409, body=%s", dupRR.Code, dupRR.Body.String())
	}
}

func TestAgentMCPServersListMasksSecretStdioEnvOnly(t *testing.T) {
	ctx := context.Background()
	s, resolver, _, owner := newAuthTestServer(t, ctx)
	agentID := createMCPTestAgent(t, ctx, s.dataStore, owner.ID)
	body := `{"name":"local","type":"stdio","command":"mcp-server","env":{"API_TOKEN":"super-secret-token","LOG_LEVEL":"debug"}}`

	createReq := mcpAuthRequest(t, ctx, resolver, http.MethodPost, "/api/agents/"+agentID+"/mcp", owner.ID, body)
	createReq.SetPathValue("id", agentID)
	createRR := httptest.NewRecorder()
	s.authMiddleware(s.handleCreateAgentMCPServer)(createRR, createReq)
	if createRR.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", createRR.Code, createRR.Body.String())
	}

	listReq := mcpAuthRequest(t, ctx, resolver, http.MethodGet, "/api/agents/"+agentID+"/mcp", owner.ID, "")
	listReq.SetPathValue("id", agentID)
	listRR := httptest.NewRecorder()
	s.authMiddleware(s.handleListAgentMCPServers)(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", listRR.Code, listRR.Body.String())
	}
	var listResp struct {
		Servers []agentMCPServerOut `json:"servers"`
	}
	if err := json.Unmarshal(listRR.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.Servers) != 1 {
		t.Fatalf("want 1 server, got %#v", listResp.Servers)
	}
	if got := listResp.Servers[0].Env["API_TOKEN"]; got == "super-secret-token" || got == "" {
		t.Fatalf("secret env should be masked, got %q", got)
	}
	if got := listResp.Servers[0].Env["LOG_LEVEL"]; got != "debug" {
		t.Fatalf("non-secret env should remain visible, got %q", got)
	}
}

func TestAgentMCPServersRejectInvalidCreate(t *testing.T) {
	ctx := context.Background()
	s, resolver, _, owner := newAuthTestServer(t, ctx)
	agentID := createMCPTestAgent(t, ctx, s.dataStore, owner.ID)

	cases := []struct {
		name string
		body string
	}{
		{name: "bad name", body: `{"name":"bad-name","type":"http","url":"https://example.com/mcp"}`},
		{name: "bad type", body: `{"name":"demo","type":"websocket","url":"https://example.com/mcp"}`},
		{name: "http missing url", body: `{"name":"demo","type":"http"}`},
		{name: "http bad scheme", body: `{"name":"demo","type":"http","url":"file:///tmp/mcp"}`},
		{name: "http env expansion header", body: `{"name":"demo","type":"http","url":"https://example.com/mcp","headers":{"X-Leak":"$DATABASE_URL"}}`},
		{name: "stdio missing command", body: `{"name":"demo","type":"stdio"}`},
		{name: "stdio env expansion", body: `{"name":"demo","type":"stdio","command":"mcp-server","env":{"API_TOKEN":"$SECRET_TOKEN"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := mcpAuthRequest(t, ctx, resolver, http.MethodPost, "/api/agents/"+agentID+"/mcp", owner.ID, tc.body)
			req.SetPathValue("id", agentID)
			rr := httptest.NewRecorder()
			s.authMiddleware(s.handleCreateAgentMCPServer)(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestAgentMCPServersRejectHostedPrivateHTTPURL(t *testing.T) {
	t.Setenv("FASTCLAW_DEPLOY", "hosted")
	ctx := context.Background()
	s, resolver, _, owner := newAuthTestServer(t, ctx)
	agentID := createMCPTestAgent(t, ctx, s.dataStore, owner.ID)

	for _, body := range []string{
		`{"name":"loopback","type":"http","url":"http://127.0.0.1:8080/mcp"}`,
		`{"name":"localhost","type":"http","url":"http://localhost:8080/mcp"}`,
		`{"name":"private","type":"http","url":"http://10.0.0.1/mcp"}`,
		`{"name":"metadata","type":"http","url":"http://169.254.169.254/latest/meta-data"}`,
	} {
		req := mcpAuthRequest(t, ctx, resolver, http.MethodPost, "/api/agents/"+agentID+"/mcp", owner.ID, body)
		req.SetPathValue("id", agentID)
		rr := httptest.NewRecorder()
		s.authMiddleware(s.handleCreateAgentMCPServer)(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400, body=%s", rr.Code, rr.Body.String())
		}
	}
}

func TestAgentMCPServersNonOwnerForbidden(t *testing.T) {
	ctx := context.Background()
	s, resolver, _, owner := newAuthTestServer(t, ctx)
	accts, err := users.NewAccounts(s.dataStore)
	if err != nil {
		t.Fatalf("NewAccounts: %v", err)
	}
	other := createAuthTestUser(t, ctx, accts, "other", users.RoleUser)
	agentID := createMCPTestAgent(t, ctx, s.dataStore, owner.ID)

	ownerBody := `{"name":"github","type":"http","url":"https://example.com/mcp"}`
	ownerReq := mcpAuthRequest(t, ctx, resolver, http.MethodPost, "/api/agents/"+agentID+"/mcp", owner.ID, ownerBody)
	ownerReq.SetPathValue("id", agentID)
	ownerRR := httptest.NewRecorder()
	s.authMiddleware(s.handleCreateAgentMCPServer)(ownerRR, ownerReq)
	if ownerRR.Code != http.StatusOK {
		t.Fatalf("owner create status = %d, body=%s", ownerRR.Code, ownerRR.Body.String())
	}

	cases := []struct {
		name    string
		method  string
		path    string
		mcpName string
		body    string
		handler http.HandlerFunc
	}{
		{name: "list", method: http.MethodGet, path: "/api/agents/" + agentID + "/mcp", handler: s.handleListAgentMCPServers},
		{name: "create", method: http.MethodPost, path: "/api/agents/" + agentID + "/mcp", body: `{"name":"other","type":"http","url":"https://example.com/mcp"}`, handler: s.handleCreateAgentMCPServer},
		{name: "update", method: http.MethodPut, path: "/api/agents/" + agentID + "/mcp/github", mcpName: "github", body: ownerBody, handler: s.handleUpdateAgentMCPServer},
		{name: "delete", method: http.MethodDelete, path: "/api/agents/" + agentID + "/mcp/github", mcpName: "github", handler: s.handleDeleteAgentMCPServer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := mcpAuthRequest(t, ctx, resolver, tc.method, tc.path, other.ID, tc.body)
			req.SetPathValue("id", agentID)
			if tc.mcpName != "" {
				req.SetPathValue("name", tc.mcpName)
			}
			rr := httptest.NewRecorder()
			s.authMiddleware(tc.handler)(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403, body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestSystemMCPServersCRUDBySuperAdmin(t *testing.T) {
	ctx := context.Background()
	s, resolver, admin, _ := newAuthTestServer(t, ctx)
	invalidator := &mcpTestResolver{}
	s.SetUserResolver(invalidator)

	createBody := `{"name":"shared","type":"http","enabled":true,"url":"https://sys.example/mcp","headers":{"Authorization":"Bearer secret","X-Trace":"visible"}}`
	createReq := mcpAuthRequest(t, ctx, resolver, http.MethodPost, "/api/admin/mcp", admin.ID, createBody)
	createRR := httptest.NewRecorder()
	s.authMiddleware(s.handleCreateSystemMCPServer)(createRR, createReq)
	if createRR.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", createRR.Code, createRR.Body.String())
	}
	if invalidator.reloads != 1 {
		t.Fatalf("create should trigger ReloadAgents once, got %d", invalidator.reloads)
	}

	listReq := mcpAuthRequest(t, ctx, resolver, http.MethodGet, "/api/admin/mcp", admin.ID, "")
	listRR := httptest.NewRecorder()
	s.authMiddleware(s.handleListSystemMCPServers)(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", listRR.Code, listRR.Body.String())
	}
	var listResp struct {
		Servers []agentMCPServerOut `json:"servers"`
	}
	if err := json.Unmarshal(listRR.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.Servers) != 1 {
		t.Fatalf("want 1 system server, got %#v", listResp.Servers)
	}
	if got := listResp.Servers[0].Headers["Authorization"]; got == "Bearer secret" || got == "" {
		t.Fatalf("Authorization header should be masked, got %q", got)
	}

	updateBody := `{"name":"shared","type":"http","url":"https://sys.example/mcp2","headers":{"Authorization":"****","X-Trace":"changed"}}`
	updateReq := mcpAuthRequest(t, ctx, resolver, http.MethodPut, "/api/admin/mcp/shared", admin.ID, updateBody)
	updateReq.SetPathValue("name", "shared")
	updateRR := httptest.NewRecorder()
	s.authMiddleware(s.handleUpdateSystemMCPServer)(updateRR, updateReq)
	if updateRR.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", updateRR.Code, updateRR.Body.String())
	}
	rec, err := s.dataStore.GetConfigByName(ctx, store.KindMCP, "", "", "shared")
	if err != nil || rec == nil {
		t.Fatalf("read system MCP config: %v", err)
	}
	storedHeaders, _ := rec.Data["headers"].(map[string]any)
	if got := storedHeaders["Authorization"]; got != "Bearer secret" {
		t.Fatalf("masked update should preserve stored secret, got %#v", got)
	}
	if got := storedHeaders["X-Trace"]; got != "changed" {
		t.Fatalf("plain header should update, got %#v", got)
	}

	deleteReq := mcpAuthRequest(t, ctx, resolver, http.MethodDelete, "/api/admin/mcp/shared", admin.ID, "")
	deleteReq.SetPathValue("name", "shared")
	deleteRR := httptest.NewRecorder()
	s.authMiddleware(s.handleDeleteSystemMCPServer)(deleteRR, deleteReq)
	if deleteRR.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body=%s", deleteRR.Code, deleteRR.Body.String())
	}
	if invalidator.reloads != 3 {
		t.Fatalf("create+update+delete should trigger ReloadAgents 3 times, got %d", invalidator.reloads)
	}
}

func TestSystemMCPServersNonAdminReadAllowedWriteForbidden(t *testing.T) {
	ctx := context.Background()
	s, resolver, admin, regular := newAuthTestServer(t, ctx)
	s.SetUserResolver(&mcpTestResolver{})

	// Seed one row as super_admin so the read has something to return.
	seedReq := mcpAuthRequest(t, ctx, resolver, http.MethodPost, "/api/admin/mcp", admin.ID, `{"name":"shared","type":"http","url":"https://sys.example/mcp"}`)
	seedRR := httptest.NewRecorder()
	s.authMiddleware(s.handleCreateSystemMCPServer)(seedRR, seedReq)
	if seedRR.Code != http.StatusOK {
		t.Fatalf("seed status = %d, body=%s", seedRR.Code, seedRR.Body.String())
	}

	// Regular user can read system scope.
	readReq := mcpAuthRequest(t, ctx, resolver, http.MethodGet, "/api/admin/mcp", regular.ID, "")
	readRR := httptest.NewRecorder()
	s.authMiddleware(s.handleListSystemMCPServers)(readRR, readReq)
	if readRR.Code != http.StatusOK {
		t.Fatalf("non-admin read status = %d, want 200, body=%s", readRR.Code, readRR.Body.String())
	}

	// Regular user cannot mutate system scope.
	writeCases := []struct {
		name    string
		method  string
		path    string
		mcpName string
		body    string
		handler http.HandlerFunc
	}{
		{name: "create", method: http.MethodPost, path: "/api/admin/mcp", body: `{"name":"x","type":"http","url":"https://sys.example/mcp"}`, handler: s.handleCreateSystemMCPServer},
		{name: "update", method: http.MethodPut, path: "/api/admin/mcp/shared", mcpName: "shared", body: `{"name":"shared","type":"http","url":"https://sys.example/mcp"}`, handler: s.handleUpdateSystemMCPServer},
		{name: "delete", method: http.MethodDelete, path: "/api/admin/mcp/shared", mcpName: "shared", handler: s.handleDeleteSystemMCPServer},
	}
	for _, tc := range writeCases {
		t.Run(tc.name, func(t *testing.T) {
			req := mcpAuthRequest(t, ctx, resolver, tc.method, tc.path, regular.ID, tc.body)
			if tc.mcpName != "" {
				req.SetPathValue("name", tc.mcpName)
			}
			rr := httptest.NewRecorder()
			s.authMiddleware(tc.handler)(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403, body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestAgentMCPTestConnectionUsesStoredSecretForMaskedHeader(t *testing.T) {
	ctx := context.Background()
	s, resolver, _, owner := newAuthTestServer(t, ctx)
	s.SetUserResolver(&mcpTestResolver{})
	agentID := createMCPTestAgent(t, ctx, s.dataStore, owner.ID)

	gotAuth := make(chan string, 4)
	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "initialize" {
			gotAuth <- r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"a"}]}}`))
	}))
	defer mcpServer.Close()

	// Store a server with a real secret.
	createBody := `{"name":"svc","type":"http","url":"` + mcpServer.URL + `","headers":{"Authorization":"Bearer real-secret"}}`
	createReq := mcpAuthRequest(t, ctx, resolver, http.MethodPost, "/api/agents/"+agentID+"/mcp", owner.ID, createBody)
	createReq.SetPathValue("id", agentID)
	createRR := httptest.NewRecorder()
	s.authMiddleware(s.handleCreateAgentMCPServer)(createRR, createReq)
	if createRR.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", createRR.Code, createRR.Body.String())
	}

	// Test with a masked Authorization value — the handler must substitute
	// the stored secret before connecting.
	testBody := `{"name":"svc","type":"http","url":"` + mcpServer.URL + `","headers":{"Authorization":"****"}}`
	testReq := mcpAuthRequest(t, ctx, resolver, http.MethodPost, "/api/agents/"+agentID+"/mcp/test", owner.ID, testBody)
	testReq.SetPathValue("id", agentID)
	testRR := httptest.NewRecorder()
	s.authMiddleware(s.handleTestAgentMCPServer)(testRR, testReq)
	if testRR.Code != http.StatusOK {
		t.Fatalf("test status = %d, body=%s", testRR.Code, testRR.Body.String())
	}
	var res struct {
		OK        bool   `json:"ok"`
		ToolCount int    `json:"toolCount"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(testRR.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode test result: %v", err)
	}
	if !res.OK || res.ToolCount != 1 {
		t.Fatalf("want ok with 1 tool, got %#v", res)
	}

	// The most recent initialize must have carried the stored secret, not
	// the masked placeholder.
	var lastAuth string
	for len(gotAuth) > 0 {
		lastAuth = <-gotAuth
	}
	if lastAuth != "Bearer real-secret" {
		t.Fatalf("test should use stored secret, server saw %q", lastAuth)
	}
}

func TestSystemMCPTestConnectionRejectsStdio(t *testing.T) {
	ctx := context.Background()
	s, resolver, admin, _ := newAuthTestServer(t, ctx)
	s.SetUserResolver(&mcpTestResolver{})

	body := `{"name":"local","type":"stdio","command":"npx","args":["-y","x"]}`
	req := mcpAuthRequest(t, ctx, resolver, http.MethodPost, "/api/admin/mcp/test", admin.ID, body)
	rr := httptest.NewRecorder()
	s.authMiddleware(s.handleTestSystemMCPServer)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var res struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.OK || res.Error == "" {
		t.Fatalf("stdio test should fail with a message, got %#v", res)
	}
}

func createMCPTestAgent(t *testing.T, ctx context.Context, st store.Store, userID string) string {
	t.Helper()
	agentID := "agt_mcp_test"
	if err := st.SaveAgent(ctx, &store.AgentRecord{ID: agentID, UserID: userID, Name: "MCP Test"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	return agentID
}

func mcpAuthRequest(t *testing.T, ctx context.Context, resolver interface {
	IssueSession(context.Context, string) (*http.Cookie, error)
}, method, path, userID, body string) *http.Request {
	t.Helper()
	reader := bytes.NewReader([]byte(body))
	cookie, err := resolver.IssueSession(ctx, userID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(cookie)
	return req
}

type mcpTestResolver struct {
	agents  []string
	reloads int
}

func (r *mcpTestResolver) UserSpaceFor(string) (*api.UserSpaceView, error) { return nil, nil }
func (r *mcpTestResolver) LocalAgentManager() *agent.Manager               { return nil }
func (r *mcpTestResolver) IsCloudMode() bool                               { return false }
func (r *mcpTestResolver) InvalidateAgent(agentID string) {
	r.agents = append(r.agents, agentID)
}
func (r *mcpTestResolver) ReloadAgents() error {
	r.reloads++
	return nil
}
