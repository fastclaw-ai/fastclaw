package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// MeteredEvent is one row of usage_events: one billable thing an agent did
// (a model call, an image, a TTS clip, a search, a fetch, a sandbox
// command, a local-agent run). See docs/usage-metering.md.
//
// The JSON shape is the /v1/usage/events wire format; UserID and
// AccountID are internal (the namespace user that ran it and the account
// that pays) and never exported.
type MeteredEvent struct {
	ID                  int64          `json:"id"`
	CreatedAt           time.Time      `json:"created_at"`
	Kind                string         `json:"kind"`
	Provider            string         `json:"provider"`
	Model               string         `json:"model"`
	InputTokens         int64          `json:"input_tokens"`
	OutputTokens        int64          `json:"output_tokens"`
	CacheReadTokens     int64          `json:"cache_read_tokens"`
	CacheCreationTokens int64          `json:"cache_creation_tokens"`
	Units               float64        `json:"units"`
	Unit                string         `json:"unit"`
	CostUSD             *float64       `json:"cost_usd"`
	Detail              map[string]any `json:"detail"`
	EndUser             string         `json:"end_user"`
	AgentID             string         `json:"agent_id"`
	SessionKey          string         `json:"session_key"`
	Channel             string         `json:"channel"`
	TurnID              string         `json:"turn_id"`

	UserID    string `json:"-"`
	AccountID string `json:"-"`
}

// BillingTarget is who pays for a namespace user's usage and where the
// paying account's billing hook is (empty URL: no hook).
type BillingTarget struct {
	AccountID  string
	EndUser    string
	HookURL    string
	HookSecret string
}

// migrateUsageEvents creates usage_events and the account's billing-hook
// columns. Idempotent. ids are strictly increasing on both dialects
// (BIGSERIAL / AUTOINCREMENT, never reused) so they work as an export
// cursor.
func (d *DBStore) migrateUsageEvents(ctx context.Context) error {
	idCol := "id INTEGER PRIMARY KEY AUTOINCREMENT"
	if d.dialect == "postgres" {
		idCol = "id BIGSERIAL PRIMARY KEY"
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS usage_events (
			` + idCol + `,
			user_id TEXT NOT NULL DEFAULT '',
			agent_id TEXT NOT NULL DEFAULT '',
			session_key TEXT NOT NULL DEFAULT '',
			channel TEXT NOT NULL DEFAULT '',
			turn_id TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL DEFAULT '',
			provider TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			input_tokens BIGINT NOT NULL DEFAULT 0,
			output_tokens BIGINT NOT NULL DEFAULT 0,
			cache_read_tokens BIGINT NOT NULL DEFAULT 0,
			cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
			units DOUBLE PRECISION NOT NULL DEFAULT 0,
			unit TEXT NOT NULL DEFAULT '',
			cost_usd DOUBLE PRECISION,
			detail TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_user ON usage_events (user_id, id)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_turn ON usage_events (turn_id)`,
	}
	for _, s := range stmts {
		if _, err := d.db.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	for _, col := range []struct{ name, ddl string }{
		{"billing_hook_url", `ALTER TABLE users ADD COLUMN billing_hook_url TEXT NOT NULL DEFAULT ''`},
		// Stored as given: the store has no at-rest secret encryption.
		{"billing_hook_secret", `ALTER TABLE users ADD COLUMN billing_hook_secret TEXT NOT NULL DEFAULT ''`},
	} {
		has, err := d.tableHasColumn(ctx, "users", col.name)
		if err != nil {
			return err
		}
		if !has {
			if _, err := d.db.ExecContext(ctx, col.ddl); err != nil {
				return fmt.Errorf("add users.%s: %w", col.name, err)
			}
		}
	}
	return nil
}

// InsertMeteredEvent appends one usage event. ID and CreatedAt are
// assigned by the database.
func (d *DBStore) InsertMeteredEvent(ctx context.Context, e *MeteredEvent) error {
	detail := "{}"
	if len(e.Detail) > 0 {
		blob, err := json.Marshal(e.Detail)
		if err != nil {
			return fmt.Errorf("usage event detail: %w", err)
		}
		detail = string(blob)
	}
	var cost any
	if e.CostUSD != nil {
		cost = *e.CostUSD
	}
	q := fmt.Sprintf(`INSERT INTO usage_events
		(user_id, agent_id, session_key, channel, turn_id, kind, provider, model,
		 input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
		 units, unit, cost_usd, detail, created_at)
		VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, CURRENT_TIMESTAMP)`,
		d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5), d.ph(6), d.ph(7), d.ph(8),
		d.ph(9), d.ph(10), d.ph(11), d.ph(12), d.ph(13), d.ph(14), d.ph(15), d.ph(16))
	_, err := d.db.ExecContext(ctx, q,
		e.UserID, e.AgentID, e.SessionKey, e.Channel, e.TurnID, e.Kind, e.Provider, e.Model,
		e.InputTokens, e.OutputTokens, e.CacheReadTokens, e.CacheCreationTokens,
		e.Units, e.Unit, cost, detail)
	return err
}

// payingAccountSQL / endUserSQL resolve, from a row's namespace user u
// (and u2 = u's owner), the account that pays and the end user's external
// id — the same rules as ListUsageEvents: app_user and channel_user rows
// are paid by the account that owns them, up to two levels (an IM chatter
// under an end user). The end user is the app_user itself, or the app_user
// that owns the chatter.
const payingAccountSQL = `CASE
		WHEN u.role IN ('app_user', 'channel_user') AND u2.role IN ('app_user', 'channel_user') THEN COALESCE(u2.owner_user_id, '')
		WHEN u.role IN ('app_user', 'channel_user') THEN COALESCE(u.owner_user_id, '')
		ELSE COALESCE(u.id, '')
	END`

const endUserSQL = `COALESCE(CASE
		WHEN u.role = 'app_user' THEN u.external_id
		WHEN u.role = 'channel_user' AND u2.role = 'app_user' THEN u2.external_id
	END, '')`

// ListAccountUsageEvents returns the usage events accountID pays for (its
// own and its end users'), with an id above afterID, oldest first
// (≤ limit). Rows are only returned once they are a few seconds old, so a
// reader moving its cursor never skips one committed late.
func (d *DBStore) ListAccountUsageEvents(ctx context.Context, accountID string, afterID int64, limit int) ([]MeteredEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	settled := "CURRENT_TIMESTAMP - INTERVAL '5 seconds'"
	if d.dialect != "postgres" {
		settled = "datetime('now', '-5 seconds')"
	}
	q := fmt.Sprintf(`
		SELECT id, created_at, kind, provider, model,
			input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
			units, unit, cost_usd, detail, end_user, agent_id, session_key, channel, turn_id,
			user_id, account_id
		FROM (
			SELECT e.id, e.created_at, e.kind, e.provider, e.model,
				e.input_tokens, e.output_tokens, e.cache_read_tokens, e.cache_creation_tokens,
				e.units, e.unit, e.cost_usd, e.detail, %[1]s AS end_user,
				e.agent_id, e.session_key, e.channel, e.turn_id, e.user_id,
				CASE WHEN u.id IS NULL THEN e.user_id ELSE %[2]s END AS account_id
			FROM usage_events e
			LEFT JOIN users u ON u.id = e.user_id
			LEFT JOIN users u2 ON u2.id = u.owner_user_id
			WHERE e.id > %[3]s AND e.created_at <= %[4]s
		) x
		WHERE account_id = %[5]s
		ORDER BY id
		LIMIT %[6]s`, endUserSQL, payingAccountSQL, d.ph(1), settled, d.ph(2), d.ph(3))
	rows, err := d.db.QueryContext(ctx, q, afterID, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MeteredEvent{}
	for rows.Next() {
		var e MeteredEvent
		var detail string
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.Kind, &e.Provider, &e.Model,
			&e.InputTokens, &e.OutputTokens, &e.CacheReadTokens, &e.CacheCreationTokens,
			&e.Units, &e.Unit, &e.CostUSD, &detail, &e.EndUser, &e.AgentID, &e.SessionKey,
			&e.Channel, &e.TurnID, &e.UserID, &e.AccountID); err != nil {
			return nil, err
		}
		e.CreatedAt = e.CreatedAt.UTC().Truncate(time.Second)
		e.Detail = map[string]any{}
		if detail != "" {
			_ = json.Unmarshal([]byte(detail), &e.Detail)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// BillingTarget resolves who pays for userID's usage and the paying
// account's billing hook. An unknown user pays for itself and has no hook.
func (d *DBStore) BillingTarget(ctx context.Context, userID string) (BillingTarget, error) {
	t := BillingTarget{AccountID: userID}
	if userID == "" {
		return t, nil
	}
	q := fmt.Sprintf(`SELECT %s, %s FROM users u LEFT JOIN users u2 ON u2.id = u.owner_user_id WHERE u.id = %s`,
		payingAccountSQL, endUserSQL, d.ph(1))
	err := d.db.QueryRowContext(ctx, q, userID).Scan(&t.AccountID, &t.EndUser)
	if err != nil {
		if scanErr(err) == ErrNotFound {
			return BillingTarget{AccountID: userID}, nil
		}
		return t, err
	}
	if t.AccountID == "" {
		t.AccountID = userID
	}
	url, secret, err := d.GetBillingHook(ctx, t.AccountID)
	if err != nil && err != ErrNotFound {
		return t, err
	}
	t.HookURL, t.HookSecret = url, secret
	return t, nil
}

// GetBillingHook returns the account's billing hook ("" when none).
func (d *DBStore) GetBillingHook(ctx context.Context, accountID string) (url, secret string, err error) {
	err = d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT billing_hook_url, billing_hook_secret FROM users WHERE id = %s`, d.ph(1)), accountID).
		Scan(&url, &secret)
	if err != nil {
		return "", "", scanErr(err)
	}
	return url, secret, nil
}

// SetBillingHook sets (or, with an empty url, removes) the account's
// billing hook. The secret is stored as given.
func (d *DBStore) SetBillingHook(ctx context.Context, accountID, url, secret string) error {
	if url == "" {
		secret = ""
	}
	res, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE users SET billing_hook_url = %s, billing_hook_secret = %s WHERE id = %s`, d.ph(1), d.ph(2), d.ph(3)),
		url, secret, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
