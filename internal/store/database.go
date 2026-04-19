package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/lib/pq"          // PostgreSQL driver
	_ "modernc.org/sqlite"         // SQLite driver (pure Go, no CGO)
)

// DBStore implements Store using a SQL database (PostgreSQL or SQLite).
// The user_id column is kept in SQL for backwards compatibility but always set to ''.
type DBStore struct {
	db      *sql.DB
	dialect string // "postgres" or "sqlite"
}

// NewDBStore creates a database-backed store.
// dsn examples:
//
//	postgres: "postgres://user:pass@host:5432/fastclaw?sslmode=disable"
//	sqlite:   "file:fastclaw.db?_journal=WAL"
func NewDBStore(dialect, dsn string) (*DBStore, error) {
	db, err := sql.Open(driverName(dialect), dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dialect, err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping %s: %w", dialect, err)
	}

	return &DBStore{db: db, dialect: dialect}, nil
}

func driverName(dialect string) string {
	switch dialect {
	case "postgres":
		return "postgres" // lib/pq driver
	case "sqlite":
		return "sqlite" // modernc.org/sqlite driver
	default:
		return dialect
	}
}

// Migrate creates tables if they don't exist.
func (d *DBStore) Migrate(ctx context.Context) error {
	stmts := d.migrationSQL()
	for _, stmt := range stmts {
		if _, err := d.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w\nSQL: %s", err, stmt)
		}
	}
	return nil
}

func (d *DBStore) migrationSQL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS configs (
			user_id TEXT NOT NULL,
			data TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS agents (
			user_id TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			config TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (user_id, agent_id)
		)`,
		`CREATE TABLE IF NOT EXISTS workspace_files (
			user_id TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			filename TEXT NOT NULL,
			content TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (user_id, agent_id, filename)
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			user_id TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			session_key TEXT NOT NULL,
			messages TEXT NOT NULL DEFAULT '[]',
			message_count INTEGER NOT NULL DEFAULT 0,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (user_id, agent_id, session_key)
		)`,
		`CREATE TABLE IF NOT EXISTS cron_jobs (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT 'cron',
			schedule TEXT NOT NULL,
			message TEXT NOT NULL,
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			account_id TEXT NOT NULL DEFAULT '',
			timezone TEXT NOT NULL DEFAULT 'UTC',
			enabled BOOLEAN NOT NULL DEFAULT true,
			last_run TIMESTAMP,
			next_run TIMESTAMP,
			locked_by TEXT,
			locked_at TIMESTAMP,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cron_jobs_schedule ON cron_jobs (user_id, enabled, next_run)`,
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

// --- Config ---

func (d *DBStore) GetConfig(ctx context.Context) (*GlobalConfig, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT data, created_at, updated_at FROM configs WHERE user_id = %s", d.ph(1)),
		"")

	var dataStr string
	var cfg GlobalConfig
	if err := row.Scan(&dataStr, &cfg.CreatedAt, &cfg.UpdatedAt); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(dataStr), &cfg.Data)
	return &cfg, nil
}

func (d *DBStore) SaveConfig(ctx context.Context, cfg *GlobalConfig) error {
	data, _ := json.Marshal(cfg.Data)
	now := time.Now()

	if d.dialect == "postgres" {
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO configs (user_id, data, created_at, updated_at)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (user_id) DO UPDATE SET data = $2, updated_at = $4`,
			"", string(data), now, now)
		return err
	}
	// SQLite
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO configs (user_id, data, created_at, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT (user_id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		"", string(data), now, now)
	return err
}

// --- Agents ---

func (d *DBStore) ListAgents(ctx context.Context) ([]AgentRecord, error) {
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf("SELECT agent_id, name, model, config, created_at, updated_at FROM agents WHERE user_id = %s ORDER BY created_at", d.ph(1)),
		"")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []AgentRecord
	for rows.Next() {
		var ag AgentRecord
		var cfgStr string
		if err := rows.Scan(&ag.ID, &ag.Name, &ag.Model, &cfgStr, &ag.CreatedAt, &ag.UpdatedAt); err != nil {
			continue
		}
		json.Unmarshal([]byte(cfgStr), &ag.Config)
		agents = append(agents, ag)
	}
	return agents, nil
}

func (d *DBStore) GetAgent(ctx context.Context, agentID string) (*AgentRecord, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT agent_id, name, model, config, created_at, updated_at FROM agents WHERE user_id = %s AND agent_id = %s", d.ph(1), d.ph(2)),
		"", agentID)

	var ag AgentRecord
	var cfgStr string
	if err := row.Scan(&ag.ID, &ag.Name, &ag.Model, &cfgStr, &ag.CreatedAt, &ag.UpdatedAt); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(cfgStr), &ag.Config)

	return &ag, nil
}

func (d *DBStore) SaveAgent(ctx context.Context, agent *AgentRecord) error {
	cfgData, _ := json.Marshal(agent.Config)
	now := time.Now()

	if d.dialect == "postgres" {
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO agents (user_id, agent_id, name, model, config, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)
			 ON CONFLICT (user_id, agent_id) DO UPDATE SET name=$3, model=$4, config=$5, updated_at=$7`,
			"", agent.ID, agent.Name, agent.Model, string(cfgData), now, now)
		return err
	}

	_, err := d.db.ExecContext(ctx,
		`INSERT INTO agents (user_id, agent_id, name, model, config, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (user_id, agent_id) DO UPDATE SET
		   name=excluded.name, model=excluded.model, config=excluded.config, updated_at=excluded.updated_at`,
		"", agent.ID, agent.Name, agent.Model, string(cfgData), now, now)
	return err
}

func (d *DBStore) DeleteAgent(ctx context.Context, agentID string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, table := range []string{"workspace_files", "sessions", "agents"} {
		if d.dialect == "postgres" {
			tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE user_id = $1 AND agent_id = $2", table), "", agentID)
		} else {
			tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE user_id = ? AND agent_id = ?", table), "", agentID)
		}
	}

	return tx.Commit()
}

// --- Sessions ---

func (d *DBStore) GetSession(ctx context.Context, agentID, sessionKey string) (*SessionRecord, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT messages, updated_at FROM sessions WHERE user_id = %s AND agent_id = %s AND session_key = %s", d.ph(1), d.ph(2), d.ph(3)),
		"", agentID, sessionKey)

	var msgsStr string
	var rec SessionRecord
	if err := row.Scan(&msgsStr, &rec.UpdatedAt); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(msgsStr), &rec.Messages)
	return &rec, nil
}

func (d *DBStore) SaveSession(ctx context.Context, agentID, sessionKey string, session *SessionRecord) error {
	msgsData, _ := json.Marshal(session.Messages)
	now := time.Now()
	count := len(session.Messages)

	if d.dialect == "postgres" {
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO sessions (user_id, agent_id, session_key, messages, message_count, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (user_id, agent_id, session_key) DO UPDATE SET messages=$4, message_count=$5, updated_at=$6`,
			"", agentID, sessionKey, string(msgsData), count, now)
		return err
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO sessions (user_id, agent_id, session_key, messages, message_count, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (user_id, agent_id, session_key) DO UPDATE SET
		   messages=excluded.messages, message_count=excluded.message_count, updated_at=excluded.updated_at`,
		"", agentID, sessionKey, string(msgsData), count, now)
	return err
}

func (d *DBStore) ListSessions(ctx context.Context, agentID string) ([]SessionMeta, error) {
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf("SELECT session_key, message_count, updated_at FROM sessions WHERE user_id = %s AND agent_id = %s ORDER BY updated_at DESC", d.ph(1), d.ph(2)),
		"", agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metas []SessionMeta
	for rows.Next() {
		var m SessionMeta
		rows.Scan(&m.Key, &m.MessageCount, &m.UpdatedAt)
		metas = append(metas, m)
	}
	return metas, nil
}

func (d *DBStore) DeleteSession(ctx context.Context, agentID, sessionKey string) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM sessions WHERE user_id = %s AND agent_id = %s AND session_key = %s", d.ph(1), d.ph(2), d.ph(3)),
		"", agentID, sessionKey)
	return err
}

// --- Memory ---

func (d *DBStore) GetMemory(ctx context.Context, agentID string) (string, error) {
	data, err := d.GetWorkspaceFile(ctx, agentID, "MEMORY.md")
	if err != nil {
		return "", nil
	}
	return string(data), nil
}

func (d *DBStore) SaveMemory(ctx context.Context, agentID, content string) error {
	return d.SaveWorkspaceFile(ctx, agentID, "MEMORY.md", []byte(content))
}

// --- Workspace Files ---

func (d *DBStore) GetWorkspaceFile(ctx context.Context, agentID, filename string) ([]byte, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT content FROM workspace_files WHERE user_id = %s AND agent_id = %s AND filename = %s", d.ph(1), d.ph(2), d.ph(3)),
		"", agentID, filename)

	var content string
	if err := row.Scan(&content); err != nil {
		return nil, err
	}
	return []byte(content), nil
}

func (d *DBStore) SaveWorkspaceFile(ctx context.Context, agentID, filename string, data []byte) error {
	now := time.Now()
	if d.dialect == "postgres" {
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO workspace_files (user_id, agent_id, filename, content, updated_at)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (user_id, agent_id, filename) DO UPDATE SET content=$4, updated_at=$5`,
			"", agentID, filename, string(data), now)
		return err
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO workspace_files (user_id, agent_id, filename, content, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (user_id, agent_id, filename) DO UPDATE SET content=excluded.content, updated_at=excluded.updated_at`,
		"", agentID, filename, string(data), now)
	return err
}

func (d *DBStore) ListWorkspaceFiles(ctx context.Context, agentID string) ([]string, error) {
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf("SELECT filename FROM workspace_files WHERE user_id = %s AND agent_id = %s ORDER BY filename", d.ph(1), d.ph(2)),
		"", agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []string
	for rows.Next() {
		var f string
		rows.Scan(&f)
		files = append(files, f)
	}
	return files, nil
}

// --- Cron Jobs ---

func (d *DBStore) ListCronJobs(ctx context.Context) ([]CronJobRecord, error) {
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf("SELECT id, agent_id, name, type, schedule, message, channel, chat_id, account_id, timezone, enabled, last_run, next_run, created_at FROM cron_jobs WHERE user_id = %s ORDER BY created_at", d.ph(1)),
		"")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return d.scanCronJobs(rows)
}

func (d *DBStore) GetCronJob(ctx context.Context, jobID string) (*CronJobRecord, error) {
	row := d.db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT id, agent_id, name, type, schedule, message, channel, chat_id, account_id, timezone, enabled, last_run, next_run, created_at FROM cron_jobs WHERE user_id = %s AND id = %s", d.ph(1), d.ph(2)),
		"", jobID)
	var j CronJobRecord
	var lastRun, nextRun sql.NullTime
	if err := row.Scan(&j.ID, &j.AgentID, &j.Name, &j.Type, &j.Schedule, &j.Message, &j.Channel, &j.ChatID, &j.AccountID, &j.Timezone, &j.Enabled, &lastRun, &nextRun, &j.CreatedAt); err != nil {
		return nil, err
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
	now := time.Now()
	if d.dialect == "postgres" {
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO cron_jobs (id, user_id, agent_id, name, type, schedule, message, channel, chat_id, account_id, timezone, enabled, last_run, next_run, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
			 ON CONFLICT (id) DO UPDATE SET name=$4, type=$5, schedule=$6, message=$7, channel=$8, chat_id=$9, account_id=$10, timezone=$11, enabled=$12, last_run=$13, next_run=$14`,
			job.ID, "", job.AgentID, job.Name, job.Type, job.Schedule, job.Message, job.Channel, job.ChatID, job.AccountID, job.Timezone, job.Enabled, job.LastRun, job.NextRun, now)
		return err
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO cron_jobs (id, user_id, agent_id, name, type, schedule, message, channel, chat_id, account_id, timezone, enabled, last_run, next_run, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET
		   name=excluded.name, type=excluded.type, schedule=excluded.schedule, message=excluded.message,
		   channel=excluded.channel, chat_id=excluded.chat_id, account_id=excluded.account_id,
		   timezone=excluded.timezone, enabled=excluded.enabled, last_run=excluded.last_run, next_run=excluded.next_run`,
		job.ID, "", job.AgentID, job.Name, job.Type, job.Schedule, job.Message, job.Channel, job.ChatID, job.AccountID, job.Timezone, job.Enabled, job.LastRun, job.NextRun, now)
	return err
}

func (d *DBStore) DeleteCronJob(ctx context.Context, jobID string) error {
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM cron_jobs WHERE user_id = %s AND id = %s", d.ph(1), d.ph(2)),
		"", jobID)
	return err
}

func (d *DBStore) GetDueCronJobs(ctx context.Context, now time.Time) ([]CronJobRecord, error) {
	var rows *sql.Rows
	var err error
	if d.dialect == "postgres" {
		rows, err = d.db.QueryContext(ctx,
			`SELECT id, agent_id, name, type, schedule, message, channel, chat_id, account_id, timezone, enabled, last_run, next_run, created_at
			 FROM cron_jobs WHERE enabled = true AND next_run <= $1 ORDER BY next_run`, now)
	} else {
		rows, err = d.db.QueryContext(ctx,
			`SELECT id, agent_id, name, type, schedule, message, channel, chat_id, account_id, timezone, enabled, last_run, next_run, created_at
			 FROM cron_jobs WHERE enabled = 1 AND next_run <= ? ORDER BY next_run`, now)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return d.scanCronJobs(rows)
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
	if d.dialect == "postgres" {
		_, err := d.db.ExecContext(ctx,
			`UPDATE cron_jobs SET last_run=$1, next_run=$2, locked_by=NULL, locked_at=NULL WHERE id=$3`,
			lastRun, nextRun, jobID)
		return err
	}
	_, err := d.db.ExecContext(ctx,
		`UPDATE cron_jobs SET last_run=?, next_run=?, locked_by=NULL, locked_at=NULL WHERE id=?`,
		lastRun, nextRun, jobID)
	return err
}

func (d *DBStore) scanCronJobs(rows *sql.Rows) ([]CronJobRecord, error) {
	var jobs []CronJobRecord
	for rows.Next() {
		var j CronJobRecord
		var lastRun, nextRun sql.NullTime
		if err := rows.Scan(&j.ID, &j.AgentID, &j.Name, &j.Type, &j.Schedule, &j.Message, &j.Channel, &j.ChatID, &j.AccountID, &j.Timezone, &j.Enabled, &lastRun, &nextRun, &j.CreatedAt); err != nil {
			continue
		}
		if lastRun.Valid {
			j.LastRun = &lastRun.Time
		}
		if nextRun.Valid {
			j.NextRun = &nextRun.Time
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}

// Ensure DBStore implements Store.
var _ Store = (*DBStore)(nil)
