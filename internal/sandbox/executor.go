package sandbox

import (
	"context"
	"time"
)

// Executor abstracts a sandboxed execution environment for one user.
// All agent tool calls (exec, read_file, write_file, list_dir) are routed
// through this interface in cloud mode so that each user gets an isolated
// filesystem and runtime. Implementations can be Docker containers,
// Firecracker microVMs, E2B hosted sandboxes, or any other backend.
type Executor interface {
	// Exec runs a shell command and returns combined stdout+stderr.
	Exec(ctx context.Context, command string, timeout time.Duration) (string, error)
	// ReadFile reads a file from the sandbox filesystem.
	ReadFile(ctx context.Context, path string) (string, error)
	// WriteFile writes content to a file (creating parent dirs as needed).
	WriteFile(ctx context.Context, path, content string) (string, error)
	// ListDir lists a directory and returns a human-readable listing.
	ListDir(ctx context.Context, path string) (string, error)
	// Close destroys the sandbox and releases resources.
	Close() error
}

// ExecutorPool manages per-user sandbox lifecycles. Get lazily creates a
// sandbox on first access; Release tears it down.
type ExecutorPool interface {
	Get(ctx context.Context, userID string) (Executor, error)
	Release(userID string) error
	CloseAll()
}

// PoolConfig holds configuration for creating sandbox pools.
type PoolConfig struct {
	Backend   string // "docker", "e2b" (future)
	Image     string // container image (for docker backend)
	Policy    *Policy
	// E2B-specific fields (future)
	E2BTemplate string
	E2BAPIKey   string
}
