package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

// maxAttachmentBytes caps a single attachment regardless of whether it
// arrived as a data URL (in-memory base64) or an HTTPS URL (streamed
// fetch). 25 MB matches Anthropic's per-image envelope and prevents a
// pathological data URL from sinking the gateway.
const maxAttachmentBytes = 25 * 1024 * 1024

// maxAttachmentNameLen caps caller-supplied filenames after sanitization.
// 96 is roughly the longest a filename can be before terminals start
// wrapping in chat bubbles, and well clear of any path-length limits.
const maxAttachmentNameLen = 96

// Attachment is one item the caller wants materialized into /workspace
// for the current turn. URL is required (data URL or http(s) URL); Name
// is optional and, when given, is sanitized and used as the on-disk
// filename so the LLM sees something readable like `quarterly.pdf`
// instead of `image_3jk7l_0.pdf`.
//
// Same-Name semantics:
//   - Within one turn: a second attachment with the same Name is
//     disambiguated as `<stem>-<idx><ext>` (token-spliced if that also
//     collides). No silent loss.
//   - Across turns: re-uploading the same Name overwrites the prior
//     file in /workspace. This matches the "drag the same name onto a
//     folder" mental model and avoids unbounded `notes-1.md`,
//     `notes-2.md`, … buildup. Callers that need to preserve old
//     versions must vary the Name themselves.
type Attachment struct {
	URL  string
	Name string
}

// WriteSessionAttachments materializes user-attached bytes into the
// agent's session workspace so skills (image-tool, file readers, etc.)
// can reach them via /workspace/<filename>. Each URL is one of:
//   - data URL:  "data:image/png;base64,iVBORw..."
//   - HTTPS URL: "https://example.com/report.pdf"
//
// Per-item errors are logged and skipped — a single bad URL must not
// sink the whole turn. Returns the relative filenames in input order,
// omitting any that failed.
//
// Why three writes:
//
//   - Host workspace dir: covers the no-sandbox case (host exec uses this
//     dir as cwd) AND the docker case (a.workspacePath is bind-mounted at
//     /workspace inside the container).
//   - workspace.Store.Put: durable handoff for E2B / multi-pod — the
//     LifecyclePool's hydrate-on-create copies it into /workspace on the
//     next sandbox spin-up.
//   - sandbox executor.WriteFile: covers the E2B mid-session case where
//     the sandbox is already hydrated and won't re-pull from the Store.
//
// Docker doesn't need the third write (bind mount makes host writes show
// up instantly), but calling it is harmless. The host write is also
// harmless for E2B (gateway-local bytes nobody reads).
func (a *Agent) WriteSessionAttachments(ctx context.Context, sessionID, projectID string, atts []Attachment) []string {
	if len(atts) == 0 {
		return nil
	}
	var paths []string
	// Short, base36-ish token derived from the millisecond timestamp.
	// Long enough to avoid collision in any realistic chat cadence (a
	// human would have to upload twice within the same millisecond);
	// short enough that the resulting filename — `image_3jk7l_0.jpg` —
	// reads as a normal user attachment, not a system-generated temp
	// file. The earlier `in_1777972819289_0.jpg` shape made models
	// reach for read_file/`file`/`identify` to "verify" the upload
	// before passing it to a skill.
	token := strconv.FormatInt(time.Now().UnixMilli(), 36)
	if len(token) > 5 {
		token = token[len(token)-5:]
	}
	// Track names assigned in this batch so two attachments with the
	// same caller-provided Name don't clobber each other. Cross-turn
	// collisions are intentionally left to overwrite — re-uploading
	// `notes.md` should replace, not accumulate `notes-1.md` forever.
	used := make(map[string]struct{}, len(atts))
	for i, att := range atts {
		data, ext, err := decodeAttachment(ctx, att.URL)
		if err != nil {
			slog.Warn("attachment decode failed", "agent", a.name, "session", sessionID, "index", i, "error", err)
			continue
		}
		name := buildAttachmentName(att.Name, token, i, ext, used)
		used[name] = struct{}{}

		// 1. Host workspace dir (covers no-sandbox + docker via bind mount)
		if a.workspacePath != "" {
			full := filepath.Join(a.workspacePath, name)
			if mkErr := os.MkdirAll(a.workspacePath, 0o755); mkErr == nil {
				if wErr := os.WriteFile(full, data, 0o644); wErr != nil {
					slog.Warn("attachment host write failed", "agent", a.name, "session", sessionID, "path", full, "error", wErr)
				}
			}
		}

		// 2. Durable store (covers E2B / multi-pod via hydrate-on-create)
		if a.workspaceStore != nil {
			if pErr := a.workspaceStore.Put(ctx, a.agentID, projectID, sessionID, name, strings.NewReader(string(data)), int64(len(data)), contentTypeFromExt(ext)); pErr != nil {
				slog.Warn("attachment store put failed", "agent", a.name, "session", sessionID, "path", name, "error", pErr)
			}
		}

		// 3. Live sandbox (covers E2B mid-session). Best-effort; missing
		// pool / get failure just means the next exec will pull from the
		// store via hydrate-on-create. Only a sandbox that is already
		// running: creating one here made every image message wait for
		// a cold start (tens of seconds on E2B) even when the model just
		// looks at the picture. With the store write above, a sandbox
		// created later hydrates the file anyway.
		if a.sandboxPool != nil && a.attachmentNeedsSandboxWrite(projectID, sessionID) {
			if ex, gErr := a.sandboxPool.Get(ctx, a.name, projectID, sessionID); gErr == nil && ex != nil {
				if _, wErr := ex.WriteFile(ctx, "/workspace/"+name, string(data)); wErr != nil {
					slog.Warn("attachment sandbox write failed", "agent", a.name, "session", sessionID, "path", name, "error", wErr)
				}
			}
		}

		paths = append(paths, name)
	}
	return paths
}

// decodeAttachment turns a data URL or HTTPS URL into raw bytes plus a
// best-effort filename extension (".png", ".jpg", …). Unknown / missing
// MIME maps to ".bin".
func decodeAttachment(ctx context.Context, u string) ([]byte, string, error) {
	if strings.HasPrefix(u, "data:") {
		return decodeDataURL(u)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return nil, "", fmt.Errorf("parse url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, "", fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	httpCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(httpCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxAttachmentBytes {
		return nil, "", fmt.Errorf("attachment exceeds %d bytes", maxAttachmentBytes)
	}
	ext := extFromMIME(resp.Header.Get("Content-Type"))
	if ext == "" {
		ext = filepath.Ext(parsed.Path) // fallback to URL extension
	}
	if ext == "" {
		ext = ".bin"
	}
	return body, ext, nil
}

func decodeDataURL(u string) ([]byte, string, error) {
	comma := strings.IndexByte(u, ',')
	if comma < 0 {
		return nil, "", fmt.Errorf("data url missing comma")
	}
	header := u[5:comma] // strip "data:"
	payload := u[comma+1:]

	var mime string
	isB64 := false
	for _, part := range strings.Split(header, ";") {
		switch {
		case part == "base64":
			isB64 = true
		case part == "":
			// noop — leading "data:," with no MIME is legal
		case mime == "":
			mime = part
		}
	}
	var data []byte
	if isB64 {
		decoded, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return nil, "", fmt.Errorf("base64 decode: %w", err)
		}
		data = decoded
	} else {
		decoded, err := url.QueryUnescape(payload)
		if err != nil {
			return nil, "", fmt.Errorf("urlencoded decode: %w", err)
		}
		data = []byte(decoded)
	}
	if len(data) > maxAttachmentBytes {
		return nil, "", fmt.Errorf("attachment exceeds %d bytes", maxAttachmentBytes)
	}
	ext := extFromMIME(mime)
	if ext == "" {
		ext = ".bin"
	}
	return data, ext, nil
}

func extFromMIME(ct string) string {
	// Strip params: "image/png; charset=…" → "image/png"
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	switch strings.TrimSpace(strings.ToLower(ct)) {
	// Images
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/heic":
		return ".heic"
	case "image/svg+xml":
		return ".svg"
	// Documents — landing as the real extension matters even for models
	// that can't natively read the bytes, because the LLM picks its
	// tool based on the extension. `.bin` makes it reach for
	// file/identify; `.pdf` makes it reach for the right reader.
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/markdown", "text/x-markdown":
		return ".md"
	case "text/csv":
		return ".csv"
	case "text/html":
		return ".html"
	case "application/json":
		return ".json"
	case "application/xml", "text/xml":
		return ".xml"
	case "application/zip":
		return ".zip"
	}
	return ""
}

// buildAttachmentName turns the caller's optional Name into a safe
// on-disk filename. If Name is empty (or sanitizes to empty), we fall
// back to the historical `image_<token>_<i><ext>` shape so existing
// callers see no behavior change. If Name is present, we keep it
// (sanitized), append the MIME-derived ext when the user omitted one,
// and disambiguate within-batch duplicates by suffixing `-<i>`.
func buildAttachmentName(raw, token string, idx int, ext string, used map[string]struct{}) string {
	clean := sanitizeAttachmentName(raw)
	if clean == "" {
		return fmt.Sprintf("image_%s_%d%s", token, idx, ext)
	}
	if path.Ext(clean) == "" && ext != "" {
		clean += ext
	}
	if _, dup := used[clean]; !dup {
		return clean
	}
	stem := strings.TrimSuffix(clean, path.Ext(clean))
	tail := path.Ext(clean)
	// First disambiguation: `<stem>-<idx><ext>`. Usually unique, but
	// can collide if the user explicitly named an earlier attachment
	// `report-2.pdf` and a later one with the same Name happens to
	// land at idx=2.
	candidate := fmt.Sprintf("%s-%d%s", stem, idx, tail)
	if _, dup := used[candidate]; !dup {
		return candidate
	}
	// Final fallback: splice in the per-turn token. token is unique
	// per WriteSessionAttachments call, so `<stem>-<token>-<idx><ext>`
	// is collision-free within the batch.
	return fmt.Sprintf("%s-%s-%d%s", stem, token, idx, tail)
}

// sanitizeAttachmentName strips path separators, parent-dir tokens,
// control characters, and leading dots from a caller-supplied filename.
// Returns "" if nothing usable remains so the caller can fall back.
// Uses path.Base (not filepath.Base) so Windows-style paths from the
// browser are handled identically on a Linux gateway.
func sanitizeAttachmentName(raw string) string {
	if raw == "" {
		return ""
	}
	// Normalize Windows separators to / so path.Base reliably extracts
	// the last component regardless of which side of the wire we run on.
	raw = strings.ReplaceAll(raw, `\`, "/")
	raw = path.Base(raw)
	// `path.Base("..") == ".."`; reject explicitly.
	if raw == "." || raw == ".." {
		return ""
	}
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r < 0x20, r == 0x7f:
			// control char — drop
		case r == '/', r == '\\', r == ':', r == 0:
			// path separator / drive prefix / NUL — drop
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	out = strings.TrimLeft(out, ".") // hidden-dotfile prefix is rarely intended
	if len(out) > maxAttachmentNameLen {
		// Truncate from the stem so we preserve the extension. Byte-
		// slicing on UTF-8 would chop multi-byte runes (CJK filenames
		// are 3 bytes/char) and yield invalid UTF-8 on disk, so back
		// off to the nearest rune boundary at or below the byte budget.
		ext := path.Ext(out)
		stem := strings.TrimSuffix(out, ext)
		keep := maxAttachmentNameLen - len(ext)
		if keep < 1 {
			keep = 1
		}
		if len(stem) > keep {
			for keep > 0 && !utf8.RuneStart(stem[keep]) {
				keep--
			}
			stem = stem[:keep]
		}
		out = stem + ext
	}
	return out
}

func contentTypeFromExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".heic":
		return "image/heic"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".txt":
		return "text/plain"
	case ".md":
		return "text/markdown"
	case ".csv":
		return "text/csv"
	case ".html":
		return "text/html"
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	case ".zip":
		return "application/zip"
	}
	return ""
}

// attachmentNeedsSandboxWrite reports whether WriteSessionAttachments
// should also write into the sandbox: when it is already live, or when
// there is no workspace store to hydrate a later sandbox from (or the
// pool can't say whether one is live).
func (a *Agent) attachmentNeedsSandboxWrite(projectID, sessionID string) bool {
	if a.workspaceStore == nil {
		return true
	}
	lr, ok := a.sandboxPool.(sandbox.LivenessReporter)
	if !ok {
		return true
	}
	return lr.Live(a.name, projectID, sessionID)
}
