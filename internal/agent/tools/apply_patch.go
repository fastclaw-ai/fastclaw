package tools

// apply_patch — multi-file patch tool aligned with OpenAI Codex's DSL.
//
// One tool call adds, updates, deletes, or renames any number of files.
// Two-phase execution: parse the envelope and compute every file's new
// content in memory first; only when every hunk anchors successfully do
// we flush writes/deletes. If any hunk fails, no file on disk changes —
// the agent gets a clear error and can re-emit the patch.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

// -----------------------------------------------------------------------------
// AST
// -----------------------------------------------------------------------------

type patchOpType int

const (
	opAdd patchOpType = iota
	opUpdate
	opDelete
)

type hunkLineKind int

const (
	lineContext hunkLineKind = iota
	lineAdd
	lineRemove
)

type hunkLine struct {
	Kind hunkLineKind
	Text string // without the leading +/-/space marker
}

type hunk struct {
	Lines []hunkLine
	IsEOF bool // hunk anchors to end of file
}

type patchOp struct {
	Type    patchOpType
	Path    string
	MoveTo  string // Update only — empty when no rename
	AddBody string // Add only — literal new file contents
	Hunks   []hunk // Update only
}

type patch struct {
	Ops []patchOp
}

const (
	beginPatch   = "*** Begin Patch"
	endPatch     = "*** End Patch"
	addPrefix    = "*** Add File: "
	updatePrefix = "*** Update File: "
	deletePrefix = "*** Delete File: "
	moveToPrefix = "*** Move to: "
	endOfFile    = "*** End of File"
	hunkSep      = "@@"
)

// -----------------------------------------------------------------------------
// Parser
// -----------------------------------------------------------------------------

// parsePatch turns a patch envelope into a structured AST. Errors include
// the offending line so the model can self-correct.
func parsePatch(input string) (*patch, error) {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, beginPatch) {
		return nil, fmt.Errorf("apply_patch: input must start with %q", beginPatch)
	}

	lines := strings.Split(trimmed, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}

	p := &patch{}
	var (
		currentOp   *patchOp
		currentHunk *hunk
		seenEnd     bool
	)

	flushOp := func() {
		if currentOp == nil {
			return
		}
		if currentHunk != nil {
			currentOp.Hunks = append(currentOp.Hunks, *currentHunk)
			currentHunk = nil
		}
		p.Ops = append(p.Ops, *currentOp)
		currentOp = nil
	}

loop:
	for i := 1; i < len(lines); i++ {
		line := lines[i]

		switch {
		case strings.TrimSpace(line) == endPatch:
			flushOp()
			seenEnd = true
			break loop

		case strings.HasPrefix(line, addPrefix):
			flushOp()
			currentOp = &patchOp{Type: opAdd, Path: strings.TrimSpace(line[len(addPrefix):])}

		case strings.HasPrefix(line, updatePrefix):
			flushOp()
			currentOp = &patchOp{Type: opUpdate, Path: strings.TrimSpace(line[len(updatePrefix):])}

		case strings.HasPrefix(line, deletePrefix):
			flushOp()
			currentOp = &patchOp{Type: opDelete, Path: strings.TrimSpace(line[len(deletePrefix):])}

		case strings.HasPrefix(line, moveToPrefix):
			if currentOp == nil || currentOp.Type != opUpdate {
				return nil, fmt.Errorf("apply_patch: %q outside an Update File block", strings.TrimSpace(line))
			}
			if currentOp.MoveTo != "" {
				return nil, fmt.Errorf("apply_patch: duplicate Move to: for %q", currentOp.Path)
			}
			if len(currentOp.Hunks) > 0 || currentHunk != nil {
				return nil, fmt.Errorf("apply_patch: %q must come before any hunk in Update File: %q",
					strings.TrimSpace(line), currentOp.Path)
			}
			currentOp.MoveTo = strings.TrimSpace(line[len(moveToPrefix):])

		case strings.TrimSpace(line) == endOfFile:
			if currentOp == nil || currentOp.Type != opUpdate || currentHunk == nil {
				return nil, fmt.Errorf("apply_patch: %q not inside an Update hunk", endOfFile)
			}
			currentHunk.IsEOF = true
			currentOp.Hunks = append(currentOp.Hunks, *currentHunk)
			currentHunk = nil

		case strings.HasPrefix(line, hunkSep):
			if currentOp == nil || currentOp.Type != opUpdate {
				return nil, fmt.Errorf("apply_patch: %q outside an Update File block", hunkSep)
			}
			if currentHunk != nil {
				currentOp.Hunks = append(currentOp.Hunks, *currentHunk)
			}
			currentHunk = &hunk{}

		default:
			if currentOp == nil {
				if strings.TrimSpace(line) == "" {
					continue
				}
				return nil, fmt.Errorf("apply_patch: unexpected line outside any file block: %q", line)
			}
			switch currentOp.Type {
			case opAdd:
				if !strings.HasPrefix(line, "+") {
					return nil, fmt.Errorf("apply_patch: Add File body line must start with %q (got %q)", "+", line)
				}
				// Each + line contributes one line of file content. Append a
				// trailing newline so the file ends in \n (POSIX convention,
				// matches what most tools emit).
				currentOp.AddBody += line[1:] + "\n"
			case opDelete:
				if strings.TrimSpace(line) != "" {
					return nil, fmt.Errorf("apply_patch: Delete File expects no body (got %q)", line)
				}
			case opUpdate:
				if currentHunk == nil {
					currentHunk = &hunk{}
				}
				if line == "" {
					// A bare blank line inside a hunk is treated as a blank
					// context line. Strict Codex requires " " (space) but
					// LLMs often drop the prefix; tolerating is harmless.
					currentHunk.Lines = append(currentHunk.Lines, hunkLine{Kind: lineContext, Text: ""})
					continue
				}
				switch line[0] {
				case ' ':
					currentHunk.Lines = append(currentHunk.Lines, hunkLine{Kind: lineContext, Text: line[1:]})
				case '+':
					currentHunk.Lines = append(currentHunk.Lines, hunkLine{Kind: lineAdd, Text: line[1:]})
				case '-':
					currentHunk.Lines = append(currentHunk.Lines, hunkLine{Kind: lineRemove, Text: line[1:]})
				default:
					return nil, fmt.Errorf("apply_patch: hunk line must start with ' ', '+', or '-' (got %q)", line)
				}
			}
		}
	}

	if !seenEnd {
		return nil, fmt.Errorf("apply_patch: missing %q sentinel", endPatch)
	}
	if len(p.Ops) == 0 {
		return nil, errors.New("apply_patch: empty patch (no file operations)")
	}
	return p, nil
}

// -----------------------------------------------------------------------------
// Applier (pure functions)
// -----------------------------------------------------------------------------

// applyHunks applies all hunks of an Update to oldContent and returns the
// new content. Trailing-newline state is preserved. The first hunk that
// fails to anchor produces an error mentioning its expected lines.
func applyHunks(path, oldContent string, hunks []hunk) (string, error) {
	hadTrailingNL := strings.HasSuffix(oldContent, "\n")
	var lines []string
	if oldContent != "" {
		lines = strings.Split(strings.TrimSuffix(oldContent, "\n"), "\n")
	}

	// searchFrom advances past each applied hunk so successive hunks can't
	// re-match into already-rewritten regions.
	searchFrom := 0
	for hi, h := range hunks {
		pattern := patternLines(h)
		// A pure-add hunk (only '+' lines, no context, no remove) has an
		// empty pattern. Per the Codex spec these are anchored to the end
		// of the current file — and crucially, they do NOT advance the
		// search cursor, so a later anchored hunk can still match earlier
		// in the file.
		anchorEOF := h.IsEOF || len(pattern) == 0

		idx := findHunkAnchor(lines, pattern, anchorEOF, searchFrom)
		if idx < 0 {
			return "", fmt.Errorf(
				"apply_patch: hunk #%d in %s did not match — re-read the file and emit a fresh patch.\nExpected lines:\n%s",
				hi+1, path, strings.Join(pattern, "\n"))
		}

		// Build the replacement using the file's *actual* text for context
		// lines (so a successful fuzzy / Unicode-normalised match
		// preserves the file's whitespace and original glyphs instead of
		// overwriting with the patch's ASCII version). Equivalent to
		// replacementLines(h) when the match was exact.
		replacement := buildReplacement(h, lines, idx)

		next := make([]string, 0, len(lines)-len(pattern)+len(replacement))
		next = append(next, lines[:idx]...)
		next = append(next, replacement...)
		next = append(next, lines[idx+len(pattern):]...)
		lines = next
		// Only advance the cursor for anchored hunks. EOF-anchored and
		// pure-add hunks tack onto the end of the file and must not
		// constrain where subsequent anchored hunks search from.
		if !anchorEOF {
			searchFrom = idx + len(replacement)
		}
	}

	out := strings.Join(lines, "\n")
	if hadTrailingNL && out != "" {
		out += "\n"
	}
	return out, nil
}

// patternLines extracts the pattern that must be matched in the file:
// context + remove lines, in order.
func patternLines(h hunk) []string {
	out := make([]string, 0, len(h.Lines))
	for _, l := range h.Lines {
		if l.Kind == lineContext || l.Kind == lineRemove {
			out = append(out, l.Text)
		}
	}
	return out
}

// buildReplacement assembles the lines that should replace the matched
// region. Context lines are sourced from the file (preserves the file's
// own whitespace when fuzzy matching squashed differences); add lines come
// from the hunk; remove lines are dropped. fileLines[startIdx:] must
// already align with the hunk's pattern.
func buildReplacement(h hunk, fileLines []string, startIdx int) []string {
	out := make([]string, 0, len(h.Lines))
	off := 0 // cursor into the matched region in fileLines
	for _, l := range h.Lines {
		switch l.Kind {
		case lineContext:
			out = append(out, fileLines[startIdx+off])
			off++
		case lineRemove:
			off++
		case lineAdd:
			out = append(out, l.Text)
		}
	}
	return out
}

// seekSequence returns the smallest index ≥ start where pattern aligns
// with haystack, or -1. An empty pattern is allowed and anchors at start
// (used by pure-add hunks at the top of a file or with EOF anchor).
func seekSequence(haystack, pattern []string, start int) int {
	if len(pattern) == 0 {
		if start <= len(haystack) {
			return start
		}
		return -1
	}
	for i := start; i+len(pattern) <= len(haystack); i++ {
		if linesEqual(haystack, i, pattern) {
			return i
		}
	}
	return -1
}

func linesEqual(haystack []string, start int, pattern []string) bool {
	if start < 0 || start+len(pattern) > len(haystack) {
		return false
	}
	for j, p := range pattern {
		if haystack[start+j] != p {
			return false
		}
	}
	return true
}

// findHunkAnchor locates a hunk's pattern in `lines`, trying progressively
// more lenient transforms: identity → rstrip → full trim → Unicode
// normalisation. EOF-anchored hunks prefer end-of-file alignment but fall
// back to a forward scan from searchFrom (matches Codex's seek_sequence).
// Returns -1 when no transform finds a match.
//
// Transforms are line-by-line and don't change the line count, so the
// returned index is valid against the original `lines` array. Callers
// pass that index to buildReplacement so context lines are sourced from
// the file's actual (un-normalised) text — fuzzy matching tolerates
// glyph differences without rewriting them.
func findHunkAnchor(lines, pattern []string, anchorEOF bool, searchFrom int) int {
	transforms := []func(string) string{
		nil,               // identity
		rstripWS,          // trailing whitespace tolerance
		strings.TrimSpace, // full whitespace tolerance
		normalizeForFuzzy, // Unicode dashes/quotes/spaces → ASCII
	}
	for _, t := range transforms {
		var tl, tp []string
		if t == nil {
			tl, tp = lines, pattern
		} else {
			tl = mapLines(lines, t)
			tp = mapLines(pattern, t)
		}
		if anchorEOF {
			start := len(tl) - len(tp)
			if start >= 0 && linesEqual(tl, start, tp) {
				return start
			}
			// EOF position miss → forward scan, matching Codex behavior.
		}
		if idx := seekSequence(tl, tp, searchFrom); idx >= 0 {
			return idx
		}
	}
	return -1
}

func mapLines(in []string, f func(string) string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = f(s)
	}
	return out
}

func rstripWS(s string) string {
	return strings.TrimRightFunc(s, unicode.IsSpace)
}

// normalizeForFuzzy maps typographic glyphs that LLMs (or auto-correct
// editors) frequently substitute for their ASCII counterparts. After
// mapping, the line is also fully trimmed so this level subsumes the
// trim level when the difference happens to be both whitespace and
// glyphs. Mirrors the Unicode mapping in Codex's seek_sequence.
func normalizeForFuzzy(s string) string {
	if isASCII(s) {
		return strings.TrimSpace(s)
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		switch r {
		// Dashes & hyphens (en/em/figure/non-breaking/minus/small/fullwidth).
		case '‐', '‑', '‒', '–', '—', '―',
			'−', '﹘', '﹣', '－':
			sb.WriteByte('-')
		// Single quotes / apostrophes (left, right, low, high-reversed).
		case '‘', '’', '‚', '‛':
			sb.WriteByte('\'')
		// Double quotes (left, right, low, high-reversed).
		case '“', '”', '„', '‟':
			sb.WriteByte('"')
		// Various spaces (NBSP, figure, punctuation, thin, hair, narrow
		// no-break, medium math, ideographic).
		case ' ', ' ', ' ', ' ', ' ',
			' ', ' ', '　':
			sb.WriteByte(' ')
		default:
			sb.WriteRune(r)
		}
	}
	return strings.TrimSpace(sb.String())
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// -----------------------------------------------------------------------------
// Tool description / schema
// -----------------------------------------------------------------------------

const applyPatchDescription = `Apply a multi-file patch in OpenAI Codex DSL format. Use this instead of chained edit_file/write_file calls when a change touches ≥2 files or ≥2 hunks — except todo.md progress updates, which must use edit_file immediately after each completed step. One tool call performs every edit atomically (parse + hunk matching happens for every file before any write; if any hunk fails to anchor, NO file is modified).

Format:

  *** Begin Patch
  *** Add File: path/new.go
  +line one
  +line two
  *** Update File: path/old.go
  *** Move to: path/renamed.go    (optional rename, before any hunk)
  @@
   keep_this_line
  -drop_this
  +add_this
   keep_this_too
  @@
   second_anchor
  -bye
  +hi
  *** End of File                  (optional; pin the previous hunk to file end)
  *** Delete File: path/legacy.go
  *** End Patch

Rules:
- Hunks anchor on context lines (' ' prefix) plus '-' lines that must literally match the file. Provide enough context to make the location unambiguous; matching is in-order, first match wins.
- Pure-add hunks (only '+' lines) only work with *** End of File or at the very top of a file.
- Identity files (SOUL.md, IDENTITY.md, MEMORY.md, AGENTS.md, BOOTSTRAP.md, TOOLS.md, HEARTBEAT.md, USER.md, agent.json) accept Add and Update but NOT Delete or Move.
- Path resolution matches read_file/write_file: workspace-relative paths go to the workspace store, identity-file basenames go to the system store, absolute paths go to disk.`

var applyPatchSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"input": map[string]interface{}{
			"type":        "string",
			"description": "The complete patch envelope from `*** Begin Patch` to `*** End Patch`.",
		},
	},
	"required": []string{"input"},
}

type applyPatchArgs struct {
	Input string `json:"input"`
}

// -----------------------------------------------------------------------------
// Backend helpers — host filesystem mode (mirrors registerFile's routing)
// -----------------------------------------------------------------------------

func (r *Registry) readForPatch(ctx context.Context, path string) (string, error) {
	if r.identityFileBlocked(path) {
		return "", fmt.Errorf("%s", IdentityFileRefusal)
	}
	if r.workspaceStore != nil && r.agentID != "" && r.isWorkspacePath(path) {
		rc, err := r.workspaceStore.Get(ctx, r.agentID, r.projectID, r.sessionID, path)
		if err != nil {
			return "", fmt.Errorf("workspace get: %w", err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			return "", fmt.Errorf("workspace read: %w", err)
		}
		return string(data), nil
	}
	if r.systemFileStore != nil && r.agentID != "" && basenameIsSystemFile(path) {
		name := filepath.Base(filepath.Clean(path))
		if data, err := r.readSystemFileForUser(ctx, r.systemFileUserID(name), name); err == nil {
			return string(data), nil
		}
		if r.systemRoot != "" {
			if data, err := os.ReadFile(filepath.Join(r.systemRoot, name)); err == nil {
				return string(data), nil
			}
		}
		return "", nil
	}
	root := r.rootForPath(path)
	full, err := resolvePathSandboxed(root, r.effectiveSandboxRoot(root), path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) && isSingleSegmentSystemFile(path) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), nil
}

func (r *Registry) writeForPatch(ctx context.Context, path, content string) error {
	if r.identityFileBlocked(path) {
		return fmt.Errorf("%s", IdentityFileRefusal)
	}
	if r.ownerManagedFileWriteBlocked(path) {
		return fmt.Errorf("%s", OwnerManagedFileWriteRefusal)
	}
	if r.workspaceStore != nil && r.agentID != "" && r.isWorkspacePath(path) {
		return r.workspaceStore.Put(ctx, r.agentID, r.projectID, r.sessionID, path,
			strings.NewReader(content), int64(len(content)), "")
	}
	if r.systemFileStore != nil && r.agentID != "" && isSingleSegmentSystemFile(path) {
		name := filepath.Clean(path)
		if err := r.systemFileStore.SaveWorkspaceFile(ctx, r.agentID, r.systemFileUserID(name), name, []byte(content)); err != nil {
			return err
		}
		// Mirror to disk so this pod's in-process readers (context builder,
		// skills loader) see the new content immediately. Same invariant as
		// makeWriteFile.
		if r.systemRoot != "" {
			disk := filepath.Join(r.systemRoot, name)
			_ = os.MkdirAll(filepath.Dir(disk), 0o755)
			_ = os.WriteFile(disk, []byte(content), 0o644)
		}
		return nil
	}
	root := r.rootForPath(path)
	full, err := resolvePathSandboxed(root, r.effectiveSandboxRoot(root), path)
	if err != nil {
		return err
	}
	if isGlobalSkillsPath(full) {
		return errGlobalSkillsDirWrite
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", path, err)
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

func (r *Registry) deleteForPatch(ctx context.Context, path string) error {
	// Identity files refuse Delete: systemFileStore has no Delete API and
	// these files have a fixed slot — clearing them via Delete would
	// corrupt the agent. Use Update File with empty target content
	// instead.
	if isSingleSegmentSystemFile(path) {
		return fmt.Errorf("apply_patch: refusing to delete identity file %q (use Update File with empty content instead)", path)
	}
	if r.workspaceStore != nil && r.agentID != "" && r.isWorkspacePath(path) {
		return r.workspaceStore.Delete(ctx, r.agentID, r.projectID, r.sessionID, path)
	}
	root := r.rootForPath(path)
	full, err := resolvePathSandboxed(root, r.effectiveSandboxRoot(root), path)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil {
		return fmt.Errorf("delete %s: %w", path, err)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Backend helpers — sandbox mode (mirrors registerSandboxedFile's routing)
// -----------------------------------------------------------------------------

func (r *Registry) readForPatchSandbox(ctx context.Context, ex sandbox.Executor, path string) (string, error) {
	if r.systemFileStore != nil && r.agentID != "" && basenameIsSystemFile(path) {
		name := filepath.Base(filepath.Clean(path))
		if data, err := r.readSystemFileForUser(ctx, r.systemFileUserID(name), name); err == nil {
			return string(data), nil
		}
		return "", nil
	}
	if r.workspaceStore != nil && r.agentID != "" && r.isWorkspacePath(path) {
		rc, err := r.workspaceStore.Get(ctx, r.agentID, r.projectID, r.sessionID, path)
		if err == nil {
			defer rc.Close()
			data, readErr := io.ReadAll(rc)
			if readErr == nil {
				return string(data), nil
			}
		}
		// Fall through to executor on store miss / read error.
	}
	return ex.ReadFile(ctx, path)
}

func (r *Registry) writeForPatchSandbox(ctx context.Context, ex sandbox.Executor, path, content string) error {
	if r.ownerManagedFileWriteBlocked(path) {
		return fmt.Errorf("%s", OwnerManagedFileWriteRefusal)
	}
	if r.systemFileStore != nil && r.agentID != "" && isSingleSegmentSystemFile(path) {
		name := filepath.Clean(path)
		return r.systemFileStore.SaveWorkspaceFile(ctx, r.agentID, r.systemFileUserID(name), name, []byte(content))
	}
	if r.workspaceStore != nil && r.agentID != "" && r.isWorkspacePath(path) {
		return r.workspaceStore.Put(ctx, r.agentID, r.projectID, r.sessionID, path,
			strings.NewReader(content), int64(len(content)), "")
	}
	_, err := ex.WriteFile(ctx, path, content)
	return err
}

func (r *Registry) deleteForPatchSandbox(ctx context.Context, ex sandbox.Executor, path string) error {
	if isSingleSegmentSystemFile(path) {
		return fmt.Errorf("apply_patch: refusing to delete identity file %q (use Update File with empty content instead)", path)
	}
	if r.workspaceStore != nil && r.agentID != "" && r.isWorkspacePath(path) {
		return r.workspaceStore.Delete(ctx, r.agentID, r.projectID, r.sessionID, path)
	}
	// Sandbox executor exposes no Delete API; fall back to `rm`. Single-quote
	// the path and escape embedded single quotes so a pathological filename
	// can't inject shell.
	q := "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
	_, err := ex.Exec(ctx, "rm -f -- "+q, 0)
	return err
}

// -----------------------------------------------------------------------------
// Tool registration
// -----------------------------------------------------------------------------

func registerApplyPatch(r *Registry) {
	r.Register("apply_patch", applyPatchDescription, applyPatchSchema, func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args applyPatchArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("apply_patch: parse args: %w", err)
		}
		return runApplyPatch(ctx, args.Input,
			func(ctx context.Context, p string) (string, error) { return r.readForPatch(ctx, p) },
			func(ctx context.Context, p, c string) error { return r.writeForPatch(ctx, p, c) },
			func(ctx context.Context, p string) error { return r.deleteForPatch(ctx, p) },
		)
	})
}

func registerSandboxedApplyPatch(r *Registry, ex sandbox.Executor) {
	r.Register("apply_patch", applyPatchDescription, applyPatchSchema, func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args applyPatchArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("apply_patch: parse args: %w", err)
		}
		out, err := runApplyPatch(ctx, args.Input,
			func(ctx context.Context, p string) (string, error) { return r.readForPatchSandbox(ctx, ex, p) },
			func(ctx context.Context, p, c string) error { return r.writeForPatchSandbox(ctx, ex, p, c) },
			func(ctx context.Context, p string) error { return r.deleteForPatchSandbox(ctx, ex, p) },
		)
		if err != nil {
			return "", err
		}
		return MetaSandboxPrefix + out, nil
	})
}

// runApplyPatch is the storage-agnostic engine. Phase 1: parse and compute
// every file's new content / planned deletes in memory. Phase 2: only after
// all hunks anchor successfully, flush writes (then deletes — Move source
// goes last so the destination is in place before its old slot is freed).
//
// Phase-2 errors leak partial state (one file written, the next failed);
// the agent can re-emit the patch after fixing the underlying cause
// (permissions, disk full, ...). Cross-backend transactional rollback is
// not attempted — it would require snapshotting every touched store.
func runApplyPatch(
	ctx context.Context,
	input string,
	read func(context.Context, string) (string, error),
	write func(context.Context, string, string) error,
	del func(context.Context, string) error,
) (string, error) {
	p, err := parsePatch(input)
	if err != nil {
		return "", err
	}

	type plannedWrite struct{ path, content string }
	var (
		writes  []plannedWrite
		deletes []string
	)

	for _, op := range p.Ops {
		switch op.Type {
		case opAdd:
			if op.Path == "" {
				return "", errors.New("apply_patch: Add File requires a non-empty path")
			}
			writes = append(writes, plannedWrite{op.Path, op.AddBody})

		case opDelete:
			if op.Path == "" {
				return "", errors.New("apply_patch: Delete File requires a non-empty path")
			}
			// Identity files have a fixed slot in the systemFileStore and
			// no Delete API; allowing Delete here would corrupt the agent.
			// Refuse at the engine level so backend del() doesn't have to
			// repeat the rule (defense-in-depth still does in
			// deleteForPatch / deleteForPatchSandbox).
			if isSingleSegmentSystemFile(op.Path) {
				return "", fmt.Errorf("apply_patch: refusing to delete identity file %q (use Update File with empty content instead)", op.Path)
			}
			deletes = append(deletes, op.Path)

		case opUpdate:
			if op.Path == "" {
				return "", errors.New("apply_patch: Update File requires a non-empty path")
			}
			if op.MoveTo != "" && (isSingleSegmentSystemFile(op.Path) || isSingleSegmentSystemFile(op.MoveTo)) {
				return "", fmt.Errorf("apply_patch: refusing to Move identity file %q → %q", op.Path, op.MoveTo)
			}
			old, err := read(ctx, op.Path)
			if err != nil {
				return "", fmt.Errorf("apply_patch: read %s: %w", op.Path, err)
			}
			updated, err := applyHunks(op.Path, old, op.Hunks)
			if err != nil {
				return "", err
			}
			target := op.Path
			if op.MoveTo != "" && op.MoveTo != op.Path {
				target = op.MoveTo
				deletes = append(deletes, op.Path)
			}
			writes = append(writes, plannedWrite{target, updated})
		}
	}

	for _, w := range writes {
		if err := write(ctx, w.path, w.content); err != nil {
			return "", fmt.Errorf("apply_patch: write %s: %w", w.path, err)
		}
	}
	for _, d := range deletes {
		if err := del(ctx, d); err != nil {
			return "", fmt.Errorf("apply_patch: delete %s: %w", d, err)
		}
	}

	var sb strings.Builder
	for _, op := range p.Ops {
		switch op.Type {
		case opAdd:
			fmt.Fprintf(&sb, "A %s\n", op.Path)
		case opDelete:
			fmt.Fprintf(&sb, "D %s\n", op.Path)
		case opUpdate:
			if op.MoveTo != "" && op.MoveTo != op.Path {
				fmt.Fprintf(&sb, "M %s -> %s (%d hunk(s))\n", op.Path, op.MoveTo, len(op.Hunks))
			} else {
				fmt.Fprintf(&sb, "U %s (%d hunk(s))\n", op.Path, len(op.Hunks))
			}
		}
	}
	return sb.String(), nil
}
