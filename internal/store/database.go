package store

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "github.com/lib/pq"  // PostgreSQL driver
	_ "modernc.org/sqlite" // SQLite driver (pure Go)
)

// DBStore implements Store using a SQL database (PostgreSQL or SQLite).
type DBStore struct {
	db      *sql.DB
	dialect string // "postgres" or "sqlite"
}

// NewDBStore creates a database-backed store.
func NewDBStore(dialect, dsn string) (*DBStore, error) {
	db, err := sql.Open(driverName(dialect), dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dialect, err)
	}
	// SQLite serializes all writes through a single global lock; granting
	// 25 parallel connections makes the userland queue *deeper* without
	// adding throughput, and on a busy install (cron scheduler + web
	// traffic) it quickly stacks up SQLITE_BUSY past the busy_timeout.
	// Postgres handles real concurrency, so we keep the wider pool there.
	if dialect == "sqlite" {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(25)
		db.SetMaxIdleConns(5)
	}
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping %s: %w", dialect, err)
	}
	return &DBStore{db: db, dialect: dialect}, nil
}

// DB returns the underlying *sql.DB so satellite packages (e.g.
// internal/usage) can run their own queries against the same
// connection pool without re-opening the DSN.
func (d *DBStore) DB() *sql.DB { return d.db }

// Dialect returns "postgres" or "sqlite" so satellite packages can pick
// the right placeholder syntax / upsert form for their queries.
func (d *DBStore) Dialect() string { return d.dialect }

func driverName(dialect string) string {
	switch dialect {
	case "postgres":
		return "postgres"
	case "sqlite":
		return "sqlite"
	default:
		return dialect
	}
}

// Migrate creates tables if they don't exist. The schema is the canonical
// shape — there are no in-place ALTERs because there is no installed base
// from before this rewrite.
func (d *DBStore) Migrate(ctx context.Context) error {
	// Pre-DDL renames have to run before migrationSQL — otherwise the
	// `CREATE TABLE IF NOT EXISTS <new_name>` lines below would create
	// an empty target ahead of the rename and trip the "both tables
	// exist" branch.
	if err := d.migrateRenameChatEventsToSessionEvents(ctx); err != nil {
		return fmt.Errorf("migrate chat_events → session_events: %w", err)
	}
	for _, stmt := range d.migrationSQL() {
		if _, err := d.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w\nSQL: %s", err, stmt)
		}
	}
	if err := d.migrateAgentFilesUserID(ctx); err != nil {
		return fmt.Errorf("migrate agent_files.user_id: %w", err)
	}
	if err := d.migrateAgentsDropTemplateID(ctx); err != nil {
		return fmt.Errorf("migrate agents.template_id drop: %w", err)
	}
	if err := d.migrateAgentsDropModel(ctx); err != nil {
		return fmt.Errorf("migrate agents.model drop: %w", err)
	}
	if err := d.migrateSkillsAgentEntriesSplit(ctx); err != nil {
		return fmt.Errorf("migrate skills.agentEntries split: %w", err)
	}
	if err := d.migrateAgentFilesDropTemplate(ctx); err != nil {
		return fmt.Errorf("migrate agent_files drop template: %w", err)
	}
	if err := d.migrateUsersAppUserCols(ctx); err != nil {
		return fmt.Errorf("migrate users app_user cols: %w", err)
	}
	if err := d.migrateAPIKeysAddType(ctx); err != nil {
		return fmt.Errorf("migrate apikeys.type: %w", err)
	}
	if err := d.migrateUsersAvatarURL(ctx); err != nil {
		return fmt.Errorf("migrate users.avatar_url: %w", err)
	}
	if err := d.migrateCronJobsFailureCount(ctx); err != nil {
		return fmt.Errorf("migrate cron_jobs.failure_count: %w", err)
	}
	if err := d.migrateAgentsAddIsPublic(ctx); err != nil {
		return fmt.Errorf("migrate agents.is_public: %w", err)
	}
	if err := d.migrateDropAgentGrants(ctx); err != nil {
		return fmt.Errorf("migrate drop agent_grants: %w", err)
	}
	if err := d.migrateUsersAddAgentQuota(ctx); err != nil {
		return fmt.Errorf("migrate users.agent_quota: %w", err)
	}
	if err := d.migrateSessionsAddChannelTriple(ctx); err != nil {
		return fmt.Errorf("migrate sessions channel triple: %w", err)
	}
	if err := d.migrateConfigsScopeToUserAgent(ctx); err != nil {
		return fmt.Errorf("migrate configs scope→(user_id,agent_id): %w", err)
	}
	if err := d.migrateCronJobsAddUserID(ctx); err != nil {
		return fmt.Errorf("migrate cron_jobs.user_id: %w", err)
	}
	if err := d.migrateConfigsAddScopeColumn(ctx); err != nil {
		return fmt.Errorf("migrate configs.scope: %w", err)
	}
	if err := d.migrateSessionsAddProjectID(ctx); err != nil {
		return fmt.Errorf("migrate sessions.project_id: %w", err)
	}
	if err := d.migrateSessionMessagesAddOrigin(ctx); err != nil {
		return fmt.Errorf("migrate session_messages.origin: %w", err)
	}
	if err := d.migrateAgentGoalsAddRouting(ctx); err != nil {
		return fmt.Errorf("migrate agent_goals routing: %w", err)
	}
	if err := d.migrateTokenUsageAddProvider(ctx); err != nil {
		return fmt.Errorf("migrate token_usage_daily.provider: %w", err)
	}
	if err := d.migrateSessionsAddChatterUserID(ctx); err != nil {
		return fmt.Errorf("migrate sessions chatter_user_id: %w", err)
	}
	if err := d.migrateSessionMessagesAddProviderModel(ctx); err != nil {
		return fmt.Errorf("migrate session_messages provider/model: %w", err)
	}
	if err := d.migrateTokenUsageAddChannelChatter(ctx); err != nil {
		return fmt.Errorf("migrate token_usage channel/chatter: %w", err)
	}
	if err := d.migrateUsersAddOwnerUserID(ctx); err != nil {
		return fmt.Errorf("migrate users.owner_user_id: %w", err)
	}
	if err := d.migrateConfigsMergeScopeID(ctx); err != nil {
		return fmt.Errorf("migrate configs scope_id: %w", err)
	}
	if err := d.migrateChannelsAddSharedIdentity(ctx); err != nil {
		return fmt.Errorf("migrate channels shared_identity: %w", err)
	}
	if err := d.migrateChannelsFromConfigs(ctx); err != nil {
		return fmt.Errorf("migrate channels from configs: %w", err)
	}
	if err := d.migrateConfigsDropLegacyColumns(ctx); err != nil {
		return fmt.Errorf("migrate configs drop legacy columns: %w", err)
	}
	// Promote cron_jobs time columns to timestamptz. Runs last among the
	// cron_jobs migrations so the table has its final column shape before
	// the type conversion (ADD COLUMN works on any column type, so the
	// earlier failure_count / user_id steps don't depend on this one).
	if err := d.migrateCronJobsTimestampTZ(ctx); err != nil {
		return fmt.Errorf("migrate cron_jobs timestamptz: %w", err)
	}
	return nil
}

// migrateSessionsAddChatterUserID retrofits a chatter_user_id column
// onto sessions / session_messages / session_events. user_id continues
// to mean "UserSpace owner" (channel owner) so admin views that list
// "all sessions on my bots" keep working unchanged; chatter_user_id
// holds the actual conversation participant, which differs from
// user_id whenever an IM channel routes per-sender app_users into a
// single channel-owner UserSpace.
//
// Empty default + partial indexes preserve existing query plans for
// rows written before this column existed. Readers that want the
// chatter should COALESCE(NULLIF(chatter_user_id,”), user_id) — the
// fallback is exactly right for the web channel (user_id was already
// the chatter there) and matches the pre-fix behavior on IM (where
// every chatter was mis-attributed to the channel owner anyway).
func (d *DBStore) migrateSessionsAddChatterUserID(ctx context.Context) error {
	for _, t := range []string{"sessions", "session_messages", "session_events"} {
		exists, err := d.tableExists(ctx, t)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		has, err := d.tableHasColumn(ctx, t, "chatter_user_id")
		if err != nil {
			return err
		}
		if !has {
			if _, err := d.db.ExecContext(ctx,
				fmt.Sprintf(`ALTER TABLE %s ADD COLUMN chatter_user_id TEXT NOT NULL DEFAULT ''`, t)); err != nil {
				return fmt.Errorf("add column on %s: %w", t, err)
			}
		}
	}
	// Partial indexes — only rows with a non-empty chatter populate the
	// index, so legacy rows don't bloat it.
	indexSQL := []string{
		`CREATE INDEX IF NOT EXISTS idx_sessions_by_chatter ON sessions (chatter_user_id, agent_id, updated_at DESC) WHERE chatter_user_id <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_session_messages_by_chatter ON session_messages (chatter_user_id, agent_id, session_key, seq) WHERE chatter_user_id <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_session_events_by_chatter ON session_events (chatter_user_id, agent_id, session_key, seq) WHERE chatter_user_id <> ''`,
	}
	for _, stmt := range indexSQL {
		if _, err := d.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create chatter index: %w (sql: %s)", err, stmt)
		}
	}
	return nil
}

// migrateAgentGoalsAddRouting retrofits channel/account_id/chat_id/
// project_id onto legacy agent_goals tables. All four default to ”
// — pre-existing rows had no continuation infrastructure attached
// anyway, so the empty value just means "no routing recorded; can't
// auto-continue this goal" and TryFireContinuation bails safely.
// Idempotent.
func (d *DBStore) migrateAgentGoalsAddRouting(ctx context.Context) error {
	for _, col := range []string{"channel", "account_id", "chat_id", "project_id"} {
		has, err := d.tableHasColumn(ctx, "agent_goals", col)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := d.db.ExecContext(ctx,
			fmt.Sprintf(`ALTER TABLE agent_goals ADD COLUMN %s TEXT NOT NULL DEFAULT ''`, col)); err != nil {
			return fmt.Errorf("add column %s: %w", col, err)
		}
	}
	return nil
}

// migrateSessionMessagesAddOrigin retrofits the origin column onto
// legacy session_messages tables. Empty default = pre-existing user /
// assistant messages keep working unchanged. Non-empty marks runtime-
// injected rows (currently only "goal_context") so the WebChatHistory
// reader can skip them. Idempotent.
func (d *DBStore) migrateSessionMessagesAddOrigin(ctx context.Context) error {
	has, err := d.tableHasColumn(ctx, "session_messages", "origin")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := d.db.ExecContext(ctx,
		`ALTER TABLE session_messages ADD COLUMN origin TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add column: %w", err)
	}
	return nil
}

// migrateSessionMessagesAddProviderModel adds provider and model columns
// to session_messages and session_events so each row records which LLM
// produced it. Empty default preserves existing rows. Idempotent.
func (d *DBStore) migrateSessionMessagesAddProviderModel(ctx context.Context) error {
	for _, tbl := range []string{"session_messages", "session_events"} {
		for _, col := range []string{"provider", "model"} {
			has, err := d.tableHasColumn(ctx, tbl, col)
			if err != nil {
				return err
			}
			if has {
				continue
			}
			if _, err := d.db.ExecContext(ctx,
				fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s TEXT NOT NULL DEFAULT ''`, tbl, col)); err != nil {
				return fmt.Errorf("add %s.%s: %w", tbl, col, err)
			}
		}
	}
	return nil
}

// migrateTokenUsageAddProvider retrofits a `provider` column onto an
// older token_usage_daily that was created before per-provider
// breakdown shipped. Pre-release schemas only had (day, user, agent,
// session, model) in the PK, which made GROUP BY provider impossible
// (and let same-name models from different providers collide). Since
// the table only holds accrued counters that the dashboard re-reads
// every refresh, dropping it on the rare upgrade path is cheaper than
// rebuilding the PK with a SQLite "create new + copy + swap" dance.
// Idempotent: returns early if the column already exists, no-op if
// the table itself doesn't exist yet (fresh installs run the new
// CREATE TABLE in migrationSQL).
func (d *DBStore) migrateTokenUsageAddProvider(ctx context.Context) error {
	exists, err := d.tableExists(ctx, "token_usage_daily")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	has, err := d.tableHasColumn(ctx, "token_usage_daily", "provider")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := d.db.ExecContext(ctx, `DROP TABLE token_usage_daily`); err != nil {
		return fmt.Errorf("drop old token_usage_daily: %w", err)
	}
	// migrationSQL's CREATE TABLE IF NOT EXISTS will recreate it with
	// the new schema on the next pass. We rely on the fact that this
	// migration step runs AFTER migrationSQL in the same Migrate()
	// call ordering — so the table comes back with the right shape
	// before any agent traffic hits it.
	if _, err := d.db.ExecContext(ctx, `CREATE TABLE token_usage_daily (
		day DATE NOT NULL,
		user_id TEXT NOT NULL DEFAULT '',
		agent_id TEXT NOT NULL DEFAULT '',
		session_key TEXT NOT NULL DEFAULT '',
		provider TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		input_tokens BIGINT NOT NULL DEFAULT 0,
		output_tokens BIGINT NOT NULL DEFAULT 0,
		cache_read_tokens BIGINT NOT NULL DEFAULT 0,
		cache_create_tokens BIGINT NOT NULL DEFAULT 0,
		request_count BIGINT NOT NULL DEFAULT 0,
		PRIMARY KEY (day, user_id, agent_id, session_key, provider, model)
	)`); err != nil {
		return fmt.Errorf("recreate token_usage_daily: %w", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_token_usage_agent ON token_usage_daily (agent_id, day)`); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_token_usage_user ON token_usage_daily (user_id, day)`); err != nil {
		return err
	}
	return nil
}

// migrateTokenUsageAddChannelChatter adds channel + chatter_user_id
// columns to token_usage_daily and token_usage_log so usage records
// capture which channel the conversation came from and who the actual
// chatter was (as opposed to the agent owner stored in user_id).
func (d *DBStore) migrateTokenUsageAddChannelChatter(ctx context.Context) error {
	for _, table := range []string{"token_usage_daily", "token_usage_log"} {
		exists, err := d.tableExists(ctx, table)
		if err != nil || !exists {
			continue
		}
		for _, col := range []string{"channel", "chatter_user_id"} {
			has, err := d.tableHasColumn(ctx, table, col)
			if err != nil || has {
				continue
			}
			stmt := fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s TEXT NOT NULL DEFAULT ''`, table, col)
			if _, err := d.db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("add %s.%s: %w", table, col, err)
			}
		}
	}
	return nil
}

// migrateUsersAddOwnerUserID adds the owner_user_id column and
// backfills it from the existing apikey_id data. Also fixes role
// (chatter vs app_user) and normalizes username/email for non-human
// users. Idempotent.
func (d *DBStore) migrateUsersAddOwnerUserID(ctx context.Context) error {
	has, err := d.tableHasColumn(ctx, "users", "owner_user_id")
	if err != nil {
		return err
	}
	if !has {
		if _, err := d.db.ExecContext(ctx,
			`ALTER TABLE users ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add column: %w", err)
		}
	}
	// Backfill owner_user_id from apikey_id for rows that haven't been
	// migrated yet (owner_user_id still empty, but apikey_id is set).

	// 1. Rows with apikey_id = "owner:u_xxx" → chatter
	if _, err := d.db.ExecContext(ctx, `
		UPDATE users SET
			role = 'channel_user',
			owner_user_id = REPLACE(apikey_id, 'owner:', '')
		WHERE apikey_id LIKE 'owner:%' AND owner_user_id = ''`); err != nil {
		return fmt.Errorf("backfill chatters (owner: prefix): %w", err)
	}
	// 2. Rows with IM-channel external_id but apikey_id is a user_id
	//    (legacy platform-scoped namespace) → chatter
	for _, ch := range []string{"wechat", "telegram", "discord", "line", "feishu", "slack"} {
		if _, err := d.db.ExecContext(ctx, fmt.Sprintf(`
			UPDATE users SET
				role = 'channel_user',
				owner_user_id = apikey_id
			WHERE external_id LIKE '%s:%%' AND owner_user_id = '' AND apikey_id != ''
				AND apikey_id NOT LIKE 'owner:%%'`, ch)); err != nil {
			return fmt.Errorf("backfill chatters (%s): %w", ch, err)
		}
	}
	// 3. Remaining app_user rows with apikey_id = "k_xxx" or "ak_xxx" →
	//    look up the apikey's owner
	if _, err := d.db.ExecContext(ctx, `
		UPDATE users SET
			owner_user_id = (SELECT a.user_id FROM apikeys a WHERE a.id = users.apikey_id)
		WHERE role IN ('app_user', 'user') AND owner_user_id = ''
			AND apikey_id != '' AND (apikey_id LIKE 'k_%' OR apikey_id LIKE 'ak_%')
			AND EXISTS (SELECT 1 FROM apikeys a WHERE a.id = users.apikey_id)`); err != nil {
		return fmt.Errorf("backfill app_users (apikey lookup): %w", err)
	}
	// 4. Remaining app_user rows with apikey_id = a user_id (not owner:
	//    prefix, not k_/ak_ prefix) → apikey_id IS the owner
	if _, err := d.db.ExecContext(ctx, `
		UPDATE users SET
			role = CASE
				WHEN role = 'user' AND apikey_id != '' AND external_id != '' THEN 'app_user'
				ELSE role
			END,
			owner_user_id = apikey_id
		WHERE owner_user_id = '' AND apikey_id != ''
			AND apikey_id NOT LIKE 'owner:%'
			AND apikey_id NOT LIKE 'k_%' AND apikey_id NOT LIKE 'ak_%'`); err != nil {
		return fmt.Errorf("backfill app_users (user_id namespace): %w", err)
	}
	// 5. Rename legacy role='chatter' → 'channel_user' (from an earlier
	//    migration draft that shipped briefly).
	if _, err := d.db.ExecContext(ctx, `
		UPDATE users SET role = 'channel_user' WHERE role = 'chatter'`); err != nil {
		return fmt.Errorf("rename chatter→channel_user: %w", err)
	}
	// 6. Clean up apikey_id: channel_user rows should have empty
	//    apikey_id (they weren't created via API key). app_user rows
	//    with a user_id in apikey_id (legacy namespace) get cleared too
	//    since owner_user_id now holds that relationship. Only keep
	//    actual API key IDs (k_xxx / ak_xxx).
	if _, err := d.db.ExecContext(ctx, `
		UPDATE users SET apikey_id = ''
		WHERE owner_user_id != '' AND apikey_id != ''
			AND apikey_id NOT LIKE 'k_%' AND apikey_id NOT LIKE 'ak_%'`); err != nil {
		return fmt.Errorf("clean apikey_id: %w", err)
	}
	// 7. Normalize username/email for app_user and channel_user rows.
	if _, err := d.db.ExecContext(ctx, `
		UPDATE users SET
			username = id,
			email = id || '@' || role
		WHERE role IN ('app_user', 'channel_user')
			AND username LIKE 'ext:%'`); err != nil {
		return fmt.Errorf("normalize username/email: %w", err)
	}
	// Create index for the new lookup pattern.
	if _, err := d.db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_users_owner_external ON users (owner_user_id, external_id)`); err != nil {
		return fmt.Errorf("create index: %w", err)
	}
	return nil
}

// migrateSessionsAddProjectID adds the project_id column to legacy
// sessions tables. Empty default = "loose chat" (the existing behavior),
// non-empty = belongs to that project. Idempotent: returns early if
// the column already exists.
func (d *DBStore) migrateSessionsAddProjectID(ctx context.Context) error {
	has, err := d.tableHasColumn(ctx, "sessions", "project_id")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := d.db.ExecContext(ctx,
		`ALTER TABLE sessions ADD COLUMN project_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add column: %w", err)
	}
	return nil
}

// migrateConfigsAddScopeColumn retrofits the denormalized scope label
// column. Pre-feature configs rows have the (user_id, agent_id) pair
// but no scope hint — this adds the column and backfills it once. New
// rows are written by SaveConfig, which is the only place that can
// emit a scope value.
//
// Idempotent: returns early if the column already exists.
func (d *DBStore) migrateConfigsAddScopeColumn(ctx context.Context) error {
	has, err := d.tableHasColumn(ctx, "configs", "scope")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := d.db.ExecContext(ctx,
		`ALTER TABLE configs ADD COLUMN scope TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add column: %w", err)
	}
	// Backfill in one UPDATE — same CASE expression that
	// computeConfigScope encodes in Go. Both dialects support the
	// CASE WHEN form unchanged.
	if _, err := d.db.ExecContext(ctx, `UPDATE configs SET scope = CASE
		WHEN user_id != '' AND agent_id != '' THEN 'user-agent'
		WHEN user_id != ''                     THEN 'user'
		WHEN agent_id != ''                    THEN 'agent'
		ELSE 'system'
	END WHERE scope = ''`); err != nil {
		return fmt.Errorf("backfill scope: %w", err)
	}
	return nil
}

// migrateConfigsMergeScopeID adds a scope_id column that collapses
// (user_id, agent_id) into a single lookup key: whichever is non-empty
// wins (they're mutually exclusive for provider/setting rows — the only
// kinds that remain in configs now that channels have their own table).
// System rows get scope_id=”.
//
// Idempotent: skips the ALTER if the column already exists and only
// backfills rows where scope_id is still empty.
func (d *DBStore) migrateConfigsMergeScopeID(ctx context.Context) error {
	has, err := d.tableHasColumn(ctx, "configs", "scope_id")
	if err != nil {
		return err
	}
	if !has {
		if _, err := d.db.ExecContext(ctx,
			`ALTER TABLE configs ADD COLUMN scope_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add scope_id column: %w", err)
		}
	}
	// Backfill: scope_id = user_id when user_id != '', else agent_id.
	// Only touch rows where scope_id is still empty to stay idempotent.
	// Skip if user_id column no longer exists (post-drop migration).
	hasUserID, err := d.tableHasColumn(ctx, "configs", "user_id")
	if err != nil {
		return err
	}
	if !hasUserID {
		// Legacy columns already dropped — backfill not needed, but
		// ensure the index exists (migrateConfigsDropLegacyColumns
		// normally handles it, but be defensive).
		if _, err := d.db.ExecContext(ctx,
			`CREATE INDEX IF NOT EXISTS idx_configs_scope ON configs (kind, scope_id)`); err != nil {
			return fmt.Errorf("create idx_configs_scope: %w", err)
		}
		return nil
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE configs SET scope_id = CASE
		WHEN user_id != '' THEN user_id
		WHEN agent_id != '' THEN agent_id
		ELSE ''
	END WHERE scope_id = ''`); err != nil {
		return fmt.Errorf("backfill scope_id: %w", err)
	}
	// Assert the index now that scope_id is guaranteed to exist.
	// IF NOT EXISTS keeps it idempotent for fresh installs and re-runs.
	if _, err := d.db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_configs_scope ON configs (kind, scope_id)`); err != nil {
		return fmt.Errorf("create idx_configs_scope: %w", err)
	}
	return nil
}

// migrateRenameChatEventsToSessionEvents renames the streaming-event
// deltas table from `chat_events` to `session_events` so it shares the
// session_* prefix with `sessions` / `session_messages`. The "chat"
// label was misleading — the table also stores events for wechat /
// telegram / line / web sessions, not just web "chats".
//
// Idempotent: if the new name already exists OR the old name doesn't,
// the function is a no-op. On rename, the lookup index moves too.
func (d *DBStore) migrateRenameChatEventsToSessionEvents(ctx context.Context) error {
	hasNew, err := d.tableExists(ctx, "session_events")
	if err != nil {
		return err
	}
	if hasNew {
		return nil
	}
	hasOld, err := d.tableExists(ctx, "chat_events")
	if err != nil {
		return err
	}
	if !hasOld {
		// Defensive — fresh installs never have chat_events because
		// migrationSQL writes session_events directly. Older installs
		// that already ran this migration land on hasNew=true above.
		return nil
	}
	if _, err := d.db.ExecContext(ctx, `ALTER TABLE chat_events RENAME TO session_events`); err != nil {
		return fmt.Errorf("rename table: %w", err)
	}
	if d.dialect == "postgres" {
		_, _ = d.db.ExecContext(ctx,
			`ALTER INDEX IF EXISTS idx_chat_events_lookup RENAME TO idx_session_events_lookup`)
	} else {
		// SQLite has no ALTER INDEX RENAME on older versions; drop
		// and recreate with the new name. The DROP is best-effort —
		// it may already have been gone.
		_, _ = d.db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_chat_events_lookup`)
	}
	if _, err := d.db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_session_events_lookup ON session_events (user_id, agent_id, session_key, seq)`); err != nil {
		return fmt.Errorf("recreate index: %w", err)
	}
	return nil
}

// tableExists is a small helper used by table-rename migrations.
// SQLite reads sqlite_master; Postgres uses to_regclass.
func (d *DBStore) tableExists(ctx context.Context, table string) (bool, error) {
	if d.dialect == "postgres" {
		var name *string
		err := d.db.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, table).Scan(&name)
		if err != nil {
			return false, err
		}
		return name != nil, nil
	}
	var name string
	err := d.db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return name != "", nil
}

// migrateConfigsScopeToUserAgent rewrites the configs table from the
// historical (scope, scope_id) polymorphic pair into explicit
// (user_id, agent_id) columns.
//
// Backfill rules:
//
//	scope='system'           → user_id='',           agent_id=''
//	scope='user',  scope_id=X→ user_id=X,            agent_id=''
//	scope='agent', scope_id=Y→
//	  - kind='channel' or 'setting'/name='bindings':
//	      user_id = agents.user_id (the owner; channel routes to them
//	                anyway), agent_id = Y. This finally records WHO
//	                bound the channel inside the row itself.
//	  - other kinds (provider / setting):
//	      user_id = '',          agent_id = Y
//
// The kind='setting'/name='bindings' rows are migration-deleted at the
// end — channel rows now carry their agent_id directly, so the
// indirection layer is gone.
func (d *DBStore) migrateConfigsScopeToUserAgent(ctx context.Context) error {
	hasUserID, err := d.tableHasColumn(ctx, "configs", "user_id")
	if err != nil {
		return err
	}
	if !hasUserID {
		// If credential_key is also missing, the table has already been
		// through the full lifecycle (scope→user_id→drop), so there's
		// nothing to do.
		hasCredKey, err := d.tableHasColumn(ctx, "configs", "credential_key")
		if err != nil {
			return err
		}
		if !hasCredKey {
			return nil
		}
		// Probe `scope_id` rather than `scope`: the post-refactor
		// schema reintroduces `scope` as a denormalized label, so its
		// presence no longer means "this is the legacy shape".
		hasScopeID, err := d.tableHasColumn(ctx, "configs", "scope_id")
		if err != nil {
			return err
		}
		if hasScopeID {
			if d.dialect == "postgres" {
				if err := d.migrateConfigsScopeToUserAgentPostgres(ctx); err != nil {
					return err
				}
			} else {
				if err := d.migrateConfigsScopeToUserAgentSQLite(ctx); err != nil {
					return err
				}
			}
		}
	}
	// Assert the lookup index only if user_id still exists (pre-drop
	// state). After migrateConfigsDropLegacyColumns runs, the column is
	// gone and the index would fail. The drop migration creates its own
	// idx_configs_scope index as a replacement.
	if hasUserID {
		if _, err := d.db.ExecContext(ctx,
			`CREATE INDEX IF NOT EXISTS idx_configs_lookup ON configs (kind, user_id, agent_id)`); err != nil {
			return fmt.Errorf("create configs index: %w", err)
		}
	}
	return nil
}

func (d *DBStore) migrateConfigsScopeToUserAgentPostgres(ctx context.Context) error {
	// Postgres can ALTER directly: add columns, backfill, drop index +
	// unique, drop scope columns, recreate index + unique.
	stmts := []string{
		`ALTER TABLE configs ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE configs ADD COLUMN IF NOT EXISTS agent_id TEXT NOT NULL DEFAULT ''`,
		// scope=user: user_id = scope_id
		`UPDATE configs SET user_id = scope_id WHERE scope = 'user' AND user_id = ''`,
		// scope=agent + (channel or bindings): user_id = agents.user_id, agent_id = scope_id
		`UPDATE configs c SET user_id = a.user_id, agent_id = c.scope_id
		   FROM agents a
		   WHERE c.scope = 'agent' AND c.user_id = '' AND c.agent_id = ''
		     AND a.id = c.scope_id
		     AND (c.kind = 'channel' OR (c.kind = 'setting' AND c.name = 'bindings'))`,
		// scope=agent + other kinds: only set agent_id
		`UPDATE configs SET agent_id = scope_id
		   WHERE scope = 'agent' AND agent_id = ''`,
		// Drop kind=setting/name=bindings rows — bindings are now
		// implicit in channel rows' agent_id.
		`DELETE FROM configs WHERE kind = 'setting' AND name = 'bindings'`,
		`DROP INDEX IF EXISTS idx_configs_lookup`,
		`ALTER TABLE configs DROP CONSTRAINT IF EXISTS configs_kind_scope_scope_id_name_key`,
		`ALTER TABLE configs DROP COLUMN IF EXISTS scope`,
		`ALTER TABLE configs DROP COLUMN IF EXISTS scope_id`,
		`ALTER TABLE configs ADD CONSTRAINT configs_kind_user_agent_name_key UNIQUE (kind, user_id, agent_id, name)`,
		`CREATE INDEX IF NOT EXISTS idx_configs_lookup ON configs (kind, user_id, agent_id)`,
	}
	for _, s := range stmts {
		if _, err := d.db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("postgres migrate configs: %w\nSQL: %s", err, s)
		}
	}
	return nil
}

func (d *DBStore) migrateConfigsScopeToUserAgentSQLite(ctx context.Context) error {
	// SQLite can't drop / change columns in place reliably across all
	// versions in our supported range, so we copy-rename the table.
	stmts := []string{
		`CREATE TABLE configs_new (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			user_id TEXT NOT NULL DEFAULT '',
			agent_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			credential_key TEXT NOT NULL DEFAULT '',
			data TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (kind, user_id, agent_id, name)
		)`,
		// scope=system: both ids empty
		// scope=user:   user_id = scope_id, agent_id = ''
		// scope=agent + (channel | setting/name=bindings):
		//               user_id = agents.user_id, agent_id = scope_id
		// scope=agent + other:
		//               user_id = '', agent_id = scope_id
		// Skip kind='setting' AND name='bindings' rows — channel rows
		// now carry agent_id directly so this indirection table is
		// redundant.
		`INSERT INTO configs_new (id, kind, user_id, agent_id, name, enabled, credential_key, data, created_at, updated_at)
		   SELECT
		     c.id,
		     c.kind,
		     CASE
		       WHEN c.scope = 'user' THEN c.scope_id
		       WHEN c.scope = 'agent' AND (c.kind = 'channel' OR (c.kind = 'setting' AND c.name = 'bindings'))
		         THEN COALESCE((SELECT a.user_id FROM agents a WHERE a.id = c.scope_id), '')
		       ELSE ''
		     END AS user_id,
		     CASE
		       WHEN c.scope = 'agent' THEN c.scope_id
		       ELSE ''
		     END AS agent_id,
		     c.name, c.enabled, c.credential_key, c.data, c.created_at, c.updated_at
		   FROM configs c
		   WHERE NOT (c.kind = 'setting' AND c.name = 'bindings')`,
		`DROP TABLE configs`,
		`ALTER TABLE configs_new RENAME TO configs`,
		`DROP INDEX IF EXISTS idx_configs_lookup`,
		`CREATE INDEX IF NOT EXISTS idx_configs_lookup ON configs (kind, user_id, agent_id)`,
		`CREATE INDEX IF NOT EXISTS idx_configs_credential ON configs (kind, credential_key)`,
	}
	for _, s := range stmts {
		if _, err := d.db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("sqlite migrate configs: %w\nSQL: %s", err, s)
		}
	}
	return nil
}

// migrateCronJobsAddUserID retrofits user_id onto cron_jobs so the
// (user_id, agent_id) keying matches the rest of the codebase. Backfill
// joins agents to recover the owning user. New rows must populate
// user_id explicitly (SaveCronJob enforces).
func (d *DBStore) migrateCronJobsAddUserID(ctx context.Context) error {
	has, err := d.tableHasColumn(ctx, "cron_jobs", "user_id")
	if err != nil {
		return err
	}
	if !has {
		if _, err := d.db.ExecContext(ctx,
			`ALTER TABLE cron_jobs ADD COLUMN user_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add cron_jobs.user_id: %w", err)
		}
		if _, err := d.db.ExecContext(ctx,
			`UPDATE cron_jobs SET user_id = COALESCE((SELECT a.user_id FROM agents a WHERE a.id = cron_jobs.agent_id), '')
			 WHERE user_id = ''`); err != nil {
			return fmt.Errorf("backfill cron_jobs.user_id: %w", err)
		}
	}
	// Always assert the lookup index — fresh installs flow through here too.
	if _, err := d.db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_cron_jobs_user ON cron_jobs (user_id, agent_id)`); err != nil {
		return fmt.Errorf("index cron_jobs.user_id: %w", err)
	}
	return nil
}

// migrateSessionsAddChannelTriple retrofits channel / account_id / chat_id
// onto pre-feature sessions rows. Existing session_keys followed the
// `<channel>_<chatID>` convention (web_<sid>, wechat_<openid>, …), so the
// backfill splits on the first underscore. account_id has no historical
// source — pre-feature installs only ran one bot per channel anyway, so
// leaving it ” is correct for those rows. New sessions written after
// this migration always populate the full triple explicitly.
func (d *DBStore) migrateSessionsAddChannelTriple(ctx context.Context) error {
	has, err := d.tableHasColumn(ctx, "sessions", "channel")
	if err != nil {
		return err
	}
	if !has {
		for _, stmt := range []string{
			`ALTER TABLE sessions ADD COLUMN channel TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE sessions ADD COLUMN account_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE sessions ADD COLUMN chat_id TEXT NOT NULL DEFAULT ''`,
		} {
			if _, err := d.db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("add column: %w (sql: %s)", err, stmt)
			}
		}
		// Backfill from the legacy `<channel>_<chatID>` session_key shape.
		// SQLite and Postgres both expose substr / instr-style functions;
		// we pick the dialect-appropriate one. Rows with no underscore
		// (shouldn't happen in practice but defensive) get channel='' and
		// chat_id=key.
		var backfill string
		if d.dialect == "postgres" {
			backfill = `UPDATE sessions
				SET channel = COALESCE(NULLIF(SPLIT_PART(session_key, '_', 1), ''), ''),
				    chat_id = CASE
				        WHEN POSITION('_' IN session_key) > 0
				        THEN SUBSTRING(session_key FROM POSITION('_' IN session_key) + 1)
				        ELSE session_key
				    END
				WHERE channel = '' AND chat_id = ''`
		} else {
			backfill = `UPDATE sessions
				SET channel = CASE WHEN INSTR(session_key, '_') > 0 THEN SUBSTR(session_key, 1, INSTR(session_key, '_') - 1) ELSE '' END,
				    chat_id = CASE WHEN INSTR(session_key, '_') > 0 THEN SUBSTR(session_key, INSTR(session_key, '_') + 1) ELSE session_key END
				WHERE channel = '' AND chat_id = ''`
		}
		if _, err := d.db.ExecContext(ctx, backfill); err != nil {
			return fmt.Errorf("backfill: %w", err)
		}
	}
	// Always (re)assert the index — the CREATE INDEX in migrationSQL was
	// removed because it would fire before the columns existed on legacy
	// databases. IF NOT EXISTS makes it idempotent for fresh installs.
	if _, err := d.db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_sessions_chat_active ON sessions (user_id, agent_id, channel, account_id, chat_id, updated_at DESC)`); err != nil {
		return fmt.Errorf("create index: %w", err)
	}
	return nil
}

// migrateUsersAddAgentQuota retrofits the agent_quota column onto
// pre-feature installs. Default -1 = unlimited, which preserves the
// existing "anyone can create as many agents as they want" behavior
// for users that existed before the quota was introduced.
func (d *DBStore) migrateUsersAddAgentQuota(ctx context.Context) error {
	has, err := d.tableHasColumn(ctx, "users", "agent_quota")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := d.db.ExecContext(ctx,
		`ALTER TABLE users ADD COLUMN agent_quota INTEGER NOT NULL DEFAULT -1`); err != nil {
		return fmt.Errorf("add agent_quota: %w", err)
	}
	return nil
}

// migrateAgentsAddIsPublic retrofits the is_public column onto
// pre-feature installs. Default FALSE keeps every existing agent
// owner-only after the upgrade — opt-in via the Edit dialog.
func (d *DBStore) migrateAgentsAddIsPublic(ctx context.Context) error {
	has, err := d.tableHasColumn(ctx, "agents", "is_public")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := d.db.ExecContext(ctx,
		`ALTER TABLE agents ADD COLUMN is_public BOOLEAN NOT NULL DEFAULT FALSE`); err != nil {
		return fmt.Errorf("add is_public: %w", err)
	}
	return nil
}

// migrateDropAgentGrants removes the legacy per-user share table.
// Sharing now lives on agents.is_public; existing per-user grants are
// not migrated forward (the prior model wasn't shipped to general
// users). DROP TABLE IF EXISTS is idempotent and a no-op on fresh
// installs that never created the table.
func (d *DBStore) migrateDropAgentGrants(ctx context.Context) error {
	if _, err := d.db.ExecContext(ctx, `DROP TABLE IF EXISTS agent_grants`); err != nil {
		return fmt.Errorf("drop agent_grants: %w", err)
	}
	return nil
}

// migrateCronJobsFailureCount retrofits the failure_count column onto
// pre-feature installs. Default 0 backfills existing rows as "healthy"
// so the auto-delete threshold doesn't fire on first tick after the
// upgrade.
func (d *DBStore) migrateCronJobsFailureCount(ctx context.Context) error {
	has, err := d.tableHasColumn(ctx, "cron_jobs", "failure_count")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := d.db.ExecContext(ctx,
		`ALTER TABLE cron_jobs ADD COLUMN failure_count INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("add failure_count: %w", err)
	}
	return nil
}

// migrateCronJobsTimestampTZ fixes the timezone bug on the cron schedule
// columns. The time columns (next_run, last_run, locked_at, created_at)
// were declared TIMESTAMP without time zone. lib/pq sends each Go
// time.Time to the server carrying its original zone offset
// ("2006-01-02 15:04:05.999999999Z07:00"), but a TIMESTAMP-without-tz
// column silently drops the offset and keeps only the wall-clock text.
// Reading it back yields a UTC instant whose wall-clock equals the
// chatter's local wall-clock — so a "09:00 Asia/Shanghai" job is stored
// as 09:00 and reinterpreted as 09:00 UTC = 17:00 Beijing, firing 8h
// late. SQLite is unaffected (its driver stores the full instant).
//
// Fix: promote the columns to timestamptz. That type preserves the
// instant across write/read regardless of offset or session TimeZone
// (verified end-to-end against lib/pq). This is Postgres-only; SQLite
// has no separate timestamptz type and already stores instants losslessly.
//
// Existing rows are wiped rather than converted: their stored
// wall-clocks are wrong (offset already dropped), so any conversion
// would just freeze the bug into the new type. Truncating is safe here
// because cron rows are ephemeral schedule state, not history — the
// scheduler rewrites next_run on every fire and deletes "once" rows
// after firing, so a clean reschedule is the correct recovery and
// cheaper than per-row guesswork.
//
// Idempotent: the probe on pg_catalog.format_type short-circuits once a
// column is already timestamptz, so re-runs and fresh installs are no-ops.
// On SQLite the tableHasColumnType probe returns (false, nil) and the
// whole step is skipped.
func (d *DBStore) migrateCronJobsTimestampTZ(ctx context.Context) error {
	if d.dialect != "postgres" {
		return nil
	}
	cols := []string{"next_run", "last_run", "locked_at", "created_at"}
	needConvert := false
	for _, col := range cols {
		isTZ, err := d.columnIsTimestampTZ(ctx, "cron_jobs", col)
		if err != nil {
			return fmt.Errorf("probe cron_jobs.%s type: %w", col, err)
		}
		if !isTZ {
			needConvert = true
			break
		}
	}
	if !needConvert {
		return nil
	}
	// Count first so the upgrade is observable: a non-zero count tells an
	// operator that existing schedule state is about to be wiped and must be
	// recreated (see CHANGELOG). The count is logged before the TRUNCATE so
	// it survives even if the type conversion that follows were to fail.
	var wiping int
	if err := d.db.QueryRowContext(ctx, `SELECT count(*) FROM cron_jobs`).Scan(&wiping); err != nil {
		return fmt.Errorf("count cron_jobs before tz migration: %w", err)
	}
	if wiping > 0 {
		slog.Warn("resetting cron schedule state for timestamptz migration — existing jobs must be recreated",
			"rows", wiping,
			"reason", "stored next_run values carry the pre-fix wrong timezone; see CHANGELOG")
	}
	// Wipe the schedule state in place, then widen the columns. Doing the
	// DELETE before the ALTER means the type change is instant on an empty
	// table (no heap rewrite), and there are no stale rows to convert.
	if _, err := d.db.ExecContext(ctx, `TRUNCATE TABLE cron_jobs`); err != nil {
		return fmt.Errorf("truncate cron_jobs: %w", err)
	}
	for _, col := range cols {
		// AT TIME ZONE 'UTC' is a no-op type-wise but documents intent: the
		// emptied column is interpreted in UTC when repopulated. The column
		// type becomes timestamptz in all cases.
		stmt := fmt.Sprintf(
			`ALTER TABLE cron_jobs ALTER COLUMN %s TYPE timestamptz USING %s AT TIME ZONE 'UTC'`,
			col, col)
		if _, err := d.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("convert cron_jobs.%s to timestamptz: %w\nSQL: %s", col, err, stmt)
		}
	}
	return nil
}

// columnIsTimestampTZ reports whether the column's Postgres type is
// timestamptz (timestamp with time zone). Used by migrations that need
// to promote a TIMESTAMP-without-tz column and stay idempotent. Returns
// (false, nil) on SQLite — callers must gate on dialect themselves.
func (d *DBStore) columnIsTimestampTZ(ctx context.Context, table, column string) (bool, error) {
	if d.dialect != "postgres" {
		return false, nil
	}
	var dataType string
	err := d.db.QueryRowContext(ctx,
		`SELECT format_type(a.atttypid, a.atttypmod)
		   FROM pg_attribute a
		   JOIN pg_class c ON c.oid = a.attrelid
		  WHERE c.relname = $1 AND a.attname = $2 AND a.attnum > 0`,
		table, column).Scan(&dataType)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return dataType == "timestamp with time zone", nil
}

// migrateUsersAvatarURL retrofits the avatar_url column onto pre-feature
// installs. Stored as a data: URL so the file lives inline with the row
// — no separate blob store path or cleanup. Empty string means "no
// avatar"; the UI falls back to initials.
func (d *DBStore) migrateUsersAvatarURL(ctx context.Context) error {
	has, err := d.tableHasColumn(ctx, "users", "avatar_url")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := d.db.ExecContext(ctx,
		`ALTER TABLE users ADD COLUMN avatar_url TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add avatar_url: %w", err)
	}
	return nil
}

// migrateAPIKeysAddType retrofits the `type` column onto apikeys for
// pre-tier installs. Every legacy row was an explicit-agent-list key, so
// backfilling DEFAULT 'agent' preserves behavior — admin/user tiers can
// only be created from this point forward.
func (d *DBStore) migrateAPIKeysAddType(ctx context.Context) error {
	has, err := d.tableHasColumn(ctx, "apikeys", "type")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := d.db.ExecContext(ctx,
		`ALTER TABLE apikeys ADD COLUMN type TEXT NOT NULL DEFAULT 'agent'`); err != nil {
		return fmt.Errorf("add type: %w", err)
	}
	return nil
}

// migrateUsersAppUserCols retrofits the apikey_id + external_id columns
// onto the users table for existing installs, and creates the partial
// unique index used for idempotent provisioning. CREATE TABLE only fires
// on a fresh DB; older databases reach this with the legacy 7-column
// users table and exit it with the new 9-column shape. Idempotent: each
// step probes for existing state before mutating.
func (d *DBStore) migrateUsersAppUserCols(ctx context.Context) error {
	hasAPIKey, err := d.tableHasColumn(ctx, "users", "apikey_id")
	if err != nil {
		return err
	}
	if !hasAPIKey {
		if _, err := d.db.ExecContext(ctx,
			`ALTER TABLE users ADD COLUMN apikey_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add apikey_id: %w", err)
		}
	}
	hasExt, err := d.tableHasColumn(ctx, "users", "external_id")
	if err != nil {
		return err
	}
	if !hasExt {
		if _, err := d.db.ExecContext(ctx,
			`ALTER TABLE users ADD COLUMN external_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add external_id: %w", err)
		}
	}
	// CREATE UNIQUE INDEX IF NOT EXISTS is supported by both backends.
	if _, err := d.db.ExecContext(ctx,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_apikey_external
			ON users (apikey_id, external_id)
			WHERE apikey_id <> '' AND external_id <> ''`); err != nil {
		return fmt.Errorf("create idx_users_apikey_external: %w", err)
	}

	// One-time, collision-safe re-key: app_users minted under the OLD scheme
	// stored the api_key id as their mint scope, which orphans the end-user
	// when the calling app rotates/replaces that key. Re-key them onto the
	// api_key's OWNER account so identity survives key rotation. Only rows
	// whose apikey_id still resolves to a real api_key are remapped; rows that
	// would collide with an already-owner-keyed sibling (same owner +
	// external_id) are skipped. Idempotent: once apikey_id holds a "u_…" owner
	// it no longer matches any apikeys.id ("k_…"), so reruns touch nothing.
	// Non-fatal: a rare unrecoverable collision (two legacy keys, same owner,
	// same external_id) is logged and left for manual reconciliation rather
	// than blocking startup.
	if _, err := d.db.ExecContext(ctx, `
		UPDATE users SET apikey_id = (SELECT a.user_id FROM apikeys a WHERE a.id = users.apikey_id)
		WHERE role = 'app_user'
		  AND apikey_id <> ''
		  AND apikey_id IN (SELECT id FROM apikeys)
		  AND NOT EXISTS (
		    SELECT 1 FROM users u2
		    WHERE u2.id <> users.id
		      AND u2.role = 'app_user'
		      AND u2.external_id = users.external_id
		      AND u2.apikey_id = (SELECT a.user_id FROM apikeys a WHERE a.id = users.apikey_id)
		  )`); err != nil {
		slog.Warn("migrate: backfill app_user owner scope failed (non-fatal)", "error", err)
	}
	return nil
}

// migrateAgentFilesDropTemplate clears the legacy user_id=” template
// rows from agent_files. Each row is reparented to the agent's owner
// when no per-user row already exists for that (agent_id, filename) —
// preserves existing content as the owner's personal copy. After this
// pass the table holds (agent_id, real_user_id, filename) tuples only;
// any "shared SOUL.md across all users" use case should live in a local
// FS file at <agent_home>/<name>, which the runtime falls back to.
// Idempotent: re-runs find no user_id=” rows and exit clean.
func (d *DBStore) migrateAgentFilesDropTemplate(ctx context.Context) error {
	rows, err := d.db.QueryContext(ctx,
		`SELECT agent_files.agent_id, agent_files.filename, agent_files.content, agents.user_id
			FROM agent_files
			LEFT JOIN agents ON agents.id = agent_files.agent_id
			WHERE agent_files.user_id = ''`)
	if err != nil {
		return fmt.Errorf("scan template rows: %w", err)
	}
	type tmpl struct {
		agentID, filename, content string
		ownerID                    sql.NullString
	}
	var pending []tmpl
	for rows.Next() {
		var t tmpl
		if err := rows.Scan(&t.agentID, &t.filename, &t.content, &t.ownerID); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, t)
	}
	rows.Close()
	now := time.Now().UTC()
	for _, t := range pending {
		if t.ownerID.Valid && t.ownerID.String != "" {
			// Reparent only when the owner has no row of their own
			// for this (agent_id, filename) — never clobber an
			// existing personal copy.
			var exists int
			row := d.db.QueryRowContext(ctx,
				fmt.Sprintf(`SELECT 1 FROM agent_files
					WHERE agent_id = %s AND user_id = %s AND filename = %s LIMIT 1`,
					d.ph(1), d.ph(2), d.ph(3)),
				t.agentID, t.ownerID.String, t.filename)
			if err := row.Scan(&exists); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("probe existing row: %w", err)
			}
			if exists != 1 {
				if d.dialect == "postgres" {
					if _, err := d.db.ExecContext(ctx,
						`INSERT INTO agent_files (agent_id, user_id, filename, content, updated_at)
							VALUES ($1, $2, $3, $4, $5)`,
						t.agentID, t.ownerID.String, t.filename, t.content, now); err != nil {
						return fmt.Errorf("reparent template row: %w", err)
					}
				} else {
					if _, err := d.db.ExecContext(ctx,
						`INSERT INTO agent_files (agent_id, user_id, filename, content, updated_at)
							VALUES (?, ?, ?, ?, ?)`,
						t.agentID, t.ownerID.String, t.filename, t.content, now); err != nil {
						return fmt.Errorf("reparent template row: %w", err)
					}
				}
			}
		}
	}
	if _, err := d.db.ExecContext(ctx,
		`DELETE FROM agent_files WHERE user_id = ''`); err != nil {
		return fmt.Errorf("delete template rows: %w", err)
	}
	return nil
}

// migrateSkillsAgentEntriesSplit relocates per-agent skill env overrides
// off the single user/system-scope skills.agentEntries row (a JSON blob
// keyed by agent_id, which grew unboundedly with each agent × skill)
// into one row per agent at scope=agent name=skills.entries — the same
// shape the runtime now reads via scope.GetConfigByName per agent.
// Idempotent: every legacy row found gets split + deleted in a single
// pass; subsequent runs find no legacy rows and exit clean.
func (d *DBStore) migrateSkillsAgentEntriesSplit(ctx context.Context) error {
	// Gate on `scope_id`, not `scope`: the new schema brings back a
	// `scope` column as a denormalized label, but `scope_id` is gone.
	// Probing `scope_id` reliably detects "this is a pre-feature
	// install" and avoids running the legacy SELECT against the new
	// shape.
	hasScopeID, err := d.tableHasColumn(ctx, "configs", "scope_id")
	if err != nil {
		return err
	}
	if !hasScopeID {
		return nil
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, scope, scope_id, data FROM configs WHERE kind='setting' AND name='skills.agentEntries'`)
	if err != nil {
		return fmt.Errorf("scan legacy: %w", err)
	}
	type legacy struct{ id, scopeID, dataJSON string }
	var legacyRows []legacy
	for rows.Next() {
		var l legacy
		var sc string
		if err := rows.Scan(&l.id, &sc, &l.scopeID, &l.dataJSON); err != nil {
			rows.Close()
			return err
		}
		_ = sc
		legacyRows = append(legacyRows, l)
	}
	rows.Close()
	now := time.Now().UTC()
	for _, l := range legacyRows {
		// data shape: { "<agent_id>": { "<skill_name>": { ...entry } } }
		var byAgent map[string]map[string]interface{}
		if err := json.Unmarshal([]byte(l.dataJSON), &byAgent); err != nil {
			// Malformed row — drop it; not worth aborting migration.
			if _, derr := d.db.ExecContext(ctx,
				fmt.Sprintf(`DELETE FROM configs WHERE id=%s`, d.ph(1)), l.id); derr != nil {
				return fmt.Errorf("drop malformed legacy row: %w", derr)
			}
			continue
		}
		for agentID, inner := range byAgent {
			if len(inner) == 0 {
				continue
			}
			cid := configRowID("setting", "agent", agentID, "skills.entries")
			innerBlob, _ := json.Marshal(inner)
			// Skip if a per-agent row already exists (manual edit, prior
			// partial migration, etc.) — don't clobber.
			var exists int
			err := d.db.QueryRowContext(ctx,
				fmt.Sprintf(`SELECT 1 FROM configs WHERE id=%s LIMIT 1`, d.ph(1)), cid).Scan(&exists)
			if err == nil {
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("check existing per-agent row: %w", err)
			}
			insert := fmt.Sprintf(`INSERT INTO configs (id, kind, scope, scope_id, name, enabled, credential_key, data, created_at, updated_at)
				VALUES (%s, 'setting', 'agent', %s, 'skills.entries', TRUE, '', %s, %s, %s)`,
				d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5))
			if _, err := d.db.ExecContext(ctx, insert, cid, agentID, string(innerBlob), now, now); err != nil {
				return fmt.Errorf("insert per-agent row for %s: %w", agentID, err)
			}
		}
		if _, err := d.db.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM configs WHERE id=%s`, d.ph(1)), l.id); err != nil {
			return fmt.Errorf("drop legacy row: %w", err)
		}
	}
	return nil
}

// migrateAgentsDropModel relocates per-agent model overrides off the
// agents.model column into the configs table (kind=setting, scope=agent,
// scope_id=<aid>, name="agents.defaults", data={"model":"..."}). The
// configs path is what the runtime now reads via scope.SettingInto, so
// keeping the column would just duplicate state. Idempotent: silently
// no-ops once the column is gone.
func (d *DBStore) migrateAgentsDropModel(ctx context.Context) error {
	has, err := d.tableHasColumn(ctx, "agents", "model")
	if err != nil {
		return err
	}
	if !has {
		return nil
	}
	// The relocation INSERTs into configs using the legacy (scope,
	// scope_id) columns. Fresh installs that never had agents.model in
	// the first place don't reach this branch (the column-presence
	// check above already returned). But a legacy install that lost
	// the scope_id column out of order would — gate it. (scope still
	// exists post-refactor as a denormalized label, scope_id doesn't.)
	hasScopeID, err := d.tableHasColumn(ctx, "configs", "scope_id")
	if err != nil {
		return err
	}
	if !hasScopeID {
		// Legacy install in an unexpected state — drop the column so
		// the orchestrator can move on; the data was probably already
		// migrated by a prior run.
		stmt := `ALTER TABLE agents DROP COLUMN model`
		if d.dialect == "postgres" {
			stmt = `ALTER TABLE agents DROP COLUMN IF EXISTS model`
		}
		_, _ = d.db.ExecContext(ctx, stmt)
		return nil
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id, model FROM agents WHERE model <> ''`)
	if err != nil {
		return fmt.Errorf("scan legacy models: %w", err)
	}
	type row struct{ id, model string }
	var legacy []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.model); err != nil {
			rows.Close()
			return err
		}
		legacy = append(legacy, r)
	}
	rows.Close()
	now := time.Now().UTC()
	for _, r := range legacy {
		// Don't overwrite an already-existing configs row — the runtime
		// has been writing there since this migration shipped, so an
		// existing row is the source of truth.
		var exists int
		err := d.db.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT 1 FROM configs WHERE kind='setting' AND scope='agent' AND scope_id=%s AND name='agents.defaults' LIMIT 1`,
				d.ph(1)),
			r.id).Scan(&exists)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check existing setting: %w", err)
		}
		cid := configRowID("setting", "agent", r.id, "agents.defaults")
		blob, _ := json.Marshal(map[string]string{"model": r.model})
		insertSQL := fmt.Sprintf(`INSERT INTO configs (id, kind, scope, scope_id, name, enabled, credential_key, data, created_at, updated_at)
			VALUES (%s, 'setting', 'agent', %s, 'agents.defaults', TRUE, '', %s, %s, %s)`,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5))
		if _, err := d.db.ExecContext(ctx, insertSQL, cid, r.id, string(blob), now, now); err != nil {
			return fmt.Errorf("relocate model for agent %s: %w", r.id, err)
		}
	}
	stmt := `ALTER TABLE agents DROP COLUMN model`
	if d.dialect == "postgres" {
		stmt = `ALTER TABLE agents DROP COLUMN IF EXISTS model`
	}
	if _, err := d.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("drop column: %w\nSQL: %s", err, stmt)
	}
	return nil
}

// migrateAgentsDropTemplateID removes the never-read template_id column
// from existing installs. Idempotent: silently no-ops when the column
// is already gone. SQLite needs 3.35+ for DROP COLUMN — every supported
// runtime here ships well above that, so we don't fall back to rebuild.
func (d *DBStore) migrateAgentsDropTemplateID(ctx context.Context) error {
	has, err := d.tableHasColumn(ctx, "agents", "template_id")
	if err != nil {
		return err
	}
	if !has {
		return nil
	}
	stmt := `ALTER TABLE agents DROP COLUMN template_id`
	if d.dialect == "postgres" {
		stmt = `ALTER TABLE agents DROP COLUMN IF EXISTS template_id`
	}
	if _, err := d.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("drop column: %w\nSQL: %s", err, stmt)
	}
	return nil
}

// migrateAgentFilesUserID retrofits the per-user override column on
// pre-existing installs. CREATE TABLE IF NOT EXISTS only fires on a
// fresh DB, so legacy databases keep the old (agent_id, filename) PK
// until this runs. Idempotent: detects the missing column and rebuilds
// the table once. SQLite has no ALTER TABLE for changing PKs, so we
// copy-rename. Postgres can ALTER directly.
func (d *DBStore) migrateAgentFilesUserID(ctx context.Context) error {
	hasUserID, err := d.tableHasColumn(ctx, "agent_files", "user_id")
	if err != nil {
		return err
	}
	if hasUserID {
		return nil
	}
	if d.dialect == "postgres" {
		stmts := []string{
			`ALTER TABLE agent_files ADD COLUMN user_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE agent_files DROP CONSTRAINT IF EXISTS agent_files_pkey`,
			`ALTER TABLE agent_files ADD PRIMARY KEY (agent_id, user_id, filename)`,
		}
		for _, s := range stmts {
			if _, err := d.db.ExecContext(ctx, s); err != nil {
				return fmt.Errorf("postgres migrate: %w\nSQL: %s", err, s)
			}
		}
		return nil
	}
	// SQLite: rebuild the table to widen the PK.
	stmts := []string{
		`CREATE TABLE agent_files_new (
			agent_id TEXT NOT NULL,
			user_id TEXT NOT NULL DEFAULT '',
			filename TEXT NOT NULL,
			content TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (agent_id, user_id, filename)
		)`,
		`INSERT INTO agent_files_new (agent_id, user_id, filename, content, updated_at)
			SELECT agent_id, '', filename, content, updated_at FROM agent_files`,
		`DROP TABLE agent_files`,
		`ALTER TABLE agent_files_new RENAME TO agent_files`,
	}
	for _, s := range stmts {
		if _, err := d.db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("sqlite rebuild: %w\nSQL: %s", err, s)
		}
	}
	return nil
}

// tableHasColumn returns true when the named column exists on the table.
// Backend-specific: Postgres reads information_schema; SQLite uses the
// PRAGMA table_info() pseudo-table.
func (d *DBStore) tableHasColumn(ctx context.Context, table, column string) (bool, error) {
	if d.dialect == "postgres" {
		row := d.db.QueryRowContext(ctx,
			`SELECT 1 FROM information_schema.columns
				WHERE table_name = $1 AND column_name = $2 LIMIT 1`,
			table, column)
		var x int
		if err := row.Scan(&x); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}
	rows, err := d.db.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (d *DBStore) migrationSQL() []string {
	return []string{
		// users holds first-party humans (role=super_admin/user) AND
		// app-provisioned end-users (role=app_user). The latter are
		// minted by an api_key on behalf of a downstream application;
		// they cannot log in (password_hash='' is rejected by the
		// password login path). apikey_id + external_id together
		// identify "which calling app, which of its end-users", and
		// the partial UNIQUE makes provisioning idempotent on that
		// pair so the same external user always resolves to the same
		// fastclaw user_id.
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL DEFAULT 'user',
			status TEXT NOT NULL DEFAULT 'active',
			apikey_id TEXT NOT NULL DEFAULT '',
			external_id TEXT NOT NULL DEFAULT '',
			avatar_url TEXT NOT NULL DEFAULT '',
			agent_quota INTEGER NOT NULL DEFAULT -1,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		// Idempotency lookup for app_user provisioning lives in
		// migrateUsersAppUserCols, not here — on existing installs the
		// CREATE TABLE above is a no-op and the apikey_id column
		// doesn't exist yet, so the index has to wait until the
		// column-add step has run.
		`CREATE TABLE IF NOT EXISTS web_sessions (
			sid TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			expires_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_web_sessions_user ON web_sessions (user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_web_sessions_expires ON web_sessions (expires_at)`,
		`CREATE TABLE IF NOT EXISTS push_devices (
			user_id TEXT NOT NULL,
			token TEXT NOT NULL,
			platform TEXT NOT NULL DEFAULT 'ios',
			environment TEXT NOT NULL DEFAULT '',
			bundle_id TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (user_id, token)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_push_devices_user ON push_devices (user_id)`,
		// type values: "admin" | "user" | "agent". The default 'agent'
		// preserves the pre-tier behavior on existing rows — every legacy
		// key was implicitly an "agent-scoped" key (explicit list in
		// apikey_agents), so the migration can backfill blindly. See
		// migrateAPIKeysAddType for the ALTER on existing installs.
		`CREATE TABLE IF NOT EXISTS apikeys (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			key_hash TEXT NOT NULL,
			key_prefix TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT 'agent',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_apikeys_user ON apikeys (user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_apikeys_key_hash ON apikeys (key_hash)`,
		`CREATE TABLE IF NOT EXISTS apikey_agents (
			apikey_id TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			PRIMARY KEY (apikey_id, agent_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_apikey_agents_agent ON apikey_agents (agent_id)`,
		// is_public flips the "anyone with the link can chat" gate.
		// Default 0 (private — owner-only). When 1, a non-owner who hits
		// the agent's chat URL gets the agent lazy-attached into their
		// own UserSpace; sessions/memory/agent_files stay keyed by the
		// chatter's user_id, so each chatter gets a private history
		// while the agent identity (SOUL.md, IDENTITY.md, skills) is
		// shared from the owner's row.
		`CREATE TABLE IF NOT EXISTS agents (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			config TEXT NOT NULL DEFAULT '{}',
			is_public BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_agents_user ON agents (user_id)`,
		// channel / account_id / chat_id together identify the
		// (channel-type, channel-instance, conversation) the session
		// belongs to. Multiple session_keys can share that triple — the
		// active one for IM routing is the row with the latest
		// updated_at, which is what `idx_sessions_chat_active` accelerates.
		// session_key is the per-session opaque id (PK), independent of
		// the triple, so a `/new` command in IM mints a fresh row under
		// the same (channel, account_id, chat_id).
		`CREATE TABLE IF NOT EXISTS sessions (
			user_id TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			session_key TEXT NOT NULL,
			channel TEXT NOT NULL DEFAULT '',
			account_id TEXT NOT NULL DEFAULT '',
			chat_id TEXT NOT NULL DEFAULT '',
			project_id TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			messages TEXT NOT NULL DEFAULT '[]',
			message_count INTEGER NOT NULL DEFAULT 0,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			-- chatter_user_id is the actual conversation participant. For
			-- web / dashboard chats it equals user_id (= the logged-in
			-- user). For IM channels with per-sender app_users it's the
			-- minted chatter row; user_id remains the channel-owner /
			-- UserSpace owner for backward-compat with admin views that
			-- list "all sessions on my bots". Empty on rows written
			-- before this column existed — readers should COALESCE to
			-- user_id in that case.
			chatter_user_id TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (user_id, agent_id, session_key)
		)`,
		// Index creation is moved to migrateSessionsAddChannelTriple so
		// it runs *after* the column-add ALTERs on legacy databases. If
		// it lived here, an upgrade install would try to create an index
		// referencing columns that the legacy table doesn't have yet.
		// session_messages is the append-only archive of every turn ever
		// written to a session. The sessions row above stores the
		// LLM-facing working set (post-compaction); session_messages
		// stores the original full history so UI / audit / multi-tenant
		// recovery has a source of truth that compaction never touches.
		// seq is a per-session monotonic counter assigned at INSERT time
		// via COALESCE(MAX(seq), -1)+1 so callers don't need a separate
		// SELECT round-trip. Composite PK doubles as the natural order.
		`CREATE TABLE IF NOT EXISTS session_messages (
			user_id TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			session_key TEXT NOT NULL,
			seq INTEGER NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL DEFAULT '',
			content_parts TEXT NOT NULL DEFAULT '',
			tool_calls TEXT NOT NULL DEFAULT '',
			tool_call_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			metadata TEXT NOT NULL DEFAULT '',
			thinking TEXT NOT NULL DEFAULT '',
			raw_assistant TEXT NOT NULL DEFAULT '',
			-- origin marks runtime-injected rows (currently only
			-- "goal_context"). Empty = real user / assistant exchange.
			-- WebChatHistory + FTS skip non-empty origin to keep
			-- synthetic prompts out of user-visible / searchable views.
			origin TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			-- chatter_user_id mirrors sessions.chatter_user_id — see that
			-- comment for semantics. Stored per row so a per-chatter
			-- query doesn't have to join through sessions.
			chatter_user_id TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (user_id, agent_id, session_key, seq)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_session_messages_lookup ON session_messages (user_id, agent_id, session_key, seq)`,
		// session_events is the real-time event stream the agent emits
		// during a turn (content chunks, tool_call, error, done).
		// Persisted so that a client that refreshes / reconnects
		// mid-turn can resume from its last-seen seq instead of
		// missing the in-flight delta. seq is per-(user, agent,
		// session) and assigned on INSERT via COALESCE(MAX(seq),-1)+1
		// — same pattern as session_messages. Compaction never
		// touches this table; the row only goes away when the parent
		// session is deleted (DeleteSession cascade).
		`CREATE TABLE IF NOT EXISTS session_events (
				user_id TEXT NOT NULL,
				agent_id TEXT NOT NULL,
				session_key TEXT NOT NULL,
				seq INTEGER NOT NULL,
				type TEXT NOT NULL,
				data TEXT NOT NULL DEFAULT '',
				created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				-- chatter_user_id mirrors sessions.chatter_user_id — see
				-- that comment for semantics.
				chatter_user_id TEXT NOT NULL DEFAULT '',
				PRIMARY KEY (user_id, agent_id, session_key, seq)
			)`,
		`CREATE INDEX IF NOT EXISTS idx_session_events_lookup ON session_events (user_id, agent_id, session_key, seq)`,
		// agent_files holds the agent's own files: SOUL.md, IDENTITY.md,
		// MEMORY.md, AGENTS.md, BOOTSTRAP.md, etc.
		//
		// user_id splits "agent template" from "per-user override":
		//   user_id='' — shared template, edited by the agent owner via
		//                the Customize page, visible to every chatter
		//                that didn't author their own override
		//   user_id=u_xxx — that user's personal copy (USER.md / MEMORY.md
		//                during chat, or a Personalize-for-me override)
		// Read path picks `user_id IN (chatter, '') ORDER BY user_id DESC
		// LIMIT 1`, so a user-specific row wins and missing rows fall
		// back to the template.
		`CREATE TABLE IF NOT EXISTS agent_files (
			agent_id TEXT NOT NULL,
			user_id TEXT NOT NULL DEFAULT '',
			filename TEXT NOT NULL,
			content TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (agent_id, user_id, filename)
		)`,
		// configs uses (user_id, agent_id) as the ownership pair, matching
		// agent_files / sessions / session_messages / session_events. The
		// older (scope, scope_id) pair is gone — scope was redundant
		// because the (user_id, agent_id) combo already encodes it:
		//   ('', '')   = system / global
		//   (X, '')    = user X's private config
		//   ('', Y)    = agent Y's "official" config (anyone using Y inherits)
		//   (X, Y)     = user X's per-agent override on agent Y — the
		//                multi-tenant case; lets two users sharing a
		//                public agent each bind their own channel.
		`CREATE TABLE IF NOT EXISTS configs (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			scope TEXT NOT NULL DEFAULT '',
			scope_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			data TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (kind, scope_id, name)
		)`,
		// idx_configs_lookup creation moved to
		// migrateConfigsScopeToUserAgent so it runs after the column-add
		// step on legacy databases (where the columns it references
		// don't exist yet at this point in migrationSQL). Fresh installs
		// hit the IF NOT EXISTS path inside the migrator and still get
		// the index.
		//
		// idx_configs_scope likewise deferred to migrateConfigsMergeScopeID
		// — on legacy databases scope_id does not exist yet at this point.
		`CREATE TABLE IF NOT EXISTS cron_jobs (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL DEFAULT '',
			agent_id TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT 'cron',
			schedule TEXT NOT NULL,
			message TEXT NOT NULL,
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			account_id TEXT NOT NULL DEFAULT '',
			timezone TEXT NOT NULL DEFAULT 'UTC',
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			last_run TIMESTAMP,
			next_run TIMESTAMP,
			locked_by TEXT,
			locked_at TIMESTAMP,
			-- failure_count tracks consecutive fire-attempts whose
			-- destination channel was missing/unreachable. The cron
			-- scheduler increments it on each miss and self-deletes the
			-- row once it crosses the threshold so a dead bot doesn't
			-- log forever.
			failure_count INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		// idx_cron_jobs_user creation is moved to migrateCronJobsAddUserID
		// so legacy installs hit it after the column-add ALTER. Fresh
		// installs reach the same code path via Migrate's full sweep.
		`CREATE INDEX IF NOT EXISTS idx_cron_jobs_schedule ON cron_jobs (enabled, next_run)`,
		`CREATE INDEX IF NOT EXISTS idx_cron_jobs_agent ON cron_jobs (agent_id)`,
		// projects groups sessions that share a workspace folder. PK
		// matches sessions: a project is "user X's working folder on
		// agent Y", same private-per-user ownership model. The on-disk
		// workspace dir lives at workspaces/<agent>/projects/<pid>/ and
		// is shared by every session whose project_id equals pid; the
		// per-session sessions/<chat>/ subdir is bypassed for project
		// sessions so files persist across chats inside the project.
		`CREATE TABLE IF NOT EXISTS projects (
			user_id TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			project_id TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (user_id, agent_id, project_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_projects_listing ON projects (user_id, agent_id, updated_at DESC)`,
		// project_runtimes is the live-app layer on top of a project: at
		// most one running instance (long-lived sandbox + dev server +
		// preview URL) per project. Same PK as projects — a runtime is
		// 1:1 with its project and shares its ownership. Kept in a
		// separate table so the existing project feature is untouched;
		// dropping every row here degrades gracefully to "no previews",
		// it never affects chat grouping or workspace files.
		`CREATE TABLE IF NOT EXISTS project_runtimes (
			user_id TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			project_id TEXT NOT NULL,
			template_ref TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'none',
			dev_port INTEGER NOT NULL DEFAULT 0,
			host_port INTEGER NOT NULL DEFAULT 0,
			preview_url TEXT NOT NULL DEFAULT '',
			container_id TEXT NOT NULL DEFAULT '',
			git_ref TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (user_id, agent_id, project_id)
		)`,
		// agent_goals backs the /goal feature: one persistent objective
		// per (agent, session). The UNIQUE (agent_id, session_key)
		// constraint is the source of truth for "this session already
		// has a goal" — CreateGoal translates the conflict into
		// ErrGoalAlreadyExists.
		`CREATE TABLE IF NOT EXISTS agent_goals (
			id TEXT PRIMARY KEY,
			agent_id TEXT NOT NULL,
			session_key TEXT NOT NULL,
			owner_user_id TEXT NOT NULL,
			-- Routing tuple, stamped at create time so a continuation
			-- can publish onto the same bus address the original turn
			-- arrived on. Mirrors cron_jobs' channel/chat_id columns.
			channel TEXT NOT NULL DEFAULT '',
			account_id TEXT NOT NULL DEFAULT '',
			chat_id TEXT NOT NULL DEFAULT '',
			project_id TEXT NOT NULL DEFAULT '',
			objective TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			token_budget BIGINT,
			tokens_used BIGINT NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_goals_session ON agent_goals (agent_id, session_key)`,
		// token_usage_daily is the per-(day, user, agent, session,
		// provider, model) counter behind the admin Usage dashboard.
		// Every successful LLM Chat / ChatStream lands one row via
		// UPSERT — see internal/usage.SQLMeter. Empty user_id is
		// preserved on write (admin-owned or cron-fired agents) and
		// rendered as "system" in the UI. Provider is the per-agent
		// override key (e.g. "anthropic-messages"); "" means the agent
		// used the shared provider with no override. The PK is the
		// six-tuple so GROUP BY any subset rolls up cleanly without
		// extra indexing.
		`CREATE TABLE IF NOT EXISTS token_usage_daily (
			day DATE NOT NULL,
			user_id TEXT NOT NULL DEFAULT '',
			agent_id TEXT NOT NULL DEFAULT '',
			session_key TEXT NOT NULL DEFAULT '',
			provider TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			input_tokens BIGINT NOT NULL DEFAULT 0,
			output_tokens BIGINT NOT NULL DEFAULT 0,
			cache_read_tokens BIGINT NOT NULL DEFAULT 0,
			cache_create_tokens BIGINT NOT NULL DEFAULT 0,
			request_count BIGINT NOT NULL DEFAULT 0,
			channel TEXT NOT NULL DEFAULT '',
			chatter_user_id TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (day, user_id, agent_id, session_key, provider, model)
		)`,
		// Range scans on day are the dominant query (24h/7d/30d filter)
		// — the PK starts with day so SQLite/Postgres both use it without
		// a secondary index. The extra indexes below speed up
		// non-time-prefixed lookups (e.g. "all rows for agent X across
		// all time") when the table grows.
		`CREATE INDEX IF NOT EXISTS idx_token_usage_agent ON token_usage_daily (agent_id, day)`,
		`CREATE INDEX IF NOT EXISTS idx_token_usage_user ON token_usage_daily (user_id, day)`,
		// quotas stores per-user monthly token/request ceilings set by
		// upstream SaaS apps (e.g. weclaw) via PUT /v1/quota. The agent
		// loop checks this before every LLM call so channel messages
		// that arrive when the user is over-limit get a friendly
		// rejection. One row per user_id; UPSERT on write.
		`CREATE TABLE IF NOT EXISTS quotas (
			user_id TEXT NOT NULL PRIMARY KEY,
			monthly_token_limit BIGINT NOT NULL DEFAULT 0,
			monthly_request_limit BIGINT NOT NULL DEFAULT 0,
			reset_day INTEGER NOT NULL DEFAULT 1,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		// token_usage_log is the append-only per-LLM-call audit trail.
		// Unlike token_usage_daily (which UPSERTs into daily buckets),
		// this table INSERTs one row per provider.Chat / ChatStream
		// call so upstream SaaS apps can show "this message cost N
		// tokens" in their billing UI. session_key + created_at let
		// callers correlate with session_messages for a full picture.
		// No AUTOINCREMENT — SQLite rowid alias auto-increments;
		// Postgres uses GENERATED BY DEFAULT on the INTEGER PRIMARY KEY.
		`CREATE TABLE IF NOT EXISTS token_usage_log (
			user_id TEXT NOT NULL DEFAULT '',
			agent_id TEXT NOT NULL DEFAULT '',
			session_key TEXT NOT NULL DEFAULT '',
			provider TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			input_tokens BIGINT NOT NULL DEFAULT 0,
			output_tokens BIGINT NOT NULL DEFAULT 0,
			cache_read_tokens BIGINT NOT NULL DEFAULT 0,
			cache_create_tokens BIGINT NOT NULL DEFAULT 0,
			duration_ms BIGINT NOT NULL DEFAULT 0,
			channel TEXT NOT NULL DEFAULT '',
			chatter_user_id TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_token_usage_log_user ON token_usage_log (user_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_token_usage_log_session ON token_usage_log (user_id, agent_id, session_key)`,
		// channel_leases gates polling / persistent-connection channel
		// adapters (WeChat, Telegram, Discord, Slack, Feishu long-conn)
		// to one process at a time. Without it, two cloud replicas
		// sharing the same bot token would both long-poll the upstream
		// server and the user would receive every reply twice. The
		// leaseholder renews periodically; on crash the lease expires
		// and another instance takes over. See channels.Manager and
		// channels.runWithLease.
		`CREATE TABLE IF NOT EXISTS channel_leases (
			channel TEXT NOT NULL,
			account_id TEXT NOT NULL,
			holder_id TEXT NOT NULL,
			expires_at TIMESTAMP NOT NULL,
			PRIMARY KEY (channel, account_id)
		)`,
		// channels is the dedicated IM bot binding table. Extracted from
		// configs (kind='channel') so channel entities have their own
		// lifecycle, credentials, and routing independent of config rows.
		`CREATE TABLE IF NOT EXISTS channels (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL DEFAULT '',
			agent_id TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL,
			account_id TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			bot_token TEXT NOT NULL DEFAULT '',
			base_url TEXT NOT NULL DEFAULT '',
			platform_user_id TEXT NOT NULL DEFAULT '',
			shared_identity INTEGER NOT NULL DEFAULT 0,
			data TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (type, account_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_channels_user ON channels (user_id, agent_id)`,
	}
}

func (d *DBStore) Close() error {
	return d.db.Close()
}

// ph returns the correct placeholder for the dialect.
func (d *DBStore) ph(n int) string {
	if d.dialect == "postgres" {
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

// scanErr wraps sql.ErrNoRows in our public ErrNotFound.
func scanErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// --- Users ---

// userColumns is the canonical select list — keep ordering aligned with
// the Scan calls below so adding a column means editing both lines.
const userColumns = `id, username, email, password_hash, display_name, role, status, apikey_id, external_id, avatar_url, agent_quota, created_at, updated_at, owner_user_id`

func scanUser(scanner interface{ Scan(dest ...any) error }) (*UserRecord, error) {
	var u UserRecord
	if err := scanner.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.DisplayName, &u.Role, &u.Status, &u.APIKeyID, &u.ExternalID, &u.AvatarURL, &u.AgentQuota, &u.CreatedAt, &u.UpdatedAt, &u.OwnerUserID); err != nil {
		return nil, err
	}
	return &u, nil
}

func (d *DBStore) CreateUser(ctx context.Context, u *UserRecord) error {
	now := time.Now().UTC()
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now
	}
	u.UpdatedAt = now
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO users (id, username, email, password_hash, display_name, role, status, apikey_id, external_id, avatar_url, agent_quota, created_at, updated_at, owner_user_id)
			VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)`,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5), d.ph(6), d.ph(7), d.ph(8), d.ph(9), d.ph(10), d.ph(11), d.ph(12), d.ph(13), d.ph(14)),
		u.ID, u.Username, u.Email, u.PasswordHash, u.DisplayName, u.Role, u.Status, u.APIKeyID, u.ExternalID, u.AvatarURL, u.AgentQuota, u.CreatedAt, u.UpdatedAt, u.OwnerUserID)
	return err
}

func (d *DBStore) GetUser(ctx context.Context, id string) (*UserRecord, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT `+userColumns+` FROM users WHERE id = %s`, d.ph(1)), id)
	u, err := scanUser(row)
	if err != nil {
		return nil, scanErr(err)
	}
	return u, nil
}

func (d *DBStore) GetUserByLogin(ctx context.Context, usernameOrEmail string) (*UserRecord, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT `+userColumns+` FROM users WHERE username = %s OR email = %s LIMIT 1`, d.ph(1), d.ph(2)),
		usernameOrEmail, usernameOrEmail)
	u, err := scanUser(row)
	if err != nil {
		return nil, scanErr(err)
	}
	return u, nil
}

// GetUserByExternal looks up an app_user/chatter by (owner_user_id, external_id).
// Returns ErrNotFound when nothing matches — used by the lazy-mint
// flow on api_key chat calls and by the explicit provisioning endpoint
// to make creation idempotent on re-entry.
func (d *DBStore) GetUserByExternal(ctx context.Context, ownerUserID, externalID string) (*UserRecord, error) {
	if ownerUserID == "" || externalID == "" {
		return nil, ErrNotFound
	}
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT `+userColumns+` FROM users WHERE owner_user_id = %s AND external_id = %s ORDER BY created_at ASC LIMIT 1`,
			d.ph(1), d.ph(2)),
		ownerUserID, externalID)
	u, err := scanUser(row)
	if err != nil {
		return nil, scanErr(err)
	}
	return u, nil
}

// GetUserByExternalSuffix looks up an app_user/chatter by owner + external_id
// suffix match. Used by resolveChatter to find legacy chatter rows whose
// external_id is "channel:accountID:platformUserID" when only
// "channel:" and ":platformUserID" are known (accountID changed on bot
// reconnect). Returns the most recently created match.
func (d *DBStore) GetUserByExternalSuffix(ctx context.Context, ownerUserID, prefix, suffix string) (*UserRecord, error) {
	if ownerUserID == "" || prefix == "" || suffix == "" {
		return nil, ErrNotFound
	}
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT `+userColumns+` FROM users
			WHERE owner_user_id = %s AND external_id LIKE %s AND external_id LIKE %s
			ORDER BY created_at DESC LIMIT 1`,
			d.ph(1), d.ph(2), d.ph(3)),
		ownerUserID, prefix+"%", "%"+suffix)
	u, err := scanUser(row)
	if err != nil {
		return nil, scanErr(err)
	}
	return u, nil
}

func (d *DBStore) ListUsers(ctx context.Context) ([]UserRecord, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserRecord
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func (d *DBStore) UpdateUser(ctx context.Context, u *UserRecord) error {
	u.UpdatedAt = time.Now().UTC()
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE users SET username = %s, email = %s, password_hash = %s, display_name = %s,
			role = %s, status = %s, avatar_url = %s, agent_quota = %s, updated_at = %s WHERE id = %s`,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5), d.ph(6), d.ph(7), d.ph(8), d.ph(9), d.ph(10)),
		u.Username, u.Email, u.PasswordHash, u.DisplayName, u.Role, u.Status, u.AvatarURL, u.AgentQuota, u.UpdatedAt, u.ID)
	return err
}

func (d *DBStore) DeleteUser(ctx context.Context, id string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// First, find every agent owned by this user — we'll cascade through
	// per-agent state (cron, agent_files, sessions, configs) before
	// dropping the agents themselves.
	rows, err := tx.QueryContext(ctx,
		fmt.Sprintf("SELECT id FROM agents WHERE user_id = %s", d.ph(1)), id)
	if err != nil {
		return err
	}
	var ownedAgents []string
	for rows.Next() {
		var aid string
		if err := rows.Scan(&aid); err != nil {
			rows.Close()
			return err
		}
		ownedAgents = append(ownedAgents, aid)
	}
	rows.Close()
	for _, aid := range ownedAgents {
		for _, t := range []string{"agent_files", "sessions", "session_messages", "session_events", "cron_jobs"} {
			if _, err := tx.ExecContext(ctx,
				fmt.Sprintf("DELETE FROM %s WHERE agent_id = %s", t, d.ph(1)), aid); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf("DELETE FROM apikey_agents WHERE agent_id = %s", d.ph(1)), aid); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf("DELETE FROM configs WHERE scope_id = %s OR scope_id LIKE %s", d.ph(1), d.ph(2)),
			aid, "%/"+aid); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM agents WHERE user_id = %s", d.ph(1)), id); err != nil {
		return err
	}
	// Per-user state that's not agent-scoped (agent_files is now agent-only).
	for _, t := range []string{"web_sessions", "apikeys", "sessions", "session_messages", "session_events"} {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE user_id = %s", t, d.ph(1)), id); err != nil {
			return err
		}
	}
	// Drop every config row owned by this user — both their own
	// ('user_id=X, agent_id="') and any per-agent overrides they
	// authored on someone else's agent ('user_id=X, agent_id=Y').
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM configs WHERE user_id = %s", d.ph(1)), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM apikey_agents WHERE apikey_id NOT IN (SELECT id FROM apikeys)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM users WHERE id = %s", d.ph(1)), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DBStore) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// --- Web sessions ---

func (d *DBStore) CreateWebSession(ctx context.Context, s *WebSessionRecord) error {
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO web_sessions (sid, user_id, created_at, expires_at) VALUES (%s, %s, %s, %s)`,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4)),
		s.SID, s.UserID, s.CreatedAt, s.ExpiresAt)
	return err
}

func (d *DBStore) GetWebSession(ctx context.Context, sid string) (*WebSessionRecord, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT sid, user_id, created_at, expires_at FROM web_sessions WHERE sid = %s`, d.ph(1)), sid)
	var s WebSessionRecord
	if err := row.Scan(&s.SID, &s.UserID, &s.CreatedAt, &s.ExpiresAt); err != nil {
		return nil, scanErr(err)
	}
	return &s, nil
}

func (d *DBStore) DeleteWebSession(ctx context.Context, sid string) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM web_sessions WHERE sid = %s`, d.ph(1)), sid)
	return err
}

func (d *DBStore) DeleteExpiredWebSessions(ctx context.Context, before time.Time) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM web_sessions WHERE expires_at < %s`, d.ph(1)), before)
	return err
}

// --- Mobile push devices ---

func (d *DBStore) SavePushDevice(ctx context.Context, dev *PushDeviceRecord) error {
	if dev == nil {
		return errors.New("store: push device is nil")
	}
	if dev.UserID == "" || dev.Token == "" {
		return errors.New("store: push device requires user_id and token")
	}
	now := time.Now().UTC()
	if dev.CreatedAt.IsZero() {
		dev.CreatedAt = now
	}
	dev.UpdatedAt = now
	if d.dialect == "postgres" {
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO push_devices (user_id, token, platform, environment, bundle_id, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)
			 ON CONFLICT (user_id, token) DO UPDATE SET
			   platform=$3, environment=$4, bundle_id=$5, updated_at=$7`,
			dev.UserID, dev.Token, dev.Platform, dev.Environment, dev.BundleID, dev.CreatedAt, dev.UpdatedAt)
		return err
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO push_devices (user_id, token, platform, environment, bundle_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (user_id, token) DO UPDATE SET
		   platform=excluded.platform, environment=excluded.environment, bundle_id=excluded.bundle_id, updated_at=excluded.updated_at`,
		dev.UserID, dev.Token, dev.Platform, dev.Environment, dev.BundleID, dev.CreatedAt, dev.UpdatedAt)
	return err
}

func (d *DBStore) DeletePushDevice(ctx context.Context, userID, token string) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM push_devices WHERE user_id = %s AND token = %s`, d.ph(1), d.ph(2)),
		userID, token)
	return err
}

func (d *DBStore) ListPushDevices(ctx context.Context, userID string) ([]PushDeviceRecord, error) {
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT user_id, token, platform, environment, bundle_id, created_at, updated_at FROM push_devices WHERE user_id = %s ORDER BY updated_at DESC`, d.ph(1)),
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]PushDeviceRecord, 0)
	for rows.Next() {
		var dev PushDeviceRecord
		if err := rows.Scan(&dev.UserID, &dev.Token, &dev.Platform, &dev.Environment, &dev.BundleID, &dev.CreatedAt, &dev.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, dev)
	}
	return out, rows.Err()
}

// --- API keys ---

func (d *DBStore) ListAPIKeys(ctx context.Context, userID string) ([]APIKeyRecord, error) {
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT id, user_id, name, key_hash, key_prefix, type, created_at FROM apikeys WHERE user_id = %s ORDER BY created_at`, d.ph(1)),
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKeyRecord
	for rows.Next() {
		var ak APIKeyRecord
		if err := rows.Scan(&ak.ID, &ak.UserID, &ak.Name, &ak.KeyHash, &ak.KeyPrefix, &ak.Type, &ak.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, ak)
	}
	return out, rows.Err()
}

func (d *DBStore) GetAPIKey(ctx context.Context, id string) (*APIKeyRecord, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT id, user_id, name, key_hash, key_prefix, type, created_at FROM apikeys WHERE id = %s`, d.ph(1)), id)
	var ak APIKeyRecord
	if err := row.Scan(&ak.ID, &ak.UserID, &ak.Name, &ak.KeyHash, &ak.KeyPrefix, &ak.Type, &ak.CreatedAt); err != nil {
		return nil, scanErr(err)
	}
	return &ak, nil
}

func (d *DBStore) CreateAPIKey(ctx context.Context, ak *APIKeyRecord) error {
	if ak.CreatedAt.IsZero() {
		ak.CreatedAt = time.Now().UTC()
	}
	if ak.Type == "" {
		ak.Type = "agent"
	}
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO apikeys (id, user_id, name, key_hash, key_prefix, type, created_at) VALUES (%s, %s, %s, %s, %s, %s, %s)`,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5), d.ph(6), d.ph(7)),
		ak.ID, ak.UserID, ak.Name, ak.KeyHash, ak.KeyPrefix, ak.Type, ak.CreatedAt)
	return err
}

func (d *DBStore) DeleteAPIKey(ctx context.Context, id string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM apikey_agents WHERE apikey_id = %s`, d.ph(1)), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM apikeys WHERE id = %s`, d.ph(1)), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DBStore) RotateAPIKey(ctx context.Context, id, keyHash, keyPrefix string) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE apikeys SET key_hash = %s, key_prefix = %s WHERE id = %s`,
			d.ph(1), d.ph(2), d.ph(3)),
		keyHash, keyPrefix, id)
	return err
}

func (d *DBStore) LookupAPIKeyByHash(ctx context.Context, keyHash string) (*APIKeyRecord, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT id, user_id, name, key_hash, key_prefix, type, created_at FROM apikeys WHERE key_hash = %s`, d.ph(1)),
		keyHash)
	var ak APIKeyRecord
	if err := row.Scan(&ak.ID, &ak.UserID, &ak.Name, &ak.KeyHash, &ak.KeyPrefix, &ak.Type, &ak.CreatedAt); err != nil {
		return nil, scanErr(err)
	}
	return &ak, nil
}

// --- API key ↔ agent permissions ---

func (d *DBStore) SetAPIKeyAgents(ctx context.Context, apikeyID string, agentIDs []string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM apikey_agents WHERE apikey_id = %s`, d.ph(1)), apikeyID); err != nil {
		return err
	}
	for _, aid := range agentIDs {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`INSERT INTO apikey_agents (apikey_id, agent_id) VALUES (%s, %s)`, d.ph(1), d.ph(2)),
			apikeyID, aid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DBStore) ListAPIKeyAgents(ctx context.Context, apikeyID string) ([]string, error) {
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT agent_id FROM apikey_agents WHERE apikey_id = %s ORDER BY agent_id`, d.ph(1)),
		apikeyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var aid string
		if err := rows.Scan(&aid); err != nil {
			return nil, err
		}
		out = append(out, aid)
	}
	return out, rows.Err()
}

func (d *DBStore) APIKeyCanAccessAgent(ctx context.Context, apikeyID, agentID string) (bool, error) {
	var n int
	err := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM apikey_agents WHERE apikey_id = %s AND agent_id = %s`, d.ph(1), d.ph(2)),
		apikeyID, agentID).Scan(&n)
	return n > 0, err
}

// --- Agents ---

const agentSelectCols = `id, user_id, name, config, is_public, created_at, updated_at`

func (d *DBStore) ListAgents(ctx context.Context, ownerUserID string) ([]AgentRecord, error) {
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT `+agentSelectCols+` FROM agents WHERE user_id = %s ORDER BY created_at DESC`, d.ph(1)),
		ownerUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgents(rows)
}

func (d *DBStore) ListPublicAgents(ctx context.Context) ([]AgentRecord, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+agentSelectCols+` FROM agents WHERE is_public = TRUE ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgents(rows)
}

func (d *DBStore) GetAgent(ctx context.Context, agentID string) (*AgentRecord, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT `+agentSelectCols+` FROM agents WHERE id = %s`, d.ph(1)), agentID)
	var ag AgentRecord
	var cfgStr string
	if err := row.Scan(&ag.ID, &ag.UserID, &ag.Name, &cfgStr, &ag.IsPublic, &ag.CreatedAt, &ag.UpdatedAt); err != nil {
		return nil, scanErr(err)
	}
	json.Unmarshal([]byte(cfgStr), &ag.Config)
	return &ag, nil
}

func (d *DBStore) SaveAgent(ctx context.Context, agent *AgentRecord) error {
	if agent.ID == "" {
		return errors.New("store: agent.id is required")
	}
	if agent.UserID == "" {
		return errors.New("store: agent.user_id is required")
	}
	cfgData, _ := json.Marshal(agent.Config)
	now := time.Now().UTC()
	if agent.CreatedAt.IsZero() {
		agent.CreatedAt = now
	}
	agent.UpdatedAt = now
	if d.dialect == "postgres" {
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO agents (id, user_id, name, config, is_public, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (id) DO UPDATE
				SET user_id=$2, name=$3, config=$4, is_public=$5, updated_at=$7`,
			agent.ID, agent.UserID, agent.Name, string(cfgData), agent.IsPublic, agent.CreatedAt, agent.UpdatedAt)
		return err
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO agents (id, user_id, name, config, is_public, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET
			  user_id=excluded.user_id, name=excluded.name,
			  config=excluded.config, is_public=excluded.is_public,
			  updated_at=excluded.updated_at`,
		agent.ID, agent.UserID, agent.Name, string(cfgData), agent.IsPublic, agent.CreatedAt, agent.UpdatedAt)
	return err
}

func (d *DBStore) DeleteAgent(ctx context.Context, agentID string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range []string{
		"agent_files",
		"sessions",
		"session_messages",
		"session_events",
		"cron_jobs",
		"projects",
		"project_runtimes",
		"agent_goals",
	} {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE agent_id = %s`, t, d.ph(1)), agentID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM apikey_agents WHERE agent_id = %s`, d.ph(1)), agentID); err != nil {
		return err
	}
	// Drop every config row pointing at this agent — official agent rows
	// (scope_id=X) and per-user agent overrides (scope_id=user/X).
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM configs WHERE scope_id = %s OR scope_id LIKE %s`, d.ph(1), d.ph(2)),
		agentID, "%/"+agentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM agents WHERE id = %s`, d.ph(1)), agentID); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DBStore) ListAllAgents(ctx context.Context) ([]AgentRecord, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+agentSelectCols+` FROM agents ORDER BY user_id, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgents(rows)
}

func scanAgents(rows *sql.Rows) ([]AgentRecord, error) {
	var out []AgentRecord
	for rows.Next() {
		var ag AgentRecord
		var cfgStr string
		if err := rows.Scan(&ag.ID, &ag.UserID, &ag.Name, &cfgStr, &ag.IsPublic, &ag.CreatedAt, &ag.UpdatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(cfgStr), &ag.Config)
		out = append(out, ag)
	}
	return out, rows.Err()
}

// --- Sessions ---

func (d *DBStore) GetSession(ctx context.Context, userID, agentID, sessionKey string) (*SessionRecord, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT messages, channel, account_id, chat_id, project_id, updated_at FROM sessions WHERE user_id = %s AND agent_id = %s AND session_key = %s`,
			d.ph(1), d.ph(2), d.ph(3)),
		userID, agentID, sessionKey)
	var msgsStr string
	var rec SessionRecord
	if err := row.Scan(&msgsStr, &rec.Channel, &rec.AccountID, &rec.ChatID, &rec.ProjectID, &rec.UpdatedAt); err != nil {
		return nil, scanErr(err)
	}
	json.Unmarshal([]byte(msgsStr), &rec.Messages)
	return &rec, nil
}

// LookupSessionOwner returns the user_id that owns the given session row.
func (d *DBStore) LookupSessionOwner(ctx context.Context, agentID, sessionKey string) (string, error) {
	var uid string
	err := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT user_id FROM sessions WHERE agent_id = %s AND session_key = %s`,
			d.ph(1), d.ph(2)),
		agentID, sessionKey).Scan(&uid)
	if err != nil {
		return "", scanErr(err)
	}
	return uid, nil
}

// GetSessionByKey loads a session by (agentID, sessionKey) without
// user_id scoping. Safe because session_key is globally unique.
func (d *DBStore) GetSessionByKey(ctx context.Context, agentID, sessionKey string) (*SessionRecord, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT messages, channel, account_id, chat_id, project_id, updated_at FROM sessions WHERE agent_id = %s AND session_key = %s`,
			d.ph(1), d.ph(2)),
		agentID, sessionKey)
	var msgsStr string
	var rec SessionRecord
	if err := row.Scan(&msgsStr, &rec.Channel, &rec.AccountID, &rec.ChatID, &rec.ProjectID, &rec.UpdatedAt); err != nil {
		return nil, scanErr(err)
	}
	json.Unmarshal([]byte(msgsStr), &rec.Messages)
	return &rec, nil
}

// SaveSession upserts the session row. Channel / AccountID / ChatID /
// ProjectID are written on INSERT only; the ON CONFLICT branch
// deliberately preserves the existing values so a callback that didn't
// know the triple (e.g. compaction calling ReplaceMessages) can't
// accidentally clear it.
func (d *DBStore) SaveSession(ctx context.Context, userID, agentID, sessionKey string, session *SessionRecord) error {
	if userID == "" {
		return errors.New("store: SaveSession requires user_id")
	}
	msgsData, _ := json.Marshal(session.Messages)
	now := time.Now().UTC()
	count := len(session.Messages)
	// Per-turn chatter (= the actual conversation participant) is plumbed
	// via ctx so this signature stays backward compatible. Empty when no
	// upstream caller tagged ctx — readers fall back to user_id.
	chatterID := ChatterUserIDFromContext(ctx)
	if d.dialect == "postgres" {
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO sessions (user_id, agent_id, session_key, channel, account_id, chat_id, project_id, messages, message_count, updated_at, chatter_user_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
				ON CONFLICT (user_id, agent_id, session_key) DO UPDATE
				SET messages=$8, message_count=$9, updated_at=$10,
				    chatter_user_id = CASE WHEN $11 <> '' THEN $11 ELSE sessions.chatter_user_id END`,
			userID, agentID, sessionKey, session.Channel, session.AccountID, session.ChatID, session.ProjectID,
			string(msgsData), count, now, chatterID)
		return err
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO sessions (user_id, agent_id, session_key, channel, account_id, chat_id, project_id, messages, message_count, updated_at, chatter_user_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (user_id, agent_id, session_key) DO UPDATE SET
			  messages=excluded.messages, message_count=excluded.message_count, updated_at=excluded.updated_at,
			  chatter_user_id = CASE WHEN excluded.chatter_user_id <> '' THEN excluded.chatter_user_id ELSE sessions.chatter_user_id END`,
		userID, agentID, sessionKey, session.Channel, session.AccountID, session.ChatID, session.ProjectID,
		string(msgsData), count, now, chatterID)
	return err
}

func (d *DBStore) ListSessions(ctx context.Context, userID, agentID string) ([]SessionMeta, error) {
	// Include sessions owned by the caller AND sessions owned by any
	// app_user whose owner_user_id is the caller. This surfaces IM
	// conversations routed through an API-key-provisioned app_user's
	// channel binding in the web dashboard sidebar.
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT session_key, user_id, channel, account_id, chat_id, project_id, title, message_count, updated_at, COALESCE(chatter_user_id,'') FROM sessions
			WHERE agent_id = %s AND (
				user_id = %s OR user_id IN (
					SELECT id FROM users WHERE owner_user_id = %s
				)
			) ORDER BY updated_at DESC`, d.ph(1), d.ph(2), d.ph(3)),
		agentID, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var metas []SessionMeta
	for rows.Next() {
		var m SessionMeta
		if err := rows.Scan(&m.Key, &m.UserID, &m.Channel, &m.AccountID, &m.ChatID, &m.ProjectID, &m.Title, &m.MessageCount, &m.UpdatedAt, &m.ChatterUserID); err != nil {
			return nil, err
		}
		metas = append(metas, m)
	}
	return metas, rows.Err()
}

// ListSessionOwnerPairs enumerates every distinct (user_id, agent_id)
// tuple in the sessions table. The admin Chats page calls this to find
// all conversation owners (chatters/binders) across all agents — the
// per-(owner, agent) ListSessions would miss sessions where a non-owner
// user chats with a public agent or binds an IM bot to it, because
// those rows live under the chatter's user_id, not the agent owner's.
func (d *DBStore) ListSessionOwnerPairs(ctx context.Context) ([]SessionOwnerPair, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT DISTINCT user_id, agent_id FROM sessions
			WHERE user_id <> '' AND agent_id <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pairs []SessionOwnerPair
	for rows.Next() {
		var p SessionOwnerPair
		if err := rows.Scan(&p.UserID, &p.AgentID); err != nil {
			return nil, err
		}
		pairs = append(pairs, p)
	}
	return pairs, rows.Err()
}

// ListSessionOwnerPairsByAgents returns distinct (user_id, agent_id)
// pairs restricted to the given agent IDs.
func (d *DBStore) ListSessionOwnerPairsByAgents(ctx context.Context, agentIDs []string) ([]SessionOwnerPair, error) {
	if len(agentIDs) == 0 {
		return nil, nil
	}
	// Build placeholders for the IN clause.
	placeholders := make([]string, len(agentIDs))
	args := make([]any, len(agentIDs))
	for i, id := range agentIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := `SELECT DISTINCT user_id, agent_id FROM sessions
		WHERE user_id <> '' AND agent_id <> ''
		AND agent_id IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pairs []SessionOwnerPair
	for rows.Next() {
		var p SessionOwnerPair
		if err := rows.Scan(&p.UserID, &p.AgentID); err != nil {
			return nil, err
		}
		pairs = append(pairs, p)
	}
	return pairs, rows.Err()
}

func (d *DBStore) ListSessionsPaginated(ctx context.Context, agentIDs []string, offset, limit int) ([]SessionMeta, int, error) {
	var where string
	var args []any
	if agentIDs != nil {
		if len(agentIDs) == 0 {
			return nil, 0, nil
		}
		phs := make([]string, len(agentIDs))
		args = make([]any, len(agentIDs))
		for i, id := range agentIDs {
			phs[i] = d.ph(i + 1)
			args[i] = id
		}
		where = `WHERE agent_id IN (` + strings.Join(phs, ",") + `)`
	}
	// Total count.
	var total int
	countQ := `SELECT COUNT(*) FROM sessions ` + where
	if err := d.db.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	// Page query.
	dataQ := fmt.Sprintf(`SELECT session_key, user_id, agent_id, channel, account_id, chat_id, project_id, title, message_count, updated_at, COALESCE(chatter_user_id,'')
		FROM sessions %s ORDER BY updated_at DESC LIMIT %d OFFSET %d`, where, limit, offset)
	rows, err := d.db.QueryContext(ctx, dataQ, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var metas []SessionMeta
	for rows.Next() {
		var m SessionMeta
		var agentID string
		if err := rows.Scan(&m.Key, &m.UserID, &agentID, &m.Channel, &m.AccountID, &m.ChatID, &m.ProjectID, &m.Title, &m.MessageCount, &m.UpdatedAt, &m.ChatterUserID); err != nil {
			return nil, 0, err
		}
		m.AgentID = agentID
		metas = append(metas, m)
	}
	return metas, total, rows.Err()
}

// LookupSessionTriple is ResolveActiveSessionKey's inverse: given a
// session_key (the canonical row id), return the (channel, accountID,
// chatID) it belongs to. Used by handlers that take a session_key from
// a URL and need the original chat_id — e.g. to keep workspace files
// namespaced by the conversation rather than the session.
func (d *DBStore) LookupSessionTriple(ctx context.Context, userID, agentID, sessionKey string) (string, string, string, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT channel, account_id, chat_id FROM sessions
			WHERE user_id = %s AND agent_id = %s AND session_key = %s`,
			d.ph(1), d.ph(2), d.ph(3)),
		userID, agentID, sessionKey)
	var ch, acc, ci string
	if err := row.Scan(&ch, &acc, &ci); err != nil {
		return "", "", "", scanErr(err)
	}
	return ch, acc, ci, nil
}

// LookupSessionProject returns the project_id of a session_key (or "")
// — the workspace path resolver consults this to decide between
// projects/<id>/ and sessions/<chat>/ for the sandbox mount.
func (d *DBStore) LookupSessionProject(ctx context.Context, userID, agentID, sessionKey string) (string, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT project_id FROM sessions
			WHERE user_id = %s AND agent_id = %s AND session_key = %s`,
			d.ph(1), d.ph(2), d.ph(3)),
		userID, agentID, sessionKey)
	var pid string
	if err := row.Scan(&pid); err != nil {
		return "", scanErr(err)
	}
	return pid, nil
}

// ResolveActiveSessionKey returns the most recently updated session_key
// for the (channel, account_id, chat_id) triple within (user, agent), or
// ErrNotFound. The triple is the natural address for IM routing — IM
// adapters carry no session id of their own, so the gateway picks the
// freshest thread when a message arrives. `/new` mints a fresh row that
// then wins the ORDER BY on subsequent resolves.
func (d *DBStore) ResolveActiveSessionKey(ctx context.Context, userID, agentID, channel, accountID, chatID string) (string, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT session_key FROM sessions
			WHERE user_id = %s AND agent_id = %s
			  AND channel = %s AND account_id = %s AND chat_id = %s
			ORDER BY updated_at DESC LIMIT 1`,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5)),
		userID, agentID, channel, accountID, chatID)
	var key string
	if err := row.Scan(&key); err == nil {
		return key, nil
	}
	// Fallback for IM channels: when the bot is re-scanned the
	// accountID changes (e.g. iLink assigns a new bot ID) but the
	// chatID (platform user openid) stays the same. Try matching by
	// (channel, chatID) only so the conversation continues in the
	// existing session instead of minting a fresh one. Also widen
	// user_id to include child app_users (the session may have been
	// created under a different app_user or the web user).
	if channel != "" && channel != "web" && channel != "api" && channel != "shared" {
		row = d.db.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT session_key FROM sessions
				WHERE agent_id = %s AND channel = %s AND chat_id = %s
				  AND user_id IN (SELECT id FROM users WHERE id = %s OR owner_user_id = %s)
				ORDER BY updated_at DESC LIMIT 1`,
				d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5)),
			agentID, channel, chatID, userID, userID)
		if err := row.Scan(&key); err == nil {
			return key, nil
		}
	}
	return "", ErrNotFound
}

func (d *DBStore) DeleteSession(ctx context.Context, userID, agentID, sessionKey string) error {
	for _, t := range []string{"session_messages", "session_events"} {
		if _, err := d.db.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE user_id = %s AND agent_id = %s AND session_key = %s`,
				t, d.ph(1), d.ph(2), d.ph(3)),
			userID, agentID, sessionKey); err != nil {
			return err
		}
	}
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM sessions WHERE user_id = %s AND agent_id = %s AND session_key = %s`,
			d.ph(1), d.ph(2), d.ph(3)),
		userID, agentID, sessionKey)
	return err
}

// AppendSessionMessage writes one message to the per-session archive.
// seq is computed atomically inside the INSERT via
// `COALESCE(MAX(seq), -1) + 1`, so two concurrent appenders racing on
// the same session can't collide on the unique key — the second insert
// reads MAX after the first commits. Multi-pod safety relies on the
// engine's write serialization (sqlite global, postgres MVCC + the
// composite PK uniqueness check on commit).
func (d *DBStore) AppendSessionMessage(ctx context.Context, userID, agentID, sessionKey string, msg SessionMessage) error {
	if userID == "" {
		return errors.New("store: AppendSessionMessage requires user_id")
	}
	contentParts, _ := json.Marshal(msg.ContentParts)
	toolCalls, _ := json.Marshal(msg.ToolCalls)
	metadata, _ := json.Marshal(msg.Metadata)
	rawAssistant := string(msg.RawAssistant)
	ts := msg.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	chatterID := ChatterUserIDFromContext(ctx)
	if d.dialect == "postgres" {
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO session_messages
				(user_id, agent_id, session_key, seq, role, content, content_parts, tool_calls, tool_call_id, name, metadata, thinking, raw_assistant, origin, created_at, chatter_user_id, provider, model)
			SELECT $1, $2, $3, COALESCE(MAX(seq), -1) + 1, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17
				FROM session_messages
				WHERE user_id = $1 AND agent_id = $2 AND session_key = $3`,
			userID, agentID, sessionKey,
			msg.Role, msg.Content, string(contentParts), string(toolCalls),
			msg.ToolCallID, msg.Name, string(metadata), msg.Thinking, rawAssistant, msg.Origin, ts, chatterID,
			msg.Provider, msg.Model)
		return err
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO session_messages
			(user_id, agent_id, session_key, seq, role, content, content_parts, tool_calls, tool_call_id, name, metadata, thinking, raw_assistant, origin, created_at, chatter_user_id, provider, model)
		SELECT ?, ?, ?, COALESCE(MAX(seq), -1) + 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			FROM session_messages
			WHERE user_id = ? AND agent_id = ? AND session_key = ?`,
		userID, agentID, sessionKey,
		msg.Role, msg.Content, string(contentParts), string(toolCalls),
		msg.ToolCallID, msg.Name, string(metadata), msg.Thinking, rawAssistant, msg.Origin, ts, chatterID,
		msg.Provider, msg.Model,
		userID, agentID, sessionKey)
	return err
}

// AppendSessionEvent persists one streaming-event delta and returns the
// assigned seq. seq is per-(user, agent, session) — same pattern as
// session_messages — and is allocated atomically inside a transaction
// so concurrent appenders (e.g. fan-out + replay) can't collide on the
// PK. Used by reconnecting clients to skip past events they've
// already rendered.
func (d *DBStore) AppendSessionEvent(ctx context.Context, userID, agentID, sessionKey, eventType string, data []byte) (int64, error) {
	if userID == "" || agentID == "" || sessionKey == "" {
		return 0, errors.New("store: AppendSessionEvent requires user_id, agent_id, session_key")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var seq int64
	chatterID := ChatterUserIDFromContext(ctx)
	if d.dialect == "postgres" {
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq), -1) + 1 FROM session_events
				WHERE user_id = $1 AND agent_id = $2 AND session_key = $3`,
			userID, agentID, sessionKey).Scan(&seq); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO session_events (user_id, agent_id, session_key, seq, type, data, created_at, chatter_user_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			userID, agentID, sessionKey, seq, eventType, string(data), time.Now().UTC(), chatterID); err != nil {
			return 0, err
		}
	} else {
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq), -1) + 1 FROM session_events
				WHERE user_id = ? AND agent_id = ? AND session_key = ?`,
			userID, agentID, sessionKey).Scan(&seq); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO session_events (user_id, agent_id, session_key, seq, type, data, created_at, chatter_user_id)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			userID, agentID, sessionKey, seq, eventType, string(data), time.Now().UTC(), chatterID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return seq, nil
}

// ListSessionEventsSince returns every chat event with seq strictly
// greater than sinceSeq, ascending. Pass sinceSeq=-1 to get all.
func (d *DBStore) ListSessionEventsSince(ctx context.Context, userID, agentID, sessionKey string, sinceSeq int64) ([]SessionEventRecord, error) {
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT seq, type, data, created_at FROM session_events
			WHERE user_id = %s AND agent_id = %s AND session_key = %s AND seq > %s
			ORDER BY seq ASC`,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4)),
		userID, agentID, sessionKey, sinceSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionEventRecord
	for rows.Next() {
		var rec SessionEventRecord
		var dataStr string
		if err := rows.Scan(&rec.Seq, &rec.Type, &dataStr, &rec.CreatedAt); err != nil {
			return nil, err
		}
		rec.UserID = userID
		rec.AgentID = agentID
		rec.SessionKey = sessionKey
		if dataStr != "" {
			rec.Data = []byte(dataStr)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// LatestSessionEventSeq returns the highest seq for the session, or -1 if
// none. Surfaced to clients via the chat history response so they
// know where to subscribe from on a fresh page load.
func (d *DBStore) LatestSessionEventSeq(ctx context.Context, userID, agentID, sessionKey string) (int64, error) {
	var seq sql.NullInt64
	err := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT MAX(seq) FROM session_events
			WHERE user_id = %s AND agent_id = %s AND session_key = %s`,
			d.ph(1), d.ph(2), d.ph(3)),
		userID, agentID, sessionKey).Scan(&seq)
	if err != nil {
		return -1, err
	}
	if !seq.Valid {
		return -1, nil
	}
	return seq.Int64, nil
}

// ListSessionMessages returns every archived turn for one session in
// ascending seq order. Empty slice on a session that has no archive
// yet (e.g. rows pre-dating the table). Callers that want a fallback
// to sessions.messages should check len() and decide.
func (d *DBStore) ListSessionMessages(ctx context.Context, userID, agentID, sessionKey string) ([]SessionMessage, error) {
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT role, content, content_parts, tool_calls, tool_call_id, name, metadata, thinking, raw_assistant, origin, created_at, provider, model
			FROM session_messages
			WHERE user_id = %s AND agent_id = %s AND session_key = %s
			ORDER BY seq ASC`, d.ph(1), d.ph(2), d.ph(3)),
		userID, agentID, sessionKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionMessage
	for rows.Next() {
		var m SessionMessage
		var contentParts, toolCalls, metadata, rawAssistant string
		if err := rows.Scan(&m.Role, &m.Content, &contentParts, &toolCalls, &m.ToolCallID, &m.Name, &metadata, &m.Thinking, &rawAssistant, &m.Origin, &m.Timestamp, &m.Provider, &m.Model); err != nil {
			return nil, err
		}
		if contentParts != "" && contentParts != "null" {
			var v interface{}
			if json.Unmarshal([]byte(contentParts), &v) == nil {
				m.ContentParts = v
			}
		}
		if toolCalls != "" && toolCalls != "null" {
			var v interface{}
			if json.Unmarshal([]byte(toolCalls), &v) == nil {
				m.ToolCalls = v
			}
		}
		if metadata != "" && metadata != "null" {
			_ = json.Unmarshal([]byte(metadata), &m.Metadata)
		}
		if rawAssistant != "" && rawAssistant != "null" {
			m.RawAssistant = json.RawMessage(rawAssistant)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CountChatterUserMessages returns the count of user-role messages
// this chatter has accumulated under the agent across all sessions.
// Used by the autoPersist cadence gate as a durable counter — see the
// interface doc on Store for why we don't reuse the in-memory turnCount.
//
// Filter is strictly on chatter_user_id (no fallback to user_id). Old
// rows written before the chatter_user_id column existed have it set
// to ” and are not counted; those predate per-chatter resolution and
// folding them in would over-count (they're keyed by channel owner,
// not the actual chatter). New conversations write chatter_user_id
// correctly so this is only a concern for sessions migrated from
// pre-fix daemon runs.
func (d *DBStore) CountChatterUserMessages(ctx context.Context, agentID, chatterUserID string) (int, error) {
	if chatterUserID == "" {
		return 0, nil
	}
	var n int
	err := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM session_messages
			WHERE agent_id = %s AND chatter_user_id = %s AND role = 'user'`,
			d.ph(1), d.ph(2)),
		agentID, chatterUserID).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (d *DBStore) RenameSession(ctx context.Context, userID, agentID, sessionKey, title string) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE sessions SET title = %s WHERE user_id = %s AND agent_id = %s AND session_key = %s`,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4)),
		title, userID, agentID, sessionKey)
	return err
}

// MoveSession flips a session's project_id. Empty string detaches the
// session from its current project (drag-out to "Chats"). The caller
// must have already migrated the workspace files and validated that
// projectID, when non-empty, is a real project the user owns under
// this agent — this method only touches the sessions row.
func (d *DBStore) MoveSession(ctx context.Context, userID, agentID, sessionKey, projectID string) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE sessions SET project_id = %s WHERE user_id = %s AND agent_id = %s AND session_key = %s`,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4)),
		projectID, userID, agentID, sessionKey)
	return err
}

// --- Agent files ---
//
// SOUL.md / IDENTITY.md / MEMORY.md / AGENTS.md / BOOTSTRAP.md / etc.
// Keyed on (agent_id, user_id, filename). Every row carries a real
// user_id — there is no shared template row.
//
// Read path: prefer the caller's own row; fall back to the agent
// owner's row when the caller has no override. This lets non-owner
// callers (other humans the agent is shared with, or app_user accounts
// minted on behalf of a downstream app's end-users) inherit the
// owner's customized SOUL.md / IDENTITY.md while still being able to
// fork their own MEMORY.md / USER.md by saving — saves always go to
// the caller's exact row, never the owner's. The runtime additionally
// falls through to a local FS file at <agent_home>/<name> for installs
// that want a global default for an agent.

// GetAgentFile returns the file for (agent_id, filename), preferring
// the caller's own row and falling back to the agent owner's row.
// userID is required.
func (d *DBStore) GetAgentFile(ctx context.Context, agentID, userID, filename string) ([]byte, error) {
	if agentID == "" {
		return nil, errors.New("store: GetAgentFile requires agent_id")
	}
	if userID == "" {
		return nil, errors.New("store: GetAgentFile requires user_id")
	}
	// Single round-trip: pick caller's row if present (sort key 0),
	// else owner's (sort key 1). LIMIT 1 returns the winning row.
	// The subselect resolves the agent's owner; if the agent is gone
	// it just produces NULL and the IN ignores it — caller's row is
	// still returned when present, otherwise NoRows.
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT content FROM agent_files
			WHERE agent_id = %s AND filename = %s
			  AND user_id IN (%s, COALESCE((SELECT user_id FROM agents WHERE id = %s), ''))
			ORDER BY CASE WHEN user_id = %s THEN 0 ELSE 1 END
			LIMIT 1`,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5)),
		agentID, filename, userID, agentID, userID)
	var content string
	if err := row.Scan(&content); err != nil {
		return nil, scanErr(err)
	}
	return []byte(content), nil
}

// GetAgentFileExact bypasses the owner-fallback overlay and returns
// only the (agent_id, user_id, filename) row, or ErrNotFound. Used
// when a caller explicitly needs to know whether *their own* override
// row exists (e.g. a Customize page that distinguishes "you've
// authored an override" from "you're seeing the owner's content").
func (d *DBStore) GetAgentFileExact(ctx context.Context, agentID, userID, filename string) ([]byte, error) {
	if agentID == "" {
		return nil, errors.New("store: GetAgentFileExact requires agent_id")
	}
	if userID == "" {
		return nil, errors.New("store: GetAgentFileExact requires user_id")
	}
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT content FROM agent_files
			WHERE agent_id = %s AND user_id = %s AND filename = %s`,
			d.ph(1), d.ph(2), d.ph(3)),
		agentID, userID, filename)
	var content string
	if err := row.Scan(&content); err != nil {
		return nil, scanErr(err)
	}
	return []byte(content), nil
}

// SaveAgentFile writes to the (agent_id, user_id, filename) row exactly.
// userID is required — every write is per-user. Use a local FS file
// at <agent_home>/<name> if you want one shared default for the agent.
func (d *DBStore) SaveAgentFile(ctx context.Context, agentID, userID, filename string, data []byte) error {
	if agentID == "" {
		return errors.New("store: SaveAgentFile requires agent_id")
	}
	if userID == "" {
		return errors.New("store: SaveAgentFile requires user_id")
	}
	now := time.Now().UTC()
	if d.dialect == "postgres" {
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO agent_files (agent_id, user_id, filename, content, updated_at)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (agent_id, user_id, filename) DO UPDATE SET content=$4, updated_at=$5`,
			agentID, userID, filename, string(data), now)
		return err
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO agent_files (agent_id, user_id, filename, content, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (agent_id, user_id, filename) DO UPDATE SET
			  content=excluded.content, updated_at=excluded.updated_at`,
		agentID, userID, filename, string(data), now)
	return err
}

func (d *DBStore) DeleteAgentFile(ctx context.Context, agentID, userID, filename string) error {
	if agentID == "" {
		return errors.New("store: DeleteAgentFile requires agent_id")
	}
	if userID == "" {
		return errors.New("store: DeleteAgentFile requires user_id")
	}
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM agent_files WHERE agent_id = %s AND user_id = %s AND filename = %s`,
			d.ph(1), d.ph(2), d.ph(3)),
		agentID, userID, filename)
	return err
}

// ListAgentFiles returns the filenames stored for (agent_id, user_id).
// userID is required — there is no shared template fallback.
func (d *DBStore) ListAgentFiles(ctx context.Context, agentID, userID string) ([]string, error) {
	if agentID == "" {
		return nil, errors.New("store: ListAgentFiles requires agent_id")
	}
	if userID == "" {
		return nil, errors.New("store: ListAgentFiles requires user_id")
	}
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT filename FROM agent_files
			WHERE agent_id = %s AND user_id = %s ORDER BY filename`,
			d.ph(1), d.ph(2)),
		agentID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// --- Scoped configs (providers + channels + settings) ---

// ListConfigs returns all rows of the given (kind, scope) tuple. When
// scopeID is empty, it matches any scope_id within the scope — used by
// boot-time enumeration paths (registerChannelsFromStore) that want
// "every agent's channels" across all users without enumerating users
// first. Existing callers that pass a real scopeID continue to get
// exact-match semantics. System rows have scope_id="" anyway so
// system-scope queries are unaffected by this widening.
const configSelectCols = `id, kind, scope, scope_id, name, enabled, data, created_at, updated_at`

func (d *DBStore) ListConfigs(ctx context.Context, kind, userID, agentID string) ([]ConfigRecord, error) {
	scopeID := computeScopeID(userID, agentID)
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT `+configSelectCols+`
			FROM configs WHERE kind = %s AND scope_id = %s ORDER BY name`,
			d.ph(1), d.ph(2)),
		kind, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanConfigs(rows)
}

func (d *DBStore) ListConfigsByUser(ctx context.Context, kind, userID string) ([]ConfigRecord, error) {
	// scope_id contains the userID for user-scoped rows. For user-agent
	// rows scope_id is "userID/agentID", so we use a prefix match.
	if userID == "" {
		// System rows: scope_id = ''.
		rows, err := d.db.QueryContext(ctx,
			fmt.Sprintf(`SELECT `+configSelectCols+`
				FROM configs WHERE kind = %s AND scope_id = '' ORDER BY name`,
				d.ph(1)),
			kind)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanConfigs(rows)
	}
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT `+configSelectCols+`
			FROM configs WHERE kind = %s AND (scope_id = %s OR scope_id LIKE %s) ORDER BY name`,
			d.ph(1), d.ph(2), d.ph(3)),
		kind, userID, userID+"/%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanConfigs(rows)
}

func (d *DBStore) QueryAllConfigs(ctx context.Context, kind string) ([]ConfigRecord, error) {
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT `+configSelectCols+`
			FROM configs WHERE kind = %s ORDER BY scope_id, name`,
			d.ph(1)),
		kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanConfigs(rows)
}

func (d *DBStore) GetConfig(ctx context.Context, id string) (*ConfigRecord, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT `+configSelectCols+` FROM configs WHERE id = %s`, d.ph(1)), id)
	return scanConfigRow(row)
}

func (d *DBStore) GetConfigByName(ctx context.Context, kind, userID, agentID, name string) (*ConfigRecord, error) {
	scopeID := computeScopeID(userID, agentID)
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT `+configSelectCols+`
			FROM configs WHERE kind = %s AND scope_id = %s AND name = %s`,
			d.ph(1), d.ph(2), d.ph(3)),
		kind, scopeID, name)
	return scanConfigRow(row)
}

func (d *DBStore) SaveConfig(ctx context.Context, c *ConfigRecord) error {
	if c.Kind == "" || c.Name == "" {
		return errors.New("store: SaveConfig requires kind and name")
	}
	// Compute scope and scope_id from (UserID, AgentID) convenience
	// fields when they're set. These fields are kept on the Go struct
	// for backward compat with callers but are NOT persisted as columns.
	c.Scope = computeConfigScope(c.UserID, c.AgentID)
	if c.ScopeID == "" {
		c.ScopeID = computeScopeID(c.UserID, c.AgentID)
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	if c.ID == "" {
		c.ID = randomConfigID()
	}
	dataBytes, _ := json.Marshal(c.Data)
	if d.dialect == "postgres" {
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO configs (id, kind, scope, scope_id, name, enabled, data, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
				ON CONFLICT (kind, scope_id, name) DO UPDATE SET
				  scope=$3, enabled=$6, data=$7, updated_at=$9`,
			c.ID, c.Kind, c.Scope, c.ScopeID, c.Name, c.Enabled, string(dataBytes), c.CreatedAt, c.UpdatedAt)
		return err
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO configs (id, kind, scope, scope_id, name, enabled, data, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (kind, scope_id, name) DO UPDATE SET
			  scope=excluded.scope, enabled=excluded.enabled,
			  data=excluded.data, updated_at=excluded.updated_at`,
		c.ID, c.Kind, c.Scope, c.ScopeID, c.Name, c.Enabled, string(dataBytes), c.CreatedAt, c.UpdatedAt)
	return err
}

// randomConfigID generates an opaque id for a new configs row. Format
// matches the historical hex-derived shape so anything keying off the
// `sc_` prefix in logs / dashboards keeps recognizing it.
func randomConfigID() string {
	var b [10]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		// fall back to time-derived bytes — collision is fine here, the
		// natural-key upsert is what enforces uniqueness.
		now := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(now >> (i * 8))
		}
	}
	return "sc_" + hex.EncodeToString(b[:])
}

func (d *DBStore) DeleteConfig(ctx context.Context, id string) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM configs WHERE id = %s`, d.ph(1)), id)
	return err
}

func (d *DBStore) LookupChannelByCredential(ctx context.Context, channelType, credKey string) (*ConfigRecord, error) {
	// credential_key column has been removed; channels now live in
	// the channels table. Return ErrNotFound so callers fall through
	// to their channels-table code path.
	return nil, ErrNotFound
}

// configRowID produces a stable id for a (kind, scope, scope_id,
// name) tuple. Used by legacy migrations (migrateAgentsDropModel,
// migrateSkillsAgentEntriesSplit) that wrote rows under the OLD column
// layout — those callers compute IDs from the legacy 4-tuple and we
// preserve the function so the historical ids stay reproducible. New
// callers go through SaveConfig + the natural-key upsert instead.
func configRowID(kind, scope, scopeID, name string) string {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write([]byte(scope))
	h.Write([]byte{0})
	h.Write([]byte(scopeID))
	h.Write([]byte{0})
	h.Write([]byte(name))
	return "sc_" + hex.EncodeToString(h.Sum(nil)[:10])
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanConfigRow(row rowScanner) (*ConfigRecord, error) {
	var c ConfigRecord
	var dataStr string
	if err := row.Scan(&c.ID, &c.Kind, &c.Scope, &c.ScopeID, &c.Name, &c.Enabled, &dataStr, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, scanErr(err)
	}
	json.Unmarshal([]byte(dataStr), &c.Data)
	// Populate convenience fields from scope for backward compat.
	switch c.Scope {
	case "user":
		c.UserID = c.ScopeID
	case "agent":
		c.AgentID = c.ScopeID
	case "user-agent":
		// user-agent rows encode "userID/agentID" in scope_id.
		if idx := strings.Index(c.ScopeID, "/"); idx >= 0 {
			c.UserID = c.ScopeID[:idx]
			c.AgentID = c.ScopeID[idx+1:]
		}
	}
	return &c, nil
}

func scanConfigs(rows *sql.Rows) ([]ConfigRecord, error) {
	var out []ConfigRecord
	for rows.Next() {
		var c ConfigRecord
		var dataStr string
		if err := rows.Scan(&c.ID, &c.Kind, &c.Scope, &c.ScopeID, &c.Name, &c.Enabled, &dataStr, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(dataStr), &c.Data)
		switch c.Scope {
		case "user":
			c.UserID = c.ScopeID
		case "agent":
			c.AgentID = c.ScopeID
		case "user-agent":
			if idx := strings.Index(c.ScopeID, "/"); idx >= 0 {
				c.UserID = c.ScopeID[:idx]
				c.AgentID = c.ScopeID[idx+1:]
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// --- Channels (IM bot bindings) ---

const channelSelectCols = `id, user_id, agent_id, type, account_id, enabled, bot_token, base_url, platform_user_id, shared_identity, data, created_at, updated_at`

func (d *DBStore) ListChannels(ctx context.Context, userID, agentID string) ([]ChannelRecord, error) {
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT `+channelSelectCols+`
			FROM channels WHERE user_id = %s AND agent_id = %s ORDER BY type, account_id`,
			d.ph(1), d.ph(2)),
		userID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChannels(rows)
}

func (d *DBStore) ListAllChannels(ctx context.Context) ([]ChannelRecord, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+channelSelectCols+` FROM channels ORDER BY user_id, agent_id, type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChannels(rows)
}

func (d *DBStore) GetChannel(ctx context.Context, id string) (*ChannelRecord, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT `+channelSelectCols+` FROM channels WHERE id = %s`, d.ph(1)), id)
	return scanChannelRow(row)
}

func (d *DBStore) SaveChannel(ctx context.Context, ch *ChannelRecord) error {
	if ch.Type == "" || ch.AccountID == "" {
		return errors.New("store: SaveChannel requires type and accountId")
	}
	now := time.Now().UTC()
	if ch.CreatedAt.IsZero() {
		ch.CreatedAt = now
	}
	ch.UpdatedAt = now
	if ch.ID == "" {
		ch.ID = randomChannelID()
	}
	dataBytes, _ := json.Marshal(ch.Data)
	// Convert bools to int for PostgreSQL INTEGER columns.
	enabledInt := 0
	if ch.Enabled {
		enabledInt = 1
	}
	sharedIdent := 0
	if ch.SharedIdentity {
		sharedIdent = 1
	}
	if d.dialect == "postgres" {
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO channels (id, user_id, agent_id, type, account_id, enabled, bot_token, base_url, platform_user_id, shared_identity, data, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
				ON CONFLICT (type, account_id) DO UPDATE SET
				  user_id=$2, agent_id=$3, enabled=$6, bot_token=$7, base_url=$8,
				  platform_user_id=$9, shared_identity=$10, data=$11, updated_at=$13`,
			ch.ID, ch.UserID, ch.AgentID, ch.Type, ch.AccountID, enabledInt, ch.BotToken, ch.BaseURL, ch.PlatformUserID, sharedIdent, string(dataBytes), ch.CreatedAt, ch.UpdatedAt)
		return err
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO channels (id, user_id, agent_id, type, account_id, enabled, bot_token, base_url, platform_user_id, shared_identity, data, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (type, account_id) DO UPDATE SET
			  user_id=excluded.user_id, agent_id=excluded.agent_id, enabled=excluded.enabled,
			  bot_token=excluded.bot_token, base_url=excluded.base_url,
			  platform_user_id=excluded.platform_user_id, shared_identity=excluded.shared_identity,
			  data=excluded.data, updated_at=excluded.updated_at`,
		ch.ID, ch.UserID, ch.AgentID, ch.Type, ch.AccountID, enabledInt, ch.BotToken, ch.BaseURL, ch.PlatformUserID, sharedIdent, string(dataBytes), ch.CreatedAt, ch.UpdatedAt)
	return err
}

func (d *DBStore) DeleteChannel(ctx context.Context, id string) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM channels WHERE id = %s`, d.ph(1)), id)
	return err
}

func (d *DBStore) LookupChannel(ctx context.Context, channelType, accountID string) (*ChannelRecord, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT `+channelSelectCols+`
			FROM channels WHERE type = %s AND account_id = %s LIMIT 1`,
			d.ph(1), d.ph(2)),
		channelType, accountID)
	return scanChannelRow(row)
}

func randomChannelID() string {
	var b [10]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		now := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(now >> (i * 8))
		}
	}
	return "ch_" + hex.EncodeToString(b[:])
}

func scanChannelRow(row rowScanner) (*ChannelRecord, error) {
	var c ChannelRecord
	var dataStr string
	var enabledInt, sharedIdent int
	if err := row.Scan(&c.ID, &c.UserID, &c.AgentID, &c.Type, &c.AccountID, &enabledInt, &c.BotToken, &c.BaseURL, &c.PlatformUserID, &sharedIdent, &dataStr, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, scanErr(err)
	}
	c.Enabled = enabledInt != 0
	c.SharedIdentity = sharedIdent != 0
	json.Unmarshal([]byte(dataStr), &c.Data)
	return &c, nil
}

func scanChannels(rows *sql.Rows) ([]ChannelRecord, error) {
	var out []ChannelRecord
	for rows.Next() {
		var c ChannelRecord
		var dataStr string
		var enabledInt, sharedIdent int
		if err := rows.Scan(&c.ID, &c.UserID, &c.AgentID, &c.Type, &c.AccountID, &enabledInt, &c.BotToken, &c.BaseURL, &c.PlatformUserID, &sharedIdent, &dataStr, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Enabled = enabledInt != 0
		c.SharedIdentity = sharedIdent != 0
		json.Unmarshal([]byte(dataStr), &c.Data)
		out = append(out, c)
	}
	return out, rows.Err()
}

// migrateChannelsFromConfigs copies kind='channel' configs rows into the
// new channels table. Skipped when the channels table already has data
// (i.e. migration already ran). Old configs rows are kept for rollback.
func (d *DBStore) migrateChannelsFromConfigs(ctx context.Context) error {
	exists, err := d.tableExists(ctx, "channels")
	if err != nil || !exists {
		return err
	}
	// Skip the copy if channels table already has data.
	var count int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM channels`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	// Read all channel configs.
	configRows, err := d.db.QueryContext(ctx,
		`SELECT `+configSelectCols+` FROM configs WHERE kind = 'channel'`)
	if err != nil {
		return err
	}
	defer configRows.Close()
	configs, err := scanConfigs(configRows)
	if err != nil {
		return err
	}
	for _, cfg := range configs {
		// Each config row may have multiple accounts in its data JSON.
		// Extract them and create one channel row per account.
		var cc struct {
			BotToken string                     `json:"botToken"`
			BaseURL  string                     `json:"baseUrl"`
			Accounts map[string]json.RawMessage `json:"accounts"`
		}
		if blob, merr := json.Marshal(cfg.Data); merr == nil {
			_ = json.Unmarshal(blob, &cc)
		}
		if len(cc.Accounts) == 0 {
			// Single-bot legacy shape: one channel row with credential_key as accountID.
			ch := &ChannelRecord{
				ID:        randomChannelID(),
				UserID:    cfg.UserID,
				AgentID:   cfg.AgentID,
				Type:      cfg.Name,
				AccountID: cfg.CredentialKey,
				Enabled:   cfg.Enabled,
				BotToken:  cc.BotToken,
				BaseURL:   cc.BaseURL,
				Data:      cfg.Data,
				CreatedAt: cfg.CreatedAt,
				UpdatedAt: cfg.UpdatedAt,
			}
			if ch.AccountID == "" {
				ch.AccountID = cfg.Name // fallback
			}
			if err := d.SaveChannel(ctx, ch); err != nil {
				slog.Warn("migrate channel from config failed",
					"config_id", cfg.ID, "type", cfg.Name, "error", err)
			}
			continue
		}
		// Multi-account: one channel row per account entry.
		for accountID, rawAcct := range cc.Accounts {
			var acct struct {
				BotToken string `json:"botToken"`
				BaseURL  string `json:"baseUrl"`
				UserID   string `json:"userId"`
			}
			_ = json.Unmarshal(rawAcct, &acct)
			botToken := acct.BotToken
			if botToken == "" {
				botToken = cc.BotToken
			}
			baseURL := acct.BaseURL
			if baseURL == "" {
				baseURL = cc.BaseURL
			}
			ch := &ChannelRecord{
				ID:             randomChannelID(),
				UserID:         cfg.UserID,
				AgentID:        cfg.AgentID,
				Type:           cfg.Name,
				AccountID:      accountID,
				Enabled:        cfg.Enabled,
				BotToken:       botToken,
				BaseURL:        baseURL,
				PlatformUserID: acct.UserID,
				Data:           cfg.Data,
				CreatedAt:      cfg.CreatedAt,
				UpdatedAt:      cfg.UpdatedAt,
			}
			if err := d.SaveChannel(ctx, ch); err != nil {
				slog.Warn("migrate channel from config failed",
					"config_id", cfg.ID, "type", cfg.Name, "account", accountID, "error", err)
			}
		}
	}
	if len(configs) > 0 {
		slog.Info("migrated channel configs to channels table", "count", len(configs))
		// Clean up: delete channel rows from configs after successful copy.
		if _, err := d.db.ExecContext(ctx, `DELETE FROM configs WHERE kind = 'channel'`); err != nil {
			slog.Warn("failed to clean channel rows from configs", "error", err)
		}
	}
	return nil
}

// migrateConfigsDropLegacyColumns removes the user_id, agent_id, and
// credential_key columns from configs. These are redundant: scope_id
// replaces user_id+agent_id, and credential_key was only used by
// channels which now have their own table.
//
// Idempotent: skips if user_id column no longer exists.
func (d *DBStore) migrateConfigsDropLegacyColumns(ctx context.Context) error {
	has, err := d.tableHasColumn(ctx, "configs", "user_id")
	if err != nil {
		return err
	}
	if !has {
		// Already migrated — nothing to do.
		return nil
	}

	if d.dialect == "postgres" {
		// Recompute scope_id for user-agent rows before dropping the columns.
		if _, err := d.db.ExecContext(ctx,
			`UPDATE configs SET scope_id = user_id || '/' || agent_id
			 WHERE user_id != '' AND agent_id != ''`); err != nil {
			return fmt.Errorf("postgres recompute scope_id: %w", err)
		}
		// Postgres supports ALTER TABLE DROP COLUMN directly.
		stmts := []string{
			`ALTER TABLE configs DROP CONSTRAINT IF EXISTS configs_kind_user_id_agent_id_name_key`,
			`DROP INDEX IF EXISTS idx_configs_lookup`,
			`DROP INDEX IF EXISTS idx_configs_credential`,
			`ALTER TABLE configs DROP COLUMN IF EXISTS user_id`,
			`ALTER TABLE configs DROP COLUMN IF EXISTS agent_id`,
			`ALTER TABLE configs DROP COLUMN IF EXISTS credential_key`,
			`CREATE UNIQUE INDEX IF NOT EXISTS configs_kind_scope_id_name_key ON configs (kind, scope_id, name)`,
			`CREATE INDEX IF NOT EXISTS idx_configs_scope ON configs (kind, scope_id)`,
		}
		for _, s := range stmts {
			if _, err := d.db.ExecContext(ctx, s); err != nil {
				return fmt.Errorf("postgres migrate configs drop legacy: %w\nSQL: %s", err, s)
			}
		}
		return nil
	}

	// SQLite: rebuild the table via copy-rename.
	stmts := []string{
		`CREATE TABLE configs_new (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			scope TEXT NOT NULL DEFAULT '',
			scope_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			data TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (kind, scope_id, name)
		)`,
		// Recompute scope_id: user-agent rows (both user_id and agent_id
		// non-empty) get "user_id/agent_id"; other rows keep scope_id as-is.
		`INSERT INTO configs_new (id, kind, scope, scope_id, name, enabled, data, created_at, updated_at)
		   SELECT id, kind, scope,
		     CASE WHEN user_id != '' AND agent_id != '' THEN user_id || '/' || agent_id
		          ELSE scope_id END,
		     name, enabled, data, created_at, updated_at
		   FROM configs`,
		`DROP TABLE configs`,
		`ALTER TABLE configs_new RENAME TO configs`,
		`DROP INDEX IF EXISTS idx_configs_lookup`,
		`DROP INDEX IF EXISTS idx_configs_credential`,
		`CREATE INDEX IF NOT EXISTS idx_configs_scope ON configs (kind, scope_id)`,
	}
	for _, s := range stmts {
		if _, err := d.db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("sqlite migrate configs drop legacy: %w\nSQL: %s", err, s)
		}
	}
	return nil
}

// migrateChannelsAddSharedIdentity retrofits a shared_identity column
// onto the channels table. Default 0 (off) — each platform sender gets
// an isolated chatter. When set to 1, inbound messages use the channel
// owner's user_id as chatter so sessions and memory are shared across
// the owner's personal channels.
func (d *DBStore) migrateChannelsAddSharedIdentity(ctx context.Context) error {
	has, err := d.tableHasColumn(ctx, "channels", "shared_identity")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	_, err = d.db.ExecContext(ctx,
		`ALTER TABLE channels ADD COLUMN shared_identity INTEGER NOT NULL DEFAULT 0`)
	return err
}

// --- Cron jobs ---

const cronSelectCols = `id, user_id, agent_id, name, type, schedule, message, channel, chat_id, account_id, timezone, enabled, last_run, next_run, failure_count, created_at`

func (d *DBStore) ListCronJobsByOwner(ctx context.Context, ownerUserID string) ([]CronJobRecord, error) {
	// user_id is denormalized onto cron_jobs; the JOIN against agents
	// is gone now. Cheaper, and lets us list crons for a user even if
	// the agent row got deleted out from under us (orphan rows can be
	// cleaned up by a separate sweep).
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT `+cronSelectCols+` FROM cron_jobs WHERE user_id = %s ORDER BY created_at`, d.ph(1)),
		ownerUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCronJobs(rows)
}

func (d *DBStore) ListCronJobsByAgent(ctx context.Context, agentID string) ([]CronJobRecord, error) {
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT `+cronSelectCols+` FROM cron_jobs WHERE agent_id = %s ORDER BY created_at`, d.ph(1)),
		agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCronJobs(rows)
}

func (d *DBStore) GetCronJob(ctx context.Context, jobID string) (*CronJobRecord, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT `+cronSelectCols+` FROM cron_jobs WHERE id = %s`, d.ph(1)), jobID)
	var j CronJobRecord
	var lastRun, nextRun sql.NullTime
	if err := row.Scan(&j.ID, &j.UserID, &j.AgentID, &j.Name, &j.Type, &j.Schedule, &j.Message, &j.Channel, &j.ChatID, &j.AccountID, &j.Timezone, &j.Enabled, &lastRun, &nextRun, &j.FailureCount, &j.CreatedAt); err != nil {
		return nil, scanErr(err)
	}
	if lastRun.Valid {
		j.LastRun = &lastRun.Time
	}
	if nextRun.Valid {
		j.NextRun = &nextRun.Time
	}
	return &j, nil
}

func (d *DBStore) SaveCronJob(ctx context.Context, job *CronJobRecord) error {
	if job.AgentID == "" {
		return errors.New("store: cron job.agent_id is required")
	}
	// user_id was added to keep cron_jobs consistent with the rest of
	// the codebase's (user_id, agent_id) keying. SaveCronJob auto-fills
	// it from agents.user_id when the caller didn't set it, so existing
	// callers don't have to be touched all at once.
	if job.UserID == "" {
		var uid sql.NullString
		row := d.db.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT user_id FROM agents WHERE id = %s`, d.ph(1)), job.AgentID)
		if err := row.Scan(&uid); err == nil && uid.Valid {
			job.UserID = uid.String
		}
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	if d.dialect == "postgres" {
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO cron_jobs (id, user_id, agent_id, name, type, schedule, message, channel, chat_id, account_id, timezone, enabled, last_run, next_run, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
				ON CONFLICT (id) DO UPDATE SET
				  user_id=$2, agent_id=$3, name=$4, type=$5, schedule=$6, message=$7, channel=$8,
				  chat_id=$9, account_id=$10, timezone=$11, enabled=$12, last_run=$13, next_run=$14`,
			job.ID, job.UserID, job.AgentID, job.Name, job.Type, job.Schedule, job.Message, job.Channel, job.ChatID, job.AccountID, job.Timezone, job.Enabled, job.LastRun, job.NextRun, job.CreatedAt)
		return err
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO cron_jobs (id, user_id, agent_id, name, type, schedule, message, channel, chat_id, account_id, timezone, enabled, last_run, next_run, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET
			  user_id=excluded.user_id, agent_id=excluded.agent_id, name=excluded.name, type=excluded.type,
			  schedule=excluded.schedule, message=excluded.message, channel=excluded.channel,
			  chat_id=excluded.chat_id, account_id=excluded.account_id, timezone=excluded.timezone,
			  enabled=excluded.enabled, last_run=excluded.last_run, next_run=excluded.next_run`,
		job.ID, job.UserID, job.AgentID, job.Name, job.Type, job.Schedule, job.Message, job.Channel, job.ChatID, job.AccountID, job.Timezone, job.Enabled, job.LastRun, job.NextRun, job.CreatedAt)
	return err
}

func (d *DBStore) DeleteCronJob(ctx context.Context, jobID string) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM cron_jobs WHERE id = %s`, d.ph(1)), jobID)
	return err
}

func (d *DBStore) GetDueCronJobs(ctx context.Context, now time.Time) ([]CronJobRecord, error) {
	var rows *sql.Rows
	var err error
	if d.dialect == "postgres" {
		rows, err = d.db.QueryContext(ctx,
			`SELECT `+cronSelectCols+` FROM cron_jobs WHERE enabled = true AND next_run <= $1 ORDER BY next_run`, now)
	} else {
		rows, err = d.db.QueryContext(ctx,
			`SELECT `+cronSelectCols+` FROM cron_jobs WHERE enabled = 1 AND next_run <= ? ORDER BY next_run`, now)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCronJobs(rows)
}

func (d *DBStore) LockCronJob(ctx context.Context, jobID, instanceID string) (bool, error) {
	now := time.Now()
	fiveMinAgo := now.Add(-5 * time.Minute)
	var res sql.Result
	var err error
	if d.dialect == "postgres" {
		res, err = d.db.ExecContext(ctx,
			`UPDATE cron_jobs SET locked_by=$1, locked_at=$2 WHERE id=$3 AND (locked_by IS NULL OR locked_at < $4)`,
			instanceID, now, jobID, fiveMinAgo)
	} else {
		res, err = d.db.ExecContext(ctx,
			`UPDATE cron_jobs SET locked_by=?, locked_at=? WHERE id=? AND (locked_by IS NULL OR locked_at < ?)`,
			instanceID, now, jobID, fiveMinAgo)
	}
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (d *DBStore) UpdateCronJobRun(ctx context.Context, jobID string, lastRun, nextRun time.Time) error {
	// A successful tick clears failure_count too — the row only
	// auto-deletes on a *consecutive* run of misses.
	if d.dialect == "postgres" {
		_, err := d.db.ExecContext(ctx,
			`UPDATE cron_jobs SET last_run=$1, next_run=$2, failure_count=0, locked_by=NULL, locked_at=NULL WHERE id=$3`,
			lastRun, nextRun, jobID)
		return err
	}
	_, err := d.db.ExecContext(ctx,
		`UPDATE cron_jobs SET last_run=?, next_run=?, failure_count=0, locked_by=NULL, locked_at=NULL WHERE id=?`,
		lastRun, nextRun, jobID)
	return err
}

// IncrementCronJobFailure atomically bumps failure_count and returns
// the new total. Also clears the lock so the next tick is free to
// retry (or, if the caller decides to delete the row at threshold,
// the row goes away cleanly without a stuck lock).
func (d *DBStore) IncrementCronJobFailure(ctx context.Context, jobID string) (int, error) {
	if d.dialect == "postgres" {
		var n int
		err := d.db.QueryRowContext(ctx,
			`UPDATE cron_jobs SET failure_count = failure_count + 1, locked_by=NULL, locked_at=NULL
				WHERE id = $1 RETURNING failure_count`, jobID).Scan(&n)
		if err != nil {
			return 0, scanErr(err)
		}
		return n, nil
	}
	if _, err := d.db.ExecContext(ctx,
		`UPDATE cron_jobs SET failure_count = failure_count + 1, locked_by=NULL, locked_at=NULL WHERE id=?`,
		jobID); err != nil {
		return 0, err
	}
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT failure_count FROM cron_jobs WHERE id = ?`, jobID).Scan(&n); err != nil {
		return 0, scanErr(err)
	}
	return n, nil
}

func (d *DBStore) GetNextDueTime(ctx context.Context) (time.Time, error) {
	var q string
	if d.dialect == "postgres" {
		// Postgres returns a proper timestamp — sql.NullTime works.
		q = `SELECT MIN(next_run) FROM cron_jobs WHERE enabled = true AND next_run IS NOT NULL`
		var t sql.NullTime
		if err := d.db.QueryRowContext(ctx, q).Scan(&t); err != nil {
			return time.Time{}, err
		}
		if !t.Valid {
			return time.Time{}, nil
		}
		return t.Time, nil
	}
	// SQLite returns MIN() as a string — scan into NullString, then parse.
	q = `SELECT MIN(next_run) FROM cron_jobs WHERE enabled = 1 AND next_run IS NOT NULL`
	var s sql.NullString
	if err := d.db.QueryRowContext(ctx, q).Scan(&s); err != nil {
		return time.Time{}, err
	}
	if !s.Valid || s.String == "" {
		return time.Time{}, nil
	}
	return parseTimeString(s.String), nil
}

// --- Channel leases ---
//
// Cross-process singleton gate for polling / persistent-connection
// channel adapters. The pattern is one row per (channel, account_id);
// the holder writes its instanceID into holder_id and renews
// expires_at on a periodic tick. A peer wanting to take over has to
// wait until expires_at has passed — at that point the same upsert
// query atomically rotates the row to the new holder.

// AcquireChannelLease attempts to take the (channel, accountID) lease
// for `ttl`. Returns true only when the row was either absent, already
// held by holderID (renew), or expired (steal). A concurrent acquirer
// that loses the race gets (false, nil) — not an error.
func (d *DBStore) AcquireChannelLease(ctx context.Context, channel, accountID, holderID string, ttl time.Duration) (bool, error) {
	now := time.Now()
	expires := now.Add(ttl)
	if d.dialect == "postgres" {
		// ON CONFLICT updates the row only when the previous holder's
		// lease has expired OR we already hold it (renew). The WHERE
		// clause is essential — without it, a second instance would
		// steal the lease the moment its INSERT collided.
		res, err := d.db.ExecContext(ctx,
			`INSERT INTO channel_leases (channel, account_id, holder_id, expires_at)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (channel, account_id) DO UPDATE
				SET holder_id = EXCLUDED.holder_id, expires_at = EXCLUDED.expires_at
				WHERE channel_leases.expires_at < $5 OR channel_leases.holder_id = $3`,
			channel, accountID, holderID, expires, now)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		return n > 0, nil
	}
	// SQLite path: ON CONFLICT DO UPDATE ... WHERE is supported in
	// modernc.org/sqlite (SQLite 3.24+). Same semantics as the PG
	// branch above; placeholder syntax differs.
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO channel_leases (channel, account_id, holder_id, expires_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT (channel, account_id) DO UPDATE
			SET holder_id = excluded.holder_id, expires_at = excluded.expires_at
			WHERE channel_leases.expires_at < ? OR channel_leases.holder_id = ?`,
		channel, accountID, holderID, expires, now, holderID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// RenewChannelLease extends an already-held lease. Returns false (not
// an error) when the row's holder_id no longer matches — meaning the
// previous holder's TTL elapsed and a peer took over while we were
// off-cpu. Callers MUST treat false as "stop polling immediately": the
// peer is now driving inbound for this (channel, account_id) pair.
func (d *DBStore) RenewChannelLease(ctx context.Context, channel, accountID, holderID string, ttl time.Duration) (bool, error) {
	expires := time.Now().Add(ttl)
	var res sql.Result
	var err error
	if d.dialect == "postgres" {
		res, err = d.db.ExecContext(ctx,
			`UPDATE channel_leases SET expires_at = $1
				WHERE channel = $2 AND account_id = $3 AND holder_id = $4`,
			expires, channel, accountID, holderID)
	} else {
		res, err = d.db.ExecContext(ctx,
			`UPDATE channel_leases SET expires_at = ?
				WHERE channel = ? AND account_id = ? AND holder_id = ?`,
			expires, channel, accountID, holderID)
	}
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReleaseChannelLease voluntarily drops the lease so a peer can pick
// it up on its next acquire attempt without waiting for TTL. Bounded
// by holder_id so a stale Release from an evicted instance can't
// accidentally invalidate the current holder's row.
func (d *DBStore) ReleaseChannelLease(ctx context.Context, channel, accountID, holderID string) error {
	var err error
	if d.dialect == "postgres" {
		_, err = d.db.ExecContext(ctx,
			`DELETE FROM channel_leases WHERE channel = $1 AND account_id = $2 AND holder_id = $3`,
			channel, accountID, holderID)
	} else {
		_, err = d.db.ExecContext(ctx,
			`DELETE FROM channel_leases WHERE channel = ? AND account_id = ? AND holder_id = ?`,
			channel, accountID, holderID)
	}
	return err
}

// --- Projects ---

func (d *DBStore) ListProjects(ctx context.Context, userID, agentID string) ([]ProjectRecord, error) {
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT project_id, name, description, created_at, updated_at FROM projects
			WHERE user_id = %s AND agent_id = %s ORDER BY updated_at DESC`,
			d.ph(1), d.ph(2)),
		userID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectRecord
	for rows.Next() {
		p := ProjectRecord{UserID: userID, AgentID: agentID}
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DBStore) GetProject(ctx context.Context, userID, agentID, projectID string) (*ProjectRecord, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT name, description, created_at, updated_at FROM projects
			WHERE user_id = %s AND agent_id = %s AND project_id = %s`,
			d.ph(1), d.ph(2), d.ph(3)),
		userID, agentID, projectID)
	p := ProjectRecord{UserID: userID, AgentID: agentID, ID: projectID}
	if err := row.Scan(&p.Name, &p.Description, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, scanErr(err)
	}
	return &p, nil
}

// SaveProject upserts. created_at is preserved on update; updated_at
// is bumped every write. Empty name is allowed at the row level — the
// HTTP handler enforces non-empty so we don't double-validate here.
func (d *DBStore) SaveProject(ctx context.Context, p *ProjectRecord) error {
	if p.UserID == "" || p.AgentID == "" || p.ID == "" {
		return errors.New("store: SaveProject requires user_id, agent_id, project_id")
	}
	now := time.Now().UTC()
	if d.dialect == "postgres" {
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO projects (user_id, agent_id, project_id, name, description, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $6)
				ON CONFLICT (user_id, agent_id, project_id) DO UPDATE
				SET name=$4, description=$5, updated_at=$6`,
			p.UserID, p.AgentID, p.ID, p.Name, p.Description, now)
		return err
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO projects (user_id, agent_id, project_id, name, description, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (user_id, agent_id, project_id) DO UPDATE SET
			  name=excluded.name, description=excluded.description, updated_at=excluded.updated_at`,
		p.UserID, p.AgentID, p.ID, p.Name, p.Description, now, now)
	return err
}

// DeleteProject removes the row. Caller must ensure no sessions still
// reference it (via CountProjectSessions); this method does not check
// because the handler decides the policy (block vs cascade) — the
// store stays mechanical.
func (d *DBStore) DeleteProject(ctx context.Context, userID, agentID, projectID string) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM projects WHERE user_id = %s AND agent_id = %s AND project_id = %s`,
			d.ph(1), d.ph(2), d.ph(3)),
		userID, agentID, projectID)
	return err
}

func (d *DBStore) CountProjectSessions(ctx context.Context, userID, agentID, projectID string) (int, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM sessions WHERE user_id = %s AND agent_id = %s AND project_id = %s`,
			d.ph(1), d.ph(2), d.ph(3)),
		userID, agentID, projectID)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// --- Project runtimes (live-app layer) ---

const projectRuntimeCols = `template_ref, status, dev_port, host_port, preview_url, container_id, git_ref, last_error, created_at, updated_at`

func scanProjectRuntime(r *ProjectRuntimeRecord, sc func(...any) error) error {
	return sc(&r.TemplateRef, &r.Status, &r.DevPort, &r.HostPort, &r.PreviewURL,
		&r.ContainerID, &r.GitRef, &r.LastError, &r.CreatedAt, &r.UpdatedAt)
}

func (d *DBStore) GetProjectRuntime(ctx context.Context, userID, agentID, projectID string) (*ProjectRuntimeRecord, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT `+projectRuntimeCols+` FROM project_runtimes
			WHERE user_id = %s AND agent_id = %s AND project_id = %s`,
			d.ph(1), d.ph(2), d.ph(3)),
		userID, agentID, projectID)
	rec := ProjectRuntimeRecord{UserID: userID, AgentID: agentID, ProjectID: projectID}
	if err := scanProjectRuntime(&rec, row.Scan); err != nil {
		return nil, scanErr(err)
	}
	return &rec, nil
}

// SaveProjectRuntime upserts. created_at is preserved on update;
// updated_at is bumped every write. Status defaults to 'none' at the
// row level if the caller left it empty.
func (d *DBStore) SaveProjectRuntime(ctx context.Context, r *ProjectRuntimeRecord) error {
	if r.UserID == "" || r.AgentID == "" || r.ProjectID == "" {
		return errors.New("store: SaveProjectRuntime requires user_id, agent_id, project_id")
	}
	if r.Status == "" {
		r.Status = "none"
	}
	now := time.Now().UTC()
	if d.dialect == "postgres" {
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO project_runtimes (user_id, agent_id, project_id, template_ref, status, dev_port, host_port, preview_url, container_id, git_ref, last_error, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12)
				ON CONFLICT (user_id, agent_id, project_id) DO UPDATE SET
				  template_ref=$4, status=$5, dev_port=$6, host_port=$7, preview_url=$8,
				  container_id=$9, git_ref=$10, last_error=$11, updated_at=$12`,
			r.UserID, r.AgentID, r.ProjectID, r.TemplateRef, r.Status, r.DevPort, r.HostPort,
			r.PreviewURL, r.ContainerID, r.GitRef, r.LastError, now)
		return err
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO project_runtimes (user_id, agent_id, project_id, template_ref, status, dev_port, host_port, preview_url, container_id, git_ref, last_error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (user_id, agent_id, project_id) DO UPDATE SET
			  template_ref=excluded.template_ref, status=excluded.status, dev_port=excluded.dev_port,
			  host_port=excluded.host_port, preview_url=excluded.preview_url, container_id=excluded.container_id,
			  git_ref=excluded.git_ref, last_error=excluded.last_error, updated_at=excluded.updated_at`,
		r.UserID, r.AgentID, r.ProjectID, r.TemplateRef, r.Status, r.DevPort, r.HostPort,
		r.PreviewURL, r.ContainerID, r.GitRef, r.LastError, now, now)
	return err
}

func (d *DBStore) DeleteProjectRuntime(ctx context.Context, userID, agentID, projectID string) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM project_runtimes WHERE user_id = %s AND agent_id = %s AND project_id = %s`,
			d.ph(1), d.ph(2), d.ph(3)),
		userID, agentID, projectID)
	return err
}

// ListAllProjectRuntimes returns every runtime row across all owners.
// Used by the idle sweeper to evict stale containers; deliberately not
// user-scoped.
func (d *DBStore) ListAllProjectRuntimes(ctx context.Context) ([]ProjectRuntimeRecord, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT user_id, agent_id, project_id, `+projectRuntimeCols+` FROM project_runtimes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectRuntimeRecord
	for rows.Next() {
		var rec ProjectRuntimeRecord
		if err := rows.Scan(&rec.UserID, &rec.AgentID, &rec.ProjectID,
			&rec.TemplateRef, &rec.Status, &rec.DevPort, &rec.HostPort, &rec.PreviewURL,
			&rec.ContainerID, &rec.GitRef, &rec.LastError, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// parseTimeString tries common time formats that modernc.org/sqlite may
// produce for TIMESTAMP columns (RFC3339, RFC3339Nano, and the Go default
// format that older code paths wrote).
func parseTimeString(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func scanCronJobs(rows *sql.Rows) ([]CronJobRecord, error) {
	var jobs []CronJobRecord
	for rows.Next() {
		var j CronJobRecord
		var lastRun, nextRun sql.NullTime
		if err := rows.Scan(&j.ID, &j.UserID, &j.AgentID, &j.Name, &j.Type, &j.Schedule, &j.Message, &j.Channel, &j.ChatID, &j.AccountID, &j.Timezone, &j.Enabled, &lastRun, &nextRun, &j.FailureCount, &j.CreatedAt); err != nil {
			return nil, err
		}
		if lastRun.Valid {
			j.LastRun = &lastRun.Time
		}
		if nextRun.Valid {
			j.NextRun = &nextRun.Time
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// --- Agent goals ---
//
// All goal columns ride together on every CRUD path — mutations happen
// via "read, mutate domain object, write back" rather than partial
// updates. Keeps the per-turn accounting logic in Go rather than
// scattered across UPDATE … SET fragments.
//
// Legacy columns (last_accounted_token_usage / time_used_seconds /
// last_accounted_at / safety_max_iterations / iterations) still exist
// on old SQLite databases — they're not in the current CREATE TABLE
// and the SQL below neither reads nor writes them.
const goalSelectCols = `id, agent_id, session_key, owner_user_id, channel, account_id, chat_id, project_id, objective, status, token_budget, tokens_used, created_at, updated_at`

func (d *DBStore) CreateGoal(ctx context.Context, g *GoalRecord) error {
	if g.AgentID == "" || g.SessionKey == "" {
		return errors.New("store: goal.agent_id and session_key are required")
	}
	if g.OwnerUserID == "" {
		return errors.New("store: goal.owner_user_id is required")
	}
	now := time.Now().UTC()
	if g.CreatedAt.IsZero() {
		g.CreatedAt = now
	}
	if g.UpdatedAt.IsZero() {
		g.UpdatedAt = now
	}
	if g.Status == "" {
		g.Status = "active"
	}
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO agent_goals (id, agent_id, session_key, owner_user_id, channel, account_id, chat_id, project_id, objective, status, token_budget, tokens_used, created_at, updated_at)
			VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)`,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5), d.ph(6), d.ph(7), d.ph(8), d.ph(9), d.ph(10), d.ph(11), d.ph(12), d.ph(13), d.ph(14)),
		g.ID, g.AgentID, g.SessionKey, g.OwnerUserID,
		g.Channel, g.AccountID, g.ChatID, g.ProjectID,
		g.Objective, g.Status,
		g.TokenBudget, g.TokensUsed, g.CreatedAt, g.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrGoalAlreadyExists
		}
		return err
	}
	return nil
}

func (d *DBStore) GetGoalBySession(ctx context.Context, agentID, sessionKey string) (*GoalRecord, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT `+goalSelectCols+` FROM agent_goals WHERE agent_id = %s AND session_key = %s`,
			d.ph(1), d.ph(2)),
		agentID, sessionKey)
	return scanGoal(row)
}

func (d *DBStore) UpdateGoal(ctx context.Context, g *GoalRecord) error {
	if g.ID == "" {
		return errors.New("store: goal.id is required for UpdateGoal")
	}
	g.UpdatedAt = time.Now().UTC()
	res, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE agent_goals
			SET status = %s, token_budget = %s, tokens_used = %s, updated_at = %s
			WHERE id = %s`,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5)),
		g.Status, g.TokenBudget, g.TokensUsed, g.UpdatedAt, g.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DBStore) DeleteGoal(ctx context.Context, goalID string) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM agent_goals WHERE id = %s`, d.ph(1)), goalID)
	return err
}

// scanGoal reads one row (from QueryRow) into a GoalRecord. Returns
// ErrNotFound (via scanErr) when the query matched nothing.
func scanGoal(row *sql.Row) (*GoalRecord, error) {
	var g GoalRecord
	var tokenBudget sql.NullInt64
	if err := row.Scan(&g.ID, &g.AgentID, &g.SessionKey, &g.OwnerUserID,
		&g.Channel, &g.AccountID, &g.ChatID, &g.ProjectID,
		&g.Objective, &g.Status,
		&tokenBudget, &g.TokensUsed, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return nil, scanErr(err)
	}
	if tokenBudget.Valid {
		g.TokenBudget = &tokenBudget.Int64
	}
	return &g, nil
}

// isUniqueViolation reports whether err is a UNIQUE-constraint
// violation in either Postgres (SQLSTATE 23505) or SQLite (substring
// "UNIQUE constraint failed"). Both drivers expose enough detail in
// the error text to identify this without importing driver packages.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Postgres lib/pq surfaces "pq: duplicate key value violates unique constraint"
	if strings.Contains(msg, "duplicate key value") {
		return true
	}
	// modernc.org/sqlite reports "UNIQUE constraint failed: <table>.<col>"
	if strings.Contains(msg, "UNIQUE constraint failed") {
		return true
	}
	return false
}

var _ Store = (*DBStore)(nil)
