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

	// Someone else can't finish it, nor can a link-open the app refuses.
	if _, err := svc.FinishOAuth(ctx, "u2", state, code, nil); Code(err) != "oauth_expired" {
		t.Fatalf("other user finished: %v", err)
	}
	if _, err := svc.FinishOAuth(ctx, "", state, code, nil); Code(err) != "unauthorized" {
		t.Fatalf("no accept: %v", err)
	}
	srv, err := svc.FinishOAuth(ctx, "u1", state, code, nil)
	if err != nil {
		t.Fatal(err)
	}
	if srv.Auth() != "oauth" || srv.ToolCount != 1 || svc.TokenStatus(ctx, srv, "u1") != "ok" {
		t.Fatalf("saved = %+v", srv)
	}
	if _, err := svc.FinishOAuth(ctx, "u1", state, code, nil); Code(err) != "oauth_expired" {
		t.Fatalf("state reused: %v", err)
	}
	// Saved for u1 alone; the row keeps the client, never the tokens.
	list, _ := svc.List(ctx, "u1")
	if len(list) != 1 || list[0].OAuth == nil || list[0].OAuth.ClientID != "client-1" || list[0].OAuth.AccessToken != "" {
		t.Fatalf("list = %+v", list)
	}
	if other, _ := svc.List(ctx, "u2"); len(other) != 0 {
		t.Fatalf("u2 sees %+v", other)
	}
	srv = list[0]
	if out, err := svc.Call(ctx, srv, "u1", "search", nil, nil); err != nil || out != "found" {
		t.Fatalf("call: %q %v", out, err)
	}
	// A second sign-in with the same name is refused.
	authURL, _ = svc.StartOAuth(ctx, "u1", OAuthStart{Name: "Other", URL: mcpURL, RedirectURI: redirect})
	if _, err := svc.StartOAuth(ctx, "u1", OAuthStart{Name: "galactic", URL: mcpURL, RedirectURI: redirect}); Code(err) != "name_taken" {
		t.Fatalf("dup name: %v", err)
	}
	_ = authURL

	// The server stops accepting the token: one refresh, then it works.
	f.mu.Lock()
	f.access = "rotated-elsewhere"
	f.mu.Unlock()
	svc.mu.Lock()
	svc.tools = map[string]toolsEntry{}
	svc.mu.Unlock()
	if out, err := svc.Call(ctx, srv, "u1", "search", nil, nil); err != nil || out != "found" {
		t.Fatalf("call after refresh: %q %v", out, err)
	}
	if f.refreshes != 1 {
		t.Fatalf("refreshes = %d", f.refreshes)
	}
	tok, _ := svc.st.getToken(ctx, srv.ID, "u1")
	f.mu.Lock()
	ok := tok != nil && tok.AccessToken == f.access && tok.RefreshToken == f.refresh
	f.mu.Unlock()
	if !ok {
		t.Fatalf("refreshed tokens not saved: %+v", tok)
	}

	// An expiring token refreshes before use.
	svc.forgetTokens(srv.ID)
	tok.ExpiresAt = time.Now().UnixMilli()
	_ = svc.st.putToken(ctx, srv.ID, "u1", tok)
	if _, err := svc.headersFor(ctx, srv, "u1", false); err != nil || f.refreshes != 2 {
		t.Fatalf("expiring: %v refreshes=%d", err, f.refreshes)
	}

	// Refresh refused: sign in again.
	f.mu.Lock()
	f.access = "gone"
	f.refuseRef = true
	f.mu.Unlock()
	svc.mu.Lock()
	svc.tools = map[string]toolsEntry{}
	svc.mu.Unlock()
	if _, err := svc.Call(ctx, srv, "u1", "search", nil, nil); Code(err) != "reauth_required" {
		t.Fatalf("broken refresh: %v", err)
	}
	if svc.TokenStatus(ctx, srv, "u1") != "broken" {
		t.Fatal("not marked broken")
	}
	f.mu.Lock()
	f.refuseRef = false
	f.mu.Unlock()
	authURL, err = svc.StartOAuth(ctx, "u1", OAuthStart{Server: &srv, RedirectURI: redirect})
	if err != nil {
		t.Fatal(err)
	}
	code, state = f.authorize(t, authURL, redirect)
	// Opened from a chat, not signed in: the app vouches for whoever started it.
	if _, err := svc.FinishOAuth(ctx, "", state, code, func(uid string) bool { return uid == "u1" }); err != nil {
		t.Fatal(err)
	}
	if list, _ := svc.List(ctx, "u1"); len(list) != 1 {
		t.Fatalf("after sign-in again: %+v", list)
	}
	if out, err := svc.Call(ctx, srv, "u1", "search", nil, nil); err != nil || out != "found" {
		t.Fatalf("call after sign-in: %q %v", out, err)
	}

	// An OAuth server can be renamed but not moved.
	if _, err := svc.Update(ctx, "u1", srv.ID, "Renamed", mcpURL, nil); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := svc.Update(ctx, "u1", srv.ID, "Renamed", f.URL+"/other", nil); Code(err) != "invalid_url" {
		t.Fatalf("move: %v", err)
	}
	// Deleting it takes the sign-in with it.
	if err := svc.Delete(ctx, "u1", srv.ID); err != nil {
		t.Fatal(err)
	}
	if tok, _ := svc.st.getToken(ctx, srv.ID, "u1"); tok != nil {
		t.Fatal("sign-in kept")
	}
}

func TestMoveOwnerTokens(t *testing.T) {
	f := newFakeOAuthMCP(t)
	svc := newTestService(t, f.Server)
	ctx := context.Background()
	// A server row as saved before sign-ins had their own table.
	old := &OAuth{TokenEndpoint: f.URL + "/token", ClientID: "client-1", AccessToken: "a", RefreshToken: "r"}
	srv := Server{ID: "mcp_old", Name: "Old", URL: f.URL + "/mcp", Headers: map[string]string{}, OAuth: old, CreatedAt: 1}
	if err := svc.st.insert(ctx, "owner", srv); err != nil {
		t.Fatal(err)
	}
	if err := svc.st.moveOwnerTokens(ctx); err != nil {
		t.Fatal(err)
	}
	row, _, _ := svc.Server(ctx, srv.ID)
	tok, _ := svc.st.getToken(ctx, srv.ID, "owner")
	if row.OAuth.AccessToken != "" || row.OAuth.ClientID != "client-1" || tok == nil || tok.AccessToken != "a" {
		t.Fatalf("row=%+v tok=%+v", row.OAuth, tok)
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
