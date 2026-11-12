// Package runtime is the coding-agent "live app" layer that sits on top
// of a project's shared workspace folder. Where the existing project
// feature (internal/store + internal/setup/handlers_projects.go) owns a
// project's source tree, this package owns a long-lived sandbox
// container that *runs* that tree: it scaffolds from a template, boots a
// dev server, publishes the server's port to the host, and records a
// preview URL.
//
// Why a separate package and a separate long-lived container instead of
// reusing the per-turn ExecutorPool:
//
//   - The turn-scoped sandbox (sandbox.ExecutorPool) is created and torn
//     down around each agent turn. A dev server must outlive a turn, so
//     it can't live there. This package keeps its own container alive
//     across turns, evicted only on idle/sleep.
//   - Both containers bind-mount the SAME host dir
//     (workspaces/<agent>/projects/<pid>/). So when the agent edits a
//     file during a turn (through its turn sandbox), the dev server in
//     this package's container sees the change immediately and HMR
//     reloads. No file sync, no copy — the bind mount is the channel.
//
// Nothing here touches the turn-scoped pool, so the existing sandbox
// behavior is byte-for-byte unchanged: this is purely additive.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Runtime status values, mirrored in store.ProjectRuntimeRecord.Status.
const (
	StatusNone        = "none"
	StatusScaffolding = "scaffolding"
	StatusStarting    = "starting"
	StatusRunning     = "running"
	StatusSleeping    = "sleeping"
	StatusCrashed     = "crashed"
)

// devLogPath is where each runtime tees its dev-server output inside the
// container, so Logs() can tail it without attaching to the process. The
// scaffold (pnpm install) also streams here first (scaffoldToLog), so the
// preview panel shows live build progress, then the dev server APPENDS.
const devLogPath = "/workspace/.fastclaw-dev.log"

// scaffoldToLog wraps a scaffold command so its combined stdout/stderr
// streams to the dev log AS IT RUNS — a concurrent Logs() tail surfaces the
// live pnpm-install output while Up() is still blocked on the scaffold. A
// subshell + plain redirect (no pipe) preserves the scaffold's exit code,
// and the leading `>` truncates so each fresh Up starts a clean log (the
// dev server then appends with `>>`).
func scaffoldToLog(cmd string) string {
	return "( " + cmd + " ) > " + devLogPath + " 2>&1"
}

// pnpmStoreVolume is a shared Docker named volume mounted at
// pnpmStorePath in every runtime container. pnpm points its content store
// here (scaffold sets store-dir), so after the first install populates it
// every later install across any chat/project skips re-downloading. Named
// volumes live in the Docker VM — fast on macOS, unlike bind mounts.
const (
	pnpmStoreVolume = "fastclaw-pnpm-store"
	pnpmStorePath   = "/pnpm-store"
)

// AppSubdir is the folder, under a scope's workspace, that the app is
// scaffolded into and served from — so the template doesn't pollute the
// chat/project workspace root with 30+ files. The agent's file tools are
// redirected into it too (registry codingSubdir) so edits land where the
// dev server serves. Exported so the agent package uses the same name.
const AppSubdir = "app"

// TemplateSpec describes how to scaffold and run one template. It is
// intentionally a small shell-command contract so the runtime stays
// template-agnostic: register a spec per template ref and the manager
// never needs to know what "shipany-tanstack" is.
type TemplateSpec struct {
	// DevPort is the container-internal port the dev server binds to
	// (e.g. 3000 for ShipAny / Vite).
	DevPort int
	// ScaffoldCmd populates an empty /workspace with the template's
	// source and installs deps. Run once, in /workspace, only when the
	// workspace looks empty (see needsScaffold). Example:
	//   cp -a /template/. /workspace/ && pnpm install
	ScaffoldCmd string
	// DevCmd starts the dev server bound to 0.0.0.0:DevPort, backgrounded
	// by the manager. IMPORTANT for HMR: the dev server's websocket must
	// advertise the *published host port*, not DevPort — configure that
	// template-side (e.g. Vite server.hmr.clientPort). Example:
	//   pnpm dev --host 0.0.0.0 --port 3000
	DevCmd string
	// TemplateMount, when set, bind-mounts this host directory read-only
	// at /template in the runtime container, so the default
	// `cp -a /template/.` scaffold works from a local template checkout
	// (no image bake, no git clone). Empty → scaffold must self-source
	// (git clone, or /template already baked into the image).
	TemplateMount string
	// Image overrides the sandbox image/backend-template used for THIS
	// template, for the rare case a template needs a genuinely different
	// toolchain base (e.g. a Python stack vs the Node default). Empty →
	// the deployment default (Manager.image / the pool's backend image).
	//
	// Honored on the DOCKER path (the runtime owns the container, so it
	// just runs a different image). On the pooled/e2b path the dev server
	// shares the agent's pooled sandbox — one backend image per deployment
	// — so same-stack templates (the common case) differ only by ScaffoldCmd
	// / source, not Image; per-template e2b images would need a pool
	// per-project override (not wired yet). See docs/coding-agent-runtime.md.
	Image string
}

// Manager owns every project runtime. Safe for concurrent use.
type Manager struct {
	store         store.Store
	workspaceRoot string // FASTCLAW_HOME (workspaces/ lives under it)
	image         string
	policy        *sandbox.Policy
	// previewBase templates the user-facing preview URL. Two shapes:
	//   - contains "{project}" → wildcard-gateway mode; the gateway maps
	//     <project>.preview.example.com to the host port, so the URL
	//     carries no port: "https://{project}.preview.example.com"
	//   - empty → local mode; URL points straight at the published host
	//     port: "http://127.0.0.1:<hostPort>"
	previewBase string

	// backend mirrors cfg.Sandbox.Backend ("docker", "e2b", "boxlite", or
	// "" for the legacy docker default). When it names a non-docker backend
	// AND pool is set, Up() drives the dev-server preview through the shared
	// turn-sandbox pool (the SAME executor the coding agent writes files to,
	// so edits reach the server with no host bind mount). Docker keeps its
	// own long-lived container path below — unchanged, no regression.
	backend string
	// pool is the gateway's shared system sandbox pool, borrowed for the
	// non-docker preview path. nil for docker / when sandboxing is off.
	pool sandbox.ExecutorPool

	mu        sync.Mutex
	templates map[string]TemplateSpec
	// defaultRef is the template the preview tool picks when the agent
	// omits one — the FIRST registered ref, so adding more templates never
	// silently changes the default for an existing deployment.
	defaultRef string
	live       map[string]*sandbox.DockerSandbox // rtKey → container (docker path)
}

// NewManager builds a runtime manager. image is the sandbox container
// image (same one the turn pool uses is fine, as long as it has the
// template toolchain — node/pnpm for ShipAny). previewBase is documented
// on Manager.previewBase. backend + pool select the preview path: a
// non-docker backend with a non-nil pool runs the dev server inside the
// agent's pooled executor; otherwise the docker container path is used.
func NewManager(st store.Store, workspaceRoot, image string, policy *sandbox.Policy, previewBase, backend string, pool sandbox.ExecutorPool) *Manager {
	if image == "" {
		image = sandbox.DefaultImage
	}
	return &Manager{
		store:         st,
		workspaceRoot: workspaceRoot,
		image:         image,
		policy:        policy,
		previewBase:   previewBase,
		backend:       backend,
		pool:          pool,
		templates:     make(map[string]TemplateSpec),
		live:          make(map[string]*sandbox.DockerSandbox),
	}
}

// SetSandboxPool updates the borrowed turn-sandbox pool after system
// sandbox settings are saved. Existing live runtimes keep running; new
// pooled preview starts use the refreshed backend/pool.
func (m *Manager) SetSandboxPool(backend string, pool sandbox.ExecutorPool) {
	m.mu.Lock()
	m.backend = backend
	m.pool = pool
	m.mu.Unlock()
}

// usesPool reports whether the preview should run through the shared
// turn-sandbox pool (non-docker backend) rather than a dedicated docker
// container. Requires a pool to borrow; without one we fall back to the
// docker path even if the backend string says otherwise (and that path
// will fail loudly if docker is absent, which is the honest signal).
func (m *Manager) usesPool() bool {
	return m.pool != nil && m.backend != "" && m.backend != "docker"
}

// RegisterTemplate registers (or overrides) how a template ref is
// scaffolded and run. Call at boot for each supported template.
func (m *Manager) RegisterTemplate(ref string, spec TemplateSpec) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.templates[ref] = spec
	if m.defaultRef == "" {
		m.defaultRef = ref
	}
}

// DefaultTemplate returns the template the preview tool uses when the agent
// omits a ref: the first registered ref. Returns "" only when nothing is
// registered. Keeping it stable as the first registration (rather than
// "the sole one") means adding a second template doesn't suddenly force
// every caller to pass an explicit ref.
func (m *Manager) DefaultTemplate() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.defaultRef
}

// Templates returns the registered template refs, default first then the
// rest sorted — for surfacing the menu in the preview tool's description so
// the model knows which templates exist.
func (m *Manager) Templates() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	rest := make([]string, 0, len(m.templates))
	for ref := range m.templates {
		if ref != m.defaultRef {
			rest = append(rest, ref)
		}
	}
	sort.Strings(rest)
	out := make([]string, 0, len(m.templates))
	if m.defaultRef != "" {
		out = append(out, m.defaultRef)
	}
	return append(out, rest...)
}

func rtKey(userID, agentID, scopeID string) string {
	return userID + "|" + agentID + "|" + scopeID
}

// nmVolumeName is the per-scope node_modules Docker volume. Deterministic
// from scopeID so wake reuses the same install. Non-volume-safe chars
// (the "sess:" colon) are replaced so it satisfies Docker's name rules.
func nmVolumeName(scopeID string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' {
			return r
		}
		return '-'
	}, scopeID)
	return "fastclaw-nm-" + safe
}

// removeVolume best-effort deletes a Docker named volume (the per-scope
// node_modules) after its container is gone. Failure is logged, not fatal
// — an orphaned volume only costs disk.
func removeVolume(name string) {
	if err := exec.Command("docker", "volume", "rm", "-f", name).Run(); err != nil {
		slog.Warn("runtime: remove node_modules volume", "volume", name, "error", err)
	}
}

// scopeFor resolves how a runtime is addressed. A project, when present,
// is the app's home — shared across all the project's chats and serving
// projects/<pid>/. Otherwise the chat's own session dir is the home
// (sessions/<sid>/), so a preview works in ANY chat without first
// creating a project. Both mirror the dirs the agent's turn sandbox /
// workspace store write to, so the dev server sees the agent's edits.
//
// scopeID is the stable key (store row + live-container map). Session
// scopes are namespaced "sess:<sid>" so they can never collide with a
// real project id in the shared project_runtimes table.
func (m *Manager) scopeFor(agentID, projectID, sessionID string) (scopeID, workspaceDir string, err error) {
	base := filepath.Join(m.workspaceRoot, "workspaces", agentID)
	switch {
	case projectID != "":
		return projectID, filepath.Join(base, "projects", projectID), nil
	case sessionID != "":
		return "sess:" + sessionID, filepath.Join(base, "sessions", sessionID), nil
	default:
		return "", "", errors.New("runtime: need a project or a session to address a runtime")
	}
}

// Get returns the persisted runtime record for a project (or session)
// scope, or store.ErrNotFound when none exists yet. Pass sessionID="" for
// the project-addressed (HTTP/SaaS) path.
//
// Liveness reconcile: a record can say "running" while no container is
// actually serving — a daemon restart wipes the in-memory live map but
// leaves the persisted row (and its now-dead host port) untouched. Returning
// that verbatim points the client's preview iframe at a dead port. So when
// the docker path has no live handle for a "running"/"starting" record, we
// report it as "sleeping" (host port cleared) — the client then offers to
// wake/re-up, which boots a fresh container. The stored row is left as-is;
// the next Up reconciles it.
func (m *Manager) Get(ctx context.Context, userID, agentID, projectID, sessionID string) (*store.ProjectRuntimeRecord, error) {
	scopeID, _, err := m.scopeFor(agentID, projectID, sessionID)
	if err != nil {
		return nil, err
	}
	rec, err := m.store.GetProjectRuntime(ctx, userID, agentID, scopeID)
	if err != nil {
		return nil, err
	}
	// Pool-backed (e2b/boxlite) runtimes don't use the live map; trust the
	// stored status there.
	if !m.usesPool() && (rec.Status == StatusRunning || rec.Status == StatusStarting) {
		m.mu.Lock()
		live := m.live[rtKey(userID, agentID, scopeID)]
		m.mu.Unlock()
		if live == nil {
			rec.Status = StatusSleeping
			rec.HostPort = 0
			rec.PreviewURL = ""
		}
	}
	return rec, nil
}

// Up brings a runtime to StatusRunning and returns the record with a live
// PreviewURL. Idempotent: if the container is already up it just re-reads
// state; if the workspace is empty it scaffolds first. The runtime is
// homed in the project when projectID is set, else in the chat's own
// session dir (so a preview works without a pre-created project).
//
// templateRef may be empty when the runtime already exists (the stored
// ref is reused); it is required on first provisioning.
func (m *Manager) Up(ctx context.Context, userID, agentID, projectID, sessionID, templateRef string) (*store.ProjectRuntimeRecord, error) {
	scopeID, ws, err := m.scopeFor(agentID, projectID, sessionID)
	if err != nil {
		return nil, err
	}
	rec, err := m.store.GetProjectRuntime(ctx, userID, agentID, scopeID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if rec == nil {
		rec = &store.ProjectRuntimeRecord{
			UserID:    userID,
			AgentID:   agentID,
			ProjectID: scopeID,
			Status:    StatusNone,
		}
	}
	if templateRef != "" {
		rec.TemplateRef = templateRef
	}
	if rec.TemplateRef == "" {
		return nil, errors.New("runtime: templateRef is required on first Up")
	}

	m.mu.Lock()
	spec, ok := m.templates[rec.TemplateRef]
	if !ok && len(m.templates) == 1 {
		// Lenient resolution: the model commonly passes a looser name
		// ("shipany" for the registered "shipany-tanstack"). With a single
		// template configured, treat any ref as that one and canonicalize.
		for ref, s := range m.templates {
			rec.TemplateRef, spec, ok = ref, s, true
		}
	}
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("runtime: unknown template %q (register it first)", rec.TemplateRef)
	}
	if spec.DevPort == 0 {
		spec.DevPort = 3000
	}

	// Non-docker backend: run the dev server inside the agent's pooled
	// executor (same sandbox the coding file tools write to) and expose
	// the port via the backend's URL scheme. Branches off before any
	// docker-specific machinery so the docker path below is untouched.
	if m.usesPool() {
		return m.upViaPool(ctx, rec, spec, agentID, projectID, sessionID)
	}

	// Already live? Re-read host port and return without churning the
	// container.
	m.mu.Lock()
	sb := m.live[rtKey(userID, agentID, scopeID)]
	m.mu.Unlock()
	if sb != nil {
		if hp, perr := sb.HostPortFor(spec.DevPort); perr == nil {
			rec.HostPort = hp
			rec.DevPort = spec.DevPort
			rec.PreviewURL = m.previewURL(scopeID, hp)
			rec.Status = StatusRunning
			rec.LastError = ""
			_ = m.store.SaveProjectRuntime(ctx, rec)
			return rec, nil
		}
		// Stale handle (container died) — drop it and rebuild below.
		m.evict(userID, agentID, scopeID)
	}

	// Home the app in an AppSubdir of the scope workspace, not the root —
	// keeps the chat/project workspace from being buried under the
	// template's files. The agent's file tools target the same subdir
	// (registry codingSubdir), so edits still align with what's served.
	ws = filepath.Join(ws, AppSubdir)
	if mkErr := os.MkdirAll(ws, 0o755); mkErr != nil {
		rec.Status = StatusCrashed
		rec.LastError = "create app dir: " + mkErr.Error()
		_ = m.store.SaveProjectRuntime(ctx, rec)
		return nil, fmt.Errorf("runtime: create app dir: %w", mkErr)
	}

	// Create + start a fresh long-lived container with the dev port
	// published, homed on the app subdir. A template may pin its own image
	// (different toolchain base); fall back to the deployment default.
	img := m.image
	if spec.Image != "" {
		img = spec.Image
	}
	sb = sandbox.NewDockerSandbox(img, ws, m.policy)
	sb.SetPublishPorts(map[int]int{spec.DevPort: 0})
	if spec.TemplateMount != "" {
		sb.SetTemplateMount(spec.TemplateMount)
	}
	// Two named volumes (Docker-VM filesystem, fast on macOS):
	//   - a SHARED pnpm content store so installs after the first skip
	//     re-downloading (scaffold points pnpm at pnpmStorePath).
	//   - a PER-SCOPE node_modules volume mounted over /workspace/node_modules
	//     so the 1GB+ dep tree never touches the slow bind mount AND can
	//     hard-link from the store (same filesystem). Persists across
	//     sleep/wake; removed on Stop. The host's app/node_modules stays
	//     empty (shadowed) — fine, the UI filters it and only the runtime
	//     container needs it.
	sb.SetExtraVolumes([]string{
		pnpmStoreVolume + ":" + pnpmStorePath,
		nmVolumeName(scopeID) + ":/workspace/node_modules",
	})

	rec.Status = StatusStarting
	rec.DevPort = spec.DevPort
	_ = m.store.SaveProjectRuntime(ctx, rec)

	if err := sb.Create(); err != nil {
		rec.Status = StatusCrashed
		rec.LastError = "create sandbox: " + err.Error()
		_ = m.store.SaveProjectRuntime(ctx, rec)
		return nil, fmt.Errorf("runtime: create sandbox: %w", err)
	}
	rec.ContainerID = sb.ContainerID()

	m.mu.Lock()
	m.live[rtKey(userID, agentID, scopeID)] = sb
	m.mu.Unlock()

	// Scaffold on an empty workspace. Output streams to the dev log so the
	// preview panel can tail the live pnpm-install progress (scaffoldToLog).
	if m.needsScaffold(ctx, sb) && spec.ScaffoldCmd != "" {
		rec.Status = StatusScaffolding
		_ = m.store.SaveProjectRuntime(ctx, rec)
		if _, serr := sb.Exec(ctx, scaffoldToLog(spec.ScaffoldCmd), "/workspace"); serr != nil {
			logTail, _ := sb.Exec(ctx, "tail -n 60 "+devLogPath+" 2>/dev/null", "/workspace")
			rec.Status = StatusCrashed
			rec.LastError = "scaffold: " + serr.Error() + ": " + tail(logTail, 500)
			_ = m.store.SaveProjectRuntime(ctx, rec)
			return nil, fmt.Errorf("runtime: scaffold: %w", serr)
		}
	}

	// Start the dev server, backgrounded so Exec returns immediately. The
	// container's PID 1 is `tail -f /dev/null`, so the nohup'd process
	// keeps running after this docker exec returns.
	if err := m.startDevServer(ctx, sb, spec.DevCmd); err != nil {
		rec.Status = StatusCrashed
		rec.LastError = "start dev server: " + err.Error()
		_ = m.store.SaveProjectRuntime(ctx, rec)
		return nil, fmt.Errorf("runtime: start dev server: %w", err)
	}

	// Don't declare "running" until the dev server actually answers on the
	// published port — otherwise the preview URL 404s/refuses while Vite is
	// still booting (or forever, if it crashed / bound a different port
	// because something rewrote the framework config). Poll inside the
	// container; on timeout, surface the real bound port from the log so
	// the failure is actionable instead of a silent dead link. The budget
	// covers SSR templates (vite+nitro) whose FIRST request triggers a
	// cold compile that alone runs well past a minute in a container.
	if err := m.waitForDevServer(ctx, sb, spec.DevPort, 5*time.Minute); err != nil {
		rec.Status = StatusCrashed
		rec.LastError = err.Error()
		_ = m.store.SaveProjectRuntime(ctx, rec)
		return nil, fmt.Errorf("runtime: %w", err)
	}

	hp, err := sb.HostPortFor(spec.DevPort)
	if err != nil {
		rec.Status = StatusCrashed
		rec.LastError = "resolve host port: " + err.Error()
		_ = m.store.SaveProjectRuntime(ctx, rec)
		return nil, fmt.Errorf("runtime: resolve host port: %w", err)
	}
	rec.HostPort = hp
	rec.PreviewURL = m.previewURL(scopeID, hp)
	rec.Status = StatusRunning
	rec.LastError = ""
	if err := m.store.SaveProjectRuntime(ctx, rec); err != nil {
		return nil, err
	}
	slog.Info("project runtime up", "agent", agentID, "scope", scopeID,
		"hostPort", hp, "preview", rec.PreviewURL)
	return rec, nil
}

// Sleep stops the container to free compute but keeps the workspace and
// the record. Wake (or the next Up) recreates it. Status → sleeping,
// host port cleared (the old binding is gone).
func (m *Manager) Sleep(ctx context.Context, userID, agentID, projectID, sessionID string) error {
	scopeID, _, err := m.scopeFor(agentID, projectID, sessionID)
	if err != nil {
		return err
	}
	m.evict(userID, agentID, scopeID)
	rec, err := m.store.GetProjectRuntime(ctx, userID, agentID, scopeID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	rec.Status = StatusSleeping
	rec.HostPort = 0
	rec.ContainerID = ""
	rec.PreviewURL = ""
	return m.store.SaveProjectRuntime(ctx, rec)
}

// Wake is Up without a template ref (reuses the stored one).
func (m *Manager) Wake(ctx context.Context, userID, agentID, projectID, sessionID string) (*store.ProjectRuntimeRecord, error) {
	return m.Up(ctx, userID, agentID, projectID, sessionID, "")
}

// Stop tears the container down AND deletes the runtime record. The
// workspace files are left on disk (the project/chat still owns them);
// only the live-app layer is removed.
func (m *Manager) Stop(ctx context.Context, userID, agentID, projectID, sessionID string) error {
	scopeID, _, err := m.scopeFor(agentID, projectID, sessionID)
	if err != nil {
		return err
	}
	m.evict(userID, agentID, scopeID)
	// The container is gone; reclaim its per-scope node_modules volume.
	removeVolume(nmVolumeName(scopeID))
	return m.store.DeleteProjectRuntime(ctx, userID, agentID, scopeID)
}

// Exec runs a one-shot command inside the runtime container (for git
// snapshots, deploys, package installs the agent triggers out-of-band).
// Returns combined stdout+stderr. Fails if the runtime isn't live.
func (m *Manager) Exec(ctx context.Context, userID, agentID, projectID, sessionID, command string, timeout time.Duration) (string, error) {
	scopeID, _, err := m.scopeFor(agentID, projectID, sessionID)
	if err != nil {
		return "", err
	}
	if m.usesPool() {
		return m.poolExec(ctx, agentID, projectID, sessionID, command, timeout)
	}
	m.mu.Lock()
	sb := m.live[rtKey(userID, agentID, scopeID)]
	m.mu.Unlock()
	if sb == nil {
		return "", errors.New("runtime: not live (call Up first)")
	}
	ctxExec := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		ctxExec, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return sb.Exec(ctxExec, command, "/workspace")
}

// Logs tails the dev server log. tail<=0 returns the whole file.
func (m *Manager) Logs(ctx context.Context, userID, agentID, projectID, sessionID string, tailLines int) (string, error) {
	scopeID, _, err := m.scopeFor(agentID, projectID, sessionID)
	if err != nil {
		return "", err
	}
	cmd := "cat " + devLogPath
	if tailLines > 0 {
		cmd = fmt.Sprintf("tail -n %d %s", tailLines, devLogPath)
	}
	cmd += " 2>/dev/null || true"
	if m.usesPool() {
		return m.poolExec(ctx, agentID, projectID, sessionID, cmd, 30*time.Second)
	}
	m.mu.Lock()
	sb := m.live[rtKey(userID, agentID, scopeID)]
	m.mu.Unlock()
	if sb == nil {
		return "", errors.New("runtime: not live")
	}
	return sb.Exec(ctx, cmd, "/workspace")
}

// ChangedFile is one path the agent created or modified since the
// template baseline. Path is workspace-relative in the same scheme the
// file API uses (e.g. "sessions/<sid>/app/src/routes/index.tsx").
type ChangedFile struct {
	Path string `json:"path"`
}

// ChangedFiles returns only the files that differ from the pristine
// template baseline the scaffold committed — i.e. what THIS task
// generated. It runs `git status` in the running app (git's .gitignore
// already excludes node_modules / build output, so the list is clean).
// Returns an error when no live runtime / no git baseline exists; callers
// treat that as "fall back to the full file list".
func (m *Manager) ChangedFiles(ctx context.Context, userID, agentID, projectID, sessionID string) ([]ChangedFile, error) {
	out, err := m.Exec(ctx, userID, agentID, projectID, sessionID,
		"git config --global --add safe.directory '*' >/dev/null 2>&1; "+
			"if ! git -C /workspace rev-parse --git-dir >/dev/null 2>&1; then echo __NO_BASELINE__; exit 0; fi; "+
			"git -C /workspace status --porcelain -uall 2>/dev/null", 20*time.Second)
	if err != nil {
		return nil, err
	}
	// No git baseline (app scaffolded before the baseline feature) — signal
	// "unavailable" so the UI lists all files instead of claiming no changes.
	if strings.Contains(out, "__NO_BASELINE__") {
		return nil, errors.New("runtime: no git baseline for this app")
	}
	rel := "sessions/" + sessionID
	if projectID != "" {
		rel = "projects/" + projectID
	}
	prefix := rel + "/" + AppSubdir + "/"
	var files []ChangedFile
	for _, line := range strings.Split(out, "\n") {
		// Porcelain v1: 2 status chars + a space, then the path at index 3.
		if len(line) < 4 {
			continue
		}
		p := line[3:]
		// Renames render as "old -> new"; the new name is what exists.
		if i := strings.Index(p, " -> "); i >= 0 {
			p = p[i+4:]
		}
		p = strings.TrimSpace(p)
		p = strings.Trim(p, "\"") // git quotes paths with special chars
		if p == "" {
			continue
		}
		files = append(files, ChangedFile{Path: prefix + p})
	}
	return files, nil
}

// --- non-docker (pooled-executor) preview path ---

// upViaPool runs the dev server inside the agent's pooled executor and
// exposes its port via the backend's URL scheme. Because the runtime and
// the agent share the SAME executor (same pool key: agent+project), files
// the coding agent writes are already on the dev server's disk, so HMR
// works with no host bind mount and no file sync — the property that makes
// the preview work on cloud backends like E2B.
func (m *Manager) upViaPool(ctx context.Context, rec *store.ProjectRuntimeRecord, spec TemplateSpec, agentID, projectID, sessionID string) (*store.ProjectRuntimeRecord, error) {
	ex, err := m.pool.Get(ctx, agentID, projectID, sessionID)
	if err != nil {
		rec.Status = StatusCrashed
		rec.LastError = "sandbox acquire: " + err.Error()
		_ = m.store.SaveProjectRuntime(ctx, rec)
		return rec, fmt.Errorf("runtime: sandbox acquire: %w", err)
	}
	exposer, ok := ex.(sandbox.PortExposer)
	if !ok {
		rec.Status = StatusCrashed
		rec.LastError = fmt.Sprintf("sandbox backend %q can't expose preview ports", m.backend)
		_ = m.store.SaveProjectRuntime(ctx, rec)
		return rec, errors.New(rec.LastError)
	}

	rec.Status = StatusStarting
	rec.DevPort = spec.DevPort
	rec.HostPort = 0
	_ = m.store.SaveProjectRuntime(ctx, rec)

	// Seed the template into the sandbox when a local checkout is set and
	// the backend can upload it (cloud sandboxes have no host bind mount).
	// Otherwise the scaffold's `[ -d /template ]` guard falls back to a
	// template baked into the sandbox image.
	if spec.TemplateMount != "" {
		if tp, ok := ex.(sandbox.TemplateProvisioner); ok {
			if fi, statErr := os.Stat(spec.TemplateMount); statErr == nil && fi.IsDir() {
				if perr := tp.ProvisionDir(ctx, spec.TemplateMount, "/template"); perr != nil {
					slog.Warn("runtime: template provision failed; relying on baked /template", "err", perr)
				}
			}
		}
	}

	// Scaffold an empty workspace. Output streams to the dev log so the
	// preview panel can tail the live pnpm-install progress (scaffoldToLog).
	if spec.ScaffoldCmd != "" && m.needsScaffoldExec(ctx, ex) {
		rec.Status = StatusScaffolding
		_ = m.store.SaveProjectRuntime(ctx, rec)
		if _, serr := ex.Exec(ctx, scaffoldToLog(spec.ScaffoldCmd), 10*time.Minute); serr != nil {
			logTail, _ := ex.Exec(ctx, "tail -n 60 "+devLogPath+" 2>/dev/null", 30*time.Second)
			rec.Status = StatusCrashed
			rec.LastError = "scaffold: " + serr.Error() + ": " + tail(logTail, 500)
			_ = m.store.SaveProjectRuntime(ctx, rec)
			return rec, fmt.Errorf("runtime: scaffold: %w", serr)
		}
	}

	// Start the dev server (backgrounded) and wait until it answers.
	if serr := m.startDevServerExec(ctx, ex, spec.DevCmd, spec.DevPort); serr != nil {
		rec.Status = StatusCrashed
		rec.LastError = "start dev server: " + serr.Error()
		_ = m.store.SaveProjectRuntime(ctx, rec)
		return rec, fmt.Errorf("runtime: start dev server: %w", serr)
	}
	if werr := m.waitForDevServerExec(ctx, ex, spec.DevPort, 5*time.Minute); werr != nil {
		rec.Status = StatusCrashed
		rec.LastError = werr.Error()
		_ = m.store.SaveProjectRuntime(ctx, rec)
		return rec, fmt.Errorf("runtime: %w", werr)
	}

	url, perr := exposer.ExposePort(ctx, spec.DevPort)
	if perr != nil {
		rec.Status = StatusCrashed
		rec.LastError = "expose port: " + perr.Error()
		_ = m.store.SaveProjectRuntime(ctx, rec)
		return rec, fmt.Errorf("runtime: expose port: %w", perr)
	}
	rec.PreviewURL = url
	rec.Status = StatusRunning
	rec.LastError = ""
	if serr := m.store.SaveProjectRuntime(ctx, rec); serr != nil {
		return rec, serr
	}
	slog.Info("project runtime up (pooled)", "agent", agentID, "scope", rec.ProjectID,
		"backend", m.backend, "preview", url)
	return rec, nil
}

// poolExec runs a one-shot command in the project's pooled executor.
func (m *Manager) poolExec(ctx context.Context, agentID, projectID, sessionID, command string, timeout time.Duration) (string, error) {
	ex, err := m.pool.Get(ctx, agentID, projectID, sessionID)
	if err != nil {
		return "", fmt.Errorf("runtime: sandbox acquire: %w", err)
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return ex.Exec(ctx, command, timeout)
}

// needsScaffoldExec reports whether /workspace still needs scaffolding
// (no package.json). Errs toward scaffolding when the probe itself fails —
// an empty FS is the common first-Up case.
func (m *Manager) needsScaffoldExec(ctx context.Context, ex sandbox.Executor) bool {
	out, err := ex.Exec(ctx, "test -f /workspace/package.json && echo FOUND || echo MISSING", 30*time.Second)
	if err != nil {
		return true
	}
	return !strings.Contains(out, "FOUND")
}

// startDevServerExec launches the dev server backgrounded in the executor,
// skipping if something already answers on the port (a re-Up on a live
// sandbox) so we don't stack a second server that fails to bind.
func (m *Manager) startDevServerExec(ctx context.Context, ex sandbox.Executor, devCmd string, port int) error {
	if devCmd == "" {
		return errors.New("template DevCmd is empty")
	}
	probe := fmt.Sprintf(`curl -s --noproxy '*' -o /dev/null -w '%%{http_code}' --max-time 5 http://127.0.0.1:%d/ 2>/dev/null || echo 000`, port)
	if out, _ := ex.Exec(ctx, probe, 15*time.Second); func() bool {
		c := strings.TrimSpace(out)
		return len(c) == 3 && c != "000"
	}() {
		return nil
	}
	wrapped := fmt.Sprintf("setsid sh -c %s >> %s 2>&1 < /dev/null & echo started",
		shellSingleQuote(devCmd), devLogPath)
	_, err := ex.Exec(ctx, wrapped, 30*time.Second)
	return err
}

// waitForDevServerExec mirrors waitForDevServer for the Executor interface:
// poll the dev port from inside the sandbox until it answers (any 2xx–5xx,
// since a half-finished edit legitimately 500s), then return. On timeout
// surface the port Vite actually bound for an actionable error.
func (m *Manager) waitForDevServerExec(ctx context.Context, ex sandbox.Executor, port int, timeout time.Duration) error {
	probe := fmt.Sprintf(`curl -s --noproxy '*' -o /dev/null -w '%%{http_code}' --max-time 60 http://127.0.0.1:%d/ 2>/dev/null || echo 000`, port)
	const step = 3 * time.Second
	for waited := time.Duration(0); waited < timeout; waited += step {
		out, _ := ex.Exec(ctx, probe, 90*time.Second)
		code := strings.TrimSpace(out)
		if len(code) == 3 && code[0] >= '2' && code[0] <= '5' {
			return nil
		}
		_, _ = ex.Exec(ctx, fmt.Sprintf("sleep %d", int(step.Seconds())), step+10*time.Second)
	}
	logTail, _ := ex.Exec(ctx, "tail -n 40 "+devLogPath+" 2>/dev/null", 30*time.Second)
	if boundPort := detectVitePort(logTail); boundPort != 0 && boundPort != port {
		return fmt.Errorf("dev server bound port %d, not %d — revert any server.port override in the framework config so the preview maps correctly. Log tail:\n%s",
			boundPort, port, tail(logTail, 600))
	}
	return fmt.Errorf("dev server did not come up on port %d within %s. Log tail:\n%s",
		port, timeout, tail(logTail, 600))
}

// --- internals ---

// waitForDevServer polls the dev port from inside the container until it
// answers an HTTP request, or the timeout elapses. On timeout it inspects
// the dev log for the port Vite actually bound (e.g. it fell back to 5173
// because the app's vite.config server.port was rewritten) and returns an
// actionable error instead of letting Up report a dead "running" preview.
func (m *Manager) waitForDevServer(ctx context.Context, sb *sandbox.DockerSandbox, port int, timeout time.Duration) error {
	// --max-time must comfortably exceed an SSR dev server's cold-compile
	// time for the first request (vite+nitro takes tens of seconds): with a
	// short cap every probe aborts mid-compile and the server never gets to
	// answer, even though it's healthy.
	probe := fmt.Sprintf(
		`curl -s --noproxy '*' -o /dev/null -w '%%{http_code}' --max-time 60 http://127.0.0.1:%d/ 2>/dev/null || echo 000`,
		port)
	deadline := timeout
	const step = 3 * time.Second
	for waited := time.Duration(0); waited < deadline; waited += step {
		out, _ := sb.Exec(ctx, probe, "/workspace")
		code := strings.TrimSpace(out)
		// Any real HTTP response means the dev server is up and the
		// published port reaches it — INCLUDING 5xx. A coding agent's
		// half-finished edit makes SSR templates answer 500; that's a
		// build error the user fixes via more chat, not a dead preview.
		// Treating it as "not up" would 5-min-timeout the whole boot and
		// hide the very error overlay the user needs to see. Only "000"
		// (curl couldn't connect at all) keeps waiting.
		if len(code) == 3 && code[0] >= '2' && code[0] <= '5' {
			return nil
		}
		// Cheap sleep without importing a timer into the loop body: a
		// no-op exec that blocks ~step. `sleep` exists in the image.
		_, _ = sb.Exec(ctx, fmt.Sprintf("sleep %d", int(step.Seconds())), "/workspace")
	}
	// Timed out — figure out what actually happened.
	logTail, _ := sb.Exec(ctx, "tail -n 40 "+devLogPath+" 2>/dev/null", "/workspace")
	if boundPort := detectVitePort(logTail); boundPort != 0 && boundPort != port {
		return fmt.Errorf("dev server bound port %d, not the published %d — something rewrote the framework config's server.port; revert it so the preview maps correctly. Log tail:\n%s",
			boundPort, port, tail(logTail, 600))
	}
	return fmt.Errorf("dev server did not come up on port %d within %s. Log tail:\n%s",
		port, timeout, tail(logTail, 600))
}

// detectVitePort scrapes the "Local: http://localhost:NNNN/" line Vite
// prints on boot, returning the port it actually bound (0 if not found).
func detectVitePort(log string) int {
	const marker = "localhost:"
	for _, line := range strings.Split(log, "\n") {
		i := strings.Index(line, marker)
		if i < 0 {
			continue
		}
		rest := line[i+len(marker):]
		end := 0
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		if end == 0 {
			continue
		}
		n := 0
		for _, c := range rest[:end] {
			n = n*10 + int(c-'0')
		}
		return n
	}
	return 0
}

func (m *Manager) startDevServer(ctx context.Context, sb *sandbox.DockerSandbox, devCmd string) error {
	if devCmd == "" {
		return errors.New("template DevCmd is empty")
	}
	// nohup + & so the dev server outlives this docker exec; tee output
	// to the log file for Logs(). setsid keeps it off our process group.
	wrapped := fmt.Sprintf("setsid sh -c %s >> %s 2>&1 < /dev/null & echo started",
		shellSingleQuote(devCmd), devLogPath)
	_, err := sb.Exec(ctx, wrapped, "/workspace")
	return err
}

// needsScaffold reports whether /workspace looks empty enough to warrant
// running the scaffold command. Cheap heuristic: no package.json.
func (m *Manager) needsScaffold(ctx context.Context, sb *sandbox.DockerSandbox) bool {
	out, err := sb.Exec(ctx, "test -e /workspace/package.json && echo yes || echo no", "/workspace")
	if err != nil {
		// If we can't tell, assume it needs scaffolding — a redundant
		// scaffold on a populated dir is the template's problem to make
		// idempotent, but skipping a needed one leaves a dead preview.
		return true
	}
	return !strings.Contains(out, "yes")
}

func (m *Manager) previewURL(scopeID string, hostPort int) string {
	if m.previewBase == "" {
		return fmt.Sprintf("http://127.0.0.1:%d", hostPort)
	}
	// Gateway mode: the scope id becomes a subdomain, so strip the
	// "sess:" namespace colon (invalid in a hostname) to a dash.
	host := strings.ReplaceAll(scopeID, ":", "-")
	return strings.ReplaceAll(m.previewBase, "{project}", host)
}

// evict closes and forgets the live container without touching the
// record (callers update status as needed). scopeID is the project or
// session scope key.
func (m *Manager) evict(userID, agentID, scopeID string) {
	m.mu.Lock()
	sb := m.live[rtKey(userID, agentID, scopeID)]
	delete(m.live, rtKey(userID, agentID, scopeID))
	m.mu.Unlock()
	if sb != nil {
		if err := sb.Close(); err != nil {
			slog.Warn("runtime evict: close container", "scope", scopeID, "error", err)
		}
	}
}

// CloseAll tears down every live container. Call on shutdown.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	live := m.live
	m.live = make(map[string]*sandbox.DockerSandbox)
	m.mu.Unlock()
	for _, sb := range live {
		_ = sb.Close()
	}
}

// tail returns the last n bytes of s (for trimming error blobs).
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// shellSingleQuote wraps s in single quotes, escaping embedded ones, so
// it survives one layer of `sh -c`.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
