package usermcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeOAuthMCP is an MCP server behind OAuth (MCP authorization spec):
// 401 + resource_metadata, protected-resource and authorization-server
// metadata, dynamic client registration, and a PKCE token endpoint.
type fakeOAuthMCP struct {
	*httptest.Server
	mu        sync.Mutex
	access    string // the access token the MCP endpoint accepts
	refresh   string
	expiresIn int
	verifier  map[string]string // code → its PKCE challenge
	refreshes int
	refuseRef bool // refresh answers invalid_grant
}

func newFakeOAuthMCP(t *testing.T) *fakeOAuthMCP {
	t.Helper()
	f := &fakeOAuthMCP{verifier: map[string]string{}, expiresIn: 3600}
	mux := http.NewServeMux()
	f.Server = httptest.NewTLSServer(mux)
	t.Cleanup(f.Close)
	base := f.URL
	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token",
			"registration_endpoint": base + "/register", "code_challenge_methods_supported": []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none"},
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 201, map[string]any{"client_id": "client-1"})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Form.Get("client_id") != "client-1" || r.Form.Get("resource") != base+"/mcp" {
			writeJSON(w, 400, map[string]any{"error": "invalid_client"})
			return
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			want, ok := f.verifier[r.Form.Get("code")]
			delete(f.verifier, r.Form.Get("code"))
			if !ok || pkceChallenge(r.Form.Get("code_verifier")) != want {
				writeJSON(w, 400, map[string]any{"error": "invalid_grant"})
				return
			}
		case "refresh_token":
			f.refreshes++
			if f.refuseRef || r.Form.Get("refresh_token") != f.refresh {
				writeJSON(w, 400, map[string]any{"error": "invalid_grant"})
				return
			}
		default:
			writeJSON(w, 400, map[string]any{"error": "unsupported_grant_type"})
			return
		}
		f.access = randomToken(8)
		f.refresh = randomToken(8)
		writeJSON(w, 200, map[string]any{"access_token": f.access, "refresh_token": f.refresh, "expires_in": f.expiresIn, "token_type": "Bearer"})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		ok := f.access != "" && r.Header.Get("Authorization") == "Bearer "+f.access
		f.mu.Unlock()
		if !ok {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp", scope="read"`, base))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any = map[string]any{}
		switch req.Method {
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "search", "annotations": map[string]any{"readOnlyHint": true}}}}
		case "tools/call":
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "found"}}}
		}
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	})
	return f
}

// authorize plays the person approving: it checks the authorization URL
// and returns a code for it.
func (f *fakeOAuthMCP) authorize(t *testing.T, authURL, redirect string) (code, state string) {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil || !strings.HasPrefix(authURL, f.URL+"/authorize?") {
		t.Fatalf("authorize url = %s", authURL)
	}
	q := u.Query()
	if q.Get("client_id") != "client-1" || q.Get("redirect_uri") != redirect || q.Get("code_challenge_method") != "S256" ||
		q.Get("scope") != "read" || q.Get("resource") != f.URL+"/mcp" || q.Get("state") == "" {
		t.Fatalf("authorize query = %v", q)
	}
	code = randomToken(8)
	f.mu.Lock()
	f.verifier[code] = q.Get("code_challenge")
	f.mu.Unlock()
	return code, q.Get("state")
}

func TestOAuthFlow(t *testing.T) {
	f := newFakeOAuthMCP(t)
	svc := newTestService(t, f.Server)
	ctx := context.Background()
	mcpURL := f.URL + "/mcp"
	redirect := "https://app.example/api/connectors/mcp/oauth/callback"

	// Adding it plainly says it needs OAuth.
	if _, err := svc.Add(ctx, "u1", "Galactic", mcpURL, nil); Code(err) != "oauth_required" {
		t.Fatalf("plain add: %v", err)
	}
	if _, err := svc.StartOAuth(ctx, "u1", OAuthStart{Name: "G", URL: mcpURL, RedirectURI: "javascript:alert(1)"}); Code(err) != "invalid_redirect" {
		t.Fatalf("bad redirect: %v", err)
	}

	authURL, err := svc.StartOAuth(ctx, "u1", OAuthStart{Name: "Galactic", URL: mcpURL, RedirectURI: redirect, ClientName: "WeClaw"})
	if err != nil {
		t.Fatal(err)
	}
	code, state := f.authorize(t, authURL, redirect)

	// Someone else can't finish it.
	if _, err := svc.FinishOAuth(ctx, "u2", state, code); Code(err) != "oauth_expired" {
		t.Fatalf("other user finished: %v", err)
	}
	srv, err := svc.FinishOAuth(ctx, "u1", state, code)
	if err != nil {
		t.Fatal(err)
	}
	if srv.Auth() != "oauth" || srv.ToolCount != 1 || srv.NeedsReauth() {
		t.Fatalf("saved = %+v", srv)
	}
	// A state works once.
	if _, err := svc.FinishOAuth(ctx, "u1", state, code); Code(err) != "oauth_expired" {
		t.Fatalf("state reused: %v", err)
	}

	list, _ := svc.List(ctx, "u1")
	if len(list) != 1 || list[0].OAuth == nil || list[0].OAuth.AccessToken == "" || list[0].OAuth.ClientID != "client-1" {
		t.Fatalf("list = %+v", list)
	}
	if out, err := svc.Call(ctx, list[0], "search", nil, nil); err != nil || out != "found" {
		t.Fatalf("call: %q %v", out, err)
	}

	// The server stops accepting the token: one refresh, then it works.
	f.mu.Lock()
	f.access = "rotated-elsewhere"
	f.mu.Unlock()
	svc.mu.Lock()
	svc.tools = map[string]toolsEntry{}
	svc.mu.Unlock()
	if out, err := svc.Call(ctx, list[0], "search", nil, nil); err != nil || out != "found" {
		t.Fatalf("call after refresh: %q %v", out, err)
	}
	if f.refreshes != 1 {
		t.Fatalf("refreshes = %d", f.refreshes)
	}
	// The refreshed tokens are saved.
	list, _ = svc.List(ctx, "u1")
	f.mu.Lock()
	saved := list[0].OAuth.AccessToken == f.access && list[0].OAuth.RefreshToken == f.refresh
	f.mu.Unlock()
	if !saved {
		t.Fatalf("refreshed tokens not saved: %+v", list[0].OAuth)
	}

	// An expiring token refreshes before use.
	svc.forgetToken(list[0].ID)
	list[0].OAuth.ExpiresAt = time.Now().UnixMilli()
	_ = svc.st.setOAuth(ctx, list[0].ID, list[0].OAuth)
	list, _ = svc.List(ctx, "u1")
	if _, err := svc.headersFor(ctx, list[0], false); err != nil || f.refreshes != 2 {
		t.Fatalf("expiring: %v refreshes=%d", err, f.refreshes)
	}

	// Refresh refused: the server needs a new authorization.
	f.mu.Lock()
	f.access = "gone"
	f.refuseRef = true
	f.mu.Unlock()
	svc.mu.Lock()
	svc.tools = map[string]toolsEntry{}
	svc.mu.Unlock()
	list, _ = svc.List(ctx, "u1")
	if _, err := svc.Call(ctx, list[0], "search", nil, nil); Code(err) != "reauth_required" {
		t.Fatalf("broken refresh: %v", err)
	}
	list, _ = svc.List(ctx, "u1")
	if !list[0].NeedsReauth() {
		t.Fatalf("not marked broken: %+v", list[0].OAuth)
	}

	// Re-authorizing the same server fixes it, and keeps one server.
	f.mu.Lock()
	f.refuseRef = false
	f.mu.Unlock()
	authURL, err = svc.StartOAuth(ctx, "u1", OAuthStart{ServerID: list[0].ID, RedirectURI: redirect})
	if err != nil {
		t.Fatal(err)
	}
	code, state = f.authorize(t, authURL, redirect)
	if _, err := svc.FinishOAuth(ctx, "u1", state, code); err != nil {
		t.Fatal(err)
	}
	list, _ = svc.List(ctx, "u1")
	if len(list) != 1 || list[0].NeedsReauth() {
		t.Fatalf("after reauth: %+v", list)
	}
	if out, err := svc.Call(ctx, list[0], "search", nil, nil); err != nil || out != "found" {
		t.Fatalf("call after reauth: %q %v", out, err)
	}

	// An OAuth server can be renamed but not moved.
	if _, err := svc.Update(ctx, "u1", list[0].ID, "Renamed", mcpURL, nil); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := svc.Update(ctx, "u1", list[0].ID, "Renamed", f.URL+"/other", nil); Code(err) != "invalid_url" {
		t.Fatalf("move: %v", err)
	}
	// u2 can't re-authorize u1's server.
	if _, err := svc.StartOAuth(ctx, "u2", OAuthStart{ServerID: list[0].ID, RedirectURI: redirect}); Code(err) != "not_found" {
		t.Fatalf("u2 reauth: %v", err)
	}
}

func TestDiscoverPlainServer(t *testing.T) {
	ts := fakeMCP(t, false, new(atomic.Int32))
	svc := newTestService(t, ts)
	// A 401 without an OAuth challenge stays "unauthorized".
	if _, err := svc.Add(context.Background(), "u1", "A", ts.URL, nil); Code(err) != "unauthorized" {
		t.Fatalf("plain 401: %v", err)
	}
}
