package usermcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// OAuth for custom MCP servers (MCP authorization spec, 2025-06-18): a
// server that answers 401 with a WWW-Authenticate Bearer challenge names
// its protected-resource metadata, which names the authorization server.
// We register a public client there (dynamic client registration), send
// the person through the authorization-code flow with PKCE, and keep the
// tokens with the server, refreshing them as they expire.
//
// The caller (Console, or a product such as WeClaw) owns the redirect: it
// asks StartOAuth for the authorization URL with its own redirect URI, and
// hands the code and state it got back to FinishOAuth. A state is bound to
// the user who started it and works once.

// OAuth is a server's authorization: the client we registered and the
// tokens we hold. Never sent to the UI.
type OAuth struct {
	Issuer        string `json:"issuer,omitempty"`
	TokenEndpoint string `json:"tokenEndpoint"`
	ClientID      string `json:"clientId"`
	ClientSecret  string `json:"clientSecret,omitempty"`
	// AuthMethod is how the client authenticates at the token endpoint:
	// "none", "client_secret_post" or "client_secret_basic".
	AuthMethod   string `json:"authMethod,omitempty"`
	Resource     string `json:"resource,omitempty"`
	Scope        string `json:"scope,omitempty"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresAt    int64  `json:"expiresAt,omitempty"` // unix ms; 0 = unknown
	// Broken: the tokens no longer work and couldn't be refreshed; the
	// person must authorize again.
	Broken bool `json:"broken,omitempty"`
}

const (
	pendingTTL     = 15 * time.Minute
	refreshLeeway  = time.Minute
	maxMetaBody    = 1 << 20
	maxClientName  = 64
	maxRedirectLen = 2048
)

// ── discovery ──────────────────────────────────────────────────────────

type authServerMeta struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	TokenAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
}

type discovered struct {
	as       authServerMeta
	resource string
	scope    string
}

var (
	resourceMetaRE = regexp.MustCompile(`resource_metadata="([^"]+)"`)
	scopeRE        = regexp.MustCompile(`scope="([^"]*)"`)
)

// challenge asks the server without credentials and reads its 401
// challenge. ok is false when the server doesn't ask for OAuth.
func challenge(ctx context.Context, serverURL string) (metaURL, scope string, ok bool, err error) {
	s := &session{url: serverURL}
	resp, err := s.post(ctx, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "fastclaw", "version": "1"},
	}})
	if err != nil {
		return "", "", false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode != http.StatusUnauthorized {
		return "", "", false, nil
	}
	for _, h := range resp.Header.Values("WWW-Authenticate") {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(h)), "bearer") {
			continue
		}
		if m := resourceMetaRE.FindStringSubmatch(h); m != nil {
			metaURL = m[1]
		}
		if m := scopeRE.FindStringSubmatch(h); m != nil {
			scope = m[1]
		}
	}
	return metaURL, scope, true, nil
}

func getJSON(ctx context.Context, rawURL string, out any) error {
	if !httpsURL(rawURL) {
		return errors.New("not https")
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxMetaBody)).Decode(out)
}

func httpsURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// wellKnown is base's origin + /.well-known/<name> + base's path (RFC 8414
// / RFC 9728 path insertion), then without the path.
func wellKnown(base, name string) []string {
	u, err := url.Parse(base)
	if err != nil {
		return nil
	}
	origin := u.Scheme + "://" + u.Host
	path := strings.TrimRight(u.EscapedPath(), "/")
	var out []string
	if path != "" {
		out = append(out, origin+"/.well-known/"+name+path)
	}
	return append(out, origin+"/.well-known/"+name)
}

// discover finds the authorization server of an MCP server that asks for
// OAuth. Codes: oauth_not_required (it answered without credentials),
// oauth_unsupported (no usable authorization server).
func discover(ctx context.Context, serverURL string) (*discovered, error) {
	metaURL, scope, needs, err := challenge(ctx, serverURL)
	if err != nil {
		return nil, err
	}
	if !needs {
		return nil, &Error{Code: "oauth_not_required"}
	}

	// Protected-resource metadata: the challenge's URL, else well-known.
	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
	}
	candidates := wellKnown(serverURL, "oauth-protected-resource")
	if metaURL != "" {
		candidates = append([]string{metaURL}, candidates...)
	}
	for _, c := range candidates {
		if getJSON(ctx, c, &prm) == nil && len(prm.AuthorizationServers) > 0 {
			break
		}
		prm.AuthorizationServers = nil
	}
	issuer := ""
	if len(prm.AuthorizationServers) > 0 {
		issuer = prm.AuthorizationServers[0]
	} else {
		// Older servers (2025-03-26): the MCP server's origin is the
		// authorization server.
		u, _ := url.Parse(serverURL)
		issuer = u.Scheme + "://" + u.Host
	}
	if !httpsURL(issuer) {
		return nil, &Error{Code: "oauth_unsupported", Detail: "authorization server is not https"}
	}

	var as authServerMeta
	found := false
	for _, c := range append(wellKnown(issuer, "oauth-authorization-server"), wellKnown(issuer, "openid-configuration")...) {
		as = authServerMeta{}
		if getJSON(ctx, c, &as) == nil && as.AuthorizationEndpoint != "" && as.TokenEndpoint != "" {
			found = true
			break
		}
	}
	if !found {
		return nil, &Error{Code: "oauth_unsupported", Detail: "no authorization server metadata"}
	}
	if !httpsURL(as.AuthorizationEndpoint) || !httpsURL(as.TokenEndpoint) {
		return nil, &Error{Code: "oauth_unsupported", Detail: "endpoints are not https"}
	}
	if as.RegistrationEndpoint == "" || !httpsURL(as.RegistrationEndpoint) {
		return nil, &Error{Code: "oauth_unsupported", Detail: "no dynamic client registration"}
	}
	if len(as.CodeChallengeMethods) > 0 && !contains(as.CodeChallengeMethods, "S256") {
		return nil, &Error{Code: "oauth_unsupported", Detail: "no PKCE S256"}
	}

	resource := prm.Resource
	if resource == "" {
		resource = serverURL
	}
	if scope == "" && len(prm.ScopesSupported) > 0 && len(prm.ScopesSupported) <= 4 {
		scope = strings.Join(prm.ScopesSupported, " ")
	}
	return &discovered{as: as, resource: resource, scope: scope}, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ── client registration and tokens ─────────────────────────────────────

func postJSON(ctx context.Context, endpoint string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxMetaBody))
	if resp.StatusCode/100 != 2 {
		return errors.New(resp.Status + ": " + truncate(string(data), 300))
	}
	return json.Unmarshal(data, out)
}

func register(ctx context.Context, d *discovered, redirectURI, clientName string) (*OAuth, error) {
	method := "none"
	if len(d.as.TokenAuthMethods) > 0 && !contains(d.as.TokenAuthMethods, "none") {
		method = "client_secret_post"
		if !contains(d.as.TokenAuthMethods, method) {
			method = "client_secret_basic"
		}
	}
	req := map[string]any{
		"client_name":                clientName,
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": method,
	}
	if d.scope != "" {
		req["scope"] = d.scope
	}
	var res struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		AuthMethod   string `json:"token_endpoint_auth_method"`
	}
	if err := postJSON(ctx, d.as.RegistrationEndpoint, req, &res); err != nil {
		// Some servers only let named apps in (e.g. "only Claude and
		// ChatGPT redirect URIs"): nothing we can change.
		if strings.Contains(err.Error(), "invalid_redirect_uri") {
			return nil, &Error{Code: "oauth_redirect_refused", Detail: err.Error()}
		}
		return nil, &Error{Code: "oauth_unsupported", Detail: "client registration: " + err.Error()}
	}
	if res.ClientID == "" {
		return nil, &Error{Code: "oauth_unsupported", Detail: "client registration returned no client_id"}
	}
	if res.AuthMethod != "" {
		method = res.AuthMethod
	}
	if res.ClientSecret == "" {
		method = "none"
	}
	return &OAuth{
		Issuer:        d.as.Issuer,
		TokenEndpoint: d.as.TokenEndpoint,
		ClientID:      res.ClientID,
		ClientSecret:  res.ClientSecret,
		AuthMethod:    method,
		Resource:      d.resource,
		Scope:         d.scope,
	}, nil
}

// tokenRequest posts a grant to the token endpoint and stores the result
// in o. invalid is true when the server rejected the grant itself.
func tokenRequest(ctx context.Context, o *OAuth, form url.Values) (invalid bool, err error) {
	if o.Resource != "" {
		form.Set("resource", o.Resource)
	}
	if o.AuthMethod != "client_secret_basic" {
		form.Set("client_id", o.ClientID)
	}
	if o.AuthMethod == "client_secret_post" {
		form.Set("client_secret", o.ClientSecret)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if o.AuthMethod == "client_secret_basic" {
		req.SetBasicAuth(url.QueryEscape(o.ClientID), url.QueryEscape(o.ClientSecret))
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxMetaBody))

	var res struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    any    `json:"expires_in"`
		Scope        string `json:"scope"`
		Error        string `json:"error"`
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ct == "application/x-www-form-urlencoded" || ct == "text/plain" {
		v, _ := url.ParseQuery(string(data))
		res.AccessToken, res.RefreshToken, res.Error = v.Get("access_token"), v.Get("refresh_token"), v.Get("error")
		res.ExpiresIn, res.Scope = v.Get("expires_in"), v.Get("scope")
	} else {
		_ = json.Unmarshal(data, &res)
	}
	if resp.StatusCode/100 != 2 || res.AccessToken == "" {
		reason := res.Error
		if reason == "" {
			reason = resp.Status
		}
		return res.Error == "invalid_grant" || res.Error == "invalid_client" || res.Error == "unauthorized_client", errors.New(reason)
	}
	o.AccessToken = res.AccessToken
	if res.RefreshToken != "" {
		o.RefreshToken = res.RefreshToken
	}
	o.ExpiresAt = 0
	if secs := toSeconds(res.ExpiresIn); secs > 0 {
		o.ExpiresAt = time.Now().Add(time.Duration(secs) * time.Second).UnixMilli()
	}
	o.Broken = false
	return false, nil
}

func toSeconds(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ── the flow ───────────────────────────────────────────────────────────

// OAuthStart is what StartOAuth needs. ServerID set re-authorizes one of
// the user's servers; otherwise Name and URL describe a new one.
type OAuthStart struct {
	ServerID    string
	Name        string
	URL         string
	RedirectURI string
	ClientName  string
}

type pendingData struct {
	OAuth       OAuth  `json:"oauth"`
	Verifier    string `json:"verifier"`
	RedirectURI string `json:"redirectUri"`
}

func validRedirect(raw string) bool {
	if len(raw) > maxRedirectLen {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	h := u.Hostname()
	return u.Scheme == "http" && (h == "localhost" || h == "127.0.0.1")
}

// StartOAuth registers a client with the server's authorization server
// and returns the URL to send the person to.
func (s *Service) StartOAuth(ctx context.Context, userID string, in OAuthStart) (string, error) {
	if userID == "" {
		return "", &Error{Code: "unauthorized"}
	}
	if !validRedirect(in.RedirectURI) {
		return "", &Error{Code: "invalid_redirect"}
	}
	clientName := strings.TrimSpace(in.ClientName)
	if clientName == "" || utf8.RuneCountInString(clientName) > maxClientName {
		clientName = "Fastclaw"
	}
	existing, err := s.st.list(ctx, userID)
	if err != nil {
		return "", err
	}
	name, u := "", ""
	if in.ServerID != "" {
		for _, e := range existing {
			if e.ID == in.ServerID {
				name, u = e.Name, e.URL
			}
		}
		if u == "" {
			return "", &Error{Code: "not_found"}
		}
	} else {
		name, u, _, err = Normalize(in.Name, in.URL, nil)
		if err != nil {
			return "", err
		}
		if err := checkNew(existing, name); err != nil {
			return "", err
		}
	}

	d, err := discover(ctx, u)
	if err != nil {
		return "", err
	}
	o, err := register(ctx, d, in.RedirectURI, clientName)
	if err != nil {
		return "", err
	}
	verifier := randomToken(48)
	state := randomToken(32)
	data, _ := json.Marshal(pendingData{OAuth: *o, Verifier: verifier, RedirectURI: in.RedirectURI})
	if err := s.st.putPending(ctx, state, userID, in.ServerID, name, u, string(data), time.Now().Add(pendingTTL).UnixMilli()); err != nil {
		return "", err
	}

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", o.ClientID)
	q.Set("redirect_uri", in.RedirectURI)
	q.Set("state", state)
	q.Set("code_challenge", pkceChallenge(verifier))
	q.Set("code_challenge_method", "S256")
	if o.Scope != "" {
		q.Set("scope", o.Scope)
	}
	if o.Resource != "" {
		q.Set("resource", o.Resource)
	}
	authURL := d.as.AuthorizationEndpoint
	if strings.Contains(authURL, "?") {
		authURL += "&" + q.Encode()
	} else {
		authURL += "?" + q.Encode()
	}
	return authURL, nil
}

func checkNew(existing []Server, name string) error {
	if len(existing) >= MaxServers {
		return &Error{Code: "too_many"}
	}
	for _, e := range existing {
		if strings.EqualFold(e.Name, name) {
			return &Error{Code: "name_taken"}
		}
	}
	return nil
}

// FinishOAuth trades the code for tokens, checks the server answers as
// MCP with them, and saves it (or the new tokens of the server being
// re-authorized).
func (s *Service) FinishOAuth(ctx context.Context, userID, state, code string) (Server, error) {
	if userID == "" {
		return Server{}, &Error{Code: "unauthorized"}
	}
	p, err := s.st.takePending(ctx, state, userID)
	if err != nil {
		return Server{}, err
	}
	if p == nil {
		return Server{}, &Error{Code: "oauth_expired"}
	}
	var data pendingData
	if err := json.Unmarshal([]byte(p.data), &data); err != nil {
		return Server{}, err
	}
	o := data.OAuth
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", data.RedirectURI)
	form.Set("code_verifier", data.Verifier)
	if _, err := tokenRequest(ctx, &o, form); err != nil {
		return Server{}, &Error{Code: "oauth_failed", Detail: err.Error()}
	}

	sess, err := dial(ctx, p.url, bearer(nil, &o))
	if err != nil {
		return Server{}, err
	}
	tools, err := sess.listTools(ctx)
	if err != nil {
		return Server{}, err
	}

	existing, err := s.st.list(ctx, userID)
	if err != nil {
		return Server{}, err
	}
	if p.serverID != "" {
		for _, e := range existing {
			if e.ID != p.serverID {
				continue
			}
			e.Headers = map[string]string{}
			e.OAuth = &o
			e.ToolCount = len(tools)
			if _, err := s.st.update(ctx, userID, e); err != nil {
				return Server{}, err
			}
			s.forgetToken(e.ID)
			s.remember(e.ID, tools)
			return e, nil
		}
		return Server{}, &Error{Code: "not_found"}
	}
	if err := checkNew(existing, p.name); err != nil {
		return Server{}, err
	}
	srv := Server{
		ID: "mcp_" + strings.ReplaceAll(uuid.NewString(), "-", ""), Name: p.name, URL: p.url,
		Headers: map[string]string{}, OAuth: &o, ToolCount: len(tools), CreatedAt: time.Now().UnixMilli(),
	}
	if err := s.st.insert(ctx, userID, srv); err != nil {
		return Server{}, err
	}
	s.remember(srv.ID, tools)
	return srv, nil
}

// ── tokens in use ──────────────────────────────────────────────────────

func bearer(headers map[string]string, o *OAuth) map[string]string {
	out := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		out[k] = v
	}
	if o != nil && o.AccessToken != "" {
		for k := range out {
			if strings.EqualFold(k, "Authorization") {
				delete(out, k)
			}
		}
		out["Authorization"] = "Bearer " + o.AccessToken
	}
	return out
}

// tokenState is the newest tokens of each OAuth server this process
// knows, so concurrent turns refresh once and share the result.
type tokenState struct {
	mu     sync.Mutex
	latest map[string]*OAuth
	locks  map[string]*sync.Mutex
}

func (s *Service) serverLock(id string) *sync.Mutex {
	s.tok.mu.Lock()
	defer s.tok.mu.Unlock()
	if s.tok.locks == nil {
		s.tok.locks = map[string]*sync.Mutex{}
		s.tok.latest = map[string]*OAuth{}
	}
	l, ok := s.tok.locks[id]
	if !ok {
		l = &sync.Mutex{}
		s.tok.locks[id] = l
	}
	return l
}

func (s *Service) latestToken(srv Server) *OAuth {
	s.tok.mu.Lock()
	defer s.tok.mu.Unlock()
	if o, ok := s.tok.latest[srv.ID]; ok && o != nil {
		cp := *o
		return &cp
	}
	cp := *srv.OAuth
	return &cp
}

func (s *Service) keepToken(id string, o *OAuth) {
	s.tok.mu.Lock()
	defer s.tok.mu.Unlock()
	if s.tok.latest == nil {
		s.tok.latest = map[string]*OAuth{}
	}
	cp := *o
	s.tok.latest[id] = &cp
}

func (s *Service) forgetToken(id string) {
	s.tok.mu.Lock()
	defer s.tok.mu.Unlock()
	delete(s.tok.latest, id)
}

// headersFor is what a request to srv carries: its headers, plus a fresh
// access token for an OAuth server (refreshed when it is about to expire,
// or when force is set after the server refused the current one).
func (s *Service) headersFor(ctx context.Context, srv Server, force bool) (map[string]string, error) {
	if srv.OAuth == nil {
		return srv.Headers, nil
	}
	l := s.serverLock(srv.ID)
	l.Lock()
	defer l.Unlock()
	o := s.latestToken(srv)
	if o.Broken {
		return nil, &Error{Code: "reauth_required"}
	}
	expiring := o.ExpiresAt > 0 && time.Now().Add(refreshLeeway).UnixMilli() >= o.ExpiresAt
	if (force || expiring) && o.RefreshToken != "" {
		form := url.Values{}
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", o.RefreshToken)
		invalid, err := tokenRequest(ctx, o, form)
		if err != nil {
			if !invalid {
				// A network or server hiccup: try the current token anyway.
				return bearer(srv.Headers, o), nil
			}
			o.Broken = true
		}
		s.keepToken(srv.ID, o)
		_ = s.st.setOAuth(ctx, srv.ID, o)
		if o.Broken {
			return nil, &Error{Code: "reauth_required"}
		}
	} else if force {
		// Refused and nothing to refresh with.
		o.Broken = true
		s.keepToken(srv.ID, o)
		_ = s.st.setOAuth(ctx, srv.ID, o)
		return nil, &Error{Code: "reauth_required"}
	}
	return bearer(srv.Headers, o), nil
}

// dialFor opens a session to srv, refreshing an OAuth server's token once
// when the server refuses it.
func (s *Service) dialFor(ctx context.Context, srv Server) (*session, error) {
	headers, err := s.headersFor(ctx, srv, false)
	if err != nil {
		return nil, err
	}
	sess, err := dial(ctx, srv.URL, headers)
	if err == nil || srv.OAuth == nil || Code(err) != "unauthorized" {
		return sess, err
	}
	if headers, err = s.headersFor(ctx, srv, true); err != nil {
		return nil, err
	}
	return dial(ctx, srv.URL, headers)
}

// ── store ──────────────────────────────────────────────────────────────

var oauthSchema = []string{
	`CREATE TABLE IF NOT EXISTS user_mcp_oauth_pending (
		state TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		server_id TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL,
		url TEXT NOT NULL,
		data TEXT NOT NULL,
		expires_at BIGINT NOT NULL
	)`,
}

// addOAuthColumn adds user_mcp_servers.oauth to a table created before it.
func (s *sqlStore) addOAuthColumn(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `ALTER TABLE user_mcp_servers ADD COLUMN oauth TEXT NOT NULL DEFAULT ''`)
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
			return nil
		}
	}
	return err
}

func encodeOAuth(o *OAuth) string {
	if o == nil {
		return ""
	}
	raw, _ := json.Marshal(o)
	return string(raw)
}

func decodeOAuth(raw string) *OAuth {
	if raw == "" {
		return nil
	}
	var o OAuth
	if json.Unmarshal([]byte(raw), &o) != nil || o.TokenEndpoint == "" {
		return nil
	}
	return &o
}

func (s *sqlStore) setOAuth(ctx context.Context, id string, o *OAuth) error {
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE user_mcp_servers SET oauth = ? WHERE id = ?`), encodeOAuth(o), id)
	return err
}

type pendingRow struct {
	serverID, name, url, data string
}

func (s *sqlStore) putPending(ctx context.Context, state, userID, serverID, name, u, data string, expiresAt int64) error {
	_, _ = s.db.ExecContext(ctx, s.rebind(`DELETE FROM user_mcp_oauth_pending WHERE expires_at < ?`), time.Now().UnixMilli())
	_, err := s.db.ExecContext(ctx, s.rebind(
		`INSERT INTO user_mcp_oauth_pending (state, user_id, server_id, name, url, data, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`),
		state, userID, serverID, name, u, data, expiresAt)
	return err
}

// takePending claims a state once: nil when it is unknown, someone
// else's, used or expired.
func (s *sqlStore) takePending(ctx context.Context, state, userID string) (*pendingRow, error) {
	if state == "" {
		return nil, nil
	}
	var p pendingRow
	var expiresAt int64
	err := s.db.QueryRowContext(ctx, s.rebind(
		`SELECT server_id, name, url, data, expires_at FROM user_mcp_oauth_pending WHERE state = ? AND user_id = ?`), state, userID).
		Scan(&p.serverID, &p.name, &p.url, &p.data, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx, s.rebind(`DELETE FROM user_mcp_oauth_pending WHERE state = ? AND user_id = ?`), state, userID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 || expiresAt < time.Now().UnixMilli() {
		return nil, nil // another request took it, or too late
	}
	return &p, nil
}
