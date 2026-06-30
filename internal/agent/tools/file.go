package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/skills"
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

type editFileArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// editSchema is the JSON schema advertised for edit_file. Defined once and
// reused by registerFile / registerSandboxedFile so the two registration
// paths can't drift on parameter shape.
var editSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"path": map[string]interface{}{
			"type":        "string",
			"description": "File path (relative to your working directory or absolute)",
		},
		"old_string": map[string]interface{}{
			"type":        "string",
			"description": "Exact text to replace. Must match a unique substring in the file unless replace_all is true.",
		},
		"new_string": map[string]interface{}{
			"type":        "string",
			"description": "Replacement text. Must differ from old_string.",
		},
		"replace_all": map[string]interface{}{
			"type":        "boolean",
			"description": "Replace every occurrence of old_string instead of requiring uniqueness. Defaults to false.",
		},
	},
	"required": []string{"path", "old_string", "new_string"},
}

const editDescription = "Edit a file by replacing an exact substring. Prefer this over write_file when changing only part of a file (especially identity files like SOUL.md / MEMORY.md): it's cheaper, can't drop unrelated content, and validates the replacement was applied. old_string must match a unique substring unless replace_all is true; new_string must differ from old_string. Read the file first if you're unsure of the exact text."

// validateFileTargetPath rejects path arguments to write-like ops that
// can't refer to a single file. Empty strings, directory-suffix paths
// ("foo/"), and the special directory aliases (".", "..", "/") all slip
// through the downstream routing (isWorkspacePath treats "" as workspace-
// scoped because filepath.Clean("") == ".") and end up at os.OpenFile on
// the session directory, surfacing a cryptic "is a directory" error.
// Refusing early gives the model an actionable, tool-shaped message
// instead.
func validateFileTargetPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path is required and must include a filename")
	}
	if strings.HasSuffix(path, "/") || strings.HasSuffix(path, string(filepath.Separator)) {
		return fmt.Errorf("path %q ends in a separator; include a filename at the end", path)
	}
	switch filepath.Clean(path) {
	case ".", "..", "/":
		return fmt.Errorf("path %q is a directory, not a file; include a filename", path)
	}
	return nil
}

// asIsDirToolError detects the "is a directory" failure mode (raised when
// a write/edit resolves to an existing directory rather than a file) and
// promotes it to a tool-level message the model can recover from. Falls
// back to the caller's wrapped error otherwise.
func asIsDirToolError(opName, path string, err error) error {
	if err != nil && strings.Contains(err.Error(), "is a directory") {
		return fmt.Errorf("%s: %q resolves to a directory; include a filename in the path", opName, path)
	}
	return nil
}

// applyEdit performs the in-memory string replacement that backs edit_file.
// Centralised so every backend (filesystem, workspaceStore, systemFileStore,
// sandbox executor) shares the same uniqueness / not-found / no-op rules.
// Returns the new content and a count of replacements; an error if the edit
// can't be applied as requested.
func applyEdit(path, content, oldStr, newStr string, replaceAll bool) (string, int, error) {
	if oldStr == "" {
		return "", 0, fmt.Errorf("edit_file: old_string is empty (use write_file to create a file)")
	}
	if oldStr == newStr {
		return "", 0, fmt.Errorf("edit_file: new_string must differ from old_string")
	}
	count := strings.Count(content, oldStr)
	if count == 0 {
		return "", 0, fmt.Errorf("edit_file: old_string not found in %s — re-read the file and copy the exact text (whitespace/indentation matters)", path)
	}
	if count > 1 && !replaceAll {
		return "", 0, fmt.Errorf("edit_file: old_string matches %d locations in %s — provide more surrounding context to make it unique, or set replace_all=true", count, path)
	}
	if replaceAll {
		return strings.ReplaceAll(content, oldStr, newStr), count, nil
	}
	return strings.Replace(content, oldStr, newStr, 1), 1, nil
}

var errOutsideSandbox = fmt.Errorf("access denied: path is outside the allowed sandbox directory")

// globalSkillsDirSuffix is used to detect attempts to write into the
// admin-managed global skills directory (~/.fastclaw/skills/). Reads are
// fine — the skills layer already exposes this content — but writes from
// chat would let agents silently install/overwrite skills for every other
// agent on the host.
const globalSkillsDirSuffix = "/.fastclaw/skills"

// errGlobalSkillsDirWrite is returned when write_file targets
// ~/.fastclaw/skills/ from inside an agent chat. The message tells the model
// exactly how to recover.
var errGlobalSkillsDirWrite = fmt.Errorf("access denied: ~/.fastclaw/skills/ is the admin-managed global skills directory. To create a new skill, load the \"skill-creator\" skill and follow its workflow (it scaffolds into this agent's private skills dir). To install an existing one, use the install_skill tool")

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

// isWorkspacePath decides whether a write/read/list_dir path belongs in the
// workspace store (vs. the agent's home / systemRoot on disk). Uses the same
// rules as rootForPath: identity filenames, the `skills/` subtree, and
// absolute paths stay on disk; everything else is workspace-scoped.
func (r *Registry) isWorkspacePath(path string) bool {
	if filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	if clean == "skills" || strings.HasPrefix(clean, "skills"+string(filepath.Separator)) {
		return false
	}
	if !strings.ContainsRune(clean, filepath.Separator) && systemFiles[clean] {
		return false
	}
	return true
}

// hostHomePath returns the resolved absolute filesystem path when the
// arg looks like an operator-host path the chatter wants to read/write,
// and false otherwise. Three forms are recognised:
//
//	~                 → the operator's home dir
//	~/<rel>           → joined under the operator's home dir
//	/Users/<u>/...    → macOS-style absolute home roots
//	/home/<u>/...     → Linux-style absolute home roots
//
// Used by the sandboxed file tools on SELF-HOSTED installs to route
// requests like "read ~/Downloads/foo.csv" to actual host disk
// instead of 404'ing inside the sandbox FS. Hosted (multi-tenant)
// deployments deliberately don't call this — the chatter doesn't own
// the daemon's filesystem so exposing it would be a privilege leak.
//
// Returns ("", false) when the path is not a host-home reference, OR
// when it falls under one of the FastClaw-managed roots
// (~/.fastclaw/...) — those are runtime internals and should keep
// flowing through their existing routing (workspaceStore, identity
// store, etc.) so chat writes can't, say, smash the agents' DB file.
func hostHomePath(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		// Sandbox-only / FastClaw-internal subtrees: skip host expansion
		// so the read/write falls through to the sandbox executor instead
		// of trying (and failing) on host disk where the path doesn't
		// exist. Symmetric to the absolute-path guard below.
		//   ~/.fastclaw/... — runtime internals (db, workspaces, …)
		//   ~/.agents/...   — sandbox bind-mount target for npx skills.
		//                     Host has these at <FASTCLAW_HOME>/users/<uid>/skills/,
		//                     not under ~. After `ls ~/.agents/skills/<x>/` runs
		//                     in-sandbox the model naturally calls
		//                     read_file with the same path; that path only
		//                     resolves inside the container.
		if strings.HasPrefix(path, "~/.fastclaw") || strings.HasPrefix(path, "~/.agents") {
			return "", false
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		if path == "~" {
			return home, true
		}
		return filepath.Join(home, path[2:]), true
	}
	if !filepath.IsAbs(path) {
		return "", false
	}
	if strings.HasPrefix(path, "/Users/") || strings.HasPrefix(path, "/home/") {
		// Refuse FastClaw-internal subpaths even when the chatter
		// reaches them via the host-home channel. Same guard as
		// errGlobalSkillsDirWrite, broader scope.
		if home, err := os.UserHomeDir(); err == nil {
			fastclawDir := filepath.Join(home, ".fastclaw")
			if path == fastclawDir || strings.HasPrefix(path, fastclawDir+string(filepath.Separator)) {
				return "", false
			}
		}
		return path, true
	}
	return "", false
}

// isSkillPath reports whether path is a chat-time `skills/<name>/...`
// write — the skill-creator convention. Absolute paths and the bare
// `skills` segment don't qualify (the latter is a directory, not a
// file write). Cleans the path so `skills/./foo/SKILL.md` matches.
func (r *Registry) isSkillPath(path string) bool {
	if filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	return clean != "skills" && strings.HasPrefix(clean, "skills"+string(filepath.Separator))
}

// skillRoot returns the host parent of the `skills/` subdir that
// chat-time skill writes should land in. Per-user when configured
// (the chatter's personal bucket), agent home otherwise.
func (r *Registry) skillRoot() string {
	if r.userSkillsRoot != "" {
		return r.userSkillsRoot
	}
	return r.systemRoot
}

// skillStoreOwner returns the workspace.Store pseudo-owner key the
// chat-created skill should mirror to. Per-user when userSkillsRoot
// is set (so the skill follows the chatter across agents); agent ID
// otherwise (legacy / single-user mode).
func (r *Registry) skillStoreOwner() string {
	if r.userSkillsRoot != "" && r.userID != "" {
		return skills.UserSkillOwner(r.userID)
	}
	return r.agentID
}

// writeSkillToHost lands a chat-created `skills/<name>/<rel>` file on
// host disk and mirrors it to the workspace store so SkillsLoader's
// local scan and any sibling pod's hydrate both see it. Used by the
// sandbox-mode write_file path (which would otherwise trap the file
// inside the ephemeral sandbox FS) and by host-mode write_file as a
// post-write store-sync hook.
//
// The path arg must already pass isSkillPath. Returns the absolute
// host path written so the caller can echo it back to the model.
func (r *Registry) writeSkillToHost(ctx context.Context, path, content string) (string, error) {
	root := r.skillRoot()
	if root == "" {
		return "", fmt.Errorf("write_file: no skills root configured for path %q", path)
	}
	full := filepath.Join(root, filepath.Clean(path))
	if isGlobalSkillsPath(full) {
		return "", errGlobalSkillsDirWrite
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("create directory: %w", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}
	// Mirror to the workspace store so a sibling pod (cloud deploy)
	// hydrates the new skill on its next turn instead of waiting for
	// pod restart. Best-effort; failures here don't unwrite the file.
	if r.workspaceStore != nil {
		if owner := r.skillStoreOwner(); owner != "" {
			rel := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "skills/")
			parts := strings.SplitN(rel, "/", 2)
			if len(parts) >= 1 && parts[0] != "" {
				skillName := parts[0]
				skillsDir := filepath.Join(root, "skills")
				if err := skills.SyncSkillUp(ctx, r.workspaceStore, owner, skillName, skillsDir); err != nil {
					slog.Warn("skill mirror to store failed",
						"owner", owner, "skill", skillName, "error", err)
				}
			}
		}
	}
	return full, nil
}

// rootForPath returns the root a relative path should resolve against:
//   - systemRoot (agent home) for identity files (SOUL.md, IDENTITY.md, …);
//   - userSkillsRoot (~/.fastclaw/users/<uid>/skills/) for `skills/...`
//     writes when the chatter's user-skills dir is wired (default in
//     multi-user installs). Routes here so chat-created skills accumulate
//     in the chatter's personal bucket — shared across every agent they
//     chat with, isolated from the agent owner's official skills and from
//     other users on the same shared agent. Falls back to systemRoot when
//     userSkillsRoot is empty (legacy / single-user installs);
//   - userRoot (agent workspace) for everything else, which is user-facing
//     artifact territory.
//
// Absolute paths are returned as-is.
func (r *Registry) rootForPath(path string) string {
	if filepath.IsAbs(path) {
		return ""
	}
	clean := filepath.Clean(path)
	if clean == "skills" || strings.HasPrefix(clean, "skills"+string(filepath.Separator)) {
		// Per-user bucket when configured, otherwise the agent home
		// (legacy behavior). The leading `skills/` prefix is preserved
		// in either case so SkillsLoader's scan picks it up.
		if r.userSkillsRoot != "" {
			return r.userSkillsRoot
		}
		return r.systemRoot
	}
	// Single-segment system files (SOUL.md, IDENTITY.md, ...) also route
	// home; nested paths like "notes/SOUL.md" stay in user content.
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

	r.Register("edit_file", editDescription, editSchema, makeEditFile(r))
}

func resolvePath(root, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(root, path))
}

// isGlobalSkillsPath reports whether absPath points at or under the
// admin-managed ~/.fastclaw/skills/ directory. Works across user home
// locations by matching the stable suffix.
func isGlobalSkillsPath(absPath string) bool {
	clean := filepath.Clean(absPath)
	return strings.HasSuffix(clean, globalSkillsDirSuffix) || strings.Contains(clean, globalSkillsDirSuffix+string(filepath.Separator))
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

// effectiveSandboxRoot picks the bound that file ops should enforce for a
// path resolving against `root`. Identity files (SOUL.md / IDENTITY.md /
// …) live in r.systemRoot — agent home, OUTSIDE the workspace sandbox
// mount — so the workspace sandbox bound would always reject them.
// Confine system-file operations to systemRoot itself instead, which
// keeps zip-slip-style escapes blocked without breaking the legitimate
// "agent reads its own IDENTITY.md" flow when the systemFileStore lookup
// misses (fresh agent, store not yet hydrated, no store configured at
// all).
func (r *Registry) effectiveSandboxRoot(root string) string {
	if root == r.systemRoot && r.systemRoot != "" {
		return r.systemRoot
	}
	return r.sandboxRoot
}

// looksBinary returns true when the payload contains a NUL byte in the
// first 8KB — a near-perfect signal for JPEG/PNG/PDF/zip/wasm/etc. We
// refuse to read binary files via read_file because the bytes get coerced
// into a Go string, then sent to the LLM as tool_result text: a 5MB JPG
// becomes ~1.5M garbled UTF-8 tokens, blowing past every model's context
// limit and turning the next inference into a multi-minute "thinking..."
// stall (or an outright API error). The right path for binary files is
// to feed the path directly to whatever skill handles that format
// (image-tool's `input`, etc.) — never inline the bytes.
func looksBinary(data []byte) bool {
	head := data
	if len(head) > 8192 {
		head = head[:8192]
	}
	for _, b := range head {
		if b == 0 {
			return true
		}
	}
	return false
}

func binaryRefusal(path string, size int) string {
	// Skill-agnostic by design: read_file is a system tool, but which
	// skill is the right consumer for a binary path depends on what
	// the host agent actually has installed (image editing, OCR,
	// archive extract, …). Naming a specific skill here would mislead
	// agents that don't have it. Per-skill guidance belongs in that
	// skill's SKILL.md / the agent's SOUL.md, not in a system tool's
	// error path.
	//
	// What this message MUST do: stop the model's "let me probe the
	// file first" reflex (file / identify / inline python on a 5MB
	// JPEG burns turns and never produces a useful result). The
	// "Don't probe" line is the load-bearing part.
	return fmt.Sprintf("[read_file refused: %q is a binary file (%d bytes). Binary bytes don't decode as text — loading them would blow past the context window. Don't probe with `file`, `identify`, `ls`, `python`, or any inline script — pass the path directly to whichever skill in your toolset handles this format (e.g. an image-editing skill for images). If your toolset doesn't have a skill for this format, tell the user instead of trying to inline-process the bytes.]", path, size)
}

func makeReadFile(r *Registry) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args readFileArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}

		// Identity-file confidentiality gate. A chatter who asks "show me
		// your SOUL.md" must not get the verbatim persona spec back.
		if r.identityFileBlocked(ctx, args.Path) {
			return IdentityFileRefusal, nil
		}
		// Skill-manifest gate. Same rationale for a bundled SKILL.md —
		// the agent's IP, not for a chatter to pull verbatim.
		if r.skillManifestBlocked(ctx, args.Path) {
			return SkillManifestRefusal, nil
		}

		// Mirror makeWriteFile's routing: userRoot-destined paths go to the
		// workspace store when one is configured.
		if r.workspaceStore != nil && r.agentID != "" && r.isWorkspacePath(args.Path) {
			tc := turnOrZero(ctx)
			rc, err := r.workspaceStore.Get(ctx, r.agentID, tc.ProjectID, r.scopeSessionID(ctx), r.wsPath(ctx, args.Path))
			if err != nil {
				return "", fmt.Errorf("workspace get: %w", err)
			}
			defer rc.Close()
			data, err := io.ReadAll(rc)
			if err != nil {
				return "", fmt.Errorf("workspace read: %w", err)
			}
			if looksBinary(data) {
				return binaryRefusal(args.Path, len(data)), nil
			}
			return string(data), nil
		}

		// Identity file reads always go through the durable store first
		// (db is source of truth; on-disk is fallback). Use the lenient
		// basename match so an LLM that expands "IDENTITY.md" into the
		// full host path it saw in the prompt's "Working Directory"
		// line still hits the store — earlier we required a bare
		// filename and absolute paths bypassed the store entirely,
		// reading from a workspace dir where identity files don't live.
		if r.systemFileStore != nil && r.agentID != "" && basenameIsSystemFile(args.Path) {
			name := filepath.Base(filepath.Clean(args.Path))
			if data, err := r.readSystemFileForUser(ctx, r.systemFileUserID(ctx, name), name); err == nil {
				return string(data), nil
			}
			// Store miss: try the agent's systemRoot on disk directly,
			// bypassing resolvePathSandboxed. systemRoot is the agent
			// metadata dir (e.g. ~/.fastclaw/agents/<id>/agent) which
			// in K8s deployments lives OUTSIDE sandboxRoot, so the
			// sandbox bound would always reject identity files even
			// though the filename is a fixed whitelist with no escape
			// surface. "Not found" is legitimate (a fresh agent may
			// have no IDENTITY.md row yet) — return empty so the agent
			// treats the field as unset, matching how
			// ContextBuilder.loadFile loads identity files for the
			// system prompt.
			if r.systemRoot != "" {
				if data, err := os.ReadFile(filepath.Join(r.systemRoot, name)); err == nil {
					return string(data), nil
				}
			}
			return "", nil
		}

		root := r.rootForPath(args.Path)
		fullPath, err := resolvePathSandboxed(root, r.effectiveSandboxRoot(root), args.Path)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			// Identity files (SOUL/IDENTITY/BOOTSTRAP/...) are routinely unset
			// on a fresh sqlite install — the store has only what the wizard
			// wrote (typically just SOUL.md) and the agent's host home dir
			// isn't even created. Surface "" instead of a not-found error so
			// the agent treats the file as blank and continues, matching how
			// ContextBuilder.loadFile loads them for the system prompt.
			if os.IsNotExist(err) && isSingleSegmentSystemFile(args.Path) {
				return "", nil
			}
			return "", fmt.Errorf("read file: %w", err)
		}

		if looksBinary(data) {
			return binaryRefusal(args.Path, len(data)), nil
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
		if err := validateFileTargetPath(args.Path); err != nil {
			return "", fmt.Errorf("write_file: %w", err)
		}

		// Identity-file confidentiality gate — also blocks a chatter
		// from REWRITING the agent's persona via prompt injection.
		if r.identityFileBlocked(ctx, args.Path) {
			return IdentityFileRefusal, nil
		}

		// When a workspace store is configured, route userRoot-destined
		// writes through it. Identity files (systemRoot) still hit the
		// filesystem because the memory store already covers their
		// durability via a separate path.
		if r.workspaceStore != nil && r.agentID != "" && r.isWorkspacePath(args.Path) {
			if err := r.workspaceStore.Put(ctx, r.agentID, turnOrZero(ctx).ProjectID, r.scopeSessionID(ctx), r.wsPath(ctx, args.Path),
				strings.NewReader(args.Content), int64(len(args.Content)), ""); err != nil {
				if friendly := asIsDirToolError("write_file", args.Path, err); friendly != nil {
					return "", friendly
				}
				return "", fmt.Errorf("workspace put: %w", err)
			}
			return fmt.Sprintf("Written %d bytes to %s", len(args.Content), args.Path), nil
		}

		// Identity files (SOUL.md / IDENTITY.md / ...) need to land in the
		// same durable store the admin UI reads from — otherwise the
		// agent's BOOTSTRAP flow would write to pod-local disk and the
		// Customize page would show blanks. Route through the
		// systemFileStore when available.
		if r.systemFileStore != nil && r.agentID != "" && isSingleSegmentSystemFile(args.Path) {
			name := filepath.Clean(args.Path)
			if err := r.systemFileStore.SaveWorkspaceFile(ctx, r.agentID, r.systemFileUserID(ctx, name), name, []byte(args.Content)); err != nil {
				return "", fmt.Errorf("system file save: %w", err)
			}
			// Keep a filesystem mirror so the agent runtime (context
			// builder, skills loader, etc.) which still reads from disk
			// sees the same content on this pod. Other pods will pick
			// up the next call via their own store reads.
			if r.systemRoot != "" {
				disk := filepath.Join(r.systemRoot, name)
				_ = os.MkdirAll(filepath.Dir(disk), 0o755)
				_ = os.WriteFile(disk, []byte(args.Content), 0o644)
			}
			return fmt.Sprintf("Written %d bytes to %s", len(args.Content), name), nil
		}

		// Skill scaffolding takes a dedicated path so the same writeSkillToHost
		// helper that handles sandbox-mode also lands the file + mirrors to
		// the workspace store here, instead of duplicating the SyncSkillUp
		// hook in two places.
		if r.isSkillPath(args.Path) && r.skillRoot() != "" {
			full, err := r.writeSkillToHost(ctx, args.Path, args.Content)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Written %d bytes to %s", len(args.Content), full), nil
		}

		root := r.rootForPath(args.Path)
		fullPath, err := resolvePathSandboxed(root, r.effectiveSandboxRoot(root), args.Path)
		if err != nil {
			return "", err
		}
		if isGlobalSkillsPath(fullPath) {
			return "", errGlobalSkillsDirWrite
		}
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create directory: %w", err)
		}

		if err := os.WriteFile(fullPath, []byte(args.Content), 0o644); err != nil {
			if friendly := asIsDirToolError("write_file", args.Path, err); friendly != nil {
				return "", friendly
			}
			return "", fmt.Errorf("write file: %w", err)
		}

		return fmt.Sprintf("Written %d bytes to %s", len(args.Content), fullPath), nil
	}
}

func makeEditFile(r *Registry) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args editFileArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
		if err := validateFileTargetPath(args.Path); err != nil {
			return "", fmt.Errorf("edit_file: %w", err)
		}

		// Identity-file confidentiality gate.
		if r.identityFileBlocked(ctx, args.Path) {
			return IdentityFileRefusal, nil
		}
		// Skill-manifest gate — block edits to a bundled SKILL.md for
		// non-admin chatters (editing returns surrounding content + lets
		// them tamper with the agent's IP). Owner edits skills via the UI.
		if r.skillManifestBlocked(ctx, args.Path) {
			return SkillManifestRefusal, nil
		}

		// Mirror makeWriteFile's routing precedence: workspace store first
		// (user artifacts), then identity-file store (SOUL.md / IDENTITY.md /
		// MEMORY.md …), then filesystem. The read and the write must hit
		// the same backend or an edit could silently land in a different
		// store than the one the agent later reads from.
		if r.workspaceStore != nil && r.agentID != "" && r.isWorkspacePath(args.Path) {
			rc, err := r.workspaceStore.Get(ctx, r.agentID, turnOrZero(ctx).ProjectID, r.scopeSessionID(ctx), r.wsPath(ctx, args.Path))
			if err != nil {
				return "", fmt.Errorf("workspace get: %w", err)
			}
			data, readErr := io.ReadAll(rc)
			rc.Close()
			if readErr != nil {
				return "", fmt.Errorf("workspace read: %w", readErr)
			}
			if looksBinary(data) {
				return binaryRefusal(args.Path, len(data)), nil
			}
			updated, count, err := applyEdit(args.Path, string(data), args.OldString, args.NewString, args.ReplaceAll)
			if err != nil {
				return "", err
			}
			if err := r.workspaceStore.Put(ctx, r.agentID, turnOrZero(ctx).ProjectID, r.scopeSessionID(ctx), r.wsPath(ctx, args.Path),
				strings.NewReader(updated), int64(len(updated)), ""); err != nil {
				if friendly := asIsDirToolError("edit_file", args.Path, err); friendly != nil {
					return "", friendly
				}
				return "", fmt.Errorf("workspace put: %w", err)
			}
			return fmt.Sprintf("Edited %s (%d replacement(s))", args.Path, count), nil
		}

		if r.systemFileStore != nil && r.agentID != "" && isSingleSegmentSystemFile(args.Path) {
			name := filepath.Clean(args.Path)
			uid := r.systemFileUserID(ctx, name)
			data, err := r.readSystemFileForUser(ctx, uid, name)
			if err != nil {
				return "", fmt.Errorf("system file get: %w", err)
			}
			updated, count, err := applyEdit(args.Path, string(data), args.OldString, args.NewString, args.ReplaceAll)
			if err != nil {
				return "", err
			}
			if err := r.systemFileStore.SaveWorkspaceFile(ctx, r.agentID, uid, name, []byte(updated)); err != nil {
				return "", fmt.Errorf("system file save: %w", err)
			}
			// Same disk-mirror invariant as makeWriteFile so this pod's
			// in-process readers (context builder, skills loader) see the
			// new content immediately.
			if r.systemRoot != "" {
				disk := filepath.Join(r.systemRoot, name)
				_ = os.MkdirAll(filepath.Dir(disk), 0o755)
				_ = os.WriteFile(disk, []byte(updated), 0o644)
			}
			return fmt.Sprintf("Edited %s (%d replacement(s))", name, count), nil
		}

		root := r.rootForPath(args.Path)
		fullPath, err := resolvePathSandboxed(root, r.effectiveSandboxRoot(root), args.Path)
		if err != nil {
			return "", err
		}
		if isGlobalSkillsPath(fullPath) {
			return "", errGlobalSkillsDirWrite
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return "", fmt.Errorf("read file: %w", err)
		}
		if looksBinary(data) {
			return binaryRefusal(args.Path, len(data)), nil
		}
		updated, count, err := applyEdit(args.Path, string(data), args.OldString, args.NewString, args.ReplaceAll)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(fullPath, []byte(updated), 0o644); err != nil {
			return "", fmt.Errorf("write file: %w", err)
		}
		return fmt.Sprintf("Edited %s (%d replacement(s))", fullPath, count), nil
	}
}

// isSingleSegmentSystemFile matches "SOUL.md", "IDENTITY.md", etc. —
// the allow-listed identity file names, and only when the write targets
// the top-level directory (no slashes). Nested paths like
// "notes/SOUL.md" deliberately don't qualify. Used by the WRITE path
// where over-broad matching would let users hijack identity rows by
// putting an arbitrary file at /any/path/IDENTITY.md.
func isSingleSegmentSystemFile(path string) bool {
	if filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	if strings.ContainsRune(clean, filepath.Separator) {
		return false
	}
	return systemFiles[clean]
}

// basenameIsSystemFile is the lenient READ-side variant: it accepts
// absolute paths and nested paths as long as the *basename* is one of
// the identity filenames. Identity files are the source of truth in
// systemFileStore (db); the on-disk view is only a fallback. The LLM
// frequently expands a bare "IDENTITY.md" into the full host path it
// saw in the system prompt's "Working Directory" line — without this
// lenient match, those reads bypass the store entirely and miss the
// real content. Read-only, so the write-path attack surface above
// stays unchanged.
func basenameIsSystemFile(path string) bool {
	return systemFiles[filepath.Base(filepath.Clean(path))]
}

func makeListDir(r *Registry) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args listDirArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}

		// Workspace store has a flat key namespace; we synthesise a "dir
		// listing" by filtering List output to entries whose agent-relative
		// path sits under args.Path's prefix.
		if r.workspaceStore != nil && r.agentID != "" && r.isWorkspacePath(args.Path) {
			objs, err := r.workspaceStore.List(ctx, r.agentID, turnOrZero(ctx).ProjectID, r.scopeSessionID(ctx))
			if err != nil {
				return "", fmt.Errorf("workspace list: %w", err)
			}
			prefix := strings.Trim(filepath.ToSlash(filepath.Clean(args.Path)), "/")
			if prefix == "." {
				prefix = ""
			}
			var sb strings.Builder
			seenDirs := map[string]bool{}
			for _, o := range objs {
				p := filepath.ToSlash(o.Path)
				if prefix != "" && !strings.HasPrefix(p, prefix+"/") && p != prefix {
					continue
				}
				rel := strings.TrimPrefix(p, prefix)
				rel = strings.TrimPrefix(rel, "/")
				if rel == "" {
					continue
				}
				if i := strings.IndexByte(rel, '/'); i >= 0 {
					dirName := rel[:i]
					if !seenDirs[dirName] {
						seenDirs[dirName] = true
						fmt.Fprintf(&sb, "d %s/\n", dirName)
					}
					continue
				}
				fmt.Fprintf(&sb, "f %s (%d bytes)\n", rel, o.Size)
			}
			return sb.String(), nil
		}

		root := r.rootForPath(args.Path)
		fullPath, err := resolvePathSandboxed(root, r.effectiveSandboxRoot(root), args.Path)
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
// sandbox.Executor for paths that don't belong to a store.
//
// IMPORTANT: identity files (SOUL.md, USER.md, MEMORY.md, …) live in
// `systemFileStore` (Postgres in cloud mode) and workspace artifacts live
// in `workspaceStore`. If we routed every path straight to the sandbox
// executor, the agent would 404 on its own identity files — they simply
// don't exist in the sandbox fs. Mirror the store-routing from the
// non-sandboxed path; only hit the sandbox executor when no store handles
// the path (absolute paths, `skills/...`, ad-hoc scripts, etc.). The
// sandbox badge is emitted only for the executor-fallback path — store
// hits intentionally don't badge, since they didn't run in the sandbox.
// mirrorCodingWriteToSandbox pushes a coding-agent workspace write into the
// live preview sandbox. Coding writes route to workspace.Store (host), which
// docker bind-mounts into the dev-server container — but a remote backend
// (E2B) shares no host mount, so without this the dev server never sees the
// edit and HMR looks dead. Guarded on RemoteWorkspace, so it's a no-op for
// docker (whose executor isn't remote). Best-effort: the user-visible write
// already hit the store, so a mirror failure only degrades live-reload — we
// log and move on. Destination is ABSOLUTE /workspace/<path> because the
// dev server serves the sandbox /workspace root and envd resolves a bare
// path against $HOME, not /workspace.
func (r *Registry) mirrorCodingWriteToSandbox(ctx context.Context, path, content string) {
	if turnOrZero(ctx).CodingSubdir == "" || r.executor == nil {
		return
	}
	if _, ok := r.executor.(sandbox.RemoteWorkspace); !ok {
		return
	}
	dest := "/workspace/" + strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "/")
	if _, err := r.executor.WriteFile(ctx, dest, content); err != nil {
		slog.Warn("coding preview mirror to sandbox failed", "path", dest, "err", err)
	}
}

func registerSandboxedFile(r *Registry, ex sandbox.Executor) {
	r.Register("read_file", "Read the contents of a file", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "File path (identity file, workspace-relative, or absolute inside the sandbox)",
			},
		},
		"required": []string{"path"},
	}, func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args readFileArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
		// Identity-file confidentiality gate — same as the host path.
		// Runs BEFORE the systemFileStore lookup so a chatter never
		// reaches the DB row at all.
		if r.identityFileBlocked(ctx, args.Path) {
			return IdentityFileRefusal, nil
		}
		// Skill-manifest gate — refuse before any routing so a chatter
		// can't read a bundled `/skills/<name>/SKILL.md` off the mount.
		if r.skillManifestBlocked(ctx, args.Path) {
			return SkillManifestRefusal, nil
		}
		// Identity files (SOUL.md, IDENTITY.md, …) are routed by basename
		// (lenient) instead of the strict isSingleSegmentSystemFile that
		// routeFor uses. Need to be checked separately for reads so an
		// LLM-emitted absolute path like
		// /data/.fastclaw/workspaces/<id>/IDENTITY.md still hits the DB.
		if r.systemFileStore != nil && r.agentID != "" && basenameIsSystemFile(args.Path) {
			name := filepath.Base(filepath.Clean(args.Path))
			if data, err := r.readSystemFileForUser(ctx, r.systemFileUserID(ctx, name), name); err == nil {
				return string(data), nil
			}
			return "", nil // miss → treat as unset (fresh agent)
		}
		switch r.routeFor(args.Path, OpRead) {
		case RouteWorkspaceStore:
			rc, err := r.workspaceStore.Get(ctx, r.agentID, turnOrZero(ctx).ProjectID, r.scopeSessionID(ctx), r.wsPath(ctx, args.Path))
			if err == nil {
				defer rc.Close()
				data, readErr := io.ReadAll(rc)
				if readErr == nil {
					if looksBinary(data) {
						return binaryRefusal(args.Path, len(data)), nil
					}
					return string(data), nil
				}
			}
			// Fall through to sandbox on store miss so a freshly-written
			// file the agent put inside the sandbox (mid-turn, not yet
			// mirrored to store) is still readable.
			out, err := ex.ReadFile(ctx, args.Path)
			if err == nil && looksBinary([]byte(out)) {
				return binaryRefusal(args.Path, len(out)), nil
			}
			return MetaSandboxPrefix + out, err
		case RouteSkillStore:
			full := filepath.Join(r.skillRoot(), filepath.Clean(args.Path))
			if data, err := os.ReadFile(full); err == nil {
				if looksBinary(data) {
					return binaryRefusal(args.Path, len(data)), nil
				}
				return string(data), nil
			}
			// Fall through to sandbox so pre-mounted skills under
			// /skills/<name>/ inside the container are still reachable.
			out, err := ex.ReadFile(ctx, args.Path)
			if err == nil && looksBinary([]byte(out)) {
				return binaryRefusal(args.Path, len(out)), nil
			}
			return MetaSandboxPrefix + out, err
		case RouteHostFS:
			full, ok := hostHomePath(args.Path)
			if !ok {
				full = args.Path // explicit absolute non-home path
			}
			data, err := os.ReadFile(full)
			if err != nil {
				return "", fmt.Errorf("host read %s: %w", full, err)
			}
			if looksBinary(data) {
				return binaryRefusal(args.Path, len(data)), nil
			}
			return string(data), nil
		case RouteRefuseSuggestSandbox:
			return "", fmt.Errorf("%s", errSandboxRequiredMessage)
		default: // RouteSandbox (and RouteSystemStore handled above out-of-band)
			out, err := ex.ReadFile(ctx, args.Path)
			if err == nil && looksBinary([]byte(out)) {
				return binaryRefusal(args.Path, len(out)), nil
			}
			return MetaSandboxPrefix + out, err
		}
	})

	r.Register("write_file", "Write content to a file (creates directories as needed)", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "File path (identity file, workspace-relative, or absolute inside the sandbox)",
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
		if err := validateFileTargetPath(args.Path); err != nil {
			return "", fmt.Errorf("write_file: %w", err)
		}
		if r.identityFileBlocked(ctx, args.Path) {
			return IdentityFileRefusal, nil
		}
		switch r.routeFor(args.Path, OpWrite) {
		case RouteSystemStore:
			name := filepath.Clean(args.Path)
			if err := r.systemFileStore.SaveWorkspaceFile(ctx, r.agentID, r.systemFileUserID(ctx, name), name, []byte(args.Content)); err != nil {
				return "", fmt.Errorf("system file save: %w", err)
			}
			return fmt.Sprintf("Written %d bytes to %s", len(args.Content), name), nil
		case RouteWorkspaceStore:
			if err := r.workspaceStore.Put(ctx, r.agentID, turnOrZero(ctx).ProjectID, r.scopeSessionID(ctx), r.wsPath(ctx, args.Path),
				strings.NewReader(args.Content), int64(len(args.Content)), ""); err != nil {
				if friendly := asIsDirToolError("write_file", args.Path, err); friendly != nil {
					return "", friendly
				}
				return "", fmt.Errorf("workspace put: %w", err)
			}
			r.mirrorCodingWriteToSandbox(ctx, args.Path, args.Content)
			return fmt.Sprintf("Written %d bytes to %s", len(args.Content), args.Path), nil
		case RouteSkillStore:
			// Skill scaffolding (skill-creator's `skills/<name>/...`) lands
			// on host disk where SkillsLoader scans + mirrors to OSS, NOT
			// in the ephemeral sandbox FS. writeSkillToHost handles both.
			full, err := r.writeSkillToHost(ctx, args.Path, args.Content)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Written %d bytes to %s", len(args.Content), full), nil
		case RouteHostFS:
			full, ok := hostHomePath(args.Path)
			if !ok {
				full = args.Path
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return "", fmt.Errorf("create directory: %w", err)
			}
			if err := os.WriteFile(full, []byte(args.Content), 0o644); err != nil {
				return "", fmt.Errorf("host write %s: %w", full, err)
			}
			return fmt.Sprintf("Written %d bytes to %s", len(args.Content), full), nil
		case RouteRefuseSuggestSandbox:
			return "", fmt.Errorf("%s", errSandboxRequiredMessage)
		default: // RouteSandbox
			out, err := ex.WriteFile(ctx, args.Path, args.Content)
			return MetaSandboxPrefix + out, err
		}
	})

	r.Register("list_dir", "List files and directories in a path", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Directory path (workspace-relative or absolute inside the sandbox)",
			},
		},
		"required": []string{"path"},
	}, func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args listDirArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
		switch r.routeFor(args.Path, OpList) {
		case RouteWorkspaceStore:
			objs, err := r.workspaceStore.List(ctx, r.agentID, turnOrZero(ctx).ProjectID, r.scopeSessionID(ctx))
			if err == nil {
				prefix := strings.Trim(filepath.ToSlash(filepath.Clean(args.Path)), "/")
				if prefix == "." {
					prefix = ""
				}
				var sb strings.Builder
				seenDirs := map[string]bool{}
				for _, o := range objs {
					p := filepath.ToSlash(o.Path)
					if prefix != "" && !strings.HasPrefix(p, prefix+"/") && p != prefix {
						continue
					}
					rel := strings.TrimPrefix(p, prefix)
					rel = strings.TrimPrefix(rel, "/")
					if rel == "" {
						continue
					}
					if i := strings.IndexByte(rel, '/'); i >= 0 {
						dirName := rel[:i]
						if !seenDirs[dirName] {
							seenDirs[dirName] = true
							fmt.Fprintf(&sb, "d %s/\n", dirName)
						}
						continue
					}
					fmt.Fprintf(&sb, "f %s (%d bytes)\n", rel, o.Size)
				}
				return sb.String(), nil
			}
			// Store error → fall through to sandbox so a freshly-written
			// sandbox-only dir is still listable.
			out, err := ex.ListDir(ctx, args.Path)
			return MetaSandboxPrefix + out, err
		case RouteHostFS:
			full, ok := hostHomePath(args.Path)
			if !ok {
				full = args.Path
			}
			entries, err := os.ReadDir(full)
			if err != nil {
				return "", fmt.Errorf("host list %s: %w", full, err)
			}
			var sb strings.Builder
			for _, e := range entries {
				if e.IsDir() {
					fmt.Fprintf(&sb, "d %s/\n", e.Name())
					continue
				}
				info, ierr := e.Info()
				if ierr != nil {
					fmt.Fprintf(&sb, "f %s\n", e.Name())
					continue
				}
				fmt.Fprintf(&sb, "f %s (%d bytes)\n", e.Name(), info.Size())
			}
			return sb.String(), nil
		case RouteRefuseSuggestSandbox:
			return "", fmt.Errorf("%s", errSandboxRequiredMessage)
		default: // RouteSandbox / RouteSystemStore (no list semantics) / RouteSkillStore
			out, err := ex.ListDir(ctx, args.Path)
			return MetaSandboxPrefix + out, err
		}
	})

	r.Register("edit_file", editDescription, editSchema, func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args editFileArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
		if err := validateFileTargetPath(args.Path); err != nil {
			return "", fmt.Errorf("edit_file: %w", err)
		}
		if r.identityFileBlocked(ctx, args.Path) {
			return IdentityFileRefusal, nil
		}
		if r.skillManifestBlocked(ctx, args.Path) {
			return SkillManifestRefusal, nil
		}

		// editSandboxRMW is the read-modify-write fallback through the
		// sandbox executor. Used when the store route misses or for any
		// path routeFor sends to the sandbox.
		editSandboxRMW := func() (string, error) {
			content, err := ex.ReadFile(ctx, args.Path)
			if err != nil {
				return "", err
			}
			if looksBinary([]byte(content)) {
				return binaryRefusal(args.Path, len(content)), nil
			}
			updated, count, err := applyEdit(args.Path, content, args.OldString, args.NewString, args.ReplaceAll)
			if err != nil {
				return "", err
			}
			if _, err := ex.WriteFile(ctx, args.Path, updated); err != nil {
				return "", err
			}
			return MetaSandboxPrefix + fmt.Sprintf("Edited %s (%d replacement(s))", args.Path, count), nil
		}

		switch r.routeFor(args.Path, OpWrite) {
		case RouteSystemStore:
			name := filepath.Clean(args.Path)
			uid := r.systemFileUserID(ctx, name)
			data, err := r.readSystemFileForUser(ctx, uid, name)
			if err != nil {
				return "", fmt.Errorf("system file get: %w", err)
			}
			updated, count, err := applyEdit(args.Path, string(data), args.OldString, args.NewString, args.ReplaceAll)
			if err != nil {
				return "", err
			}
			if err := r.systemFileStore.SaveWorkspaceFile(ctx, r.agentID, uid, name, []byte(updated)); err != nil {
				return "", fmt.Errorf("system file save: %w", err)
			}
			return fmt.Sprintf("Edited %s (%d replacement(s))", name, count), nil
		case RouteWorkspaceStore:
			rc, err := r.workspaceStore.Get(ctx, r.agentID, turnOrZero(ctx).ProjectID, r.scopeSessionID(ctx), r.wsPath(ctx, args.Path))
			if err == nil {
				data, readErr := io.ReadAll(rc)
				rc.Close()
				if readErr == nil {
					if looksBinary(data) {
						return binaryRefusal(args.Path, len(data)), nil
					}
					updated, count, err := applyEdit(args.Path, string(data), args.OldString, args.NewString, args.ReplaceAll)
					if err != nil {
						return "", err
					}
					if err := r.workspaceStore.Put(ctx, r.agentID, turnOrZero(ctx).ProjectID, r.scopeSessionID(ctx), r.wsPath(ctx, args.Path),
						strings.NewReader(updated), int64(len(updated)), ""); err != nil {
						if friendly := asIsDirToolError("edit_file", args.Path, err); friendly != nil {
							return "", friendly
						}
						return "", fmt.Errorf("workspace put: %w", err)
					}
					r.mirrorCodingWriteToSandbox(ctx, args.Path, updated)
					return fmt.Sprintf("Edited %s (%d replacement(s))", args.Path, count), nil
				}
			}
			// Store miss → sandbox RMW so a freshly-created file (not yet
			// mirrored) is still editable.
			return editSandboxRMW()
		case RouteSkillStore:
			// Skill files have no in-place edit semantics here today; fall
			// through to the sandbox RMW which can read /skills/<name>/...
			// from the read-only mount and write through (write will fail
			// at the FS layer if the mount is RO; that's the right error
			// to surface to the model).
			return editSandboxRMW()
		case RouteHostFS:
			full, ok := hostHomePath(args.Path)
			if !ok {
				full = args.Path
			}
			data, err := os.ReadFile(full)
			if err != nil {
				return "", fmt.Errorf("host read %s: %w", full, err)
			}
			if looksBinary(data) {
				return binaryRefusal(args.Path, len(data)), nil
			}
			updated, count, err := applyEdit(args.Path, string(data), args.OldString, args.NewString, args.ReplaceAll)
			if err != nil {
				return "", err
			}
			if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
				return "", fmt.Errorf("host write %s: %w", full, err)
			}
			return fmt.Sprintf("Edited %s (%d replacement(s))", full, count), nil
		case RouteRefuseSuggestSandbox:
			return "", fmt.Errorf("%s", errSandboxRequiredMessage)
		default: // RouteSandbox
			return editSandboxRMW()
		}
	})
}
