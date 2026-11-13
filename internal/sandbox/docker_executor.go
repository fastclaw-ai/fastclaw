package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// DockerExecutor wraps DockerSandbox to implement Executor. The container
// has the user's workspace mounted at /workspace and all tool calls are
// forwarded as docker exec commands.
type DockerExecutor struct {
	sb *DockerSandbox
}

// NewDockerExecutor creates a sandbox Executor backed by a Docker container.
// workspace is the host-side directory to mount (e.g. the user's workspace
// synced from S3, or a tmpdir for ephemeral use).
func NewDockerExecutor(image, workspace string, policy *Policy) (*DockerExecutor, error) {
	sb := NewDockerSandbox(image, workspace, policy)
	if err := sb.Create(); err != nil {
		return nil, fmt.Errorf("create docker sandbox: %w", err)
	}
	return &DockerExecutor{sb: sb}, nil
}

func (d *DockerExecutor) Exec(ctx context.Context, command string, timeout time.Duration) (string, error) {
	execCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// docker is client/daemon — exec.CommandContext only SIGKILLs the
	// local `docker exec` CLI, which just detaches the attached client;
	// the inner process inside the container keeps running until natural
	// completion. To make timeouts actually take effect *inside* the
	// container, we wrap the user command in `setsid` so it becomes a
	// new session leader, stash its pid (== its pgid) in a marker file,
	// and on cancel run a *separate* `docker exec` that signals the
	// entire process group via `kill -KILL -$pgid`.
	//
	// This replaces the old behavior of force-removing the whole
	// container on cancel, which preserved the workspace bind-mount but
	// nuked any sibling daemons — most painfully camoufox-cli's headless
	// Firefox, which takes ~3 minutes to cold-start through a proxy on
	// the next call. Pgrp-scoped kill leaves the container — and the
	// camoufox daemon, which Python spawned with start_new_session=True
	// so it lives in its own session — alive.
	//
	// Command is passed via env to avoid quoting fights with the inner
	// `sh -c`; eval re-parses it so pipes/redirects/expansions behave
	// the same as if the caller had run `sh -c "$command"` directly.
	marker := randomExecMarker()
	pgidFile := "/tmp/fc-pgid-" + marker
	wrapped := fmt.Sprintf(
		`__FC_PGID=%s __FC_CMD=%s setsid -w sh -c 'echo $$ > "$__FC_PGID"; eval "$__FC_CMD"'`,
		shellQuote(pgidFile), shellQuote(command),
	)

	done := make(chan struct{})
	go func() {
		select {
		case <-execCtx.Done():
			// Best-effort: fire a separate docker exec to kill the pgrp.
			// 5s budget so a stuck `docker exec` here doesn't wedge the
			// caller — if it can't reach the daemon we already lost.
			killCtx, killCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer killCancel()
			_, _ = d.sb.Exec(killCtx, fmt.Sprintf(
				`pgid=$(cat %s 2>/dev/null); [ -n "$pgid" ] && kill -KILL -"$pgid"; rm -f %s`,
				shellQuote(pgidFile), shellQuote(pgidFile)),
				"")
		case <-done:
		}
	}()
	defer close(done)

	out, err := d.sb.Exec(execCtx, wrapped, "/workspace")
	// On normal exit, the goroutine never fires — clean the marker
	// ourselves. On timeout, the goroutine already rm'd it.
	if execCtx.Err() == nil {
		_, _ = d.sb.Exec(context.Background(),
			fmt.Sprintf("rm -f %s", shellQuote(pgidFile)), "")
	}
	return out, err
}

// randomExecMarker returns 16 hex chars — sufficient to keep parallel
// execs from clobbering each other's pgid files in /tmp.
func randomExecMarker() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(buf[:])
}

func (d *DockerExecutor) ReadFile(ctx context.Context, path string) (string, error) {
	return d.sb.Exec(ctx, fmt.Sprintf("cat %s", shellQuote(path)), "/workspace")
}

func (d *DockerExecutor) WriteFile(ctx context.Context, path, content string) (string, error) {
	// Pipe content via stdin instead of argv. Heredoc-in-argv (the previous
	// implementation) sliced bytes into the docker-exec command line, which
	// fails with "fork/exec: invalid argument" the moment content contains
	// a NULL byte — every PNG, audio file, or other binary blob hits this
	// because execve rejects NULs inside argv elements. stdin sidesteps the
	// argv limit entirely.
	cmd := fmt.Sprintf("mkdir -p \"$(dirname %s)\" && cat > %s",
		shellQuote(path), shellQuote(path))
	out, err := d.sb.ExecWithStdin(ctx, cmd, "/workspace", strings.NewReader(content))
	if err != nil {
		return out, err
	}
	return fmt.Sprintf("Written to %s", path), nil
}

func (d *DockerExecutor) ListDir(ctx context.Context, path string) (string, error) {
	return d.sb.Exec(ctx, fmt.Sprintf("ls -la %s", shellQuote(path)), "/workspace")
}

func (d *DockerExecutor) Close() error {
	return d.sb.Close()
}

// SnapshotWorkspace walks the host-side mounted workspace dir (which is
// bind-mounted into the container at /workspace) and returns every
// regular file's bytes keyed by its container-relative path. Used by
// LifecyclePool for flush-on-evict so files the agent created via
// `exec` (not via write_file) still make it to the durable store.
//
// Walking the host dir directly is faster and more reliable than doing
// tar-over-exec: the mount already gives us a POSIX view of the same
// bytes the sandbox sees.
func (d *DockerExecutor) SnapshotWorkspace(ctx context.Context) (map[string][]byte, error) {
	root := d.sb.workspace
	if root == "" {
		return nil, nil
	}
	out := make(map[string][]byte)
	err := filepath.WalkDir(root, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return filepath.SkipAll
			}
			return walkErr
		}
		if entry.IsDir() {
			// Don't snapshot dependency / build trees back to the durable
			// store on evict — a scaffolded node project's node_modules is
			// tens of thousands of files. The dev server rebuilds them from
			// the bind mount, so they never need durable persistence.
			if p != root && workspace.IsBuildArtifactDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return out, nil
}

// Backend returns "docker" — used by the per-exec log line so operators
// can confirm which provider handled a given tool call.
func (d *DockerExecutor) Backend() string { return "docker" }

// Ensure DockerExecutor satisfies the optional snapshot contract. A compile
// error here would flag any accidental interface drift.
var _ WorkspaceSnapshotter = (*DockerExecutor)(nil)

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// DockerExecutorPool manages per-(agent,session) DockerExecutor instances.
type DockerExecutorPool struct {
	mu        sync.Mutex
	executors map[string]*DockerExecutor // key = poolKey(agentID, sessionID)
	image     string
	policy    *Policy
	// workspaceRoot is FASTCLAW_HOME — each session gets a private mount
	// rooted at workspaceRoot/workspaces/<agentID>/sessions/<sessionID>/.
	workspaceRoot string
}

// poolKey is the composite map key used by the executor pools. Every
// (project, session) pair gets its own slot — including chats that
// belong to the same project — because two project chats running in
// parallel would otherwise share a Python kernel / shell state and
// step on each other. The project mount itself is shared at the FS
// level so siblings stay visible (see pool.Get for the mount logic).
//
// Both empty falls back to the agent-shared sandbox slot for legacy
// callers (admin shell, fixtures).
func poolKey(agentID, projectID, sessionID string) string {
	switch {
	case projectID != "" && sessionID != "":
		return agentID + ":p:" + projectID + ":s:" + sessionID
	case projectID != "":
		return agentID + ":p:" + projectID
	case sessionID != "":
		return agentID + ":s:" + sessionID
	default:
		return agentID
	}
}

// Backend on the pool mirrors DockerExecutor.Backend so the LifecyclePool
// can surface the provider identity without resolving a lazy executor.
func (p *DockerExecutorPool) Backend() string { return "docker" }

// NewDockerExecutorPool creates a pool of Docker-backed executors.
func NewDockerExecutorPool(image, workspaceRoot string, policy *Policy) *DockerExecutorPool {
	if image == "" {
		image = DefaultImage
	}
	return &DockerExecutorPool{
		executors:     make(map[string]*DockerExecutor),
		image:         image,
		policy:        policy,
		workspaceRoot: workspaceRoot,
	}
}

func (p *DockerExecutorPool) Get(ctx context.Context, agentID, projectID, sessionID string) (Executor, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := poolKey(agentID, projectID, sessionID)
	if ex, ok := p.executors[key]; ok {
		return ex, nil
	}

	// Bind-mount layout. Project chats mount the project ROOT (so
	// siblings show up under /workspace) and cwd into their own
	// subdir, so relative writes default to the chat's files but
	// reads/walks see the whole project. Mirrors workspace.LocalFS:
	//
	//   pid="p", sid="s" → mount projects/p/, workdir /workspace/s/
	//   pid="",  sid="s" → mount sessions/s/,  workdir /workspace
	//   pid="p", sid=""  → mount projects/p/,  workdir /workspace
	//   both empty       → mount agent root,   workdir /workspace
	//
	// Per-chat per-container — even within the same project — so
	// concurrent chats don't share shell state. The shared part is the
	// FS mount, not the container.
	workspace := filepath.Join(p.workspaceRoot, "workspaces", agentID)
	var workdir string
	switch {
	case projectID != "" && sessionID != "":
		workspace = filepath.Join(workspace, "projects", projectID)
		workdir = "/workspace/" + sessionID
		// Pre-create the per-chat subdir on disk so docker's `-w` lands
		// in an existing path; Docker creates missing workdirs but
		// only as root, leaving the agent unable to write later.
		if err := os.MkdirAll(filepath.Join(workspace, sessionID), 0o755); err != nil {
			return nil, fmt.Errorf("create chat workspace subdir: %w", err)
		}
	case projectID != "":
		workspace = filepath.Join(workspace, "projects", projectID)
	case sessionID != "":
		workspace = filepath.Join(workspace, "sessions", sessionID)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace dir %s: %w", workspace, err)
	}

	// Build the sandbox by hand so we can wire skill mounts BEFORE
	// Create() bakes the docker run args. Constructing through
	// NewDockerExecutor would call Create immediately on a sandbox
	// that hasn't been told about skill dirs.
	sb := NewDockerSandbox(p.image, workspace, p.policy)
	if workdir != "" {
		sb.SetWorkdir(workdir)
	}
	sb.SetSkillDirs(skillDirsForAgent(p.workspaceRoot, agentID, SkillOwnerFromContext(ctx)))
	// Bind-mount the chatter's per-user skills host dir into the
	// sandbox at the path `npx skills add -g -y` writes to, so any
	// skill the agent installs mid-chat lands on host disk and is
	// visible to the next LoadSkills scan. UserID flows in via ctx
	// (set by HandleMessage / HandleMessageStream); empty just skips
	// the mount, which is the right fallback for non-chat callers.
	if uid := UserIDFromContext(ctx); uid != "" {
		base := os.Getenv("FASTCLAW_HOME")
		if base == "" {
			if h, err := os.UserHomeDir(); err == nil {
				base = filepath.Join(h, ".fastclaw")
			}
		}
		if base != "" {
			sb.SetUserSkillsHostDir(filepath.Join(base, "users", uid, "skills"))
		}
	}
	if err := sb.Create(); err != nil {
		return nil, fmt.Errorf("create docker sandbox: %w", err)
	}
	ex := &DockerExecutor{sb: sb}
	p.executors[key] = ex
	return ex, nil
}

func (p *DockerExecutorPool) Release(agentID, projectID, sessionID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := poolKey(agentID, projectID, sessionID)
	if ex, ok := p.executors[key]; ok {
		delete(p.executors, key)
		return ex.Close()
	}
	return nil
}

func (p *DockerExecutorPool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, ex := range p.executors {
		ex.Close()
		delete(p.executors, k)
	}
}

// Ensure interfaces are satisfied.
var (
	_ Executor     = (*DockerExecutor)(nil)
	_ ExecutorPool = (*DockerExecutorPool)(nil)
)
