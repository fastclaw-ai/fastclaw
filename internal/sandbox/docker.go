package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Policy holds resource/network constraints for a sandbox container.
type Policy struct {
	MaxCPU    string // e.g. "2"
	MaxMemory string // e.g. "512m"
	NetMode   string // "none", "host", "bridge"
}

// DockerSandbox manages a single Docker container for sandboxed execution.
type DockerSandbox struct {
	containerID string
	image       string
	workspace   string
	// workdir is the container's starting working directory. Defaults
	// to /workspace when empty. Project chats override to
	// /workspace/<sessionID>/ so the chat sees the whole project at
	// /workspace (siblings included) but its relative writes default
	// to its own subdir.
	workdir   string
	skillDirs []string // host paths to mount read-only at /skills/<name>/
	// userSkillsHostDir, when non-empty, gets bind-mounted RW at
	// /root/.agents/skills inside the container. That's where
	// `npx skills add -g -y` (which is how find-skills tells the agent
	// to install community skills) writes its global install — so any
	// skill installed mid-chat lands directly in the chatter's
	// per-user host dir and persists across sandbox eviction. Empty
	// means no mount, which is the right behavior for legacy / system-
	// injected calls that don't carry a chatter identity.
	userSkillsHostDir string
	// templateMount, when non-empty, is a host directory bind-mounted
	// read-only at /template inside the container. The coding-agent
	// runtime uses it so a scaffold command like `cp -a /template/.
	// /workspace/` can seed a new project from a local template checkout
	// without baking it into the image or cloning over the network. Empty
	// for ordinary sandboxes.
	templateMount string
	// extraVolumes are raw `-v` specs appended verbatim to docker create
	// (e.g. "fastclaw-pnpm-store:/pnpm-store" to share a pnpm content store
	// across runtimes so installs skip re-downloading). Named volumes live
	// in the Docker VM (fast on macOS, unlike bind mounts) and persist
	// across container recreation.
	extraVolumes []string
	// publishPorts maps a container-internal port to a host port. A host
	// value of 0 asks Docker to pick a free ephemeral port, which is then
	// read back with HostPortFor after Create. Used by the coding-agent
	// runtime to expose a project's dev server (e.g. container :3000) so
	// the preview gateway can reverse-proxy to it. Empty for ordinary
	// turn-scoped sandboxes — they never serve traffic.
	publishPorts map[int]int
	policy       *Policy
	env          map[string]string
	mu           sync.Mutex
}

// NewDockerSandbox creates a new sandbox configuration (container is created lazily).
//
// Default policy leaves NetMode unset, which means Docker uses the
// default bridge network (= internet access). Most product agents
// need outbound HTTP for LLM provider calls / image APIs / pip
// installs; locking the sandbox down with NetMode="none" used to be
// the default and silently broke generate-image-style skills with
// DNS resolution errors. Operators that want hard isolation pass an
// explicit policy with NetMode: "none".
func NewDockerSandbox(image, workspace string, policy *Policy) *DockerSandbox {
	if image == "" {
		image = DefaultImage
	}
	if policy == nil {
		policy = &Policy{}
	}
	return &DockerSandbox{
		image:     image,
		workspace: workspace,
		policy:    policy,
		env:       make(map[string]string),
	}
}

// SetWorkdir overrides the container's starting working directory.
// Empty restores the default (/workspace). Must be called before
// Create() — once the container exists the workdir is baked in.
func (s *DockerSandbox) SetWorkdir(wd string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workdir = wd
}

// SetSkillDirs configures host paths whose contents (skill folders)
// should be visible inside the sandbox at /skills/<skill-name>/. The
// LLM is told to invoke skills via `python /skills/<name>/main.py`,
// so without these mounts the script files don't exist in the
// container. Passed paths are mounted read-only.
func (s *DockerSandbox) SetSkillDirs(dirs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skillDirs = append(s.skillDirs[:0], dirs...)
}

// SetUserSkillsHostDir tells Create() to bind-mount this host directory
// RW at /root/.agents/skills inside the sandbox — the location
// `npx skills add -g -y` writes to. Empty value disables the mount
// (no per-user persistence). Caller is responsible for the directory
// existing; Create() will mkdir it defensively but a permission error
// silently degrades to "no mount" rather than failing sandbox start.
func (s *DockerSandbox) SetUserSkillsHostDir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userSkillsHostDir = dir
}

// SetTemplateMount bind-mounts a host template directory read-only at
// /template inside the container. Must be called before Create(). Empty
// disables the mount.
func (s *DockerSandbox) SetTemplateMount(hostDir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.templateMount = hostDir
}

// SetExtraVolumes appends raw `docker -v` specs (e.g.
// "fastclaw-pnpm-store:/pnpm-store"). Must be called before Create().
func (s *DockerSandbox) SetExtraVolumes(specs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.extraVolumes = append(s.extraVolumes[:0], specs...)
}

// SetPublishPorts configures container→host port publishing. Keys are
// container-internal ports; a value of 0 lets Docker assign a free host
// port (read it back with HostPortFor after Create). Must be called
// before Create() — port bindings are baked into `docker create`.
func (s *DockerSandbox) SetPublishPorts(ports map[int]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishPorts = make(map[int]int, len(ports))
	for c, h := range ports {
		s.publishPorts[c] = h
	}
}

// HostPortFor returns the host port Docker bound to the given
// container-internal port. Resolves via `docker port` so it works even
// when the host port was auto-assigned (SetPublishPorts value 0). Errors
// if the container isn't created or the port wasn't published.
func (s *DockerSandbox) HostPortFor(containerPort int) (int, error) {
	s.mu.Lock()
	id := s.containerID
	s.mu.Unlock()
	if id == "" {
		return 0, errors.New("sandbox: container not created")
	}
	out, err := exec.Command("docker", "port", id, fmt.Sprintf("%d/tcp", containerPort)).Output()
	if err != nil {
		return 0, fmt.Errorf("docker port %d: %w", containerPort, err)
	}
	// Output is like "0.0.0.0:49153\n[::]:49153\n"; take the first line's
	// trailing port.
	line := strings.TrimSpace(string(out))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	idx := strings.LastIndexByte(line, ':')
	if idx < 0 {
		return 0, fmt.Errorf("docker port: unexpected output %q", line)
	}
	port, err := strconv.Atoi(strings.TrimSpace(line[idx+1:]))
	if err != nil {
		return 0, fmt.Errorf("docker port: parse %q: %w", line, err)
	}
	return port, nil
}

// SetEnv sets environment variables to inject into the container.
func (s *DockerSandbox) SetEnv(env map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range env {
		s.env[k] = v
	}
}

// Create creates the Docker container.
func (s *DockerSandbox) Create() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.containerID != "" {
		return nil // already created
	}

	args := []string{
		"create",
		"--interactive",
		"--label", "fastclaw=sandbox",
	}

	// Inherit the host's HTTP(S)_PROXY config so curl / pip / npm / git
	// inside the sandbox can reach blocked origins through whatever
	// proxy fastclaw itself uses. Without this, in restricted networks
	// (GFW etc.) DNS for the target domain resolves to a sinkhole and
	// the container sees TLS resets surfaced as NS_ERROR_NET_INTERRUPT
	// in Camoufox / Playwright. Localhost-bound proxy URLs are rewritten
	// to host.docker.internal — `127.0.0.1` inside the container means
	// the container itself, never the host. Cloud deploys typically have
	// no proxy env, so this whole loop is a no-op there.
	rewroteToHostInternal := false
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
		v := os.Getenv(k)
		if v == "" {
			continue
		}
		// Only rewrite the proxy URL itself, not NO_PROXY's bypass list.
		switch k {
		case "HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy":
			if strings.Contains(v, "127.0.0.1") || strings.Contains(v, "localhost") {
				v = strings.ReplaceAll(v, "127.0.0.1", "host.docker.internal")
				v = strings.ReplaceAll(v, "localhost", "host.docker.internal")
				rewroteToHostInternal = true
			}
		}
		// Preserve operator-supplied env (SetEnv) — explicit beats inherited.
		if _, set := s.env[k]; !set {
			args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
		}
	}

	// Only force-resolve host.docker.internal when we actually rewrote a
	// proxy URL to point at it. Always-adding the flag would be harmless
	// on modern Docker (20.10+ host-gateway keyword), but adding it
	// conditionally keeps the cloud path — where no proxy env is set —
	// byte-identical to the pre-change behavior and avoids duplicating
	// the host.docker.internal entry that Docker Desktop / OrbStack
	// already inject themselves.
	if rewroteToHostInternal {
		args = append(args, "--add-host", "host.docker.internal:host-gateway")
	}

	// Mount workspace
	if s.workspace != "" {
		args = append(args, "-v", fmt.Sprintf("%s:/workspace:rw", s.workspace))
		wd := s.workdir
		if wd == "" {
			wd = "/workspace"
		}
		args = append(args, "-w", wd)
	}

	// Mount each skill dir read-only at /skills/<basename>/. The LLM
	// is told to invoke skills via `python /skills/<name>/main.py`,
	// so without these mounts the script files don't exist in the
	// container. Auto-default to FASTCLAW_HOME/skills/ when no dirs
	// are explicitly set, so a freshly-installed product agent works
	// without operators having to wire SetSkillDirs themselves.
	dirs := s.skillDirs
	if len(dirs) == 0 {
		if h := os.Getenv("FASTCLAW_HOME"); h != "" {
			dirs = []string{filepath.Join(h, "skills")}
		} else if home, err := os.UserHomeDir(); err == nil {
			dirs = []string{filepath.Join(home, ".fastclaw", "skills")}
		}
	}
	mounted := make(map[string]bool)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			// Dedupe by container path. Earlier dirs win (per-agent
			// before global), matching SkillsLoader's precedence.
			if mounted[e.Name()] {
				continue
			}
			mounted[e.Name()] = true
			host := filepath.Join(dir, e.Name())
			args = appendSkillMounts(args, host, "/skills/"+e.Name())
		}
	}

	// Per-user RW mount for `npx skills add -g -y` — the CLI writes its
	// global install to /root/.agents/skills/<name>/ inside the sandbox,
	// so binding that path to the chatter's per-user host dir means
	// installs persist on host disk and SkillsLoader picks them up on
	// the next turn without any extra plumbing. mkdir is defensive;
	// silent on failure so a busted host dir degrades to "agent install
	// fails inside sandbox" instead of "sandbox refuses to start".
	if s.userSkillsHostDir != "" {
		if err := os.MkdirAll(s.userSkillsHostDir, 0o755); err == nil {
			args = append(args, "-v", fmt.Sprintf("%s:/root/.agents/skills:rw", s.userSkillsHostDir))
		}
	}

	// Resource limits
	if s.policy.MaxCPU != "" {
		args = append(args, "--cpus", s.policy.MaxCPU)
	}
	if s.policy.MaxMemory != "" {
		args = append(args, "--memory", s.policy.MaxMemory)
	}

	// Network mode
	if s.policy.NetMode != "" {
		args = append(args, "--network", s.policy.NetMode)
	}

	// Template mount (coding-agent runtime scaffolding). Read-only so a
	// scaffold copies OUT of it; the project's own files live in the
	// /workspace bind mount.
	if s.templateMount != "" {
		args = append(args, "-v", fmt.Sprintf("%s:/template:ro", s.templateMount))
	}

	// Extra volumes (e.g. the shared pnpm store).
	for _, v := range s.extraVolumes {
		args = append(args, "-v", v)
	}

	// Port publishing (coding-agent runtime previews). Bind to 127.0.0.1
	// on the host, never 0.0.0.0 — the preview gateway is the only thing
	// that should reach the dev server, and it runs on the same host /
	// proxies over the Docker network. Exposing 0.0.0.0 here would put
	// every project's dev server (running LLM-generated code) directly on
	// the box's public interface. Host value 0 → Docker picks a free
	// port, read back via HostPortFor.
	for containerPort, hostPort := range s.publishPorts {
		if hostPort > 0 {
			args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d", hostPort, containerPort))
		} else {
			args = append(args, "-p", fmt.Sprintf("127.0.0.1::%d", containerPort))
		}
	}

	// Environment variables
	for k, v := range s.env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, s.image, "tail", "-f", "/dev/null")

	cmd := exec.Command("docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker create: %s: %w", strings.TrimSpace(stderr.String()), err)
	}

	s.containerID = strings.TrimSpace(stdout.String())

	// Start the container
	startCmd := exec.Command("docker", "start", s.containerID)
	if out, err := startCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker start: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return nil
}

// appendSkillMounts binds a host skill directory into the container at
// containerDir, EXCLUDING the top-level SKILL.md. The manifest is the
// agent's IP — the model already gets its contents via the load_skill
// tool, and the sandbox only needs the skill's executable scripts /
// resources to run `python /skills/<name>/main.py`. Leaving SKILL.md out
// of the mount closes the `cat /skills/<name>/SKILL.md` exfil path that a
// whole-directory `:ro` bind would otherwise expose.
//
// It mounts each top-level entry individually rather than the whole dir
// so the exclusion is a true physical absence (no nested over-mount
// tricks). If the directory can't be enumerated, it falls back to a
// whole-dir mount so the skill still works — a rare path where the
// manifest isn't hidden, but a broken skill is worse than a hidden one.
func appendSkillMounts(args []string, hostSkillDir, containerDir string) []string {
	entries, err := os.ReadDir(hostSkillDir)
	if err != nil {
		return append(args, "-v", fmt.Sprintf("%s:%s:ro", hostSkillDir, containerDir))
	}
	for _, e := range entries {
		if e.Name() == "SKILL.md" {
			continue
		}
		src := filepath.Join(hostSkillDir, e.Name())
		args = append(args, "-v", fmt.Sprintf("%s:%s/%s:ro", src, containerDir, e.Name()))
	}
	return args
}

// Exec runs a command inside the container.
func (s *DockerSandbox) Exec(ctx context.Context, command string, workdir string) (string, error) {
	s.mu.Lock()
	if s.containerID == "" {
		s.mu.Unlock()
		if err := s.Create(); err != nil {
			return "", err
		}
		s.mu.Lock()
	}
	id := s.containerID
	s.mu.Unlock()

	args := []string{"exec"}
	if workdir != "" {
		args = append(args, "-w", workdir)
	}
	args = append(args, id, "sh", "-c", command)

	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.CombinedOutput()
	result := string(output)
	if err != nil {
		return fmt.Sprintf("%s\nError: %s", result, err.Error()), err
	}
	return result, nil
}

// ExecWithStdin runs a command inside the container with the given bytes
// piped to stdin. Used for writing binary files (PNG, audio, ...) — passing
// raw bytes through argv (as our heredoc-based WriteFile does) blows up
// with "fork/exec: invalid argument" the moment the content contains a
// NULL byte, because execve rejects NUL inside argv elements.
func (s *DockerSandbox) ExecWithStdin(ctx context.Context, command string, workdir string, stdin io.Reader) (string, error) {
	s.mu.Lock()
	if s.containerID == "" {
		s.mu.Unlock()
		if err := s.Create(); err != nil {
			return "", err
		}
		s.mu.Lock()
	}
	id := s.containerID
	s.mu.Unlock()

	args := []string{"exec", "-i"}
	if workdir != "" {
		args = append(args, "-w", workdir)
	}
	args = append(args, id, "sh", "-c", command)

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = stdin
	output, err := cmd.CombinedOutput()
	result := string(output)
	if err != nil {
		return fmt.Sprintf("%s\nError: %s", result, err.Error()), err
	}
	return result, nil
}

// Close stops and removes the container.
func (s *DockerSandbox) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.containerID == "" {
		return nil
	}

	cmd := exec.Command("docker", "rm", "-f", s.containerID)
	cmd.CombinedOutput() // best effort
	s.containerID = ""
	return nil
}

// ContainerID returns the current container ID, or empty if not created.
func (s *DockerSandbox) ContainerID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.containerID
}
