// Package usermcp holds the MCP servers a person added themselves
// (WeClaw → Connectors → Custom MCP). A server belongs to the one user who
// added it, who offers it on agents they made (SetAgentServers): everyone
// chatting with such an agent may use it there, under the same turn rules
// as their connected accounts (docs/connectors.md).
//
// A server with headers (an API key) runs on the adder's headers for
// everyone. An OAuth server runs on each person's own sign-in.
package usermcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Error is a failure with a closed code the UI words itself.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return "usermcp: " + e.Code
	}
	return "usermcp: " + e.Code + ": " + e.Detail
}

// Code is err's closed code ("internal" for anything else).
func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "internal"
}

// Server is one MCP server a person added.
type Server struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"-"` // values never leave the server
	OAuth     *OAuth            `json:"-"` // its OAuth client; set for a server that signs in with OAuth
	ToolCount int               `json:"toolCount"`
	CreatedAt int64             `json:"createdAt"`
}

// Auth is how requests to the server authenticate: "oauth" or "headers".
func (s Server) Auth() string {
	if s.OAuth != nil {
		return "oauth"
	}
	return "headers"
}

// HeaderNames lists the header names (values stay secret).
func (s Server) HeaderNames() []string {
	out := make([]string, 0, len(s.Headers))
	for k := range s.Headers {
		out = append(out, k)
	}
	return out
}

// Limits.
const (
	MaxServers     = 20
	maxNameRunes   = 40
	maxURLLen      = 2048
	maxHeaders     = 16
	maxHeaderValue = 4096
)

// DB is what this package needs from the main store.
type DB interface {
	DB() *sql.DB
	Dialect() string
}

// Service is the store plus a short cache of each server's tools.
type Service struct {
	st *sqlStore

	mu    sync.Mutex
	tools map[string]toolsEntry // server id → tools

	tok tokenState
}

type toolsEntry struct {
	at    time.Time
	tools []ToolDef
}

const toolsTTL = 5 * time.Minute

var global atomic.Pointer[Service]

// Init creates the table and makes the service available (Get). A store
// without SQL leaves the feature off.
func Init(ctx context.Context, st any) error {
	db, ok := st.(DB)
	if !ok {
		return nil
	}
	s, err := newSQLStore(ctx, db)
	if err != nil {
		return err
	}
	global.Store(&Service{st: s, tools: map[string]toolsEntry{}})
	return nil
}

// Get is the service, or nil when it is off.
func Get() *Service { return global.Load() }

// List is the user's servers, oldest first.
func (s *Service) List(ctx context.Context, userID string) ([]Server, error) {
	if userID == "" {
		return nil, nil
	}
	return s.st.list(ctx, userID)
}

var headerNameRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// reservedHeaders are set by the client itself.
var reservedHeaders = map[string]bool{
	"host": true, "content-type": true, "content-length": true, "accept": true,
	"connection": true, "transfer-encoding": true, "mcp-session-id": true,
	"mcp-protocol-version": true, "te": true, "upgrade": true, "keep-alive": true,
}

// Normalize checks a server's name, address and headers.
func Normalize(name, rawURL string, headers map[string]string) (string, string, map[string]string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxNameRunes || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", "", nil, &Error{Code: "invalid_name"}
	}
	rawURL = strings.TrimSpace(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil || len(rawURL) > maxURLLen || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", "", nil, &Error{Code: "invalid_url"}
	}
	if len(headers) > maxHeaders {
		return "", "", nil, &Error{Code: "invalid_headers"}
	}
	clean := make(map[string]string, len(headers))
	for k, v := range headers {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if !headerNameRE.MatchString(k) || reservedHeaders[strings.ToLower(k)] ||
			len(v) > maxHeaderValue || strings.ContainsAny(v, "\r\n\x00") {
			return "", "", nil, &Error{Code: "invalid_headers", Detail: k}
		}
		clean[k] = v
	}
	return name, u.String(), clean, nil
}

// OAuthClient is where a product sends people back after signing in to
// an OAuth server, and the name it registers there.
type OAuthClient struct {
	RedirectURI string
	ClientName  string
}

// Add checks the server answers as MCP (initialize + tools/list), then
// saves it for userID. A server that asks for OAuth is saved without
// signing in (each person signs in when they use it); with client set,
// its client is registered now, so a server that won't take this app
// says so here rather than to the first person who tries it.
func (s *Service) Add(ctx context.Context, userID, name, rawURL string, headers map[string]string, client *OAuthClient) (Server, error) {
	if userID == "" {
		return Server{}, &Error{Code: "unauthorized"}
	}
	name, u, headers, err := Normalize(name, rawURL, headers)
	if err != nil {
		return Server{}, err
	}
	existing, err := s.st.list(ctx, userID)
	if err != nil {
		return Server{}, err
	}
	if len(existing) >= MaxServers {
		return Server{}, &Error{Code: "too_many"}
	}
	for _, e := range existing {
		if strings.EqualFold(e.Name, name) {
			return Server{}, &Error{Code: "name_taken"}
		}
	}
	srv := Server{ID: "mcp_" + strings.ReplaceAll(uuid.NewString(), "-", ""), Name: name, URL: u, Headers: headers, CreatedAt: time.Now().UnixMilli()}
	sess, err := dial(ctx, u, headers)
	if err != nil {
		if Code(err) != "unauthorized" {
			return Server{}, err
		}
		// It asks for OAuth: saved as is. Each person signs in with their
		// own account when they first use it; the adder needn't.
		d, derr := discover(ctx, u)
		if derr != nil {
			return Server{}, err // a plain 401: the key is wrong
		}
		srv.OAuth = oauthConfig(d)
		if client != nil {
			if !validRedirect(client.RedirectURI) {
				return Server{}, &Error{Code: "invalid_redirect"}
			}
			reg, err := register(ctx, d, client.RedirectURI, clientName(client.ClientName))
			if err != nil {
				return Server{}, err
			}
			reg.RedirectURI = client.RedirectURI
			srv.OAuth = reg
		}
		if err := s.st.insert(ctx, userID, srv); err != nil {
			return Server{}, err
		}
		return srv, nil
	}
	tools, err := sess.listTools(ctx)
	if err != nil {
		return Server{}, err
	}
	srv.ToolCount = len(tools)
	if err := s.st.insert(ctx, userID, srv); err != nil {
		return Server{}, err
	}
	s.remember(srv.ID, tools)
	return srv, nil
}

// Update changes one of userID's servers. headers nil keeps the saved
// headers; otherwise it is the full new set, where an empty value for a
// header already saved keeps that saved value (values are never shown, so
// the UI can't send them back). A new address or headers are checked again
// (initialize + tools/list) before saving.
func (s *Service) Update(ctx context.Context, userID, id, name, rawURL string, headers map[string]string) (Server, error) {
	if userID == "" {
		return Server{}, &Error{Code: "unauthorized"}
	}
	existing, err := s.st.list(ctx, userID)
	if err != nil {
		return Server{}, err
	}
	var cur *Server
	for i := range existing {
		if existing[i].ID == id {
			cur = &existing[i]
			break
		}
	}
	if cur == nil {
		return Server{}, &Error{Code: "not_found"}
	}
	if cur.OAuth != nil {
		// Its client and everyone's sign-ins belong to this address:
		// only the name changes.
		if strings.TrimSpace(rawURL) != cur.URL {
			return Server{}, &Error{Code: "invalid_url", Detail: "an OAuth server's address can't change"}
		}
		headers = nil
	}
	merged := cur.Headers
	if headers != nil {
		merged = make(map[string]string, len(headers))
		for k, v := range headers {
			if strings.TrimSpace(v) == "" {
				if old, ok := cur.Headers[strings.TrimSpace(k)]; ok {
					v = old
				}
			}
			merged[k] = v
		}
	}
	name, u, merged, err := Normalize(name, rawURL, merged)
	if err != nil {
		return Server{}, err
	}
	for _, e := range existing {
		if e.ID != id && strings.EqualFold(e.Name, name) {
			return Server{}, &Error{Code: "name_taken"}
		}
	}
	next := *cur
	next.Name, next.URL, next.Headers = name, u, merged
	recheck := u != cur.URL || !sameHeaders(merged, cur.Headers)
	var tools []ToolDef
	if recheck {
		sess, err := dial(ctx, u, merged)
		if err != nil {
			return Server{}, err
		}
		if tools, err = sess.listTools(ctx); err != nil {
			return Server{}, err
		}
		next.ToolCount = len(tools)
	}
	n, err := s.st.update(ctx, userID, next)
	if err != nil {
		return Server{}, err
	}
	if n == 0 {
		return Server{}, &Error{Code: "not_found"}
	}
	if recheck {
		s.remember(id, tools)
	}
	return next, nil
}

func sameHeaders(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// Delete removes one of userID's servers.
func (s *Service) Delete(ctx context.Context, userID, id string) error {
	n, err := s.st.delete(ctx, userID, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return &Error{Code: "not_found"}
	}
	s.mu.Lock()
	for k := range s.tools {
		if k == id || strings.HasPrefix(k, id+"\x00") {
			delete(s.tools, k)
		}
	}
	s.mu.Unlock()
	s.forgetTokens(id)
	return nil
}

// ── offered on agents ──────────────────────────────────────────────────

// MaxAgentServers caps the servers offered on one agent.
const MaxAgentServers = MaxServers

// SetAgentServers makes ids (servers ownerID added) the custom MCP servers
// offered on agentID, replacing what it had. The caller checks ownerID
// made the agent.
func (s *Service) SetAgentServers(ctx context.Context, ownerID, agentID string, ids []string) ([]string, error) {
	if ownerID == "" || agentID == "" {
		return nil, &Error{Code: "unauthorized"}
	}
	own, err := s.st.list(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	mine := map[string]bool{}
	for _, srv := range own {
		mine[srv.ID] = true
	}
	seen := map[string]bool{}
	clean := []string{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		if !mine[id] {
			return nil, &Error{Code: "not_found", Detail: id}
		}
		seen[id] = true
		clean = append(clean, id)
	}
	if len(clean) > MaxAgentServers {
		return nil, &Error{Code: "too_many"}
	}
	if err := s.st.setAgentServers(ctx, agentID, clean); err != nil {
		return nil, err
	}
	return clean, nil
}

// MaxServerAgents caps the agents one server is offered on.
const MaxServerAgents = 200

// SetServerAgents makes agentIDs the agents ownerID's server id is offered
// on, replacing what it had. The caller checks ownerID made each agent.
func (s *Service) SetServerAgents(ctx context.Context, ownerID, id string, agentIDs []string) ([]string, error) {
	srv, owner, err := s.st.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if srv == nil || owner != ownerID || ownerID == "" {
		return nil, &Error{Code: "not_found"}
	}
	seen := map[string]bool{}
	clean := []string{}
	for _, a := range agentIDs {
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		clean = append(clean, a)
	}
	if len(clean) > MaxServerAgents {
		return nil, &Error{Code: "too_many"}
	}
	for _, a := range clean {
		ids, err := s.st.agentServerIDs(ctx, a)
		if err != nil {
			return nil, err
		}
		has := false
		for _, x := range ids {
			has = has || x == id
		}
		if !has && len(ids) >= MaxAgentServers {
			return nil, &Error{Code: "too_many"}
		}
	}
	if err := s.st.setServerAgents(ctx, id, clean); err != nil {
		return nil, err
	}
	return clean, nil
}

// AgentServerIDs are the servers offered on agentID.
func (s *Service) AgentServerIDs(ctx context.Context, agentID string) ([]string, error) {
	return s.st.agentServerIDs(ctx, agentID)
}

// AgentServers are the servers offered on agentID, oldest first.
func (s *Service) AgentServers(ctx context.Context, agentID string) ([]Server, error) {
	if agentID == "" {
		return nil, nil
	}
	return s.st.agentServers(ctx, agentID)
}

// ServerAgents maps each of ownerID's servers to the agents it's offered on.
func (s *Service) ServerAgents(ctx context.Context, ownerID string) (map[string][]string, error) {
	return s.st.serverAgents(ctx, ownerID)
}

// Server is one server by id, whoever added it (nil when there is none),
// and who added it.
func (s *Service) Server(ctx context.Context, id string) (*Server, string, error) {
	return s.st.get(ctx, id)
}

// toolsKey: an OAuth server lists what the person signed in may see.
func toolsKey(srv Server, userID string) string {
	if srv.OAuth == nil {
		return srv.ID
	}
	return tokenKey(srv.ID, userID)
}

func (s *Service) remember(id string, tools []ToolDef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, e := range s.tools {
		if now.Sub(e.at) > toolsTTL {
			delete(s.tools, k)
		}
	}
	s.tools[id] = toolsEntry{at: now, tools: tools}
}

// Tools lists a server's tools as userID sees them (cached for a few
// minutes). An OAuth server answers auth_needed until they sign in.
func (s *Service) Tools(ctx context.Context, srv Server, userID string) ([]ToolDef, error) {
	key := toolsKey(srv, userID)
	s.mu.Lock()
	e, ok := s.tools[key]
	s.mu.Unlock()
	if ok && time.Since(e.at) < toolsTTL {
		return e.tools, nil
	}
	sess, err := s.dialFor(ctx, srv, userID)
	if err != nil {
		return nil, err
	}
	tools, err := sess.listTools(ctx)
	if err != nil {
		return nil, err
	}
	s.remember(key, tools)
	return tools, nil
}

// Approve runs before a tool that may change data; an error stops it.
type Approve func(ctx context.Context, def ToolDef) error

// Call runs one tool. The definition is read from the server's current
// tool list, so a tool can't be called under another's read-only flag. A
// tool not marked read-only runs only when approve allows it (nil
// approve: refused).
func (s *Service) Call(ctx context.Context, srv Server, userID, tool string, args map[string]any, approve Approve) (string, error) {
	tools, err := s.Tools(ctx, srv, userID)
	if err != nil {
		return "", err
	}
	var def *ToolDef
	for i := range tools {
		if tools[i].Name == tool {
			def = &tools[i]
			break
		}
	}
	if def == nil {
		return "", &Error{Code: "tool_not_found", Detail: tool}
	}
	if !def.ReadOnly() {
		if approve == nil {
			return "", &Error{Code: "write_not_allowed", Detail: tool}
		}
		if err := approve(ctx, *def); err != nil {
			return "", err
		}
	}
	sess, err := s.dialFor(ctx, srv, userID)
	if err != nil {
		return "", err
	}
	return sess.callTool(ctx, tool, args)
}

// ── store ──────────────────────────────────────────────────────────────

var schema = []string{
	`CREATE TABLE IF NOT EXISTS user_mcp_servers (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		name TEXT NOT NULL,
		url TEXT NOT NULL,
		headers TEXT NOT NULL DEFAULT '{}',
		tool_count INTEGER NOT NULL DEFAULT 0,
		created_at BIGINT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS user_mcp_servers_user ON user_mcp_servers (user_id)`,
	// The servers offered on an agent (all added by whoever made it).
	`CREATE TABLE IF NOT EXISTS user_mcp_agent_servers (
		agent_id TEXT NOT NULL,
		server_id TEXT NOT NULL,
		PRIMARY KEY (agent_id, server_id)
	)`,
	`CREATE INDEX IF NOT EXISTS user_mcp_agent_servers_server ON user_mcp_agent_servers (server_id)`,
}

type sqlStore struct {
	db       *sql.DB
	postgres bool
}

func newSQLStore(ctx context.Context, d DB) (*sqlStore, error) {
	s := &sqlStore{db: d.DB(), postgres: d.Dialect() == "postgres"}
	for _, stmt := range append(schema, oauthSchema...) {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return nil, fmt.Errorf("usermcp schema: %w", err)
		}
	}
	if err := s.addOAuthColumn(ctx); err != nil {
		return nil, fmt.Errorf("usermcp schema: %w", err)
	}
	if err := s.moveOwnerTokens(ctx); err != nil {
		return nil, fmt.Errorf("usermcp tokens: %w", err)
	}
	return s, nil
}

func (s *sqlStore) rebind(q string) string {
	if !s.postgres {
		return q
	}
	var b strings.Builder
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

const serverCols = `s.id, s.name, s.url, s.headers, s.oauth, s.tool_count, s.created_at, s.user_id`

func scanServers(rows *sql.Rows) ([]Server, []string, error) {
	defer rows.Close()
	var out []Server
	var owners []string
	for rows.Next() {
		var srv Server
		var headers, oauth, owner string
		if err := rows.Scan(&srv.ID, &srv.Name, &srv.URL, &headers, &oauth, &srv.ToolCount, &srv.CreatedAt, &owner); err != nil {
			return nil, nil, err
		}
		srv.OAuth = decodeOAuth(oauth)
		srv.Headers = map[string]string{}
		_ = json.Unmarshal([]byte(headers), &srv.Headers)
		out = append(out, srv)
		owners = append(owners, owner)
	}
	return out, owners, rows.Err()
}

func (s *sqlStore) list(ctx context.Context, userID string) ([]Server, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`SELECT `+serverCols+` FROM user_mcp_servers s WHERE s.user_id = ? ORDER BY s.created_at, s.id`), userID)
	if err != nil {
		return nil, err
	}
	out, _, err := scanServers(rows)
	return out, err
}

func (s *sqlStore) get(ctx context.Context, id string) (*Server, string, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(`SELECT `+serverCols+` FROM user_mcp_servers s WHERE s.id = ?`), id)
	if err != nil {
		return nil, "", err
	}
	out, owners, err := scanServers(rows)
	if err != nil || len(out) == 0 {
		return nil, "", err
	}
	return &out[0], owners[0], nil
}

func (s *sqlStore) agentServers(ctx context.Context, agentID string) ([]Server, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`SELECT `+serverCols+` FROM user_mcp_servers s JOIN user_mcp_agent_servers a ON a.server_id = s.id
		 WHERE a.agent_id = ? ORDER BY s.created_at, s.id`), agentID)
	if err != nil {
		return nil, err
	}
	out, _, err := scanServers(rows)
	return out, err
}

func (s *sqlStore) agentServerIDs(ctx context.Context, agentID string) ([]string, error) {
	list, err := s.agentServers(ctx, agentID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(list))
	for _, srv := range list {
		ids = append(ids, srv.ID)
	}
	return ids, nil
}

func (s *sqlStore) serverAgents(ctx context.Context, ownerID string) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`SELECT a.server_id, a.agent_id FROM user_mcp_agent_servers a JOIN user_mcp_servers s ON s.id = a.server_id
		 WHERE s.user_id = ? ORDER BY a.agent_id`), ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var sid, aid string
		if err := rows.Scan(&sid, &aid); err != nil {
			return nil, err
		}
		out[sid] = append(out[sid], aid)
	}
	return out, rows.Err()
}

func (s *sqlStore) setServerAgents(ctx context.Context, serverID string, agentIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM user_mcp_agent_servers WHERE server_id = ?`), serverID); err != nil {
		return err
	}
	for _, a := range agentIDs {
		if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO user_mcp_agent_servers (agent_id, server_id) VALUES (?, ?)`), a, serverID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *sqlStore) setAgentServers(ctx context.Context, agentID string, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM user_mcp_agent_servers WHERE agent_id = ?`), agentID); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO user_mcp_agent_servers (agent_id, server_id) VALUES (?, ?)`), agentID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *sqlStore) insert(ctx context.Context, userID string, srv Server) error {
	headers, err := json.Marshal(srv.Headers)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, s.rebind(
		`INSERT INTO user_mcp_servers (id, user_id, name, url, headers, oauth, tool_count, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		srv.ID, userID, srv.Name, srv.URL, string(headers), encodeOAuth(srv.OAuth), srv.ToolCount, srv.CreatedAt)
	return err
}

func (s *sqlStore) update(ctx context.Context, userID string, srv Server) (int64, error) {
	headers, err := json.Marshal(srv.Headers)
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, s.rebind(
		`UPDATE user_mcp_servers SET name = ?, url = ?, headers = ?, oauth = ?, tool_count = ? WHERE user_id = ? AND id = ?`),
		srv.Name, srv.URL, string(headers), encodeOAuth(srv.OAuth), srv.ToolCount, userID, srv.ID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *sqlStore) delete(ctx context.Context, userID, id string) (int64, error) {
	res, err := s.db.ExecContext(ctx, s.rebind(`DELETE FROM user_mcp_servers WHERE user_id = ? AND id = ?`), userID, id)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return n, err
	}
	// Its offers on agents and everyone's sign-ins go with it.
	for _, q := range []string{
		`DELETE FROM user_mcp_agent_servers WHERE server_id = ?`,
		`DELETE FROM user_mcp_tokens WHERE server_id = ?`,
		`DELETE FROM user_mcp_oauth_pending WHERE server_id = ?`,
	} {
		if _, err := s.db.ExecContext(ctx, s.rebind(q), id); err != nil {
			return n, err
		}
	}
	return n, nil
}
