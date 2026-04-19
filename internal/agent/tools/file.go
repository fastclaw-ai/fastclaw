package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

type readFileArgs struct {
	Path string `json:"path"`
}

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type listDirArgs struct {
	Path string `json:"path"`
}

var errOutsideSandbox = fmt.Errorf("access denied: path is outside the allowed sandbox directory")

// systemFiles are the agent metadata/identity files. When a relative path
// references one of these by basename, file tools resolve it against the
// system root rather than the user root.
var systemFiles = map[string]bool{
	"SOUL.md":      true,
	"IDENTITY.md":  true,
	"USER.md":      true,
	"BOOTSTRAP.md": true,
	"MEMORY.md":    true,
	"HEARTBEAT.md": true,
	"AGENTS.md":    true,
	"TOOLS.md":     true,
	"agent.json":   true,
}

// rootForPath returns the root a relative path should resolve against: the
// system root for identity files (SOUL.md, IDENTITY.md, ...), the user root
// for everything else. Absolute paths are returned as-is.
func (r *Registry) rootForPath(path string) string {
	if filepath.IsAbs(path) {
		return ""
	}
	clean := filepath.Clean(path)
	// Only single-segment relative paths can target a system file — nested
	// paths like "notes/SOUL.md" are treated as user content.
	if !strings.ContainsRune(clean, filepath.Separator) && systemFiles[clean] {
		return r.systemRoot
	}
	return r.userRoot
}

func registerFile(r *Registry) {
	r.Register("read_file", "Read the contents of a file", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "File path (relative to your working directory or absolute)",
			},
		},
		"required": []string{"path"},
	}, makeReadFile(r))

	r.Register("write_file", "Write content to a file (creates directories as needed)", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "File path (relative to your working directory or absolute)",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Content to write",
			},
		},
		"required": []string{"path", "content"},
	}, makeWriteFile(r))

	r.Register("list_dir", "List files and directories in a path", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Directory path (relative to your working directory or absolute)",
			},
		},
		"required": []string{"path"},
	}, makeListDir(r))
}

func resolvePath(root, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(root, path))
}

// resolvePathSandboxed resolves a path and validates that it stays within
// sandboxRoot. Returns an error when the resolved path escapes.
func resolvePathSandboxed(root, sandboxRoot, path string) (string, error) {
	full := resolvePath(root, path)
	if sandboxRoot == "" {
		return full, nil
	}
	absRoot, err := filepath.Abs(sandboxRoot)
	if err != nil {
		return "", fmt.Errorf("invalid sandbox root: %w", err)
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	if !strings.HasPrefix(absFull, absRoot+string(filepath.Separator)) && absFull != absRoot {
		return "", errOutsideSandbox
	}
	return absFull, nil
}

func makeReadFile(r *Registry) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args readFileArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}

		fullPath, err := resolvePathSandboxed(r.rootForPath(args.Path), r.sandboxRoot, args.Path)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return "", fmt.Errorf("read file: %w", err)
		}

		return string(data), nil
	}
}

func makeWriteFile(r *Registry) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args writeFileArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}

		fullPath, err := resolvePathSandboxed(r.rootForPath(args.Path), r.sandboxRoot, args.Path)
		if err != nil {
			return "", err
		}
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create directory: %w", err)
		}

		if err := os.WriteFile(fullPath, []byte(args.Content), 0o644); err != nil {
			return "", fmt.Errorf("write file: %w", err)
		}

		return fmt.Sprintf("Written %d bytes to %s", len(args.Content), fullPath), nil
	}
}

func makeListDir(r *Registry) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args listDirArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}

		fullPath, err := resolvePathSandboxed(r.rootForPath(args.Path), r.sandboxRoot, args.Path)
		if err != nil {
			return "", err
		}
		entries, err := os.ReadDir(fullPath)
		if err != nil {
			return "", fmt.Errorf("read dir: %w", err)
		}

		var sb strings.Builder
		for _, entry := range entries {
			info, _ := entry.Info()
			if entry.IsDir() {
				fmt.Fprintf(&sb, "d %s/\n", entry.Name())
			} else if info != nil {
				fmt.Fprintf(&sb, "f %s (%d bytes)\n", entry.Name(), info.Size())
			} else {
				fmt.Fprintf(&sb, "f %s\n", entry.Name())
			}
		}

		return sb.String(), nil
	}
}

// registerSandboxedFile re-registers file tools so they delegate to a
// sandbox.Executor instead of operating on the host filesystem.
func registerSandboxedFile(r *Registry, ex sandbox.Executor) {
	r.Register("read_file", "Read the contents of a file", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "File path inside the sandbox workspace",
			},
		},
		"required": []string{"path"},
	}, func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args readFileArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
		return ex.ReadFile(ctx, args.Path)
	})

	r.Register("write_file", "Write content to a file (creates directories as needed)", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "File path inside the sandbox workspace",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Content to write",
			},
		},
		"required": []string{"path", "content"},
	}, func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args writeFileArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
		return ex.WriteFile(ctx, args.Path, args.Content)
	})

	r.Register("list_dir", "List files and directories in a path", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Directory path inside the sandbox workspace",
			},
		},
		"required": []string{"path"},
	}, func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args listDirArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
		return ex.ListDir(ctx, args.Path)
	})
}
