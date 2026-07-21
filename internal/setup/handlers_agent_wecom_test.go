package setup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/api"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

type weComChannelResolverProbe struct {
	registered   []store.ChannelRecord
	unregistered [][2]string
	registerErr  error
}

func (p *weComChannelResolverProbe) UserSpaceFor(string) (*api.UserSpaceView, error) {
	return nil, errors.New("not used")
}
func (p *weComChannelResolverProbe) LocalAgentManager() *agent.Manager { return nil }
func (p *weComChannelResolverProbe) IsCloudMode() bool                 { return false }
func (p *weComChannelResolverProbe) RegisterChannel(rec store.ChannelRecord) error {
	p.registered = append(p.registered, rec)
	return p.registerErr
}
func (p *weComChannelResolverProbe) UnregisterChannel(channelType, accountID string) {
	p.unregistered = append(p.unregistered, [2]string{channelType, accountID})
}

func TestConnectAgentWeComValidatesThenPersistsMaskedSecret(t *testing.T) {
	ctx := context.Background()
	s, _, _, owner := newAuthTestServer(t, ctx)
	saveWeComTestAgent(t, s.dataStore, "agt_wecom", owner.ID)
	probe := &weComChannelResolverProbe{}
	s.SetUserResolver(probe)
	validated := false
	s.weComValidateCredentials = func(_ context.Context, botID, secret string) error {
		validated = true
		if botID != "bot-1" || secret != "fixture-secret" {
			t.Fatalf("validator got botID=%q secret=%q", botID, secret)
		}
		return nil
	}

	rr := callAgentChannelHandler(t, s.handleConnectAgentWeCom, owner, http.MethodPost, "agt_wecom", nil,
		`{"botId":"bot-1","secret":"fixture-secret"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("connect status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !validated {
		t.Fatal("credentials were not validated")
	}
	if len(probe.registered) != 1 || probe.registered[0].AccountID != "bot-1" {
		t.Fatalf("hot registrations = %#v", probe.registered)
	}
	stored, err := s.dataStore.LookupChannel(ctx, "wecom", "bot-1")
	if err != nil {
		t.Fatalf("LookupChannel: %v", err)
	}
	if stored.BotToken != "fixture-secret" {
		t.Fatalf("stored secret = %q", stored.BotToken)
	}
	if stored.SharedIdentity {
		t.Fatal("WeCom channel persisted sharedIdentity=true")
	}

	listRR := callAgentChannelHandler(t, s.handleListAgentChannels, owner, http.MethodGet, "agt_wecom", nil, "")
	if listRR.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRR.Code, listRR.Body.String())
	}
	var list struct {
		Channels []channelOut `json:"channels"`
	}
	if err := json.Unmarshal(listRR.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Channels) != 1 {
		t.Fatalf("channels = %#v", list.Channels)
	}
	if list.Channels[0].BotToken == "fixture-secret" || list.Channels[0].BotToken != "fixt****cret" {
		t.Fatalf("listed secret = %q", list.Channels[0].BotToken)
	}
}

func TestConnectAgentWeComRejectsEmptyCredentials(t *testing.T) {
	ctx := context.Background()
	s, _, _, owner := newAuthTestServer(t, ctx)
	saveWeComTestAgent(t, s.dataStore, "agt_wecom_empty", owner.ID)

	for name, body := range map[string]string{
		"bot ID": `{"botId":"","secret":"fixture-secret"}`,
		"secret": `{"botId":"bot-1","secret":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			rr := callAgentChannelHandler(t, s.handleConnectAgentWeCom, owner, http.MethodPost, "agt_wecom_empty", nil, body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestConnectAgentWeComInvalidCredentialsDoNotPersist(t *testing.T) {
	ctx := context.Background()
	s, _, _, owner := newAuthTestServer(t, ctx)
	saveWeComTestAgent(t, s.dataStore, "agt_wecom_invalid", owner.ID)
	s.weComValidateCredentials = func(context.Context, string, string) error {
		return errors.New("credentials rejected")
	}

	rr := callAgentChannelHandler(t, s.handleConnectAgentWeCom, owner, http.MethodPost, "agt_wecom_invalid", nil,
		`{"botId":"bot-invalid","secret":"fixture-secret"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if _, err := s.dataStore.LookupChannel(ctx, "wecom", "bot-invalid"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("invalid credentials persisted: %v", err)
	}
}

func TestConnectAgentWeComRejectsDuplicateBotID(t *testing.T) {
	ctx := context.Background()
	s, _, _, owner := newAuthTestServer(t, ctx)
	saveWeComTestAgent(t, s.dataStore, "agt_wecom_first", owner.ID)
	other := createAuthTestUser(t, ctx, s.accounts, "wecom-other", users.RoleUser)
	saveWeComTestAgent(t, s.dataStore, "agt_wecom_second", other.ID)
	saveWeComTestChannel(t, s.dataStore, owner.ID, "agt_wecom_first", "bot-duplicate", "first-secret")
	s.weComValidateCredentials = func(context.Context, string, string) error { return nil }

	rr := callAgentChannelHandler(t, s.handleConnectAgentWeCom, other, http.MethodPost, "agt_wecom_second", nil,
		`{"botId":"bot-duplicate","secret":"second-secret"}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	stored, err := s.dataStore.LookupChannel(ctx, "wecom", "bot-duplicate")
	if err != nil {
		t.Fatalf("LookupChannel: %v", err)
	}
	if stored.UserID != owner.ID || stored.BotToken != "first-secret" {
		t.Fatalf("duplicate overwrote existing row: %#v", stored)
	}
}

func TestConnectAgentWeComForcesSharedIdentityFalse(t *testing.T) {
	ctx := context.Background()
	s, _, _, owner := newAuthTestServer(t, ctx)
	saveWeComTestAgent(t, s.dataStore, "agt_wecom_shared", owner.ID)
	s.weComValidateCredentials = func(context.Context, string, string) error { return nil }

	rr := callAgentChannelHandler(t, s.handleConnectAgentWeCom, owner, http.MethodPost, "agt_wecom_shared", nil,
		`{"botId":"bot-shared","secret":"fixture-secret","sharedIdentity":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	stored, err := s.dataStore.LookupChannel(ctx, "wecom", "bot-shared")
	if err != nil {
		t.Fatalf("LookupChannel: %v", err)
	}
	if stored.SharedIdentity {
		t.Fatal("sharedIdentity must remain false for WeCom")
	}
}

func TestUpdateAgentWeComRejectsSharedIdentityTrue(t *testing.T) {
	ctx := context.Background()
	s, _, _, owner := newAuthTestServer(t, ctx)
	saveWeComTestAgent(t, s.dataStore, "agt_wecom_update", owner.ID)
	saveWeComTestChannel(t, s.dataStore, owner.ID, "agt_wecom_update", "bot-update", "fixture-secret")

	rr := callAgentChannelHandler(t, s.handleUpdateAgentChannel, owner, http.MethodPatch, "agt_wecom_update",
		map[string]string{"type": "wecom", "accountId": "bot-update"}, `{"sharedIdentity":true}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	stored, err := s.dataStore.LookupChannel(ctx, "wecom", "bot-update")
	if err != nil {
		t.Fatalf("LookupChannel: %v", err)
	}
	if stored.SharedIdentity {
		t.Fatal("rejected update changed sharedIdentity")
	}
}

func TestDisconnectAgentWeComUnregistersRunningAdapter(t *testing.T) {
	ctx := context.Background()
	s, _, _, owner := newAuthTestServer(t, ctx)
	saveWeComTestAgent(t, s.dataStore, "agt_wecom_disconnect", owner.ID)
	saveWeComTestChannel(t, s.dataStore, owner.ID, "agt_wecom_disconnect", "bot-disconnect", "fixture-secret")
	probe := &weComChannelResolverProbe{}
	s.SetUserResolver(probe)

	rr := callAgentChannelHandler(t, s.handleDisconnectAgentChannel, owner, http.MethodDelete, "agt_wecom_disconnect",
		map[string]string{"type": "wecom", "accountId": "bot-disconnect"}, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if len(probe.unregistered) != 1 || probe.unregistered[0] != [2]string{"wecom", "bot-disconnect"} {
		t.Fatalf("unregistered = %#v", probe.unregistered)
	}
	if _, err := s.dataStore.LookupChannel(ctx, "wecom", "bot-disconnect"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("disconnected row still exists: %v", err)
	}
}

func TestConnectAgentWeComRollsBackWhenHotStartFails(t *testing.T) {
	ctx := context.Background()
	s, _, _, owner := newAuthTestServer(t, ctx)
	saveWeComTestAgent(t, s.dataStore, "agt_wecom_hot_fail", owner.ID)
	s.weComValidateCredentials = func(context.Context, string, string) error { return nil }
	s.SetUserResolver(&weComChannelResolverProbe{registerErr: errors.New("socket start failed")})

	rr := callAgentChannelHandler(t, s.handleConnectAgentWeCom, owner, http.MethodPost, "agt_wecom_hot_fail", nil,
		`{"botId":"bot-hot-fail","secret":"fixture-secret"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if _, err := s.dataStore.LookupChannel(ctx, "wecom", "bot-hot-fail"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed hot-start left persisted row: %v", err)
	}
}

func saveWeComTestAgent(t *testing.T, st store.Store, agentID, ownerID string) {
	t.Helper()
	if err := st.SaveAgent(context.Background(), &store.AgentRecord{ID: agentID, UserID: ownerID, Name: "WeCom Agent"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
}

func saveWeComTestChannel(t *testing.T, st store.Store, userID, agentID, botID, secret string) {
	t.Helper()
	cc := config.ChannelConfig{
		Enabled: true,
		Accounts: map[string]config.AccountConfig{
			botID: {BotToken: secret},
		},
	}
	if err := st.SaveChannel(context.Background(), &store.ChannelRecord{
		UserID: userID, AgentID: agentID, Type: "wecom", AccountID: botID,
		Enabled: true, BotToken: secret, SharedIdentity: false, Data: channelConfigToData(cc),
	}); err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}
}

func callAgentChannelHandler(t *testing.T, handler http.HandlerFunc, user *users.Account, method, agentID string, pathValues map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/agents/"+agentID+"/channels", strings.NewReader(body))
	req.SetPathValue("id", agentID)
	for key, value := range pathValues {
		req.SetPathValue(key, value)
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{
		UserID: user.ID, Role: user.Role, AuthMethod: "session",
	}))
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}
