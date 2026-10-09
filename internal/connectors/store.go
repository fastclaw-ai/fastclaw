package connectors

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DB is what this package needs from the main store: the raw handle and
// its SQL dialect ("sqlite" or "postgres"). store.DBStore satisfies it.
type DB interface {
	DB() *sql.DB
	Dialect() string
}

// Times are unix milliseconds, so the schema is the same on SQLite and
// Postgres.
var schema = []string{
	// A subject's preferences for one of its connections. The connection
	// itself lives in Connany.
	`CREATE TABLE IF NOT EXISTS connector_accounts (
		subject TEXT NOT NULL,
		connection_id TEXT NOT NULL,
		connector TEXT NOT NULL,
		display_name TEXT NOT NULL DEFAULT '',
		is_default INTEGER NOT NULL DEFAULT 0,
		updated_at BIGINT NOT NULL DEFAULT 0,
		PRIMARY KEY (subject, connection_id)
	)`,
	// One authorization a person opened, from Settings or from a card an
	// agent showed in a conversation (agent_id + session_id: the chat to
	// wake once it succeeds).
	`CREATE TABLE IF NOT EXISTS connector_requests (
		id TEXT PRIMARY KEY,
		subject TEXT NOT NULL,
		user_id TEXT NOT NULL,
		connector TEXT NOT NULL,
		kind TEXT NOT NULL,
		target_connection_id TEXT NOT NULL DEFAULT '',
		agent_id TEXT NOT NULL DEFAULT '',
		session_id TEXT NOT NULL DEFAULT '',
		provider_session_id TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		error_code TEXT NOT NULL DEFAULT '',
		connection_id TEXT NOT NULL DEFAULT '',
		access_baseline INTEGER NOT NULL DEFAULT 0,
		expires_at BIGINT NOT NULL DEFAULT 0,
		checked_at BIGINT NOT NULL DEFAULT 0,
		notified_at BIGINT NOT NULL DEFAULT 0,
		created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS connector_requests_subject ON connector_requests (subject, status)`,
	`CREATE INDEX IF NOT EXISTS connector_requests_provider ON connector_requests (provider_session_id)`,
}

type sqlStore struct {
	db       *sql.DB
	postgres bool
}

func newSQLStore(ctx context.Context, d DB) (*sqlStore, error) {
	s := &sqlStore{db: d.DB(), postgres: d.Dialect() == "postgres"}
	for _, stmt := range schema {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return nil, fmt.Errorf("connectors schema: %w", err)
		}
	}
	return s, nil
}

// rebind turns ? placeholders into $n on Postgres.
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

func (s *sqlStore) exec(ctx context.Context, q string, args ...any) (int64, error) {
	res, err := s.db.ExecContext(ctx, s.rebind(q), args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func nowMS() int64 { return time.Now().UnixMilli() }

type accountPref struct {
	ConnectionID string
	Connector    string
	DisplayName  string
	IsDefault    bool
}

func (s *sqlStore) prefs(ctx context.Context, subject string) (map[string]accountPref, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`SELECT connection_id, connector, display_name, is_default FROM connector_accounts WHERE subject = ?`), subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]accountPref{}
	for rows.Next() {
		var p accountPref
		var def int
		if err := rows.Scan(&p.ConnectionID, &p.Connector, &p.DisplayName, &def); err != nil {
			return nil, err
		}
		p.IsDefault = def != 0
		out[p.ConnectionID] = p
	}
	return out, rows.Err()
}

// setPref upserts one account's preferences. Making it the default clears
// the flag on the subject's other accounts of the same connector.
func (s *sqlStore) setPref(ctx context.Context, subject, connectionID, connector string, displayName *string, isDefault *bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowMS()
	if isDefault != nil && *isDefault {
		if _, err := tx.ExecContext(ctx, s.rebind(
			`UPDATE connector_accounts SET is_default = 0, updated_at = ? WHERE subject = ? AND connector = ? AND connection_id <> ?`),
			now, subject, connector, connectionID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, s.rebind(
		`INSERT INTO connector_accounts (subject, connection_id, connector, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (subject, connection_id) DO NOTHING`), subject, connectionID, connector, now); err != nil {
		return err
	}
	if displayName != nil {
		if _, err := tx.ExecContext(ctx, s.rebind(
			`UPDATE connector_accounts SET display_name = ?, updated_at = ? WHERE subject = ? AND connection_id = ?`),
			*displayName, now, subject, connectionID); err != nil {
			return err
		}
	}
	if isDefault != nil {
		def := 0
		if *isDefault {
			def = 1
		}
		if _, err := tx.ExecContext(ctx, s.rebind(
			`UPDATE connector_accounts SET is_default = ?, updated_at = ? WHERE subject = ? AND connection_id = ?`),
			def, now, subject, connectionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *sqlStore) deletePref(ctx context.Context, subject, connectionID string) error {
	_, err := s.exec(ctx, `DELETE FROM connector_accounts WHERE subject = ? AND connection_id = ?`, subject, connectionID)
	return err
}

// request is one row of connector_requests.
type request struct {
	ID                 string
	Subject            string
	UserID             string
	Connector          string
	Kind               string // connect | reconnect | access
	TargetConnectionID string
	AgentID            string
	SessionID          string
	ProviderSessionID  string
	Status             string // requested | pending | connected | granted | error | expired | cancelled
	ErrorCode          string
	ConnectionID       string
	AccessBaseline     int
	ExpiresAt          int64
	CheckedAt          int64
	NotifiedAt         int64
	CreatedAt          int64
	UpdatedAt          int64
}

const requestCols = `id, subject, user_id, connector, kind, target_connection_id, agent_id, session_id,
	provider_session_id, status, error_code, connection_id, access_baseline, expires_at, checked_at,
	notified_at, created_at, updated_at`

func scanRequest(row interface{ Scan(...any) error }) (*request, error) {
	var r request
	err := row.Scan(&r.ID, &r.Subject, &r.UserID, &r.Connector, &r.Kind, &r.TargetConnectionID, &r.AgentID,
		&r.SessionID, &r.ProviderSessionID, &r.Status, &r.ErrorCode, &r.ConnectionID, &r.AccessBaseline,
		&r.ExpiresAt, &r.CheckedAt, &r.NotifiedAt, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, newError("not_found")
	}
	return &r, err
}

func (s *sqlStore) insertRequest(ctx context.Context, r *request) error {
	_, err := s.exec(ctx, `INSERT INTO connector_requests (`+requestCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Subject, r.UserID, r.Connector, r.Kind, r.TargetConnectionID, r.AgentID, r.SessionID,
		r.ProviderSessionID, r.Status, r.ErrorCode, r.ConnectionID, r.AccessBaseline, r.ExpiresAt,
		r.CheckedAt, r.NotifiedAt, r.CreatedAt, r.UpdatedAt)
	return err
}

// getRequest loads a request only for the subject + person who started it.
func (s *sqlStore) getRequest(ctx context.Context, subject, userID, id string) (*request, error) {
	return scanRequest(s.db.QueryRowContext(ctx, s.rebind(
		`SELECT `+requestCols+` FROM connector_requests WHERE id = ? AND subject = ? AND user_id = ?`), id, subject, userID))
}

func (s *sqlStore) getRequestByProvider(ctx context.Context, subject, userID, providerID string) (*request, error) {
	return scanRequest(s.db.QueryRowContext(ctx, s.rebind(
		`SELECT `+requestCols+` FROM connector_requests WHERE provider_session_id = ? AND subject = ? AND user_id = ?`),
		providerID, subject, userID))
}

// update applies set to the row, only while its status is one of from
// (empty: any). Reports whether the row changed — the "once" guard when
// two tabs confirm the same authorization.
func (s *sqlStore) update(ctx context.Context, id string, from []string, set map[string]any) (bool, error) {
	cols := make([]string, 0, len(set)+1)
	args := make([]any, 0, len(set)+len(from)+2)
	for k, v := range set {
		cols = append(cols, k+" = ?")
		args = append(args, v)
	}
	cols = append(cols, "updated_at = ?")
	args = append(args, nowMS())
	q := `UPDATE connector_requests SET ` + strings.Join(cols, ", ") + ` WHERE id = ?`
	args = append(args, id)
	if len(from) > 0 {
		q += ` AND status IN (?` + strings.Repeat(", ?", len(from)-1) + `)`
		for _, f := range from {
			args = append(args, f)
		}
	}
	n, err := s.exec(ctx, q, args...)
	return n > 0, err
}

// claimNotify sets notified_at once; false when already notified.
func (s *sqlStore) claimNotify(ctx context.Context, id string) (bool, error) {
	n, err := s.exec(ctx, `UPDATE connector_requests SET notified_at = ? WHERE id = ? AND notified_at = 0`, nowMS(), id)
	return n > 0, err
}

// everConnected lists connectors the subject connected at some point.
func (s *sqlStore) everConnected(ctx context.Context, subject string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`SELECT DISTINCT connector FROM connector_requests WHERE subject = ? AND status IN ('connected', 'granted')`), subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
