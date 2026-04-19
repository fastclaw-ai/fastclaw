package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// fakeResolver returns a pre-baked set of UserSpaceView objects so we can
// test the auth + routing path without booting a full gateway.
type fakeResolver struct {
	spaces map[string]*UserSpaceView
}

func (f *fakeResolver) UserSpaceFor(userID string) (*UserSpaceView, error) {
	if sp, ok := f.spaces[userID]; ok {
		return sp, nil
	}
	return nil, errors.New("user space not found: " + userID)
}

func (f *fakeResolver) LocalAgentManager() *agent.Manager {
	if sp, ok := f.spaces[config.DefaultUserID]; ok {
		return sp.Agents
	}
	return nil
}

func (f *fakeResolver) IsCloudMode() bool { return true }

// seenContextKey records the user ID observed inside the handler so tests
// can assert that middleware injected the right value.
type observed struct {
	userID string
}

func newTestServer(t *testing.T, token string, reg *users.Registry) (*Server, *observed) {
	t.Helper()
	obs := &observed{}
	resolver := &fakeResolver{
		spaces: map[string]*UserSpaceView{
			config.DefaultUserID: {UserID: config.DefaultUserID},
			"alice":              {UserID: "alice"},
			"bob":                {UserID: "bob"},
		},
	}
	srv := NewServer(resolver, token, reg, &config.GatewayCfg{
		Auth: config.GatewayAuth{Mode: "token"},
	})
	// Install a probe handler that records UserIDFromContext.
	// We wrap it in authMiddleware directly since RegisterRoutes pins the
	// public endpoints.
	_ = srv
	return srv, obs
}

func runThroughMiddleware(srv *Server, obs *observed, auth string) *httptest.ResponseRecorder {
	handler := srv.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		obs.userID = config.UserIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest("GET", "/v1/agents", nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	handler(w, req)
	return w
}

func TestAuthMiddleware_LocalToken(t *testing.T) {
	srv, obs := newTestServer(t, "secret-token", nil)

	// Valid local token → DefaultUserID
	resp := runThroughMiddleware(srv, obs, "Bearer secret-token")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
	if obs.userID != config.DefaultUserID {
		t.Fatalf("expected userID=%q, got %q", config.DefaultUserID, obs.userID)
	}

	// Wrong token → 401
	resp = runThroughMiddleware(srv, obs, "Bearer wrong")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.Code)
	}

	// Missing header → 401
	resp = runThroughMiddleware(srv, obs, "")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.Code)
	}
}

func TestAuthMiddleware_CloudRegistry(t *testing.T) {
	tmp := t.TempDir()
	reg, err := users.LoadFrom(fmt.Sprintf("%s/users.json", tmp))
	if err != nil {
		t.Fatal(err)
	}
	_, aliceTok, err := reg.Add("alice", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	_, bobTok, err := reg.Add("bob", "Bob")
	if err != nil {
		t.Fatal(err)
	}

	srv, obs := newTestServer(t, "", reg)

	// Alice's token → userID=alice
	resp := runThroughMiddleware(srv, obs, "Bearer "+aliceTok)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", resp.Code, resp.Body.String())
	}
	if obs.userID != "alice" {
		t.Fatalf("expected alice, got %q", obs.userID)
	}

	// Bob's token → userID=bob (isolation)
	resp = runThroughMiddleware(srv, obs, "Bearer "+bobTok)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 for bob, got %d", resp.Code)
	}
	if obs.userID != "bob" {
		t.Fatalf("expected bob, got %q", obs.userID)
	}

	// Unknown token → 401
	resp = runThroughMiddleware(srv, obs, "Bearer fc_unknown")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unknown token, got %d", resp.Code)
	}
}

func TestAuthMiddleware_NoAuth(t *testing.T) {
	// When no token and no registry are configured, every request is
	// treated as the local user (single-user install with auth=none).
	resolver := &fakeResolver{
		spaces: map[string]*UserSpaceView{
			config.DefaultUserID: {UserID: config.DefaultUserID},
		},
	}
	srv := NewServer(resolver, "", nil, &config.GatewayCfg{})
	obs := &observed{}
	resp := runThroughMiddleware(srv, obs, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
	if obs.userID != config.DefaultUserID {
		t.Fatalf("expected local, got %q", obs.userID)
	}
}
