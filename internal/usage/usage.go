// Package usage records LLM token consumption per (day, user, agent,
// session, model) and answers admin "who burned what" queries.
//
// The data flows in one direction: every successful provider.Chat /
// ChatStream call lands one RecordTokens() invocation; the admin
// dashboard reads aggregates back out via Top* / Totals.
//
// Two implementations exist:
//   - MemMeter: in-process map. Cheap, loses state on restart. Useful
//     for unit tests and stand-alone dev runs.
//   - SQLMeter: UPSERTs into token_usage_daily on the same DB the
//     Store uses. This is what the prod admin endpoint reads from.
//
// Empty user_id (admin-owned / cron-fired agents) is preserved on
// write — handlers render it as "system" on the way out.
package usage

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Tokens is one Chat call's token accounting. Mirrors provider.Usage
// but lives here so the usage package doesn't depend on provider.
// RequestCount is always 1 per call; the meter accumulates it.
type Tokens struct {
	Input         int
	Output        int
	CacheRead     int
	CacheCreation int
}

// Total returns input + output + cache (everything the model billed for).
func (t Tokens) Total() int64 {
	return int64(t.Input) + int64(t.Output) + int64(t.CacheRead) + int64(t.CacheCreation)
}

// Range is a UTC half-open day window used for queries. Both ends are
// inclusive at day granularity (the meter stores one row per day).
type Range struct {
	Since time.Time // first day to include (UTC, day-truncated)
	Until time.Time // last day to include  (UTC, day-truncated)
}

// LastN returns the [today-(n-1) … today] range so callers can ask for
// "last 1/7/30 days" without thinking about timezones.
func LastN(n int) Range {
	today := dayBucket(time.Now())
	return Range{Since: today.AddDate(0, 0, -(n - 1)), Until: today}
}

// Totals is the headline numbers for a range: one row per kind, plus
// total request_count across the window.
type Totals struct {
	Input         int64 `json:"inputTokens"`
	Output        int64 `json:"outputTokens"`
	CacheRead     int64 `json:"cacheReadTokens"`
	CacheCreation int64 `json:"cacheCreationTokens"`
	Requests      int64 `json:"requestCount"`
}

// Rank is one row of a per-agent or per-user leaderboard.
type Rank struct {
	Key      string `json:"key"`   // agent_id or user_id ("" → "system" on render)
	Tokens   int64  `json:"tokens"` // input+output+cache combined
	Input    int64  `json:"inputTokens"`
	Output   int64  `json:"outputTokens"`
	Requests int64  `json:"requestCount"`
}

// Meter is the recording + readback interface.
type Meter interface {
	// RecordTokens adds one Chat call's token counts onto the
	// (today, userID, agentID, sessionKey, provider, model) bucket.
	// provider is the per-agent override key (e.g.
	// "anthropic-messages") or "" when the agent uses the shared
	// provider; model is the bare model id with no prefix. Splitting
	// the two so the dashboard can answer "tokens by provider"
	// without parsing "<prov>/<model>" strings in SQL. Zero counts
	// still bump request_count so we can answer "how many calls".
	RecordTokens(ctx context.Context, userID, agentID, sessionKey, provider, model string, t Tokens) error
	// Totals returns the aggregate token counts for a range.
	Totals(ctx context.Context, r Range) (Totals, error)
	// TopAgents returns the top-N agents by total tokens.
	TopAgents(ctx context.Context, r Range, limit int) ([]Rank, error)
	// TopUsers returns the top-N users by total tokens.
	TopUsers(ctx context.Context, r Range, limit int) ([]Rank, error)
	// SessionsForAgent returns per-session token rollups for one
	// agent. Backs the per-agent "Token Usage" tab — owner asks
	// "which of my chats burned the most"; the table is the answer.
	// Optional userID scopes to one chatter (useful when the agent
	// is public and you only want your own sessions); pass "" to
	// include all chatters.
	SessionsForAgent(ctx context.Context, agentID, userID string, r Range, limit int) ([]Rank, error)
	// TotalsForUser returns aggregate token counts scoped to one user.
	// Used by quota enforcement and the /v1/usage API.
	TotalsForUser(ctx context.Context, userID string, r Range) (Totals, error)
	// DailyForUser returns per-day, per-agent token breakdowns for
	// one user. Backs the GET /v1/usage API so upstream SaaS apps
	// can pull detailed consumption.
	DailyForUser(ctx context.Context, userID string, r Range) ([]DailyUsage, error)
	// Query returns per-day rows and totals for an app-facing usage
	// query (GET /v1/usage): one user, or an app owner plus every
	// end-user (app_user) it minted, optionally narrowed to one agent.
	Query(ctx context.Context, q Query) ([]DailyUsage, Totals, error)
	// RecordTokenLog appends one row per LLM call to token_usage_log.
	// Unlike RecordTokens (which UPSERTs into daily buckets), this is
	// append-only so every call is individually auditable. durationMs
	// is the wall-clock time of the provider.Chat call. costUSD is the
	// call's cost at the model's unit price, nil when the model has none.
	RecordTokenLog(ctx context.Context, userID, agentID, sessionKey, provider, model string, t Tokens, costUSD *float64, durationMs int64) error
	Close() error
}

// Query selects usage rows for Meter.Query.
type Query struct {
	// UserID restricts rows to one user (an app owner or one app_user).
	UserID string
	// AppOwnerID, when UserID is empty, selects the app owner's own rows
	// plus the rows of every app_user owned by it.
	AppOwnerID string
	// AgentID optionally restricts rows to one agent.
	AgentID string
	Range   Range
}

// DailyUsage is one day+agent row returned by DailyForUser and Query.
type DailyUsage struct {
	Day     string `json:"day"`
	AgentID string `json:"agentId"`
	// UserID is the FastClaw user the tokens were billed to; EndUser is
	// the integrating app's external id for it when it is an app_user.
	// Both are only filled in by Query.
	UserID        string `json:"userId,omitempty"`
	EndUser       string `json:"endUser,omitempty"`
	Model         string `json:"model"`
	InputTokens   int64  `json:"inputTokens"`
	OutputTokens  int64  `json:"outputTokens"`
	CacheRead     int64  `json:"cacheReadTokens"`
	CacheCreation int64  `json:"cacheCreationTokens"`
	Requests      int64  `json:"requestCount"`
}

// dayBucket truncates a time to UTC midnight. Exported indirectly via
// LastN; tests can call it through the helper.
func dayBucket(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// --------------------------------------------------------------------
// MemMeter
// --------------------------------------------------------------------

type memKey struct {
	day        time.Time
	userID     string
	agentID    string
	sessionKey string
	provider   string
	model      string
}

type memCell struct {
	input, output, cacheRead, cacheCreate int64
	requests                              int64
}

// MemMeter keeps everything in a map. Lost on process restart, which is
// fine for dev / tests but useless for the admin dashboard in prod.
type MemMeter struct {
	mu   sync.Mutex
	data map[memKey]*memCell
}

func NewMemMeter() *MemMeter {
	return &MemMeter{data: make(map[memKey]*memCell)}
}

func (m *MemMeter) RecordTokens(_ context.Context, userID, agentID, sessionKey, provider, model string, t Tokens) error {
	k := memKey{
		day:        dayBucket(time.Now()),
		userID:     userID,
		agentID:    agentID,
		sessionKey: sessionKey,
		provider:   provider,
		model:      model,
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.data[k]
	if !ok {
		c = &memCell{}
		m.data[k] = c
	}
	c.input += int64(t.Input)
	c.output += int64(t.Output)
	c.cacheRead += int64(t.CacheRead)
	c.cacheCreate += int64(t.CacheCreation)
	c.requests++
	return nil
}

func (m *MemMeter) Totals(_ context.Context, r Range) (Totals, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out Totals
	for k, c := range m.data {
		if !inRange(k.day, r) {
			continue
		}
		out.Input += c.input
		out.Output += c.output
		out.CacheRead += c.cacheRead
		out.CacheCreation += c.cacheCreate
		out.Requests += c.requests
	}
	return out, nil
}

func (m *MemMeter) TopAgents(_ context.Context, r Range, limit int) ([]Rank, error) {
	return m.rank(r, limit, func(k memKey) string { return k.agentID })
}

func (m *MemMeter) TopUsers(_ context.Context, r Range, limit int) ([]Rank, error) {
	return m.rank(r, limit, func(k memKey) string { return k.userID })
}

func (m *MemMeter) SessionsForAgent(_ context.Context, agentID, userID string, r Range, limit int) ([]Rank, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	agg := map[string]*Rank{}
	for k, c := range m.data {
		if k.agentID != agentID {
			continue
		}
		if userID != "" && k.userID != userID {
			continue
		}
		if !inRange(k.day, r) {
			continue
		}
		row, ok := agg[k.sessionKey]
		if !ok {
			row = &Rank{Key: k.sessionKey}
			agg[k.sessionKey] = row
		}
		row.Input += c.input
		row.Output += c.output
		row.Tokens += c.input + c.output + c.cacheRead + c.cacheCreate
		row.Requests += c.requests
	}
	out := make([]Rank, 0, len(agg))
	for _, v := range agg {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tokens > out[j].Tokens })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemMeter) rank(r Range, limit int, key func(memKey) string) ([]Rank, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	agg := map[string]*Rank{}
	for k, c := range m.data {
		if !inRange(k.day, r) {
			continue
		}
		id := key(k)
		row, ok := agg[id]
		if !ok {
			row = &Rank{Key: id}
			agg[id] = row
		}
		row.Input += c.input
		row.Output += c.output
		row.Tokens += c.input + c.output + c.cacheRead + c.cacheCreate
		row.Requests += c.requests
	}
	out := make([]Rank, 0, len(agg))
	for _, v := range agg {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tokens > out[j].Tokens })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemMeter) TotalsForUser(_ context.Context, userID string, r Range) (Totals, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out Totals
	for k, c := range m.data {
		if k.userID != userID || !inRange(k.day, r) {
			continue
		}
		out.Input += c.input
		out.Output += c.output
		out.CacheRead += c.cacheRead
		out.CacheCreation += c.cacheCreate
		out.Requests += c.requests
	}
	return out, nil
}

func (m *MemMeter) DailyForUser(_ context.Context, userID string, r Range) ([]DailyUsage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	type groupKey struct {
		day     string
		agentID string
		model   string
	}
	agg := map[groupKey]*DailyUsage{}
	for k, c := range m.data {
		if k.userID != userID || !inRange(k.day, r) {
			continue
		}
		gk := groupKey{day: k.day.Format("2006-01-02"), agentID: k.agentID, model: k.model}
		row, ok := agg[gk]
		if !ok {
			row = &DailyUsage{Day: gk.day, AgentID: gk.agentID, Model: gk.model}
			agg[gk] = row
		}
		row.InputTokens += c.input
		row.OutputTokens += c.output
		row.CacheRead += c.cacheRead
		row.CacheCreation += c.cacheCreate
		row.Requests += c.requests
	}
	out := make([]DailyUsage, 0, len(agg))
	for _, v := range agg {
		out = append(out, *v)
	}
	return out, nil
}

// Query on MemMeter has no users table, so AppOwnerID matches only the
// owner's own rows and EndUser is never filled in.
func (m *MemMeter) Query(_ context.Context, q Query) ([]DailyUsage, Totals, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	uid := q.UserID
	if uid == "" {
		uid = q.AppOwnerID
	}
	type groupKey struct {
		day, agentID, userID, model string
	}
	agg := map[groupKey]*DailyUsage{}
	var tot Totals
	for k, c := range m.data {
		if k.userID != uid || !inRange(k.day, q.Range) || (q.AgentID != "" && k.agentID != q.AgentID) {
			continue
		}
		gk := groupKey{day: k.day.Format("2006-01-02"), agentID: k.agentID, userID: k.userID, model: k.model}
		row, ok := agg[gk]
		if !ok {
			row = &DailyUsage{Day: gk.day, AgentID: gk.agentID, UserID: gk.userID, Model: gk.model}
			agg[gk] = row
		}
		row.InputTokens += c.input
		row.OutputTokens += c.output
		row.CacheRead += c.cacheRead
		row.CacheCreation += c.cacheCreate
		row.Requests += c.requests
		tot.Input += c.input
		tot.Output += c.output
		tot.CacheRead += c.cacheRead
		tot.CacheCreation += c.cacheCreate
		tot.Requests += c.requests
	}
	out := make([]DailyUsage, 0, len(agg))
	for _, v := range agg {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Day != out[j].Day {
			return out[i].Day > out[j].Day
		}
		return out[i].AgentID < out[j].AgentID
	})
	return out, tot, nil
}

func (m *MemMeter) RecordTokenLog(_ context.Context, _, _, _, _, _ string, _ Tokens, _ *float64, _ int64) error {
	return nil // MemMeter doesn't persist logs
}

func (m *MemMeter) Close() error { return nil }

func inRange(day time.Time, r Range) bool {
	if !r.Since.IsZero() && day.Before(r.Since) {
		return false
	}
	if !r.Until.IsZero() && day.After(r.Until) {
		return false
	}
	return true
}

// --------------------------------------------------------------------
// SQLMeter
// --------------------------------------------------------------------

// SQLMeter writes to token_usage_daily using UPSERT semantics. Works on
// both SQLite and Postgres — they both support
// `INSERT … ON CONFLICT (…) DO UPDATE SET …` with the same column-ref
// form.
//
// The table schema is owned by store/database.go's migration block (see
// migrateTokenUsageDaily). SQLMeter is a thin query layer on top.
type SQLMeter struct {
	db      *sql.DB
	dialect string // "postgres" | "sqlite"
}

// NewSQLMeter wraps an open *sql.DB. The caller (gateway boot) supplies
// the same db+dialect the Store was built on so we share the connection
// pool and respect SetMaxOpenConns tuning.
func NewSQLMeter(db *sql.DB, dialect string) *SQLMeter {
	return &SQLMeter{db: db, dialect: dialect}
}

func (s *SQLMeter) Close() error { return nil } // pool owned by store

// placeholders generates $1,$2,… for postgres and ?,?,… for sqlite.
func (s *SQLMeter) ph(i int) string {
	if s.dialect == "postgres" {
		return fmt.Sprintf("$%d", i)
	}
	return "?"
}

// rebind rewrites a query written with ? placeholders to $1..$N when
// running on postgres. Keeps query strings readable.
func (s *SQLMeter) rebind(q string) string {
	if s.dialect != "postgres" {
		return q
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
			continue
		}
		b.WriteByte(q[i])
	}
	return b.String()
}

// dayParam returns the value to bind for a day column. SQLite stores
// DATE as TEXT 'YYYY-MM-DD'; Postgres accepts time.Time directly.
func (s *SQLMeter) dayParam(t time.Time) any {
	if s.dialect == "sqlite" {
		return t.Format("2006-01-02")
	}
	return t
}

func (s *SQLMeter) RecordTokens(ctx context.Context, userID, agentID, sessionKey, provider, model string, t Tokens) error {
	day := s.dayParam(dayBucket(time.Now()))
	channel := store.ChannelFromContext(ctx)
	chatterUID := store.ChatterUserIDFromContext(ctx)
	// Both dialects support this six-column conflict target and the
	// EXCLUDED reference. We additionally bump request_count by 1.
	// channel + chatter_user_id are informational — not part of the PK.
	q := s.rebind(`
		INSERT INTO token_usage_daily
			(day, user_id, agent_id, session_key, provider, model,
			 input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, request_count,
			 channel, chatter_user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT (day, user_id, agent_id, session_key, provider, model) DO UPDATE SET
			input_tokens         = token_usage_daily.input_tokens         + EXCLUDED.input_tokens,
			output_tokens        = token_usage_daily.output_tokens        + EXCLUDED.output_tokens,
			cache_read_tokens    = token_usage_daily.cache_read_tokens    + EXCLUDED.cache_read_tokens,
			cache_create_tokens  = token_usage_daily.cache_create_tokens  + EXCLUDED.cache_create_tokens,
			request_count        = token_usage_daily.request_count        + 1,
			channel              = EXCLUDED.channel,
			chatter_user_id      = EXCLUDED.chatter_user_id`)
	_, err := s.db.ExecContext(ctx, q,
		day, userID, agentID, sessionKey, provider, model,
		t.Input, t.Output, t.CacheRead, t.CacheCreation,
		channel, chatterUID,
	)
	return err
}

func (s *SQLMeter) Totals(ctx context.Context, r Range) (Totals, error) {
	q := s.rebind(`
		SELECT
			COALESCE(SUM(input_tokens),0),
			COALESCE(SUM(output_tokens),0),
			COALESCE(SUM(cache_read_tokens),0),
			COALESCE(SUM(cache_create_tokens),0),
			COALESCE(SUM(request_count),0)
		FROM token_usage_daily
		WHERE day BETWEEN ? AND ?`)
	row := s.db.QueryRowContext(ctx, q, s.dayParam(r.Since), s.dayParam(r.Until))
	var out Totals
	if err := row.Scan(&out.Input, &out.Output, &out.CacheRead, &out.CacheCreation, &out.Requests); err != nil {
		return Totals{}, err
	}
	return out, nil
}

func (s *SQLMeter) TopAgents(ctx context.Context, r Range, limit int) ([]Rank, error) {
	return s.topBy(ctx, r, limit, "agent_id")
}

func (s *SQLMeter) TopUsers(ctx context.Context, r Range, limit int) ([]Rank, error) {
	return s.topBy(ctx, r, limit, "user_id")
}

func (s *SQLMeter) SessionsForAgent(ctx context.Context, agentID, userID string, r Range, limit int) ([]Rank, error) {
	if limit <= 0 {
		limit = 50
	}
	// userID is optional — when blank we don't constrain on it. Two
	// variants of the query keep the prepared statement form clean
	// rather than building NULL-checks into the WHERE.
	if userID == "" {
		q := s.rebind(`
			SELECT session_key AS key,
				COALESCE(SUM(input_tokens + output_tokens + cache_read_tokens + cache_create_tokens),0) AS tokens,
				COALESCE(SUM(input_tokens),0)  AS input_tokens,
				COALESCE(SUM(output_tokens),0) AS output_tokens,
				COALESCE(SUM(request_count),0) AS requests
			FROM token_usage_daily
			WHERE agent_id = ? AND day BETWEEN ? AND ?
			GROUP BY session_key
			ORDER BY tokens DESC
			LIMIT ?`)
		return s.scanRanks(ctx, q, agentID, s.dayParam(r.Since), s.dayParam(r.Until), limit)
	}
	q := s.rebind(`
		SELECT session_key AS key,
			COALESCE(SUM(input_tokens + output_tokens + cache_read_tokens + cache_create_tokens),0) AS tokens,
			COALESCE(SUM(input_tokens),0)  AS input_tokens,
			COALESCE(SUM(output_tokens),0) AS output_tokens,
			COALESCE(SUM(request_count),0) AS requests
		FROM token_usage_daily
		WHERE agent_id = ? AND user_id = ? AND day BETWEEN ? AND ?
		GROUP BY session_key
		ORDER BY tokens DESC
		LIMIT ?`)
	return s.scanRanks(ctx, q, agentID, userID, s.dayParam(r.Since), s.dayParam(r.Until), limit)
}

// scanRanks is the shared row-iterator for SessionsForAgent and
// topBy — they only differ in WHERE/GROUP BY, so the scan boilerplate
// is factored out.
func (s *SQLMeter) scanRanks(ctx context.Context, q string, args ...any) ([]Rank, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rank
	for rows.Next() {
		var r Rank
		if err := rows.Scan(&r.Key, &r.Tokens, &r.Input, &r.Output, &r.Requests); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLMeter) TotalsForUser(ctx context.Context, userID string, r Range) (Totals, error) {
	q := s.rebind(`
		SELECT
			COALESCE(SUM(input_tokens),0),
			COALESCE(SUM(output_tokens),0),
			COALESCE(SUM(cache_read_tokens),0),
			COALESCE(SUM(cache_create_tokens),0),
			COALESCE(SUM(request_count),0)
		FROM token_usage_daily
		WHERE user_id = ? AND day BETWEEN ? AND ?`)
	row := s.db.QueryRowContext(ctx, q, userID, s.dayParam(r.Since), s.dayParam(r.Until))
	var out Totals
	if err := row.Scan(&out.Input, &out.Output, &out.CacheRead, &out.CacheCreation, &out.Requests); err != nil {
		return Totals{}, err
	}
	return out, nil
}

func (s *SQLMeter) DailyForUser(ctx context.Context, userID string, r Range) ([]DailyUsage, error) {
	q := s.rebind(`
		SELECT
			day,
			agent_id,
			model,
			COALESCE(SUM(input_tokens),0),
			COALESCE(SUM(output_tokens),0),
			COALESCE(SUM(cache_read_tokens),0),
			COALESCE(SUM(cache_create_tokens),0),
			COALESCE(SUM(request_count),0)
		FROM token_usage_daily
		WHERE user_id = ? AND day BETWEEN ? AND ?
		GROUP BY day, agent_id, model
		ORDER BY day DESC, agent_id`)
	rows, err := s.db.QueryContext(ctx, q, userID, s.dayParam(r.Since), s.dayParam(r.Until))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DailyUsage
	for rows.Next() {
		var d DailyUsage
		var day time.Time
		if err := rows.Scan(&day, &d.AgentID, &d.Model, &d.InputTokens, &d.OutputTokens, &d.CacheRead, &d.CacheCreation, &d.Requests); err != nil {
			return nil, err
		}
		d.Day = day.Format("2006-01-02")
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *SQLMeter) Query(ctx context.Context, q Query) ([]DailyUsage, Totals, error) {
	var where []string
	var args []any
	switch {
	case q.UserID != "":
		where = append(where, "t.user_id = ?")
		args = append(args, q.UserID)
	case q.AppOwnerID != "":
		where = append(where, "(t.user_id = ? OR t.user_id IN (SELECT id FROM users WHERE owner_user_id = ? AND role = 'app_user'))")
		args = append(args, q.AppOwnerID, q.AppOwnerID)
	default:
		return nil, Totals{}, fmt.Errorf("usage.Query: UserID or AppOwnerID required")
	}
	if q.AgentID != "" {
		where = append(where, "t.agent_id = ?")
		args = append(args, q.AgentID)
	}
	where = append(where, "t.day BETWEEN ? AND ?")
	args = append(args, s.dayParam(q.Range.Since), s.dayParam(q.Range.Until))
	query := s.rebind(`
		SELECT
			t.day,
			t.agent_id,
			t.user_id,
			COALESCE(u.external_id, ''),
			t.model,
			COALESCE(SUM(t.input_tokens),0),
			COALESCE(SUM(t.output_tokens),0),
			COALESCE(SUM(t.cache_read_tokens),0),
			COALESCE(SUM(t.cache_create_tokens),0),
			COALESCE(SUM(t.request_count),0)
		FROM token_usage_daily t
		LEFT JOIN users u ON u.id = t.user_id AND u.role = 'app_user'
		WHERE ` + strings.Join(where, " AND ") + `
		GROUP BY t.day, t.agent_id, t.user_id, u.external_id, t.model
		ORDER BY t.day DESC, t.agent_id, t.user_id`)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, Totals{}, err
	}
	defer rows.Close()
	var out []DailyUsage
	var tot Totals
	for rows.Next() {
		var d DailyUsage
		var day time.Time
		if err := rows.Scan(&day, &d.AgentID, &d.UserID, &d.EndUser, &d.Model,
			&d.InputTokens, &d.OutputTokens, &d.CacheRead, &d.CacheCreation, &d.Requests); err != nil {
			return nil, Totals{}, err
		}
		d.Day = day.Format("2006-01-02")
		tot.Input += d.InputTokens
		tot.Output += d.OutputTokens
		tot.CacheRead += d.CacheRead
		tot.CacheCreation += d.CacheCreation
		tot.Requests += d.Requests
		out = append(out, d)
	}
	return out, tot, rows.Err()
}

func (s *SQLMeter) RecordTokenLog(ctx context.Context, userID, agentID, sessionKey, prov, model string, t Tokens, costUSD *float64, durationMs int64) error {
	channel := store.ChannelFromContext(ctx)
	chatterUID := store.ChatterUserIDFromContext(ctx)
	q := s.rebind(`
		INSERT INTO token_usage_log
			(user_id, agent_id, session_key, provider, model,
			 input_tokens, output_tokens, cache_read_tokens, cache_create_tokens,
			 cost_usd, duration_ms, channel, chatter_user_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`)
	var cost any // NULL when the model has no price
	if costUSD != nil {
		cost = *costUSD
	}
	_, err := s.db.ExecContext(ctx, q,
		userID, agentID, sessionKey, prov, model,
		t.Input, t.Output, t.CacheRead, t.CacheCreation,
		cost, durationMs, channel, chatterUID,
	)
	return err
}

func (s *SQLMeter) topBy(ctx context.Context, r Range, limit int, col string) ([]Rank, error) {
	if limit <= 0 {
		limit = 20
	}
	// col is a hardcoded constant from TopAgents/TopUsers — never
	// user-supplied — so concatenation is safe here.
	q := s.rebind(`
		SELECT ` + col + ` AS key,
			COALESCE(SUM(input_tokens + output_tokens + cache_read_tokens + cache_create_tokens),0) AS tokens,
			COALESCE(SUM(input_tokens),0)  AS input_tokens,
			COALESCE(SUM(output_tokens),0) AS output_tokens,
			COALESCE(SUM(request_count),0) AS requests
		FROM token_usage_daily
		WHERE day BETWEEN ? AND ?
		GROUP BY ` + col + `
		ORDER BY tokens DESC
		LIMIT ?`)
	return s.scanRanks(ctx, q, s.dayParam(r.Since), s.dayParam(r.Until), limit)
}
