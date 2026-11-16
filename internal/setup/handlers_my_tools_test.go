package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/api"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/gateway"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

type invalidated struct {
	api.UserResolver
	accounts []string
}

func (i *invalidated) InvalidateAccount(uid string) { i.accounts = append(i.accounts, uid) }

// Console → Tools: a user's own tool layer. It never returns keys (theirs
// or the deployment's), keeps a saved key when the form sends none, drops
// categories they no longer override, and refuses private endpoints.
func TestMyToolsReadsAndWritesOnlyTheUsersLayer(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewDBStore("sqlite", "file:"+filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_ = scope.SaveSetting(ctx, st, "", "", gateway.NSToolProviders, map[string]any{
		"searxng": map[string]any{"endpoint": "http://searxng-svc.local:8080"},
		"openai":  map[string]any{"apiKey": "sk-system-secret"},
	})
	_ = scope.SaveSetting(ctx, st, "", "", gateway.NSToolCategories, map[string]any{
		"web_search": map[string]any{"primary": "searxng/default"},
		"tts":        map[string]any{"primary": "openai/tts-1"},
		"web_fetch":  map[string]any{"primary": "jina/default"}, // credential-free
	})
	inv := &invalidated{}
	s := &Server{dataStore: st, userResolver: inv}
	id := auth.Identity{UserID: "u_acct", Role: users.RoleUser, AuthMethod: "session"}
	call := func(method string, body any) (int, map[string]any) {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, "/api/my-tools", &buf)
		req = req.WithContext(auth.WithIdentity(req.Context(), id))
		rec := httptest.NewRecorder()
		if method == http.MethodGet {
			s.handleGetMyTools(rec, req)
		} else {
			s.handleSaveMyTools(rec, req)
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, out := call(http.MethodGet, nil)
	if code != 200 {
		t.Fatalf("get: %d %v", code, out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "sk-system-secret") || strings.Contains(string(raw), "searxng-svc") {
		t.Fatalf("the deployment's key or endpoint leaked: %s", raw)
	}
	inh := out["inherited"].(map[string]any)
	if _, ok := inh["web_search"]; !ok {
		t.Fatalf("inherited = %v", inh)
	}
	if len(out["tools"].(map[string]any)) != 0 {
		t.Fatalf("own tools = %v, want none", out["tools"])
	}

	// Save an image_gen of their own.
	code, out = call(http.MethodPut, map[string]any{
		"toolProviders": map[string]any{"fal": map[string]any{"apiKey": "fal_user_key", "options": map[string]string{"model": "fal-ai/qwen-image"}}},
		"tools":         map[string]any{"image_gen": map[string]any{"primary": "fal/fal-ai/qwen-image"}},
	})
	if code != 200 {
		t.Fatalf("put: %d %v", code, out)
	}
	if len(inv.accounts) != 1 || inv.accounts[0] != "u_acct" {
		t.Fatalf("invalidated %v", inv.accounts)
	}
	rec, _ := st.GetConfigByName(ctx, store.KindSetting, "u_acct", "", gateway.NSToolProviders)
	if rec == nil || rec.Data["fal"].(map[string]any)["apiKey"] != "fal_user_key" {
		t.Fatalf("user row = %+v", rec)
	}
	if sys, _ := st.GetConfigByName(ctx, store.KindSetting, "", "", gateway.NSToolProviders); sys.Data["fal"] != nil {
		t.Fatal("the system layer was written")
	}
	_, out = call(http.MethodGet, nil)
	raw, _ = json.Marshal(out)
	if strings.Contains(string(raw), "fal_user_key") {
		t.Fatalf("the user's key came back: %s", raw)
	}
	if !out["toolProviders"].(map[string]any)["fal"].(map[string]any)["apiKeySet"].(bool) {
		t.Fatalf("apiKeySet missing: %v", out["toolProviders"])
	}

	// Saving again without the key keeps it; a category left out inherits.
	if code, out := call(http.MethodPut, map[string]any{
		"toolProviders": map[string]any{"fal": map[string]any{"options": map[string]string{"model": "flux-dev"}}},
		"tools":         map[string]any{},
	}); code != 200 {
		t.Fatalf("put 2: %d %v", code, out)
	}
	rec, _ = st.GetConfigByName(ctx, store.KindSetting, "u_acct", "", gateway.NSToolProviders)
	if rec.Data["fal"].(map[string]any)["apiKey"] != "fal_user_key" {
		t.Fatalf("saved key lost: %+v", rec.Data)
	}
	if rec, _ := st.GetConfigByName(ctx, store.KindSetting, "u_acct", "", gateway.NSToolCategories); rec != nil && len(rec.Data) > 0 {
		t.Fatalf("categories row = %+v, want none", rec.Data)
	}

	// Endpoints the server would call: public https only, except the
	// deployment's own address for that provider.
	for _, bad := range []string{"http://10.0.0.5:8080", "https://127.0.0.1", "http://example.com"} {
		if code, _ := call(http.MethodPut, map[string]any{
			"toolProviders": map[string]any{"searxng": map[string]any{"endpoint": bad}},
		}); code != http.StatusBadRequest {
			t.Fatalf("%s accepted: %d", bad, code)
		}
	}
	if code, out := call(http.MethodPut, map[string]any{
		"toolProviders": map[string]any{"searxng": map[string]any{"endpoint": "http://searxng-svc.local:8080"}},
		"tools":         map[string]any{"web_search": map[string]any{"primary": "searxng/default"}},
	}); code != 200 {
		t.Fatalf("the deployment's own endpoint refused: %d %v", code, out)
	}

	// A provider that doesn't exist is refused.
	if code, _ := call(http.MethodPut, map[string]any{
		"tools": map[string]any{"image_gen": map[string]any{"primary": "nope/x"}},
	}); code != http.StatusBadRequest {
		t.Fatalf("unknown provider accepted: %d", code)
	}
}
