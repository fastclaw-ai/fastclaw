package sandbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// E2B API: https://e2b.dev/docs
// Sandbox creation: POST https://api.e2b.dev/sandboxes
// Command execution: Connect protocol via envd on the sandbox

const (
	e2bDefaultAPIURL = "https://api.e2b.dev"
	e2bDefaultDomain = "e2b.app"
	e2bEnvdPort      = "49983"
)

type E2BOption func(*e2bOptions)

type e2bOptions struct {
	apiURL string
	domain string
}

func defaultE2BOptions() e2bOptions {
	return e2bOptions{
		apiURL: e2bDefaultAPIURL,
		domain: e2bDefaultDomain,
	}
}

// WithAPIURL overrides the E2B control-plane base URL (default
// https://api.e2b.dev). Used for self-hosted E2B infrastructure or
// E2B-compatible endpoints such as Aliyun FC cloud sandbox.
func WithAPIURL(u string) E2BOption {
	return func(o *e2bOptions) { o.apiURL = u }
}

// WithDomain overrides the E2B data-plane domain (default e2b.app) used
// to construct per-sandbox envd and exposed-port URLs.
func WithDomain(d string) E2BOption {
	return func(o *e2bOptions) { o.domain = d }
}

// E2BExecutor implements Executor using E2B hosted sandboxes.
type E2BExecutor struct {
	apiKey      string
	sandboxID   string
	accessToken string
	apiURL      string
	domain      string
	client      *http.Client
	template    string        // remembered for recreate() so the new sandbox uses the same template
	timeout     time.Duration // remembered for recreate()
	// hydrate sources — set by the pool after creation so recreate()
	// can rebuild /skills + /workspace without reaching back into the
	// pool. Workspace store is optional; skill dirs may be empty.
	skillDirs []string
	workspace workspace.Store
	agentID   string
	projectID string
	sessionID string
}

func newE2BExecutor(ctx context.Context, apiKey, template string, timeout time.Duration, opts ...E2BOption) (*E2BExecutor, error) {
	o := defaultE2BOptions()
	for _, opt := range opts {
		opt(&o)
	}
	if template == "" {
		template = "base"
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}

	// No global Client.Timeout: it covers the entire round-trip
	// including streaming the body, which silently cut long execs
	// (image generation, etc.) at 60s and made tools return empty
	// output with no error. We rely on per-request context.WithTimeout
	// at the call site instead — execOnce derives ctx from the user-
	// supplied tool timeout, and create-sandbox below uses an
	// explicit short ctx.
	client := &http.Client{}

	// Field name is `templateID` (camelCase) — verified by server's
	// validation error: `Error at "/templateID": property "templateID"
	// is missing` when the field was renamed to snake_case. The
	// snake_case form shows up in some SDK source code but the
	// production REST API rejects it.
	body, _ := json.Marshal(map[string]interface{}{
		"templateID": template,
		"timeout":    int(timeout.Seconds()),
	})
	// Bound the create-sandbox call to 60s — the call itself usually
	// completes in 1–2s; if it's hanging past that there's a control-
	// plane problem and we'd rather surface a clear timeout than wait
	// indefinitely on a request that inherits no deadline from ctx.
	createCtx, cancelCreate := context.WithTimeout(ctx, 60*time.Second)
	defer cancelCreate()
	req, err := http.NewRequestWithContext(createCtx, "POST", o.apiURL+"/sandboxes", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("e2b create sandbox: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("e2b create sandbox: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		SandboxID       string `json:"sandboxID"`
		EnvdAccessToken string `json:"envdAccessToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("e2b parse response: %w", err)
	}

	slog.Info("e2b sandbox created", "sandboxID", result.SandboxID, "template", template)

	return &E2BExecutor{
		apiKey:      apiKey,
		sandboxID:   result.SandboxID,
		accessToken: result.EnvdAccessToken,
		apiURL:      o.apiURL,
		domain:      o.domain,
		client:      client,
		template:    template,
		timeout:     timeout,
	}, nil
}

func (e *E2BExecutor) envdURL() string {
	return fmt.Sprintf("https://%s-%s.%s", e2bEnvdPort, e.sandboxID, e.domain)
}

// recreate destroys the current sandbox and creates a new one. The same
// template / timeout the executor was originally built with are reused —
// hardcoding "base" here would silently demote a custom-template sandbox
// once it idled out. The full hydrate (skills + workspace) is replayed so
// /skills/<name>/ and /workspace/ stay populated across recreations.
func (e *E2BExecutor) recreate(ctx context.Context) error {
	slog.Info("e2b sandbox expired, recreating", "oldSandboxID", e.sandboxID)
	newEx, err := newE2BExecutor(ctx, e.apiKey, e.template, e.timeout,
		WithAPIURL(e.apiURL), WithDomain(e.domain))
	if err != nil {
		return err
	}
	e.sandboxID = newEx.sandboxID
	e.accessToken = newEx.accessToken
	// Hydrate is mandatory for the same reason it is in Get() — it's
	// the only step that makes /workspace writable to the exec user.
	// If we just warn-log a failure here, the recreated sandbox is
	// silently broken: agent calls succeed but every /workspace write
	// fails with Permission denied, and the bytes get stranded in
	// /tmp where they evaporate at the next eviction.
	if err := e.Hydrate(ctx); err != nil {
		return fmt.Errorf("hydrate after recreate (sandboxID=%s): %w", e.sandboxID, err)
	}
	if err := verifyWorkspaceWritable(ctx, e); err != nil {
		return fmt.Errorf("recreated sandbox unusable (sandboxID=%s): %w", e.sandboxID, err)
	}
	return nil
}

// SetHydrationSources records the inputs Hydrate() should pull from on
// the next call. Called by the pool right after sandbox creation; the
// executor then carries them so recreate() can replay everything without
// asking the pool. Pass nil/empty for any source you don't have.
func (e *E2BExecutor) SetHydrationSources(skillDirs []string, ws workspace.Store, agentID, projectID, sessionID string) {
	e.skillDirs = append(e.skillDirs[:0], skillDirs...)
	e.workspace = ws
	e.agentID = agentID
	e.projectID = projectID
	e.sessionID = sessionID
}

// Hydrate populates the sandbox with everything the agent's tools expect
// to find on disk:
//   - /skills/<name>/...   from each configured skill dir (per-agent +
//     global, first-wins precedence to match the docker bind-mount layer)
//   - /workspace/...       from the agent's workspace.Store (so files
//     written via write_file in past sessions survive sandbox restarts,
//     same contract as the existing per-file hydrateWorkspace)
//
// Implementation: pack everything into one tar.gz, upload it via envd's
// /files multipart endpoint (same path writeFileOnce uses — known good),
// then run a tiny `bash -c` to extract+chown. Earlier versions inlined
// the base64'd tar inside the `bash -c` arg; that worked for trivially
// small bundles but empirically truncated the Connect-protocol response
// once the encoded payload climbed past ~80KB-of-base64 (8 bundled
// skills was already 103KB and tripped it). Truncation surfaced as an
// envd End-frame with `exited=false` and no exit-status — the old code
// treated that as success because exitCode was still 0, so Hydrate
// looked like it ran while the chown step had actually been cut off
// mid-flight. The fix moves the bulk transfer off the exec channel
// entirely so the script stays small and constant-sized regardless of
// bundle size.
func (e *E2BExecutor) Hydrate(ctx context.Context) error {
	bundle := newTarBundle()

	skillCount := 0
	skillFileCount := 0
	seen := make(map[string]bool) // per-skill: first dir wins
	for _, dir := range e.skillDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if seen[name] {
				continue
			}
			seen[name] = true
			n, err := bundle.addLocalDir(filepath.Join(dir, name), "skills/"+name)
			if err != nil {
				slog.Warn("e2b hydrate: skill tar", "skill", name, "error", err)
				continue
			}
			skillCount++
			skillFileCount += n
		}
	}

	workspaceCount := 0
	if e.workspace != nil {
		// For project chats, hydrate the whole project (List with
		// session=""), so the chat sees sibling chats' files at
		// /workspace/<other-sid>/... — same visibility docker gets
		// from mounting projects/<pid>/ as the bind root. Loose chats
		// stay scoped to their own session subtree.
		listProject := e.projectID
		listSession := e.sessionID
		if e.projectID != "" {
			listSession = ""
		}
		objs, err := e.workspace.List(ctx, e.agentID, listProject, listSession)
		if err != nil {
			slog.Warn("e2b hydrate: workspace list", "agent", e.agentID, "project", e.projectID, "session", e.sessionID, "error", err)
		} else {
			for _, obj := range objs {
				rc, err := e.workspace.Get(ctx, e.agentID, listProject, listSession, obj.Path)
				if err != nil {
					slog.Warn("e2b hydrate: workspace get", "path", obj.Path, "error", err)
					continue
				}
				data, rerr := io.ReadAll(rc)
				rc.Close()
				if rerr != nil {
					slog.Warn("e2b hydrate: workspace read", "path", obj.Path, "error", rerr)
					continue
				}
				rel := strings.TrimPrefix(obj.Path, "/")
				if err := bundle.addBytes("workspace/"+rel, data, 0o644, obj.ModTime); err != nil {
					slog.Warn("e2b hydrate: workspace tar", "path", obj.Path, "error", err)
					continue
				}
				workspaceCount++
			}
		}
	}

	if err := bundle.close(); err != nil {
		return fmt.Errorf("close tar: %w", err)
	}

	// Why every word here matters:
	// - We ALWAYS run the mkdir+chown step, even when there are no
	//   files to push. /workspace and /skills must exist and be
	//   writable by `user` regardless — image-tool / write_file /
	//   anything that writes there fails with ENOENT or EACCES if the
	//   dirs are missing. This was the failure mode on a fresh session
	//   with empty workspace: no files → previous code returned early
	//   → /workspace never created → "mkdir: Permission denied" when
	//   the LLM tried to make it itself as the non-root `user`.
	// - `sudo`: E2B's "base" template runs as `user`, who has no
	//   write access to /. The default user has passwordless sudo per
	//   e2b's published Dockerfile; custom templates that strip sudo
	//   either need to keep it or pre-create /skills + /workspace
	//   chowned to user.
	// - The bundle (when any files exist) is now uploaded via the
	//   /files endpoint BEFORE this exec runs — see uploadBytes above
	//   and the size-related comment on Hydrate itself. The script
	//   below only ever references /tmp/fc-hydrate.tar.gz as a path,
	//   so the bash -c arg stays small regardless of how large the
	//   bundle gets.
	// - chown after extract: tar-as-root lands files root-owned, so
	//   re-chown after extract; agent's subsequent writes run as user.
	if bundle.fileCount > 0 {
		// Upload as `user`-owned to /tmp; tar still runs under sudo so
		// it can land /skills/* and /workspace/* at the filesystem root.
		if err := e.uploadBytes(ctx, "/tmp/fc-hydrate.tar.gz", bundle.gz.Bytes()); err != nil {
			return fmt.Errorf("hydrate upload tar: %w", err)
		}
	}
	cmdParts := []string{
		"set -e",
		"sudo mkdir -p /skills /workspace",
		"sudo chown user:user /skills /workspace",
	}
	if bundle.fileCount > 0 {
		cmdParts = append(cmdParts,
			"sudo tar -xzf /tmp/fc-hydrate.tar.gz -C /",
			"sudo chown -R user:user /skills /workspace",
			"rm -f /tmp/fc-hydrate.tar.gz",
		)
	}
	cmd := strings.Join(cmdParts, "; ")
	out, err := e.execOnce(ctx, cmd, 60*time.Second)
	if err != nil {
		slog.Warn("e2b hydrate extract failed", "sandboxID", e.sandboxID, "error", err, "out", out)
		return fmt.Errorf("hydrate sandbox dirs: %w (output: %s)", err, out)
	}
	slog.Info("e2b sandbox hydrated",
		"sandboxID", e.sandboxID,
		"skills", skillCount,
		"skillFiles", skillFileCount,
		"workspaceFiles", workspaceCount,
		"tarBytes", bundle.gz.Len())
	return nil
}

// tarBundle is a small helper around archive/tar + gzip so the Hydrate
// path doesn't have to repeat the writer-close dance. All paths in the
// bundle are sandbox-relative (no leading slash); callers pick the
// extraction root.
type tarBundle struct {
	gz        bytes.Buffer
	gw        *gzip.Writer
	tw        *tar.Writer
	fileCount int
}

func newTarBundle() *tarBundle {
	b := &tarBundle{}
	b.gw = gzip.NewWriter(&b.gz)
	b.tw = tar.NewWriter(b.gw)
	return b
}

// addBytes adds an in-memory file to the bundle. tar -xz auto-creates
// parent dirs from the entry path, so we don't need explicit dir
// entries — verified by a roundtrip test on the host.
func (b *tarBundle) addBytes(name string, data []byte, mode int64, modTime time.Time) error {
	if modTime.IsZero() {
		modTime = time.Now()
	}
	if err := b.tw.WriteHeader(&tar.Header{
		Name:     strings.TrimPrefix(name, "/"),
		Mode:     mode,
		Size:     int64(len(data)),
		Typeflag: tar.TypeReg,
		ModTime:  modTime,
	}); err != nil {
		return err
	}
	if _, err := b.tw.Write(data); err != nil {
		return err
	}
	b.fileCount++
	return nil
}

// addLocalDir walks a host directory and adds every regular file under
// it to the bundle, rooted at sandboxPrefix. Symlinks / sockets / etc.
// are skipped — a skill bundle should be plain files.
func (b *tarBundle) addLocalDir(localRoot, sandboxPrefix string) (int, error) {
	count := 0
	err := filepath.Walk(localRoot, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(localRoot, p)
		if err != nil {
			return err
		}
		// Never ship a skill's top-level SKILL.md into the sandbox — it's
		// the agent's IP, the model already has it via load_skill, and the
		// sandbox only needs the skill's scripts/resources to run. Keeping
		// it out closes the `cat /skills/<name>/SKILL.md` exfil path.
		if rel == "SKILL.md" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		name := sandboxPrefix + "/" + filepath.ToSlash(rel)
		if err := b.addBytes(name, data, int64(info.Mode().Perm()), info.ModTime()); err != nil {
			return err
		}
		count++
		return nil
	})
	return count, err
}

func (b *tarBundle) close() error {
	if err := b.tw.Close(); err != nil {
		return err
	}
	return b.gw.Close()
}

// isSandboxGone checks if the error indicates the sandbox was destroyed.
func isSandboxGone(statusCode int) bool {
	return statusCode == 502 || statusCode == 404
}

// connectEnvelope wraps JSON payload in Connect protocol envelope framing.
// Format: [1 byte flags][4 bytes big-endian length][payload]
func connectEnvelope(payload []byte) []byte {
	buf := make([]byte, 5+len(payload))
	buf[0] = 0 // flags: no compression, not end of stream
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	copy(buf[5:], payload)
	return buf
}

// parseConnectStream reads Connect protocol streaming response.
// Each frame: [1 byte flags][4 bytes length][payload]
func parseConnectStream(data []byte) []json.RawMessage {
	var messages []json.RawMessage
	for len(data) >= 5 {
		flags := data[0]
		length := binary.BigEndian.Uint32(data[1:5])
		data = data[5:]
		if uint32(len(data)) < length {
			break
		}
		payload := data[:length]
		data = data[length:]

		// flags & 0x02 = end_stream (trailer), skip it
		if flags&0x02 != 0 {
			continue
		}
		messages = append(messages, json.RawMessage(payload))
	}
	return messages
}

func (e *E2BExecutor) Exec(ctx context.Context, command string, timeout time.Duration) (string, error) {
	// Match Docker's behavior: default cwd is /workspace, not the
	// invoking user's $HOME. Without this, relative-path writes from
	// agent commands (e.g. `camoufox-cli screenshot out.png`) land in
	// /home/user/ and never make it to the host-visible workspace.
	// Hydrate creates /workspace before any agent Exec runs, and
	// recreate() re-hydrates after a sandbox replacement, so `cd` is
	// guaranteed to succeed here.
	wrapped := "cd /workspace && " + command
	result, err := e.execOnce(ctx, wrapped, timeout)
	if err != nil && strings.Contains(err.Error(), "HTTP 502") || strings.Contains(fmt.Sprint(err), "HTTP 404") {
		if rerr := e.recreate(ctx); rerr != nil {
			return "", fmt.Errorf("sandbox recreate failed: %w (original: %v)", rerr, err)
		}
		return e.execOnce(ctx, wrapped, timeout)
	}
	return result, err
}

func (e *E2BExecutor) execOnce(ctx context.Context, command string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"process": map[string]interface{}{
			"cmd":  "/bin/bash",
			"args": []string{"-c", command},
		},
	})

	enveloped := connectEnvelope(payload)

	// Per-request deadline: timeout (the user-supplied tool budget) plus
	// a 30s slack so the server has room to flush trailing frames before
	// our side gives up. Critical because the underlying http.Client now
	// has no global timeout — without this the request would hang
	// forever if envd died mid-stream.
	execCtx, cancelExec := context.WithTimeout(ctx, timeout+30*time.Second)
	defer cancelExec()

	reqURL := e.envdURL() + "/process.Process/Start"
	req, err := http.NewRequestWithContext(execCtx, "POST", reqURL, bytes.NewReader(enveloped))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/connect+json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Connect-Timeout-Ms", fmt.Sprintf("%d", int(timeout.Milliseconds())))
	if e.accessToken != "" {
		req.Header.Set("X-Access-Token", e.accessToken)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("e2b exec: %w", err)
	}
	defer resp.Body.Close()

	// Don't drop ReadAll's error — when the connection is severed
	// mid-stream (e.g. our deadline hit before the process finished
	// streaming back its output), this is the only signal we have that
	// the bytes we got are incomplete. Previously we silently kept the
	// partial body, the parser saw zero complete frames, and the tool
	// returned empty stdout — exactly the symptom we just hit with
	// long-running exec calls.
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return string(body), fmt.Errorf("e2b exec body read: %w (got %d bytes)", readErr, len(body))
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("e2b exec HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Parse Connect streaming response frames
	frames := parseConnectStream(body)

	var stdout, stderr strings.Builder
	exitCode := 0
	exited := false

	for _, frame := range frames {
		// E2B response format: {"event":{"data":{"stdout":"base64..."}}}
		var msg struct {
			Event struct {
				Start *struct {
					Pid int `json:"pid"`
				} `json:"start,omitempty"`
				Data *struct {
					Stdout string `json:"stdout,omitempty"` // base64 encoded
					Stderr string `json:"stderr,omitempty"` // base64 encoded
				} `json:"data,omitempty"`
				End *struct {
					Exited bool   `json:"exited"`
					Status string `json:"status"` // "exit status 0"
				} `json:"end,omitempty"`
			} `json:"event"`
		}
		if json.Unmarshal(frame, &msg) != nil {
			continue
		}
		if msg.Event.Data != nil {
			if msg.Event.Data.Stdout != "" {
				if decoded, err := base64.StdEncoding.DecodeString(msg.Event.Data.Stdout); err == nil {
					stdout.Write(decoded)
				}
			}
			if msg.Event.Data.Stderr != "" {
				if decoded, err := base64.StdEncoding.DecodeString(msg.Event.Data.Stderr); err == nil {
					stderr.Write(decoded)
				}
			}
		}
		if msg.Event.End != nil {
			exited = msg.Event.End.Exited
			// Parse "exit status N" to get exit code
			if strings.HasPrefix(msg.Event.End.Status, "exit status ") {
				fmt.Sscanf(msg.Event.End.Status, "exit status %d", &exitCode)
			}
		}
	}

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}
	output = strings.TrimSpace(output)

	slog.Info("e2b exec completed", "sandboxID", e.sandboxID, "exitCode", exitCode, "exited", exited, "outputLen", len(output), "frames", len(frames), "bodyBytes", len(body))

	// Reject a stream that didn't deliver a proper "End/exited=true" trailer.
	// Why this matters: when the request payload pushes envd past some
	// internal buffer (empirically >~80KB-of-base64 in a single bash -c arg
	// triggers it), the response comes back with frames but no final exit
	// status. exitCode stays at its zero default and we'd otherwise return
	// nil error — which is exactly the silent-failure mode that left Hydrate
	// looking successful while the chown step hadn't actually run, then
	// verifyWorkspaceWritable reported "/workspace probe: Permission denied"
	// with no clue why the chown didn't take.
	if !exited {
		if output == "" {
			output = "(no output — response stream truncated before exit-status trailer)"
		}
		return output, fmt.Errorf("e2b exec did not exit cleanly (frames=%d, bodyBytes=%d): %s", len(frames), len(body), output)
	}

	if exitCode != 0 {
		if output == "" {
			output = fmt.Sprintf("Process exited with code %d", exitCode)
		}
		return output, fmt.Errorf("exit code %d", exitCode)
	}
	return output, nil
}

func (e *E2BExecutor) ReadFile(ctx context.Context, path string) (string, error) {
	result, err := e.readFileOnce(ctx, path)
	if err != nil && (strings.Contains(err.Error(), "HTTP 502") || strings.Contains(err.Error(), "HTTP 404")) {
		if rerr := e.recreate(ctx); rerr != nil {
			return "", rerr
		}
		return e.readFileOnce(ctx, path)
	}
	return result, err
}

func (e *E2BExecutor) readFileOnce(ctx context.Context, path string) (string, error) {
	reqURL := fmt.Sprintf("%s/files?path=%s&username=user", e.envdURL(), url.QueryEscape(path))
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return "", err
	}
	if e.accessToken != "" {
		req.Header.Set("X-Access-Token", e.accessToken)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("e2b read: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("e2b read HTTP %d: %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}

func (e *E2BExecutor) WriteFile(ctx context.Context, path, content string) (string, error) {
	result, err := e.writeFileOnce(ctx, path, content)
	if err != nil && (strings.Contains(err.Error(), "HTTP 502") || strings.Contains(err.Error(), "HTTP 404")) {
		if rerr := e.recreate(ctx); rerr != nil {
			return "", rerr
		}
		return e.writeFileOnce(ctx, path, content)
	}
	return result, err
}

func (e *E2BExecutor) writeFileOnce(ctx context.Context, filePath, content string) (string, error) {
	if err := e.uploadBytes(ctx, filePath, []byte(content)); err != nil {
		return "", err
	}
	return fmt.Sprintf("Wrote %d bytes to %s", len(content), filePath), nil
}

// uploadBytes POSTs `data` to envd's /files multipart endpoint at
// `sandboxPath`, owned by the `user` account exec runs as. Pulled out of
// writeFileOnce so Hydrate can ship its tar bundle through the same
// large-payload-safe path instead of inlining a base64 blob inside a
// `bash -c` arg (the latter empirically truncates the Connect response
// stream at ~80KB and leaves the sandbox half-hydrated; see the comment
// on the Hydrate caller).
func (e *E2BExecutor) uploadBytes(ctx context.Context, sandboxPath string, data []byte) error {
	// E2B envd's POST /files expects multipart/form-data with a `file`
	// field, NOT a raw octet-stream body. The earlier raw-body version
	// returned 200 OK but silently dropped the upload, leaving the file
	// non-existent inside the sandbox — caught when uploaded skills
	// failed with "No such file or directory" at exec time.
	reqURL := fmt.Sprintf("%s/files?path=%s&username=user",
		e.envdURL(), url.QueryEscape(sandboxPath))

	// envd: the destination path comes from the `path` query param;
	// the multipart `filename` is just metadata, so basename is fine.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", path.Base(sandboxPath))
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if e.accessToken != "" {
		req.Header.Set("X-Access-Token", e.accessToken)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("e2b upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("e2b upload HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (e *E2BExecutor) ListDir(ctx context.Context, path string) (string, error) {
	// Use exec to list directory since the files API doesn't have a list endpoint
	return e.Exec(ctx, fmt.Sprintf("ls -la %s", path), 10*time.Second)
}

// IsRemoteWorkspace marks this executor as cloud-hosted so the
// LifecyclePool runs syncSnapshot after every exec instead of only on
// idle eviction. See sandbox.RemoteWorkspace.
func (e *E2BExecutor) IsRemoteWorkspace() {}

// ExposePort implements PortExposer. E2B serves every sandbox port at
// https://<port>-<sandboxID>.e2b.app with no publish step (same scheme
// envdURL uses for the control port), so the dev server bound to 0.0.0.0
// is reachable the moment it listens.
func (e *E2BExecutor) ExposePort(_ context.Context, port int) (string, error) {
	if e.sandboxID == "" {
		return "", fmt.Errorf("e2b: sandbox not created")
	}
	return fmt.Sprintf("https://%d-%s.%s", port, e.sandboxID, e.domain), nil
}

// ProvisionDir implements TemplateProvisioner: tar localDir (skipping the
// heavy, host-specific trees the scaffold reinstalls anyway), upload it
// over the same /files channel Hydrate uses, and extract it into destDir
// inside the sandbox. This seeds a coding template into an E2B sandbox
// that has no host bind mount to share it through.
func (e *E2BExecutor) ProvisionDir(ctx context.Context, localDir, destDir string) error {
	root := filepath.Clean(localDir)
	skip := map[string]bool{"node_modules": true, ".git": true, ".output": true, "dist": true, ".next": true}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	walkErr := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		top := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		if skip[top] {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Symlinks (and other irregular files) can't be faithfully tarred
		// without resolving targets; skip them — templates are plain trees.
		if !fi.IsDir() && !fi.Mode().IsRegular() {
			return nil
		}
		hdr, herr := tar.FileInfoHeader(fi, "")
		if herr != nil {
			return herr
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			f, oerr := os.Open(p)
			if oerr != nil {
				return oerr
			}
			_, cerr := io.Copy(tw, f)
			f.Close()
			if cerr != nil {
				return cerr
			}
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	const tmp = "/tmp/fc-template.tar.gz"
	if err := e.uploadBytes(ctx, tmp, buf.Bytes()); err != nil {
		return fmt.Errorf("upload template: %w", err)
	}
	cmd := fmt.Sprintf("mkdir -p %q && tar -C %q -xzf %s && rm -f %s", destDir, destDir, tmp, tmp)
	if out, err := e.Exec(ctx, cmd, 3*time.Minute); err != nil {
		return fmt.Errorf("extract template: %w: %s", err, out)
	}
	return nil
}

// SnapshotWorkspace tars /workspace and ships the bytes back as base64 over
// stdout. This is the inverse of Hydrate's tar+base64 push — used by the
// LifecyclePool to flush sandbox-side files back to the durable
// workspace.Store after every successful exec, so files that the skill
// wrote inside the sandbox (image-tool's /workspace/gen_xxx.webp etc.)
// end up reachable from the host's UI / signed URL paths.
//
// Returns map of /workspace-relative path → contents. Skips silently
// when /workspace is empty or doesn't exist.
func (e *E2BExecutor) SnapshotWorkspace(ctx context.Context) (map[string][]byte, error) {
	// `2>/dev/null` swallows the "tar: ./: directory not found" noise
	// when /workspace doesn't exist yet; we still want to proceed with
	// an empty result. base64 -w0 keeps output on a single line so
	// envd's frame parser doesn't fight whitespace folding. Falls back
	// to the empty tar if /workspace is missing entirely.
	cmd := "if [ -d /workspace ]; then " +
		"tar -czf - -C /workspace . 2>/dev/null | base64 -w0; " +
		"fi"
	out, err := e.execOnce(ctx, cmd, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("snapshot workspace exec: %w (output: %s)", err, out)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	gz, err := base64.StdEncoding.DecodeString(out)
	if err != nil {
		return nil, fmt.Errorf("snapshot workspace decode: %w", err)
	}
	gr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("snapshot workspace gunzip: %w", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	out2 := make(map[string][]byte)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out2, fmt.Errorf("snapshot workspace tar read: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// Tar names start with "./" because we tarred `.` from inside
		// /workspace; strip it so callers see the same agent-relative
		// path layout the workspace.Store uses.
		name := strings.TrimPrefix(hdr.Name, "./")
		name = strings.TrimPrefix(name, "/")
		if name == "" {
			continue
		}
		// Skip macOS resource forks (`._foo`) in case anyone ever runs
		// this against a BSD-tar template — Linux/E2B's GNU tar
		// doesn't emit these, but they'd otherwise pollute the store.
		base := name
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		if strings.HasPrefix(base, "._") {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return out2, fmt.Errorf("snapshot workspace read entry %s: %w", name, err)
		}
		out2[name] = data
	}
	return out2, nil
}

// verifyWorkspaceWritable runs a one-shot probe against /workspace as
// the same `user` account agent exec runs under. It catches the
// silent-failure mode where Hydrate appeared to succeed but the
// underlying chown didn't actually take — empirically observed when
// the e2b template ships /workspace owned by root and the chown step
// is skipped or sudoless. The probe writes a single byte and removes
// it; the touch+rm round-trip is the same operation pattern the agent
// uses on its first /workspace write, so anything that would fail
// there fails here too.
func verifyWorkspaceWritable(ctx context.Context, ex *E2BExecutor) error {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	const probeCmd = `touch /workspace/.fc-health && rm -f /workspace/.fc-health && echo ok`
	out, err := ex.execOnce(probeCtx, probeCmd, 10*time.Second)
	if err != nil {
		return fmt.Errorf("/workspace probe: %w (out=%s)", err, strings.TrimSpace(out))
	}
	if !strings.Contains(out, "ok") {
		return fmt.Errorf("/workspace not writable as exec user (out=%s)", strings.TrimSpace(out))
	}
	return nil
}

func (e *E2BExecutor) Close() error {
	req, _ := http.NewRequest("DELETE",
		fmt.Sprintf("%s/sandboxes/%s", e.apiURL, e.sandboxID), nil)
	req.Header.Set("X-API-Key", e.apiKey)
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	slog.Info("e2b sandbox closed", "sandboxID", e.sandboxID)
	return nil
}

// Backend returns "e2b" — used by the per-exec log line so operators can
// confirm at a glance which provider handled a given tool call.
func (e *E2BExecutor) Backend() string { return "e2b" }

// Backend on the pool mirrors E2BExecutor.Backend so the LifecyclePool can
// surface the provider identity without resolving a lazy executor.
func (p *E2BExecutorPool) Backend() string { return "e2b" }

// E2BExecutorPool manages per-user E2B sandboxes.
type E2BExecutorPool struct {
	mu        sync.Mutex
	executors map[string]*E2BExecutor
	apiKey    string
	template  string
	timeout   time.Duration
	home      string          // workspace root used to resolve per-agent skill dirs
	workspace workspace.Store // optional — when set, /workspace is hydrated alongside /skills
	opts      []E2BOption
}

// NewE2BExecutorPool — `home` is the FASTCLAW_HOME the docker backend
// would have used for `-v` mounts; the pool uses it to resolve which
// skill dirs to push into each fresh sandbox. opts is optional — pass
// WithAPIURL / WithDomain to target a self-hosted or E2B-compatible
// endpoint; with no opts, the pool targets the official E2B cloud.
func NewE2BExecutorPool(apiKey, template, home string, timeout time.Duration, opts ...E2BOption) *E2BExecutorPool {
	return &E2BExecutorPool{
		executors: make(map[string]*E2BExecutor),
		apiKey:    apiKey,
		template:  template,
		timeout:   timeout,
		home:      home,
		opts:      opts,
	}
}

// SetWorkspace plugs in the workspace.Store whose contents should be
// mirrored to /workspace inside every fresh sandbox. Optional — when
// nil, only /skills is hydrated. Called by the gateway after
// LifecyclePool's own workspace is wired so the inner pool and the
// lifecycle layer see the same source of truth.
func (p *E2BExecutorPool) SetWorkspace(ws workspace.Store) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.workspace = ws
}

func (p *E2BExecutorPool) Get(ctx context.Context, agentID, projectID, sessionID string) (Executor, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := poolKey(agentID, projectID, sessionID)
	if ex, ok := p.executors[key]; ok {
		return ex, nil
	}
	ex, err := newE2BExecutor(ctx, p.apiKey, p.template, p.timeout, p.opts...)
	if err != nil {
		return nil, err
	}
	ex.SetHydrationSources(skillDirsForAgent(p.home, agentID), p.workspace, agentID, projectID, sessionID)
	if err := ex.Hydrate(ctx); err != nil {
		// Hydrate is what chowns /workspace to the non-root `user`
		// account exec runs as; without it every agent write to
		// /workspace gets Permission denied silently — see the
		// reproducer in e2b_probe_diag_test.go. Used to be a WARN
		// here; the sandbox would still get cached and every
		// subsequent turn would surface as "agent wrote files but
		// they vanished". Tear it down so the caller retries or
		// fails loudly.
		_ = ex.Close()
		return nil, fmt.Errorf("e2b hydrate: %w", err)
	}
	if err := verifyWorkspaceWritable(ctx, ex); err != nil {
		_ = ex.Close()
		return nil, fmt.Errorf("e2b sandbox unusable: %w", err)
	}
	warmupCamoufoxDaemon(ctx, ex)
	p.executors[key] = ex
	return ex, nil
}

// warmupCamoufoxDaemon spawns the camoufox-cli background daemon as part
// of sandbox provisioning so the agent's first `camoufox-cli open` call
// attaches to a live daemon instead of racing to spawn one. Without this,
// the CLI's auto-spawn path waits only 5 seconds for the daemon socket
// — empirically too short in a fresh e2b sandbox (Python startup +
// Firefox handshake routinely take longer), and concurrent execs inside
// the same sandbox can both try to spawn, surfacing as the user-visible
// "Daemon did not start within 5 seconds" failure.
//
// Best-effort: any error is logged at WARN and sandbox creation still
// succeeds — agents that never touch the browser shouldn't fail because
// camoufox couldn't come up, and the original (slow) cold-start path
// remains intact as a fallback. Bounded to 120s so a wedged camoufox
// install can't pin sandbox creation indefinitely.
func warmupCamoufoxDaemon(ctx context.Context, ex *E2BExecutor) {
	warmCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	// `open about:blank` is the cheapest invocation that starts the
	// daemon: no network, no GeoIP lookup, no real page load. The proxy
	// shim in the Dockerfile still applies — if HTTPS_PROXY is set the
	// browser launches with --proxy, matching what the agent's later
	// calls will see, so the warmed daemon is configured identically.
	out, err := ex.execOnce(warmCtx, "cd /workspace && camoufox-cli open about:blank", 120*time.Second)
	if err != nil {
		slog.Warn("e2b camoufox warmup failed (first browser call will pay cold-start)",
			"sandboxID", ex.sandboxID, "error", err, "out", strings.TrimSpace(out))
		return
	}
	slog.Info("e2b camoufox daemon warmed", "sandboxID", ex.sandboxID)
}

func (p *E2BExecutorPool) Release(agentID, projectID, sessionID string) error {
	p.mu.Lock()
	key := poolKey(agentID, projectID, sessionID)
	ex, ok := p.executors[key]
	delete(p.executors, key)
	p.mu.Unlock()
	if ok {
		return ex.Close()
	}
	return nil
}

func (p *E2BExecutorPool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ex := range p.executors {
		ex.Close()
	}
	p.executors = make(map[string]*E2BExecutor)
}

var (
	_ Executor             = (*E2BExecutor)(nil)
	_ ExecutorPool         = (*E2BExecutorPool)(nil)
	_ WorkspaceSnapshotter = (*E2BExecutor)(nil)
	_ RemoteWorkspace      = (*E2BExecutor)(nil)
)
