package sandbox

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceStore abstracts durable storage for user workspace files.
// Implementations can be S3, MinIO, local filesystem, or database BLOBs.
type WorkspaceStore interface {
	// List returns all file paths for a user's workspace.
	List(ctx context.Context, userID string) ([]string, error)
	// Get reads a file from durable storage.
	Get(ctx context.Context, userID, path string) (io.ReadCloser, error)
	// Put writes a file to durable storage.
	Put(ctx context.Context, userID, path string, r io.Reader) error
	// Delete removes a file from durable storage.
	Delete(ctx context.Context, userID, path string) error
}

// WorkspaceSync handles hydrating a sandbox workspace from durable storage
// and flushing changes back.
type WorkspaceSync struct {
	store WorkspaceStore
}

// NewWorkspaceSync creates a sync manager.
func NewWorkspaceSync(store WorkspaceStore) *WorkspaceSync {
	return &WorkspaceSync{store: store}
}

// Hydrate downloads all workspace files for a user into a local directory.
// Called when a sandbox is first created for a user.
func (ws *WorkspaceSync) Hydrate(ctx context.Context, userID, localDir string) error {
	files, err := ws.store.List(ctx, userID)
	if err != nil {
		return fmt.Errorf("list workspace files: %w", err)
	}

	for _, path := range files {
		rc, err := ws.store.Get(ctx, userID, path)
		if err != nil {
			slog.Warn("workspace hydrate: skip file", "user", userID, "path", path, "error", err)
			continue
		}

		localPath := filepath.Join(localDir, path)
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			rc.Close()
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(localPath), err)
		}

		f, err := os.Create(localPath)
		if err != nil {
			rc.Close()
			return fmt.Errorf("create %s: %w", localPath, err)
		}
		_, copyErr := io.Copy(f, rc)
		f.Close()
		rc.Close()
		if copyErr != nil {
			return fmt.Errorf("write %s: %w", localPath, copyErr)
		}
	}

	slog.Info("workspace hydrated", "user", userID, "files", len(files))
	return nil
}

// Flush uploads all files from a local directory to durable storage.
// Called when a sandbox is about to be destroyed.
func (ws *WorkspaceSync) Flush(ctx context.Context, userID, localDir string) error {
	var count int
	err := filepath.Walk(localDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		relPath, _ := filepath.Rel(localDir, path)
		relPath = filepath.ToSlash(relPath)

		// Skip hidden files and known temporaries
		if strings.HasPrefix(filepath.Base(relPath), ".") {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		if err := ws.store.Put(ctx, userID, relPath, f); err != nil {
			slog.Warn("workspace flush: skip file", "user", userID, "path", relPath, "error", err)
			return nil // continue flushing other files
		}
		count++
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk %s: %w", localDir, err)
	}
	slog.Info("workspace flushed", "user", userID, "files", count)
	return nil
}

// SyncFile uploads a single file to durable storage. Called after write_file
// tool execution for real-time sync (optional — callers can batch via Flush).
func (ws *WorkspaceSync) SyncFile(ctx context.Context, userID, localDir, relPath string) error {
	fullPath := filepath.Join(localDir, relPath)
	f, err := os.Open(fullPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return ws.store.Put(ctx, userID, filepath.ToSlash(relPath), f)
}
