package store

import (
	"database/sql"
	"fmt"
	"time"
)

// FTSStore manages a SQLite FTS5 index for conversation history.
type FTSStore struct {
	db *sql.DB
}

// FTSResult is a single full-text search hit.
type FTSResult struct {
	Content   string
	Timestamp time.Time
	AgentID   string
	ChatID    string
	Snippet   string  // FTS5 snippet() function output
	Rank      float64
}

// NewFTSStore opens (or creates) a SQLite database at dbPath and returns an FTSStore.
func NewFTSStore(dbPath string) (*FTSStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open fts db: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite single-writer
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping fts db: %w", err)
	}
	return &FTSStore{db: db}, nil
}

// Init creates the FTS5 virtual table and the shadow content table if they don't exist.
func (f *FTSStore) Init() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS messages_content (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			content TEXT NOT NULL,
			timestamp TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			chat_id TEXT NOT NULL
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
			content,
			timestamp,
			agent_id,
			chat_id,
			content='messages_content',
			content_rowid='id',
			tokenize='porter unicode61'
		)`,
		// Triggers to keep FTS in sync with content table
		`CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages_content BEGIN
			INSERT INTO messages_fts(rowid, content, timestamp, agent_id, chat_id)
			VALUES (new.id, new.content, new.timestamp, new.agent_id, new.chat_id);
		END`,
		`CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages_content BEGIN
			INSERT INTO messages_fts(messages_fts, rowid, content, timestamp, agent_id, chat_id)
			VALUES ('delete', old.id, old.content, old.timestamp, old.agent_id, old.chat_id);
		END`,
	}
	for _, stmt := range stmts {
		if _, err := f.db.Exec(stmt); err != nil {
			return fmt.Errorf("fts init: %w\nSQL: %s", err, stmt)
		}
	}
	return nil
}

// Index adds a message to the FTS index.
func (f *FTSStore) Index(agentID, chatID, role, content string, ts time.Time) error {
	_, err := f.db.Exec(
		`INSERT INTO messages_content (content, timestamp, agent_id, chat_id) VALUES (?, ?, ?, ?)`,
		role+": "+content, ts.Format(time.RFC3339), agentID, chatID,
	)
	return err
}

// Search performs a full-text search and returns ranked results.
func (f *FTSStore) Search(query string, limit int) ([]FTSResult, error) {
	if limit <= 0 {
		limit = 10
	}

	rows, err := f.db.Query(
		`SELECT
			snippet(messages_fts, 0, '<b>', '</b>', '...', 32) AS snippet,
			messages_fts.content,
			messages_fts.timestamp,
			messages_fts.agent_id,
			messages_fts.chat_id,
			rank
		FROM messages_fts
		WHERE messages_fts MATCH ?
		ORDER BY rank
		LIMIT ?`,
		query, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer rows.Close()

	var results []FTSResult
	for rows.Next() {
		var r FTSResult
		var tsStr string
		if err := rows.Scan(&r.Snippet, &r.Content, &tsStr, &r.AgentID, &r.ChatID, &r.Rank); err != nil {
			continue
		}
		r.Timestamp, _ = time.Parse(time.RFC3339, tsStr)
		results = append(results, r)
	}
	return results, nil
}

// Close closes the underlying database.
func (f *FTSStore) Close() error {
	return f.db.Close()
}
