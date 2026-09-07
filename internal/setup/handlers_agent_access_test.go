package setup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

func TestPrivateAgentReadAccess(t *testing.T) {
	ctx := context.Background()
	s, resolver, adminUser, owner := newAuthTestServer(t, ctx)
	otherUser := createAuthTestUser(t, ctx, s.accounts, "other", users.RoleUser)
	agent := &store.AgentRecord{
		ID:       "agt_private_access_test",
		UserID:   owner.ID,
		Name:     "Private Agent",
		IsPublic: false,
	}
	if err := s.dataStore.SaveAgent(ctx, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	handler := s.authMiddleware(s.handleGetAgent)
	request := func(t *testing.T, userID, query string) *httptest.ResponseRecorder {
		t.Helper()
		path := "/api/agents/" + agent.ID + query
		req := authTestRequest(t, ctx, resolver, http.MethodGet, path, userID)
		req.SetPathValue("id", agent.ID)
		rr := httptest.NewRecorder()
		handler(rr, req)
		return rr
	}

	t.Run("owner can open private agent", func(t *testing.T) {
		rr := request(t, owner.ID, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
	})

	t.Run("another user cannot open private agent", func(t *testing.T) {
		rr := request(t, otherUser.ID, "")
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusForbidden, rr.Body.String())
		}
	})

	t.Run("super admin cannot directly open private agent", func(t *testing.T) {
		rr := request(t, adminUser.ID, "")
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusForbidden, rr.Body.String())
		}
	})

	t.Run("super admin can audit with actAs as a viewer", func(t *testing.T) {
		rr := request(t, adminUser.ID, "?actAs="+owner.ID)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		var response struct {
			Agent struct {
				Role string `json:"role"`
			} `json:"agent"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if response.Agent.Role != "viewer" {
			t.Fatalf("role = %q, want viewer", response.Agent.Role)
		}
	})

	agent.IsPublic = true
	if err := s.dataStore.SaveAgent(ctx, agent); err != nil {
		t.Fatalf("make agent public: %v", err)
	}

	t.Run("another user can open public agent", func(t *testing.T) {
		rr := request(t, otherUser.ID, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
	})

	t.Run("super admin can directly open public agent", func(t *testing.T) {
		rr := request(t, adminUser.ID, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
	})
}

func TestActAsChatMutationIsRejected(t *testing.T) {
	ctx := context.Background()
	s, resolver, adminUser, owner := newAuthTestServer(t, ctx)

	req := authTestRequest(t, ctx, resolver, http.MethodPost, "/api/chat?actAs="+owner.ID, adminUser.ID)
	rr := httptest.NewRecorder()
	s.authMiddleware(s.handleChat)(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusForbidden, rr.Body.String())
	}
}
