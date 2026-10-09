package connectors

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Error is a failure with a closed code the caller words for its audience
// (the settings pane in the reader's language, the model in English).
// Never Connany's own message: it may name the service.
type Error struct {
	Code string
	// PlatformDetail is what the PLATFORM said about a rejected tool call,
	// for the model to fix its arguments. Third-party text.
	PlatformDetail string
}

func (e *Error) Error() string { return "connectors: " + e.Code }

func newError(code string) *Error { return &Error{Code: code} }

// Code returns err's closed code.
func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "service_unavailable"
}

var (
	connectorNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	providerIDRe    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)
	errorCodeRe     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// wrap turns a client failure into an *Error.
func wrap(err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return err
	}
	if errors.Is(err, ErrRateLimited) {
		return newError("rate_limited")
	}
	var se *ServiceError
	if !errors.As(err, &se) {
		slog.Warn("connectors: service call failed", "error", err)
		return newError("service_unavailable")
	}
	out := &Error{Code: "service_unavailable"}
	switch {
	case se.Status == 429:
		out.Code = "rate_limited"
	case se.Status == 401:
		// A rejected project key is the operator's problem, not the person's.
		slog.Error("connectors: Connany rejected the project key")
	case errorCodeRe.MatchString(se.Code):
		out.Code = se.Code
	}
	out.PlatformDetail = platformDetail(se)
	return out
}

// platformDetail extracts the platform's own words from a rejected call:
// without them the model only learns "rejected" and retries blind.
func platformDetail(se *ServiceError) string {
	switch se.Code {
	case "upstream_error":
		said, _ := se.Details["upstream_error"].(string)
		if strings.TrimSpace(said) == "" {
			return ""
		}
		if st, ok := se.Details["upstream_status"].(float64); ok {
			said = fmt.Sprintf("%d %s", int(st), said)
		}
		return truncate(strings.TrimSpace(said), 2000)
	case "mcp_tool_error":
		res, _ := se.Details["result"].(map[string]any)
		content, _ := res["content"].([]any)
		var parts []string
		for _, c := range content {
			if m, ok := c.(map[string]any); ok {
				if t, ok := m["text"].(string); ok && t != "" {
					parts = append(parts, t)
				}
			}
		}
		return truncate(strings.TrimSpace(strings.Join(parts, "\n")), 2000)
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Waker hands a system note to a web conversation and runs a turn for it.
type Waker func(userID, agentID, sessionID, text string)

// Service is the process-wide connectors service.
type Service struct {
	client *Client
	store  *sqlStore

	mu      sync.Mutex
	catalog map[string]catalogEntry
	conns   map[string]connsEntry
	waker   Waker
}

type catalogEntry struct {
	at         time.Time
	categories []Category
	connectors []Connector
}

type connsEntry struct {
	at    time.Time
	conns []Connection
}

var (
	defaultMu  sync.RWMutex
	defaultSvc *Service
)

// Configure sets up the process-wide service. Connectors stay off (Get
// returns nil) unless both the base URL and key are set.
func Configure(ctx context.Context, baseURL, apiKey string, db DB) error {
	if baseURL == "" || apiKey == "" || db == nil {
		return nil
	}
	client, err := NewClient(baseURL, apiKey)
	if err != nil {
		return err
	}
	st, err := newSQLStore(ctx, db)
	if err != nil {
		return err
	}
	defaultMu.Lock()
	defaultSvc = &Service{client: client, store: st, catalog: map[string]catalogEntry{}, conns: map[string]connsEntry{}}
	defaultMu.Unlock()
	slog.Info("connectors enabled", "service", client.base)
	return nil
}

// Get returns the configured service, or nil when connectors are off.
func Get() *Service {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultSvc
}

// SetWaker installs how a conversation is woken after an authorization.
func (s *Service) SetWaker(w Waker) {
	s.mu.Lock()
	s.waker = w
	s.mu.Unlock()
}

// Subject is a person's opaque id in Connany. Stable forever: changing
// it orphans every connection made under the old value.
func Subject(userID string) string { return "fastclaw:user:" + userID }

// ── Catalog ─────────────────────────────────────────────────────────────

const catalogTTL = 5 * time.Minute

// Catalog lists the enabled connectors in lang. fresh bypasses the cache.
func (s *Service) Catalog(ctx context.Context, lang string, fresh bool) ([]Category, []Connector, error) {
	if lang == "" {
		lang = "en"
	}
	s.mu.Lock()
	hit, ok := s.catalog[lang]
	s.mu.Unlock()
	if ok && !fresh && time.Since(hit.at) < catalogTTL {
		return hit.categories, hit.connectors, nil
	}
	cats, list, err := s.client.Connectors(ctx, lang)
	if err != nil {
		return nil, nil, wrap(err)
	}
	valid := list[:0:0]
	for _, c := range list {
		if connectorNameRe.MatchString(c.Name) {
			valid = append(valid, c)
		}
	}
	s.mu.Lock()
	s.catalog[lang] = catalogEntry{at: time.Now(), categories: cats, connectors: valid}
	s.mu.Unlock()
	return cats, valid, nil
}

// Title is a connector's display name ("Notion"), falling back to its name.
func (s *Service) Title(ctx context.Context, name string) string {
	_, list, err := s.Catalog(ctx, "en", false)
	if err == nil {
		for _, c := range list {
			if c.Name == name {
				return c.Title
			}
		}
	}
	return name
}

// Avatar proxies a connector's icon.
func (s *Service) Avatar(ctx context.Context, name string) ([]byte, string, error) {
	_, list, err := s.Catalog(ctx, "en", false)
	if err != nil {
		return nil, "", err
	}
	for _, c := range list {
		if c.Name == name && c.AvatarURL != "" {
			return s.client.Avatar(ctx, c.AvatarURL)
		}
	}
	return nil, "", newError("not_found")
}

// ── Accounts ────────────────────────────────────────────────────────────

// Account is a connection as people and the model see it. ID is shown to
// its owner's browser only, never to the model.
type Account struct {
	ID            string `json:"id"`
	Connector     string `json:"connector"`
	Status        string `json:"status"` // connected | reauth_required
	Name          string `json:"name"`
	AccountName   string `json:"accountName,omitempty"`
	WorkspaceName string `json:"workspaceName,omitempty"`
	DisplayName   string `json:"displayName,omitempty"`
	NeedsAccess   bool   `json:"needsAccess"`
	IsDefault     bool   `json:"isDefault"`
}

// The service is the source of truth; a short cache keeps one turn's
// calls from re-listing each time. Changes made here drop it.
const connsTTL = 20 * time.Second

func (s *Service) invalidate(subject string) {
	s.mu.Lock()
	delete(s.conns, subject)
	s.mu.Unlock()
}

func (s *Service) connections(ctx context.Context, subject string, fresh bool) ([]Connection, error) {
	s.mu.Lock()
	hit, ok := s.conns[subject]
	s.mu.Unlock()
	if ok && !fresh && time.Since(hit.at) < connsTTL {
		return hit.conns, nil
	}
	all, err := s.client.Connections(ctx, subject)
	if err != nil {
		return nil, wrap(err)
	}
	live := all[:0:0]
	for _, c := range all {
		if c.Status != "revoked" && connectorNameRe.MatchString(c.Connector) && providerIDRe.MatchString(c.ID) {
			live = append(live, c)
		}
	}
	s.mu.Lock()
	s.conns[subject] = connsEntry{at: time.Now(), conns: live}
	s.mu.Unlock()
	return live, nil
}

// Accounts lists a person's connected accounts with their preferences.
func (s *Service) Accounts(ctx context.Context, userID string, fresh bool) ([]Account, error) {
	subject := Subject(userID)
	conns, err := s.connections(ctx, subject, fresh)
	if err != nil {
		return nil, err
	}
	prefs, err := s.store.prefs(ctx, subject)
	if err != nil {
		return nil, err
	}
	return toAccounts(conns, prefs), nil
}

func toAccounts(conns []Connection, prefs map[string]accountPref) []Account {
	out := make([]Account, 0, len(conns))
	for _, c := range conns {
		status := "reauth_required"
		if c.Status == "connected" {
			status = "connected"
		}
		display := prefs[c.ID].DisplayName
		name := firstNonEmpty(display, c.Identity.WorkspaceName, c.Identity.AccountName, c.Connector)
		out = append(out, Account{
			ID: c.ID, Connector: c.Connector, Status: status, Name: name,
			AccountName: c.Identity.AccountName, WorkspaceName: c.Identity.WorkspaceName,
			DisplayName: display, NeedsAccess: c.NeedsAccess,
		})
	}
	// A connector's default: the stored one while it's connected, else the
	// only connected account. Several and none chosen → no default: never
	// pick the first of several.
	seen := map[string]bool{}
	for _, a := range out {
		if seen[a.Connector] {
			continue
		}
		seen[a.Connector] = true
		var live []int
		chosen := -1
		for i := range out {
			if out[i].Connector != a.Connector || out[i].Status != "connected" {
				continue
			}
			live = append(live, i)
			if prefs[out[i].ID].IsDefault {
				chosen = i
			}
		}
		if chosen < 0 && len(live) == 1 {
			chosen = live[0]
		}
		if chosen >= 0 {
			out[chosen].IsDefault = true
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (s *Service) ownAccount(ctx context.Context, userID, connectionID string) (*Account, error) {
	if !providerIDRe.MatchString(connectionID) {
		return nil, newError("not_found")
	}
	accounts, err := s.Accounts(ctx, userID, true)
	if err != nil {
		return nil, err
	}
	for i := range accounts {
		if accounts[i].ID == connectionID {
			return &accounts[i], nil
		}
	}
	return nil, newError("not_found")
}

// SetDefault makes an account its connector's default.
func (s *Service) SetDefault(ctx context.Context, userID, connectionID string) error {
	a, err := s.ownAccount(ctx, userID, connectionID)
	if err != nil {
		return err
	}
	if a.Status != "connected" {
		return newError("reauth_required")
	}
	yes := true
	return s.store.setPref(ctx, Subject(userID), a.ID, a.Connector, nil, &yes)
}

// Rename sets an account's local name ("" restores the platform's name).
func (s *Service) Rename(ctx context.Context, userID, connectionID, name string) error {
	name = strings.TrimSpace(name)
	if len(name) > 80 || strings.ContainsAny(name, "\x00\r\n\t") {
		return newError("invalid_request")
	}
	a, err := s.ownAccount(ctx, userID, connectionID)
	if err != nil {
		return err
	}
	return s.store.setPref(ctx, Subject(userID), a.ID, a.Connector, &name, nil)
}

// Check verifies an account still works.
func (s *Service) Check(ctx context.Context, userID, connectionID string) (int, error) {
	if _, err := s.ownAccount(ctx, userID, connectionID); err != nil {
		return 0, err
	}
	defer s.invalidate(Subject(userID)) // a failed check may flip it to reauth_required
	n, err := s.client.Check(ctx, connectionID, Subject(userID))
	return n, wrap(err)
}

// Disconnect revokes an account and forgets its preferences.
func (s *Service) Disconnect(ctx context.Context, userID, connectionID string) error {
	if _, err := s.ownAccount(ctx, userID, connectionID); err != nil {
		return err
	}
	subject := Subject(userID)
	if _, err := s.client.Disconnect(ctx, connectionID, subject); err != nil {
		return wrap(err)
	}
	s.invalidate(subject)
	return s.store.deletePref(ctx, subject, connectionID)
}

// Access is what an account shares beyond its sign-in.
type Access struct {
	CanAdd bool          `json:"canAdd"`
	Total  int           `json:"total"`
	Grants []AccessGrant `json:"grants"`
}

// AccessOf lists an account's shared resources.
func (s *Service) AccessOf(ctx context.Context, userID, connectionID string) (*Access, error) {
	if _, err := s.ownAccount(ctx, userID, connectionID); err != nil {
		return nil, err
	}
	l, err := s.client.Access(ctx, connectionID, Subject(userID))
	if err != nil {
		return nil, wrap(err)
	}
	out := &Access{CanAdd: safeURL(l.AddURL) != "", Total: l.Total, Grants: l.Data}
	for i := range out.Grants {
		out.Grants[i].ManageURL = safeURL(out.Grants[i].ManageURL)
	}
	if out.Grants == nil {
		out.Grants = []AccessGrant{}
	}
	return out, nil
}

// safeURL returns raw when it's https (or http on this machine), else "".
func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || raw == "" || u.User != nil {
		return ""
	}
	local := u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")
	if u.Scheme != "https" && !local {
		return ""
	}
	return u.String()
}

// ── Authorization ───────────────────────────────────────────────────────

// RequestView is a request as the browser sees it.
type RequestView struct {
	ID        string `json:"id"`
	Connector string `json:"connector"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	ErrorCode string `json:"errorCode,omitempty"`
}

func viewOf(r *request) RequestView {
	return RequestView{ID: r.ID, Connector: r.Connector, Kind: r.Kind, Status: r.Status, ErrorCode: r.ErrorCode}
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ChatRef names the web conversation a request came from.
type ChatRef struct {
	AgentID   string
	SessionID string
}

// CreateRequest records an authorization the person can open later — an
// agent's card in a conversation. No link exists yet: Connany's links are
// one-use and short-lived, so one is minted when the person clicks.
func (s *Service) CreateRequest(ctx context.Context, userID, connector, kind, targetConnectionID string, chat ChatRef) (*RequestView, error) {
	if !connectorNameRe.MatchString(connector) {
		return nil, newError("connector_not_found")
	}
	now := nowMS()
	r := &request{
		ID: newID(), Subject: Subject(userID), UserID: userID, Connector: connector, Kind: kind,
		TargetConnectionID: targetConnectionID, AgentID: chat.AgentID, SessionID: chat.SessionID,
		Status: "requested", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.insertRequest(ctx, r); err != nil {
		return nil, err
	}
	v := viewOf(r)
	return &v, nil
}

// OpenTarget is what an authorization opens: an existing request, a new
// account of a connector, a reconnection, or more resource access.
type OpenTarget struct {
	RequestID   string
	Connector   string
	ReconnectID string
	AccessID    string
}

// Open mints the link for an authorization. returnURL is where Connany
// sends the browser afterwards (it appends connany_session_id).
func (s *Service) Open(ctx context.Context, userID string, t OpenTarget, returnURL string) (string, *RequestView, error) {
	subject := Subject(userID)
	var r *request
	var err error
	switch {
	case t.RequestID != "":
		if r, err = s.store.getRequest(ctx, subject, userID, t.RequestID); err != nil {
			return "", nil, err
		}
		if r.Status == "connected" || r.Status == "granted" {
			return "", nil, newError("already_done")
		}
	case t.Connector != "":
		_, list, err := s.Catalog(ctx, "en", true)
		if err != nil {
			return "", nil, err
		}
		found := false
		for _, c := range list {
			found = found || c.Name == t.Connector
		}
		if !found {
			return "", nil, newError("connector_not_found")
		}
		v, err := s.CreateRequest(ctx, userID, t.Connector, "connect", "", ChatRef{})
		if err != nil {
			return "", nil, err
		}
		if r, err = s.store.getRequest(ctx, subject, userID, v.ID); err != nil {
			return "", nil, err
		}
	default:
		id, kind := t.ReconnectID, "reconnect"
		if t.AccessID != "" {
			id, kind = t.AccessID, "access"
		}
		a, err := s.ownAccount(ctx, userID, id)
		if err != nil {
			return "", nil, err
		}
		v, err := s.CreateRequest(ctx, userID, a.Connector, kind, a.ID, ChatRef{})
		if err != nil {
			return "", nil, err
		}
		if r, err = s.store.getRequest(ctx, subject, userID, v.ID); err != nil {
			return "", nil, err
		}
	}

	if r.Kind == "access" {
		if r.TargetConnectionID == "" {
			return "", nil, newError("not_found")
		}
		l, err := s.client.Access(ctx, r.TargetConnectionID, subject)
		if err != nil {
			return "", nil, wrap(err)
		}
		link := safeURL(l.AddURL)
		if link == "" {
			return "", nil, newError("no_access_step")
		}
		if _, err := s.store.update(ctx, r.ID, nil, map[string]any{
			"status": "pending", "access_baseline": l.Total, "checked_at": 0, "notified_at": 0,
		}); err != nil {
			return "", nil, err
		}
		r.Status = "pending"
		v := viewOf(r)
		return link, &v, nil
	}

	var sess *Session
	if r.Kind == "reconnect" && r.TargetConnectionID != "" {
		sess, err = s.client.Reconnect(ctx, r.TargetConnectionID, subject, returnURL)
	} else {
		sess, err = s.client.CreateSession(ctx, r.Connector, subject, returnURL)
	}
	if err != nil {
		return "", nil, wrap(err)
	}
	link := safeURL(sess.ConnectURL)
	if !providerIDRe.MatchString(sess.ID) || link == "" {
		return "", nil, newError("invalid_response")
	}
	var expires int64
	if t, err := time.Parse(time.RFC3339, sess.ExpiresAt); err == nil {
		expires = t.UnixMilli()
	}
	// notified_at resets: a request the person cancelled and opened again
	// must still reach the conversation when it succeeds.
	if _, err := s.store.update(ctx, r.ID, nil, map[string]any{
		"provider_session_id": sess.ID, "status": "pending", "error_code": "",
		"notified_at": 0, "expires_at": expires, "checked_at": 0,
	}); err != nil {
		return "", nil, err
	}
	r.Status = "pending"
	v := viewOf(r)
	return link, &v, nil
}

const checkThrottle = 4 * time.Second

// Confirm asks Connany how an opened authorization ended and acts on it
// once: remember the account, wake the conversation that asked. Looked
// up by our id (the card) or Connany's session id (the return page) —
// either way only among this person's own requests.
func (s *Service) Confirm(ctx context.Context, userID, requestID, providerSessionID string, force bool) (*RequestView, error) {
	subject := Subject(userID)
	var r *request
	var err error
	if requestID != "" {
		r, err = s.store.getRequest(ctx, subject, userID, requestID)
	} else {
		if !providerIDRe.MatchString(providerSessionID) {
			return nil, newError("not_found")
		}
		r, err = s.store.getRequestByProvider(ctx, subject, userID, providerSessionID)
	}
	if err != nil {
		return nil, err
	}
	if r.Kind == "access" {
		return s.confirmAccess(ctx, userID, r, force)
	}
	if r.Status != "pending" || r.ProviderSessionID == "" {
		v := viewOf(r)
		return &v, nil
	}
	if !force && r.CheckedAt > 0 && time.Since(time.UnixMilli(r.CheckedAt)) < checkThrottle {
		v := viewOf(r)
		return &v, nil
	}
	sess, err := s.client.GetSession(ctx, r.Connector, r.ProviderSessionID, subject)
	if err != nil {
		return nil, wrap(err)
	}
	status := "pending"
	switch sess.Status {
	case "connected":
		status = "connected"
	case "error":
		status = "error"
	case "expired":
		status = "expired"
	}
	set := map[string]any{"status": status, "checked_at": nowMS(), "error_code": ""}
	if status == "error" {
		code := sess.ErrorCode
		if !errorCodeRe.MatchString(code) {
			code = "error"
		}
		set["error_code"] = code
	}
	if providerIDRe.MatchString(sess.ConnectionID) {
		set["connection_id"] = sess.ConnectionID
	}
	// Conditional on still pending: two tabs (or the card and the return
	// page) confirming at once act on the outcome once.
	changed, err := s.store.update(ctx, r.ID, []string{"pending"}, set)
	if err != nil {
		return nil, err
	}
	if r, err = s.store.getRequest(ctx, subject, userID, r.ID); err != nil {
		return nil, err
	}
	if changed && status != "pending" {
		s.invalidate(subject)
		if status == "connected" && r.ConnectionID != "" {
			if err := s.keepDefault(ctx, userID, r.Connector, r.ConnectionID); err != nil {
				slog.Warn("connectors: default account not recorded", "error", err)
			}
			s.notify(ctx, r, nil, "done")
		}
	}
	v := viewOf(r)
	return &v, nil
}

// keepDefault makes a newly connected account its connector's default
// unless another connected one already is.
func (s *Service) keepDefault(ctx context.Context, userID, connector, connectionID string) error {
	accounts, err := s.Accounts(ctx, userID, true)
	if err != nil {
		return err
	}
	yes := true
	for _, a := range accounts {
		if a.Connector == connector && a.ID != connectionID && a.Status == "connected" && a.IsDefault {
			// Store it: with the new account there are two, and an implicit
			// default (the only one) would otherwise disappear.
			return s.store.setPref(ctx, Subject(userID), a.ID, a.Connector, nil, &yes)
		}
	}
	return s.store.setPref(ctx, Subject(userID), connectionID, connector, nil, &yes)
}

func (s *Service) confirmAccess(ctx context.Context, userID string, r *request, force bool) (*RequestView, error) {
	if r.Status != "pending" || r.TargetConnectionID == "" {
		v := viewOf(r)
		return &v, nil
	}
	if !force && r.CheckedAt > 0 && time.Since(time.UnixMilli(r.CheckedAt)) < checkThrottle {
		v := viewOf(r)
		return &v, nil
	}
	l, err := s.client.Access(ctx, r.TargetConnectionID, Subject(userID))
	if err != nil {
		return nil, wrap(err)
	}
	// More grants than when the person opened the page: adding another
	// organization is a change too, while what was already there is not.
	granted := l.Total > r.AccessBaseline
	set := map[string]any{"checked_at": nowMS()}
	if granted {
		set["status"] = "granted"
		set["connection_id"] = r.TargetConnectionID
	}
	changed, err := s.store.update(ctx, r.ID, []string{"pending"}, set)
	if err != nil {
		return nil, err
	}
	if r, err = s.store.getRequest(ctx, Subject(userID), userID, r.ID); err != nil {
		return nil, err
	}
	if changed && granted {
		s.invalidate(Subject(userID))
		s.notify(ctx, r, l, "done")
	}
	v := viewOf(r)
	return &v, nil
}

// Cancel records that the person declined; the conversation that asked
// is told once so the agent can answer without the account.
func (s *Service) Cancel(ctx context.Context, userID, requestID string) (*RequestView, error) {
	subject := Subject(userID)
	r, err := s.store.getRequest(ctx, subject, userID, requestID)
	if err != nil {
		return nil, err
	}
	changed, err := s.store.update(ctx, r.ID, []string{"requested", "pending", "error", "expired"},
		map[string]any{"status": "cancelled", "error_code": ""})
	if err != nil {
		return nil, err
	}
	if r, err = s.store.getRequest(ctx, subject, userID, r.ID); err != nil {
		return nil, err
	}
	if changed {
		s.notify(ctx, r, nil, "cancelled")
	}
	v := viewOf(r)
	return &v, nil
}

const accessPollWindow = 10 * time.Minute

// Status is the card's view of a request. An open one is confirmed with
// Connany on the way (throttled), so the card settles even when the
// person never came back through the return page.
func (s *Service) Status(ctx context.Context, userID, requestID string) (*RequestView, error) {
	r, err := s.store.getRequest(ctx, Subject(userID), userID, requestID)
	if err != nil {
		return nil, err
	}
	if r.Status != "pending" {
		v := viewOf(r)
		return &v, nil
	}
	if r.Kind != "access" && r.ExpiresAt > 0 && time.Now().UnixMilli() > r.ExpiresAt+60_000 {
		_, _ = s.store.update(ctx, r.ID, []string{"pending"}, map[string]any{"status": "expired"})
		r.Status = "expired"
		v := viewOf(r)
		return &v, nil
	}
	// Platforms never send the person back from an access page, so the
	// card asks for a while, then leaves it to "I've finished".
	if r.Kind == "access" && time.Since(time.UnixMilli(r.UpdatedAt)) > accessPollWindow {
		v := viewOf(r)
		return &v, nil
	}
	v, err := s.Confirm(ctx, userID, requestID, "", false)
	if err != nil {
		fallback := viewOf(r)
		return &fallback, nil
	}
	return v, nil
}

// notify tells the conversation that asked — once — and wakes it.
func (s *Service) notify(ctx context.Context, r *request, access *AccessListing, outcome string) {
	if r.AgentID == "" || r.SessionID == "" {
		return
	}
	claimed, err := s.store.claimNotify(ctx, r.ID)
	if err != nil || !claimed {
		return
	}
	s.mu.Lock()
	wake := s.waker
	s.mu.Unlock()
	if wake == nil {
		return
	}
	title := s.Title(ctx, r.Connector)
	account := ""
	if r.ConnectionID != "" {
		if accounts, err := s.Accounts(ctx, r.UserID, false); err == nil {
			for _, a := range accounts {
				if a.ID == r.ConnectionID {
					account = fmt.Sprintf(" %q", a.Name)
				}
			}
		}
	}
	var text string
	switch {
	case outcome == "cancelled":
		what := "connection"
		if r.Kind == "access" {
			what = "access request"
		}
		text = fmt.Sprintf("[connector] The person cancelled the %s %s. Do not ask again unless they bring it up; tell them in one line what you can or cannot do without it.", title, what)
	case r.Kind == "access":
		var names []string
		if access != nil {
			for i, g := range access.Data {
				if i == 10 {
					break
				}
				names = append(names, g.Type+" "+g.Name)
			}
		}
		granted := strings.Join(names, ", ")
		if granted == "" {
			granted = "access granted"
		}
		text = fmt.Sprintf("[connector] The person granted resource access for their %s account%s: %s. Continue their request with the connectors tool. Pick the exact tool with list_tools first and request only what they asked for (filters, small pages).", title, account, granted)
	default:
		text = fmt.Sprintf("[connector] The person connected their %s account%s. Continue their request with the connectors tool (connector %q). Pick the exact tool with list_tools first and request only what they asked for (filters, small pages).", title, account, r.Connector)
	}
	go wake(r.UserID, r.AgentID, r.SessionID, text)
}

// ── What an agent does with an account ─────────────────────────────────

// EverConnected lists platforms the person connected at some point.
func (s *Service) EverConnected(ctx context.Context, userID string) ([]string, error) {
	return s.store.everConnected(ctx, Subject(userID))
}

// ResolveAccount picks the account a call goes to: the one named (by its
// local, workspace or account name — never an id), else the default.
func (s *Service) ResolveAccount(ctx context.Context, userID, connector, name string) (*Account, error) {
	accounts, err := s.Accounts(ctx, userID, false)
	if err != nil {
		return nil, err
	}
	var mine []Account
	for _, a := range accounts {
		if a.Connector == connector {
			mine = append(mine, a)
		}
	}
	if len(mine) == 0 {
		return nil, newError("no_account")
	}
	if want := strings.TrimSpace(name); want != "" {
		var matches []Account
		for _, a := range mine {
			if a.DisplayName == want || a.WorkspaceName == want || a.AccountName == want || a.Name == want {
				matches = append(matches, a)
			}
		}
		switch len(matches) {
		case 0:
			return nil, newError("account_not_found")
		case 1:
			return &matches[0], nil
		default:
			return nil, newError("account_ambiguous")
		}
	}
	for i := range mine {
		if mine[i].IsDefault {
			return &mine[i], nil
		}
	}
	for _, a := range mine {
		if a.Status == "connected" {
			return nil, newError("account_ambiguous")
		}
	}
	return nil, newError("reauth_required")
}

// ToolInfo is a tool as the model sees it.
type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// ListTools searches an account's read-only tools.
func (s *Service) ListTools(ctx context.Context, userID string, a *Account, query string, offset int) ([]ToolInfo, *int, error) {
	if a.Status != "connected" {
		return nil, nil, newError("reauth_required")
	}
	defs, next, err := s.client.ListTools(ctx, a.ID, Subject(userID), query, offset, 20, true)
	if err != nil {
		err = wrap(err)
		if c := Code(err); c == "reauth_required" || c == "connection_revoked" {
			s.invalidate(Subject(userID))
		}
		return nil, nil, err
	}
	out := make([]ToolInfo, 0, len(defs))
	for _, d := range defs {
		if d.Connector == a.Connector && d.ReadOnly {
			out = append(out, ToolInfo{Name: d.Name, Description: d.Description, InputSchema: d.InputSchema})
		}
	}
	return out, next, nil
}

// CallTool runs a read-only tool. Whether it only reads is Connany's
// word from the tool's current definition, never the model's.
func (s *Service) CallTool(ctx context.Context, userID string, a *Account, tool string, input map[string]any) (json.RawMessage, error) {
	if a.Status != "connected" {
		return nil, newError("reauth_required")
	}
	if !strings.HasPrefix(tool, a.Connector+".") || strings.HasPrefix(strings.TrimPrefix(tool, a.Connector+"."), "__") {
		return nil, newError("connector_mismatch")
	}
	subject := Subject(userID)
	defs, _, err := s.client.ListTools(ctx, a.ID, subject, tool, 0, 20, false)
	if err != nil {
		return nil, wrap(err)
	}
	var def *ToolDefinition
	for i := range defs {
		if defs[i].Name == tool && defs[i].Connector == a.Connector {
			def = &defs[i]
		}
	}
	if def == nil {
		return nil, newError("tool_not_found")
	}
	if !def.ReadOnly {
		return nil, newError("write_not_allowed")
	}
	data, err := s.client.CallTool(ctx, a.ID, subject, tool, input)
	if err != nil {
		err = wrap(err)
		if c := Code(err); c == "reauth_required" || c == "connection_revoked" {
			s.invalidate(subject)
		}
		return nil, err
	}
	return data, nil
}

// MaxResultChars caps a tool result: it stays in context and is re-read
// on every later turn.
const MaxResultChars = 15000

// RenderResult turns a tool result into text. MCP text blocks (often
// JSON themselves) go through as text rather than being escaped again.
func RenderResult(data json.RawMessage) string {
	var env struct {
		Content []json.RawMessage `json:"content"`
		IsError bool              `json:"isError"`
	}
	var out string
	if json.Unmarshal(data, &env) == nil && len(env.Content) > 0 {
		parts := make([]string, 0, len(env.Content)+1)
		if env.IsError {
			parts = append(parts, "[the platform reported an error]")
		}
		for _, c := range env.Content {
			var block struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(c, &block) == nil && block.Type == "text" {
				parts = append(parts, block.Text)
			} else {
				parts = append(parts, string(c))
			}
		}
		out = strings.Join(parts, "\n")
	} else {
		out = string(data)
	}
	if len(out) > MaxResultChars {
		out = fmt.Sprintf("%s\n[truncated: %d characters in all, only the first %d shown — narrow the query (filters, fewer results per page) or fetch the next page]", out[:MaxResultChars], len(out), MaxResultChars)
	}
	return out
}
