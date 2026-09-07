// Package auth resolves an HTTP request to a user identity. It supports
// two credential types:
//   - cookie session ("fastclaw_session"): set by /api/login, validated
//     against the web_sessions table; used by the web UI
//   - Bearer apikey: validated against the apikeys table; used by API
//     consumers and CLI clients
//
// Both paths funnel into the same Identity struct stamped onto ctx via
// config.WithUserID. There is no anonymous "local" fallback — requests
// without valid credentials get 401.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// SessionCookieName is the cookie that backs the web UI's login state.
const SessionCookieName = "fastclaw_session"

// SessionTTL is how long a freshly-issued login cookie is valid.
const SessionTTL = 30 * 24 * time.Hour

// Identity is the resolved caller for one request.
type Identity struct {
	UserID string
	Role   string

	// AuthMethod is "session" or "apikey".
	AuthMethod string

	// APIKeyID is set when AuthMethod=="apikey". APIKeyType is one of
	// users.APIKeyType{Admin,User,Agent}. APIKeyAgents is the agent
	// scope resolved at auth time:
	//   - type=admin: empty (the agent gate short-circuits on type)
	//   - type=user:  every agent owned by the apikey owner at the
	//     moment of the request (resolved fresh per request)
	//   - type=agent: the explicit ACL from apikey_agents
	APIKeyID     string
	APIKeyType   string
	APIKeyAgents []string

	// ActAsUserID is non-empty when a super_admin is browsing another
	// user's resources read-only via ?actAs=. Mutating handlers MUST
	// 403 when this is set.
	ActAsUserID string
}

// EffectiveUserID is who we read data for. For super_admin in actAs mode
// it's the impersonated user; otherwise it's the caller themselves.
func (i Identity) EffectiveUserID() string {
	if i.ActAsUserID != "" {
		return i.ActAsUserID
	}
	return i.UserID
}

// IsActingAs reports whether super_admin is impersonating another user.
func (i Identity) IsActingAs() bool {
	return i.ActAsUserID != "" && i.ActAsUserID != i.UserID
}

// ReadOnly reports whether mutating endpoints must reject this request.
// Active actAs mode is the only read-only condition we enforce here.
func (i Identity) ReadOnly() bool {
	return i.IsActingAs()
}

// CanAccessAgent answers "is this caller authorized for agentID?"
//   - super_admin (session): identity-level yes; record-level handlers
//     still require owner/public access or the read-only actAs audit flow
//   - apikey type=admin: yes, on any agent
//   - apikey type=user/agent: only if agentID ∈ APIKeyAgents (the list
//     is pre-resolved at auth time per type — see Resolved.Agents)
//   - session user (non-admin): agent must belong to UserID (verified by
//     the caller querying agents table; we don't carry that list on
//     Identity)
func (i Identity) CanAccessAgent(agentID string) bool {
	if i.AuthMethod == "apikey" {
		if i.APIKeyType == users.APIKeyTypeAdmin {
			return true
		}
		for _, a := range i.APIKeyAgents {
			if a == agentID {
				return true
			}
		}
		return false
	}
	if i.Role == users.RoleSuperAdmin {
		return true
	}
	// session caller: agent ownership check happens in the handler
	// after reading the agent row (cheap M:1 lookup, no list scan).
	return true
}

// CanAdminPlatform answers "may this caller hit /api/admin/* and other
// platform-wide mutating endpoints?" Only super_admin sessions and
// type=admin apikeys qualify. Distinct from CanAccessAgent because a
// super_admin's type=user/agent apikey deliberately downgrades them to
// the narrower scope they signed it for.
func (i Identity) CanAdminPlatform() bool {
	if i.AuthMethod == "apikey" {
		return i.APIKeyType == users.APIKeyTypeAdmin
	}
	return i.Role == users.RoleSuperAdmin
}

// CanCreateAgent answers "may this caller create new agents?"
// type=agent keys explicitly cannot — they're sandboxed to a fixed list.
// Everyone else (sessions and admin/user keys) can.
func (i Identity) CanCreateAgent() bool {
	if i.AuthMethod == "apikey" && i.APIKeyType == users.APIKeyTypeAgent {
		return false
	}
	return true
}

type identityKey struct{}

// WithIdentity stamps the resolved identity onto ctx so handlers can read
// it without re-validating credentials.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	ctx = context.WithValue(ctx, identityKey{}, id)
	if uid := id.EffectiveUserID(); uid != "" {
		ctx = config.WithUserID(ctx, uid)
	}
	return ctx
}

// FromContext returns the resolved identity stamped by Middleware. The
// bool is false if no auth has run, which means a route is misconfigured
// (every API route must go through Middleware first).
func FromContext(ctx context.Context) (Identity, bool) {
	if ctx == nil {
		return Identity{}, false
	}
	v, ok := ctx.Value(identityKey{}).(Identity)
	return v, ok
}

// Resolver loads accounts, apikeys, and web sessions from the store.
type Resolver struct {
	store    store.Store
	apikeys  *users.APIKeys
	accounts *users.Accounts
}

// NewResolver returns a resolver bound to the platform store.
func NewResolver(st store.Store) (*Resolver, error) {
	if st == nil {
		return nil, errors.New("auth.NewResolver: store is required")
	}
	ak, err := users.NewAPIKeys(st)
	if err != nil {
		return nil, err
	}
	ac, err := users.NewAccounts(st)
	if err != nil {
		return nil, err
	}
	return &Resolver{store: st, apikeys: ak, accounts: ac}, nil
}

// IssueSession creates a web session for userID and returns the cookie.
// Caller writes the cookie to the response.
func (r *Resolver) IssueSession(ctx context.Context, userID string) (*http.Cookie, error) {
	sid, err := newSID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	rec := &store.WebSessionRecord{
		SID:       sid,
		UserID:    userID,
		CreatedAt: now,
		ExpiresAt: now.Add(SessionTTL),
	}
	if err := r.store.CreateWebSession(ctx, rec); err != nil {
		return nil, err
	}
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  rec.ExpiresAt,
	}, nil
}

// RevokeSession drops a session from the store.
func (r *Resolver) RevokeSession(ctx context.Context, sid string) error {
	return r.store.DeleteWebSession(ctx, sid)
}

// ResolveSession turns a cookie SID into an Identity.
func (r *Resolver) ResolveSession(ctx context.Context, sid string) (Identity, error) {
	if sid == "" {
		return Identity{}, ErrUnauthorized
	}
	sess, err := r.store.GetWebSession(ctx, sid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Identity{}, ErrUnauthorized
		}
		return Identity{}, err
	}
	if time.Now().UTC().After(sess.ExpiresAt) {
		_ = r.store.DeleteWebSession(ctx, sid)
		return Identity{}, ErrUnauthorized
	}
	user, err := r.accounts.Get(ctx, sess.UserID)
	if err != nil {
		return Identity{}, ErrUnauthorized
	}
	if user.Status != users.StatusActive {
		return Identity{}, ErrUnauthorized
	}
	return Identity{
		UserID:     user.ID,
		Role:       user.Role,
		AuthMethod: "session",
	}, nil
}

// ResolveBearer turns a Bearer token into an Identity.
func (r *Resolver) ResolveBearer(ctx context.Context, token string) (Identity, error) {
	res, err := r.apikeys.LookupByToken(ctx, token)
	if err != nil {
		if errors.Is(err, users.ErrInvalidCredentials) {
			return Identity{}, ErrUnauthorized
		}
		return Identity{}, err
	}
	return Identity{
		UserID:       res.Account.ID,
		Role:         res.Account.Role,
		AuthMethod:   "apikey",
		APIKeyID:     res.APIKey.ID,
		APIKeyType:   res.APIKey.Type,
		APIKeyAgents: append([]string(nil), res.Agents...),
	}, nil
}

// SwitchToAppUser rebinds ident to the app_user for (owner account,
// externalID), minting that row the first time it's seen. The app_user is
// keyed on the api_key's OWNER account — NOT the api_key id — so the calling
// app can rotate/replace its api_key without orphaning the end-user. APIKeyID
// + APIKeyAgents are preserved (only UserID + Role flip) so the apikey's agent
// ACL still gates access. Empty externalID passes through untouched. Only
// valid for AuthMethod=="apikey"; session callers stay as-is.
func (r *Resolver) SwitchToAppUser(ctx context.Context, ident Identity, externalID string) (Identity, error) {
	if externalID == "" {
		return ident, nil
	}
	if ident.AuthMethod != "apikey" || ident.APIKeyID == "" {
		return ident, errors.New("auth.SwitchToAppUser: api_key auth required")
	}
	// Already an app_user (request switched once) — re-keying off the
	// app_user's own id would mint a nested user. No-op instead.
	if ident.Role == users.RoleAppUser {
		return ident, nil
	}
	// ident.UserID is the api_key's owner account here (pre-switch); key the
	// app_user on it so rotating/replacing the api_key keeps the same user.
	acc, err := r.accounts.EnsureAppUser(ctx, ident.UserID, externalID, "", ident.APIKeyID)
	if err != nil {
		return ident, err
	}
	ident.UserID = acc.ID
	ident.Role = acc.Role
	return ident, nil
}

// EndUserHeader is the per-request header that names the calling app's
// end-user. When set on an api_key authenticated request, the auth
// middleware will lazily mint (or look up) a fastclaw user for
// (apikey, header) and switch the request identity to it. Sessions and
// agent_files written under that identity then partition cleanly per
// end-user instead of piling up under the api_key owner.
const EndUserHeader = "X-Fastclaw-End-User"

// ErrUnauthorized is returned when no valid credential is present.
var ErrUnauthorized = errors.New("unauthorized")

// extract returns the bearer token (if any) and session cookie SID (if
// any) from a request. A `?token=` query param is also accepted, but
// ONLY on the narrow set of paths that legitimately need it — file
// downloads (which the browser can't add an Authorization header to
// when rendered via <img> / <a download>) and the chat SSE
// subscription (EventSource has no header API). Everywhere else the
// query-param fallback is denied: tokens in URLs leak via Referer,
// browser history, reverse-proxy access logs, and observability
// pipelines. Header-only enforcement on /v1/* and the rest of /api/*
// closes that leak surface; CLI scripts that previously built
// `?token=` URLs for those endpoints must switch to
// `Authorization: Bearer <token>` (every HTTP client supports it).
func extract(r *http.Request) (bearer, sid string) {
	if c, err := r.Cookie(SessionCookieName); err == nil {
		sid = c.Value
	}
	if h := r.Header.Get("Authorization"); h != "" {
		if t := strings.TrimPrefix(h, "Bearer "); t != h {
			bearer = t
		}
	} else if t := r.URL.Query().Get("token"); t != "" && queryTokenAllowed(r) {
		bearer = t
	}
	return bearer, sid
}

// queryTokenAllowed gates the `?token=` bearer fallback to a
// narrow allowlist of paths whose clients have no other way to
// attach an Authorization header.
//
// Allowed:
//   - GET /api/agents/<id>/files/...        — workspace file download
//   - GET /api/agents/<id>/files.zip        — workspace archive
//   - GET /api/agents/<id>/system-files/<n> — identity-file fetch (rare)
//   - GET /api/chat/subscribe               — EventSource SSE stream
//
// Everything else (/v1/*, /api/chat, /api/agents/<id> JSON, …) must
// use the Authorization header. Deliberately *not* a prefix match
// on /api/agents/<id>/files since some workspace endpoints under
// that prefix accept POST/PUT bodies — limit to GET so a write
// path can never authenticate via a logged URL.
func queryTokenAllowed(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/api/agents/") && strings.Contains(p, "/files"):
		return true
	case strings.HasPrefix(p, "/api/agents/") && strings.Contains(p, "/system-files/"):
		return true
	case p == "/api/chat/subscribe":
		return true
	}
	return false
}

// Middleware enforces auth on every wrapped route. 401 on no/invalid
// credentials. Resolves ?actAs= for super_admins.
func (r *Resolver) Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ident, err := r.resolve(req)
		if err != nil {
			writeUnauthorized(w)
			return
		}
		req = req.WithContext(WithIdentity(req.Context(), ident))
		next(w, req)
	}
}

// Optional resolves credentials when present but lets unauthenticated
// requests through. Used for /api/status during onboarding so the
// onboarding UI can probe the install before any user exists.
func (r *Resolver) Optional(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ident, err := r.resolve(req)
		if err == nil {
			req = req.WithContext(WithIdentity(req.Context(), ident))
		}
		next(w, req)
	}
}

func (r *Resolver) resolve(req *http.Request) (Identity, error) {
	bearer, sid := extract(req)

	var ident Identity
	var err error
	if sid != "" {
		ident, err = r.ResolveSession(req.Context(), sid)
		if err == nil {
			goto done
		}
	}
	if bearer != "" {
		ident, err = r.ResolveBearer(req.Context(), bearer)
		if err == nil {
			goto done
		}
	}
	return Identity{}, ErrUnauthorized

done:
	// actAs is reserved for super_admin and only applies to session
	// callers (apikey impersonation would defeat the apikey ACL).
	if act := req.URL.Query().Get("actAs"); act != "" {
		if ident.AuthMethod == "session" && ident.Role == users.RoleSuperAdmin {
			ident.ActAsUserID = act
		}
	}
	// If the calling app named an end-user via X-Fastclaw-End-User on an
	// api_key request, rebind to the corresponding app_user (lazy mint).
	// We swallow errors here so a malformed header can't 401 a request —
	// the request just stays under the api_key owner. The OpenAI
	// /v1/chat/completions handler also honors `user` in the request
	// body for clients that prefer the OpenAI shape; that path calls
	// SwitchToAppUser explicitly after parsing the body.
	if eu := strings.TrimSpace(req.Header.Get(EndUserHeader)); eu != "" {
		if next, swErr := r.SwitchToAppUser(req.Context(), ident, eu); swErr == nil {
			ident = next
		}
	}
	return ident, nil
}

// RequireSuperAdmin returns a middleware that 403s any non-super-admin
// caller. Wraps another middleware (typically the auth Middleware).
//
// This is the strictest gate: it requires the live caller's identity to
// be super_admin regardless of how they authenticated. A super_admin
// using a type=user apikey is rejected — that's the deliberate downgrade
// the user signed up for when they issued the narrower key. For routes
// that should accept either path (admin session OR type=admin apikey),
// use RequirePlatformAdmin instead.
func RequireSuperAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ident, ok := FromContext(req.Context())
		if !ok || ident.Role != users.RoleSuperAdmin {
			writeForbidden(w, "super_admin required")
			return
		}
		// Apikey callers must additionally hold a type=admin key — a
		// super_admin's type=user key is intentionally narrower.
		if ident.AuthMethod == "apikey" && ident.APIKeyType != users.APIKeyTypeAdmin {
			writeForbidden(w, "admin apikey required")
			return
		}
		next(w, req)
	}
}

// RequirePlatformAdmin gates handlers that should accept any platform
// admin — session super_admin OR type=admin apikey. Same authority as
// RequireSuperAdmin in terms of what's allowed; just doesn't require the
// session path.
func RequirePlatformAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ident, ok := FromContext(req.Context())
		if !ok || !ident.CanAdminPlatform() {
			writeForbidden(w, "platform admin required")
			return
		}
		next(w, req)
	}
}

// RequireWritable rejects requests where Identity.ReadOnly() (i.e. the
// caller is acting as another user). Wrap mutating handlers.
func RequireWritable(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ident, ok := FromContext(req.Context())
		if !ok {
			writeUnauthorized(w)
			return
		}
		if ident.ReadOnly() {
			writeForbidden(w, "read-only: cannot mutate while acting as another user")
			return
		}
		next(w, req)
	}
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(`{"ok":false,"error":"unauthorized"}`))
}

func writeForbidden(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	w.Write([]byte(`{"ok":false,"error":"` + msg + `"}`))
}

func newSID() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}
