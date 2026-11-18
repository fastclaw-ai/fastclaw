package setup

import (
	"context"
	"encoding/json"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/usermodels"
	"github.com/fastclaw-ai/fastclaw/internal/users"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEndUserModels(t *testing.T) {
	s, _, _, _ := newAuthTestServer(t, context.Background())
	call := func(uid, method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/my-models", strings.NewReader(body))
		if uid != "" {
			r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{UserID: uid, Role: users.RoleAppUser, AuthMethod: "apikey", AppUserID: "owner", EndUser: "user:" + uid}))
		}
		w := httptest.NewRecorder()
		s.handleUserModels(w, r)
		return w
	}
	body := `{"providers":[{"id":"mine","name":"Mine","apiType":"openai-completions","apiBase":"https://api.example.com/v1","apiKey":"test-secret","models":["m"]}],"defaultModel":"mine/m"}`
	if w := call("alice", "PUT", body); w.Code != 400 {
		t.Fatalf("saved untested provider: %d %s", w.Code, w.Body)
	}
	var seeded usermodels.Config
	if err := json.Unmarshal([]byte(body), &seeded); err != nil {
		t.Fatal(err)
	}
	if err := usermodels.Save(context.Background(), s.dataStore, "alice", seeded); err != nil {
		t.Fatal(err)
	}
	body = strings.Replace(body, `"apiKey":"test-secret",`, "", 1)
	if w := call("alice", "PUT", body); w.Code != 200 || strings.Contains(w.Body.String(), "test-secret") {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if w := call("bob", "GET", ""); w.Code != 200 || strings.Contains(w.Body.String(), "Mine") {
		t.Fatalf("isolation: %d %s", w.Code, w.Body)
	}
	if w := call("", "GET", ""); w.Code != 401 {
		t.Fatal("unauthenticated models allowed")
	}
	if w := call("alice", "PUT", strings.Replace(body, `"defaultModel":"mine/m"`, `"defaultModel":"mine/m","userId":"bob"`, 1)); w.Code != 400 {
		t.Fatal("accepted caller supplied ownership")
	}
}
