// Package usermcp holds the MCP servers a person added themselves
// (Console / WeClaw → Connectors → Custom MCP). A server belongs to the
// one user who added it: only that user's own turns can use it, under the
// same rules as their connected accounts (docs/connectors.md).
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
	ToolCount int               `json:"toolCount"`
	CreatedAt int64             `json:"createdAt"`
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

// Add checks the server answers as MCP (initialize + tools/list), then
// saves it for userID.
func (s *Service) Add(ctx context.Context, userID, name, rawURL string, headers map[string]string) (Server, error) {
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
	sess, err := dial(ctx, u, headers)
	if err != nil {
		return Server{}, err
	}
	tools, err := sess.listTools(ctx)
	if err != nil {
		return Server{}, err
	}
	srv := Server{ID: "mcp_" + strings.ReplaceAll(uuid.NewString(), "-", ""), Name: name, URL: u, Headers: headers, ToolCount: len(tools), CreatedAt: time.Now().UnixMilli()}
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
	delete(s.tools, id)
	s.mu.Unlock()
	return nil
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

// Tools lists a server's tools (cached for a few minutes).
func (s *Service) Tools(ctx context.Context, srv Server) ([]ToolDef, error) {
	s.mu.Lock()
	e, ok := s.tools[srv.ID]
	s.mu.Unlock()
	if ok && time.Since(e.at) < toolsTTL {
		return e.tools, nil
	}
	sess, err := dial(ctx, srv.URL, srv.Headers)
	if err != nil {
		return nil, err
	}
	tools, err := sess.listTools(ctx)
	if err != nil {
		return nil, err
	}
	s.remember(srv.ID, tools)
	return tools, nil
}

// Approve runs before a tool that may change data; an error stops it.
type Approve func(ctx context.Context, def ToolDef) error

// Call runs one tool. The definition is read from the server's current
// tool list, so a tool can't be called under another's read-only flag. A
// tool not marked read-only runs only when approve allows it (nil
// approve: refused).
func (s *Service) Call(ctx context.Context, srv Server, tool string, args map[string]any, approve Approve) (string, error) {
	tools, err := s.Tools(ctx, srv)
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
	sess, err := dial(ctx, srv.URL, srv.Headers)
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
}

type sqlStore struct {
	db       *sql.DB
	postgres bool
}

func newSQLStore(ctx context.Context, d DB) (*sqlStore, error) {
	s := &sqlStore{db: d.DB(), postgres: d.Dialect() == "postgres"}
	for _, stmt := range schema {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return nil, fmt.Errorf("usermcp schema: %w", err)
		}
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

func (s *sqlStore) list(ctx context.Context, userID string) ([]Server, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`SELECT id, name, url, headers, tool_count, created_at FROM user_mcp_servers WHERE user_id = ? ORDER BY created_at, id`), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Server
	for rows.Next() {
		var srv Server
		var headers string
		if err := rows.Scan(&srv.ID, &srv.Name, &srv.URL, &headers, &srv.ToolCount, &srv.CreatedAt); err != nil {
			return nil, err
		}
		srv.Headers = map[string]string{}
		_ = json.Unmarshal([]byte(headers), &srv.Headers)
		out = append(out, srv)
	}
	return out, rows.Err()
}

func (s *sqlStore) insert(ctx context.Context, userID string, srv Server) error {
	headers, err := json.Marshal(srv.Headers)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, s.rebind(
		`INSERT INTO user_mcp_servers (id, user_id, name, url, headers, tool_count, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`),
		srv.ID, userID, srv.Name, srv.URL, string(headers), srv.ToolCount, srv.CreatedAt)
	return err
}

func (s *sqlStore) update(ctx context.Context, userID string, srv Server) (int64, error) {
	headers, err := json.Marshal(srv.Headers)
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, s.rebind(
		`UPDATE user_mcp_servers SET name = ?, url = ?, headers = ?, tool_count = ? WHERE user_id = ? AND id = ?`),
		srv.Name, srv.URL, string(headers), srv.ToolCount, userID, srv.ID)
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
	return res.RowsAffected()
}
