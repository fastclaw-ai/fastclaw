// Package store provides a pluggable storage backend for FastClaw.
// Default: file-based. For production: database-backed (postgres/sqlite).
// All persistent state is agent-scoped.
package store

import (
	"context"
	"time"
)

// Store is the unified interface for all persistent data.
type Store interface {
	// Config (global)
	GetConfig(ctx context.Context) (*GlobalConfig, error)
	SaveConfig(ctx context.Context, cfg *GlobalConfig) error

	// Agents
	ListAgents(ctx context.Context) ([]AgentRecord, error)
	GetAgent(ctx context.Context, agentID string) (*AgentRecord, error)
	SaveAgent(ctx context.Context, agent *AgentRecord) error
	DeleteAgent(ctx context.Context, agentID string) error

	// Sessions (per agent)
	GetSession(ctx context.Context, agentID, sessionKey string) (*SessionRecord, error)
	SaveSession(ctx context.Context, agentID, sessionKey string, session *SessionRecord) error
	ListSessions(ctx context.Context, agentID string) ([]SessionMeta, error)
	DeleteSession(ctx context.Context, agentID, sessionKey string) error

	// Memory (per agent)
	GetMemory(ctx context.Context, agentID string) (string, error)
	SaveMemory(ctx context.Context, agentID, content string) error

	// Workspace files (per agent: SOUL.md, etc.)
	GetWorkspaceFile(ctx context.Context, agentID, filename string) ([]byte, error)
	SaveWorkspaceFile(ctx context.Context, agentID, filename string, data []byte) error
	ListWorkspaceFiles(ctx context.Context, agentID string) ([]string, error)

	// Cron Jobs
	ListCronJobs(ctx context.Context) ([]CronJobRecord, error)
	GetCronJob(ctx context.Context, jobID string) (*CronJobRecord, error)
	SaveCronJob(ctx context.Context, job *CronJobRecord) error
	DeleteCronJob(ctx context.Context, jobID string) error
	GetDueCronJobs(ctx context.Context, now time.Time) ([]CronJobRecord, error)
	LockCronJob(ctx context.Context, jobID, instanceID string) (bool, error)
	UpdateCronJobRun(ctx context.Context, jobID string, lastRun, nextRun time.Time) error

	Close() error
}

// CronJobRecord holds a scheduled job.
type CronJobRecord struct {
	ID        string     `json:"id"`
	AgentID   string     `json:"agentId"`
	Name      string     `json:"name"`
	Type      string     `json:"type"`
	Schedule  string     `json:"schedule"`
	Message   string     `json:"message"`
	Channel   string     `json:"channel"`
	ChatID    string     `json:"chatId"`
	AccountID string     `json:"accountId"`
	Timezone  string     `json:"timezone"`
	Enabled   bool       `json:"enabled"`
	LastRun   *time.Time `json:"lastRun,omitempty"`
	NextRun   *time.Time `json:"nextRun,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

// GlobalConfig holds the global configuration.
type GlobalConfig struct {
	Data      map[string]interface{} `json:"data"`
	CreatedAt time.Time              `json:"createdAt"`
	UpdatedAt time.Time              `json:"updatedAt"`
}

// AgentRecord is the persisted state for one agent.
type AgentRecord struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Model     string                 `json:"model"`
	Config    map[string]interface{} `json:"config"`
	CreatedAt time.Time              `json:"createdAt"`
	UpdatedAt time.Time              `json:"updatedAt"`
}

// SessionRecord holds a conversation session.
type SessionRecord struct {
	Messages  []SessionMessage `json:"messages"`
	UpdatedAt time.Time        `json:"updatedAt"`
}

// SessionMessage is a single message in a session.
type SessionMessage struct {
	Role       string      `json:"role"`
	Content    string      `json:"content"`
	ToolCalls  interface{} `json:"toolCalls,omitempty"`
	ToolCallID string      `json:"toolCallId,omitempty"`
	Timestamp  time.Time   `json:"timestamp"`
}

// SessionMeta is summary info for a session (for listing).
type SessionMeta struct {
	Key          string    `json:"key"`
	MessageCount int       `json:"messageCount"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// MemoryEntry is one searchable memory log entry.
type MemoryEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	SessionID string    `json:"sessionId,omitempty"`
}

// StorageType identifies the storage backend.
type StorageType string

const (
	StorageFile     StorageType = "file"
	StoragePostgres StorageType = "postgres"
	StorageSQLite   StorageType = "sqlite"
)

// StorageConfig is the config block for choosing and configuring the store.
type StorageConfig struct {
	Type        StorageType `json:"type"`
	DSN         string      `json:"dsn,omitempty"`
	AutoMigrate bool        `json:"autoMigrate,omitempty"`
}
