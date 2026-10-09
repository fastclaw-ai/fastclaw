package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/api"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/connectors"
	"github.com/fastclaw-ai/fastclaw/internal/daemon"
	"github.com/fastclaw-ai/fastclaw/internal/gateway"
	coderuntime "github.com/fastclaw-ai/fastclaw/internal/runtime"
	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/setup"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// apiResolver adapts *gateway.Gateway to api.UserResolver.
type apiResolver struct {
	gw *gateway.Gateway
}

func (a *apiResolver) UserSpaceFor(userID string) (*api.UserSpaceView, error) {
	sp, err := a.gw.UserSpaceFor(userID)
	if err != nil {
		return nil, err
	}
	return &api.UserSpaceView{
		UserID: sp.UserID,
		Agents: sp.Agents,
		Config: sp.Config,
	}, nil
}

func (a *apiResolver) LocalAgentManager() *agent.Manager { return a.gw.LocalAgentManager() }
func (a *apiResolver) IsCloudMode() bool                 { return a.gw.IsCloudMode() }
func (a *apiResolver) InvalidateUser(userID string)      { a.gw.InvalidateUser(userID) }

// InvalidateAgent forwards to the gateway so agent-scope mutations
// (PUT /api/agents/{id} model change, agent-scope provider/setting
// writes) actually drop the cached UserSpace. Without this method on
// the resolver, setup.invalidateAgent's type assertion silently fails
// and chat keeps firing the pre-change model until the 30-min idle
// eviction kicks in.
func (a *apiResolver) InvalidateAgent(agentID string) { a.gw.InvalidateAgent(agentID) }

func (a *apiResolver) EnsureAgent(ctx context.Context, userID, agentID string) error {
	return a.gw.EnsureAgent(ctx, userID, agentID)
}

// ReloadAgents drops every cached UserSpace so each one reloads on the
// next request. setup.invalidateScope's system-scope branch type-
// asserts the resolver to this interface — without the method the
// assertion silently fails and a system-scope settings save (sandbox,
// agents.defaults, …) leaves the running gateway pinned to its pre-
// save snapshot, surfacing as "model is empty" mysteries on the next
// chat turn.
func (a *apiResolver) ReloadAgents() error { return a.gw.ReloadAgents() }

// ReloadSandbox rebuilds the gateway-owned sandbox executor pool after
// system-scope sandbox settings change, avoiding a manual process restart.
func (a *apiResolver) ReloadSandbox() error { return a.gw.ReloadSandbox() }

// RegisterChannelFromConfig hot-starts a freshly-saved channel row.
// Called by setup handlers after they persist a new bot config so the
// adapter starts polling without a process restart.
func (a *apiResolver) RegisterChannelFromConfig(rec store.ConfigRecord) error {
	return a.gw.RegisterChannelFromConfig(rec)
}

// RegisterChannel hot-starts a freshly-saved ChannelRecord.
func (a *apiResolver) RegisterChannel(rec store.ChannelRecord) error {
	return a.gw.RegisterChannel(rec)
}

func (a *apiResolver) UnregisterChannel(channelType, accountID string) {
	a.gw.UnregisterChannel(channelType, accountID)
}

func (a *apiResolver) DispatchFeishuWebhook(accountID string, body []byte) ([]byte, int, error) {
	return a.gw.DispatchFeishuWebhook(accountID, body)
}

func (a *apiResolver) DispatchLINEWebhook(accountID string, body []byte, signature, publicBase string) ([]byte, int, error) {
	return a.gw.DispatchLINEWebhook(accountID, body, signature, publicBase)
}

func (a *apiResolver) ServeLINEMedia(accountID, name string) ([]byte, string, error) {
	return a.gw.ServeLINEMedia(accountID, name)
}

func main() {
	var showVersion bool
	rootCmd := &cobra.Command{
		Use:   "fastclaw",
		Short: "FastClaw - Multi-User AI Agent Platform",
		// Long-running services (gateway, daemon) narrate at INFO; one-shot
		// CLI commands must not. Every `fastclaw agents ls` was prefixing
		// its output with two lines of storage/migration chatter, which is
		// noise for a human and context pollution for an agent shelling out
		// to us — eight CLI calls in one turn meant sixteen wasted lines,
		// every one of them re-sent to the model on the next request.
		//
		// runGateway raises the level back to INFO for its own process.
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
				Level: cliLogLevel(),
			})))
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if showVersion {
				versionCmd().Run(cmd, args)
				return nil
			}
			if isInteractiveTerminal(os.Stdin, os.Stdout) {
				return runChat(cmd.Context(), chatOptions{})
			}
			return runGateway(18953)
		},
	}
	rootCmd.Flags().BoolVar(&showVersion, "version", false, "Print FastClaw version")

	rootCmd.AddCommand(gatewayCmd())
	rootCmd.AddCommand(chatCmd())
	rootCmd.AddCommand(skillCmd())
	rootCmd.AddCommand(versionCmd())
	rootCmd.AddCommand(upgradeCmd())
	rootCmd.AddCommand(pluginCmd())
	rootCmd.AddCommand(providerCmd())
	rootCmd.AddCommand(sandboxCmd())
	rootCmd.AddCommand(policyCmd())
	rootCmd.AddCommand(daemonCmd())
	rootCmd.AddCommand(logCmd())
	rootCmd.AddCommand(adminCmd())
	rootCmd.AddCommand(apikeyCmd())
	rootCmd.AddCommand(agentsCmd())
	rootCmd.AddCommand(sessionCmd())
	rootCmd.AddCommand(cronCmd())
	rootCmd.AddCommand(channelsCmd())
	rootCmd.AddCommand(toolsCmd())
	rootCmd.AddCommand(usageCmd())
	rootCmd.AddCommand(projectsCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// cliLogLevel is the slog level for one-shot CLI commands: quiet by
// default, opened up via FASTCLAW_LOG_LEVEL for debugging. Warnings and
// errors still print, so nothing that needs the operator's attention is
// suppressed — only the routine "here's what I'm doing" narration.
func cliLogLevel() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("FASTCLAW_LOG_LEVEL"))) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "error":
		return slog.LevelError
	default:
		return slog.LevelWarn
	}
}

func gatewayCmd() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Start the FastClaw gateway (foreground; use `fastclaw daemon` for background management)",
		// NoArgs so `fastclaw gateway restart` errors instead of silently
		// starting a second foreground gateway — restart/stop/status live
		// under `fastclaw daemon`.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGateway(port)
		},
	}
	cmd.Flags().IntVar(&port, "port", 18953, "port for setup wizard / web UI")
	return cmd
}

func runGateway(port int) error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	env := config.LoadEnv()
	if env.Gateway.Port > 0 {
		port = env.Gateway.Port
	}

	if home, err := config.HomeDir(); err == nil {
		slog.Info("fastclaw instance", "home", home, "port", port)
	}

	agent.InstallBundledSkills()

	if err := daemon.WritePIDFile(); err != nil {
		slog.Warn("failed to write PID file", "error", err)
	}
	defer daemon.RemovePIDFile()

	gw, err := gateway.New(env)
	if err != nil {
		return fmt.Errorf("create gateway: %w", err)
	}

	// The person's connected third-party accounts (internal/connectors):
	// configured in System → Tools → Connectors, else from
	// FASTCLAW_CONNANY_URL + FASTCLAW_CONNANY_API_KEY; off when neither.
	connectors.SetEnvFallback(connectors.Settings{URL: env.Connany.URL, APIKey: env.Connany.APIKey})
	if _, err := connectors.Apply(context.Background(), gw.Store()); err != nil {
		slog.Error("connectors disabled", "error", err)
	}

	// Remove credential-bearing env vars from the process environment
	// now that boot config has been read. Closes the /proc/<pid>/environ
	// path that a shell-having LLM could otherwise use to recover the
	// daemon's storage DSN and object-store keys. See
	// config.ScrubBootSecrets for the trade-off note.
	config.ScrubBootSecrets()

	authResolver, err := auth.NewResolver(gw.Store())
	if err != nil {
		return fmt.Errorf("create auth resolver: %w", err)
	}

	gwCfg := &config.GatewayCfg{
		Port: port,
		Bind: env.Gateway.Bind,
		HTTP: config.GatewayHTTP{
			Endpoints: config.GatewayHTTPEndpoints{
				ChatCompletions: config.GatewayEndpoint{Enabled: true},
				Agents:          config.GatewayEndpoint{Enabled: true},
			},
		},
	}

	webSrv := setup.NewServer(port)
	webSrv.SetTaskQueue(gw.TaskQueue())
	webSrv.SetGatewayConfig(gwCfg)
	webSrv.SetUserResolver(&apiResolver{gw: gw})
	webSrv.SetStore(gw.Store())
	webSrv.SetWorkspaceStore(gw.Workspace())
	webSrv.SetUsageMeter(gw.Usage())
	webSrv.SetAuth(authResolver)
	webSrv.SetWebChannel(gw.WebChannel())
	// Share the chat-event hub so bus-fired web turns (cron / goal
	// continuation / heartbeat / sub-agent) stream through the same
	// SSE pipeline a user-typed turn uses. Must be wired before
	// gw.Run() starts the bus consumer.
	gw.SetChatEvents(webSrv.ChatEventHub())

	apiSrv := api.NewServer(&apiResolver{gw: gw}, authResolver, gwCfg)
	apiSrv.SetMeter(gw.Usage())
	apiSrv.SetQuotaStore(gw.QuotaStore())
	apiSrv.SetStore(gw.Store())
	apiSrv.SetWorkspaceStore(gw.Workspace())
	webSrv.SetAPIServer(apiSrv)

	// Coding-agent project runtime: long-lived dev-server sandbox +
	// preview URL, layered on top of the existing project feature. Wired
	// for the docker sandbox backend; other backends leave it nil and the
	// /runtime endpoints return 503. Template scaffold/dev commands are
	// env-overridable so fastclaw stays template-agnostic — the default
	// targets a ShipAny image with the template baked at /template.
	if home, herr := config.HomeDir(); herr == nil {
		// backend + shared pool let the runtime host previews through the
		// agent's pooled executor on non-docker backends (e2b/boxlite);
		// docker keeps its dedicated-container path. Pool is nil when
		// sandboxing is disabled, which keeps the docker path.
		rtMgr := coderuntime.NewManager(
			gw.Store(), home, env.Sandbox.Image,
			&sandbox.Policy{}, os.Getenv("FASTCLAW_PREVIEW_BASE"),
			env.Sandbox.Backend, gw.SandboxPool())
		rtMgr.RegisterTemplate("shipany-tanstack", coderuntime.TemplateSpec{
			DevPort: 3000,
			// Default scaffold, validated end-to-end against
			// thinkany/fastclaw-sandbox (node+npm, no pnpm):
			//   1. copy the template EXCLUDING node_modules — the host
			//      checkout's are platform-specific + huge; a fresh
			//      in-container install is correct.
			//   2. ensure pnpm (base image ships node+npm only).
			//   3. verify-deps-before-run=false — pnpm 11 otherwise re-runs
			//      `install` before every `dev`, which keeps failing on the
			//      ignored-builds gate and aborts the dev server.
			//   4. `install || true` — the ignored-builds gate exits non-zero
			//      AFTER deps are on disk, so tolerate it.
			//   5. `pnpm rebuild` — actually build esbuild/sharp so Vite runs.
			// Override wholesale with FASTCLAW_SHIPANY_SCAFFOLD.
			ScaffoldCmd: envOr("FASTCLAW_SHIPANY_SCAFFOLD",
				"set -e; if [ -d /template ]; then tar -C /template "+
					"--exclude=node_modules --exclude=.git --exclude=.output --exclude=dist "+
					"-cf - . | tar -C /workspace -xf -; fi; cd /workspace; "+
					"command -v pnpm >/dev/null 2>&1 || npm i -g pnpm; "+
					// Point pnpm at the shared store volume so installs after the
					// first reuse downloaded packages instead of re-fetching.
					"pnpm config set store-dir /pnpm-store 2>/dev/null || true; "+
					"pnpm config set verify-deps-before-run false; "+
					"pnpm install || true; pnpm rebuild; "+
					// Snapshot the pristine template as a git baseline so the UI
					// can later show ONLY the files the agent changed (git status
					// vs this commit). .gitignore already excludes node_modules /
					// build output, so the diff stays clean.
					"git config --global --add safe.directory '*' 2>/dev/null || true; "+
					"git init -q 2>/dev/null || true; git add -A 2>/dev/null || true; "+
					"git -c user.email=bot@fastclaw -c user.name=fastclaw commit -q -m baseline 2>/dev/null || true"),
			// Self-heal pnpm before running: the base image ships node+npm
			// only, and pnpm is installed globally (container FS, NOT the
			// bind-mounted /workspace). On a wake/re-up the container is
			// recreated but the workspace already has files, so scaffold is
			// skipped and the global pnpm is gone — `pnpm dev` would then
			// fail "pnpm: not found". Ensuring it here makes DevCmd robust
			// across container recreation without re-running the scaffold.
			// Self-heal pnpm + its run-time config before starting dev. Both
			// live in the container FS (global pnpm install + ~/.pnpmrc), NOT
			// the bind-mounted /workspace, so a wake/re-up that recreates the
			// container loses them while scaffold is skipped (workspace
			// non-empty). Without `verify-deps-before-run false`, pnpm 11
			// re-runs a deps-status check before `dev` that fails against the
			// volume-mounted node_modules and aborts the server.
			// Self-heal pnpm, its config, AND deps before dev. All three live
			// outside the bind-mounted /workspace (global pnpm in container
			// FS; node_modules in a per-scope volume that Stop removes), so a
			// re-up after Stop recreates the container with files present
			// (scaffold skipped) but no deps — `pnpm dev` would then die with
			// "vite: not found". Reinstalling when node_modules/.bin/vite is
			// missing makes every boot self-correct (fast: the shared pnpm
			// store volume hard-links, no re-download).
			DevCmd: envOr("FASTCLAW_SHIPANY_DEV",
				"command -v pnpm >/dev/null 2>&1 || npm i -g pnpm; "+
					"pnpm config set verify-deps-before-run false 2>/dev/null || true; "+
					"[ -x node_modules/.bin/vite ] || pnpm install || true; "+
					"pnpm dev --host 0.0.0.0 --port 3000"),
			// Local template checkout bind-mounted at /template (option C):
			// set FASTCLAW_SHIPANY_TEMPLATE_DIR=~/code/shipany-tanstack to
			// scaffold from disk without baking the image or cloning.
			TemplateMount: os.Getenv("FASTCLAW_SHIPANY_TEMPLATE_DIR"),
		})
		// A second, lighter template demonstrating multi-template support:
		// a plain Vite + React + TS starter. It needs no baked /template and
		// no R2 source — it self-scaffolds with `npm create vite` — and shares
		// the SAME sandbox image as shipany (Node toolchain), so it proves the
		// "one e2b image, many templates differing only by ScaffoldCmd" model
		// (no per-template image needed for same-stack templates). Vite's HMR
		// client follows the page origin, so it works through both docker's
		// host-port map and e2b's <port>-<id>.e2b.app proxy with no extra
		// config. shipany-tanstack above is untouched.
		rtMgr.RegisterTemplate("vite-react", coderuntime.TemplateSpec{
			DevPort: 5173,
			ScaffoldCmd: envOr("FASTCLAW_VITE_REACT_SCAFFOLD",
				"set -e; cd /workspace; "+
					"if [ ! -f package.json ]; then "+
					"npm create vite@latest .fctmp -- --template react-ts && "+
					"cp -a .fctmp/. ./ && rm -rf .fctmp; fi; "+
					"npm install; "+
					// Git baseline so the UI's changed-files view diffs against
					// the pristine scaffold — same pattern as shipany above.
					"git config --global --add safe.directory '*' 2>/dev/null || true; "+
					"git init -q 2>/dev/null || true; git add -A 2>/dev/null || true; "+
					"git -c user.email=bot@fastclaw -c user.name=fastclaw commit -q -m baseline 2>/dev/null || true"),
			DevCmd: envOr("FASTCLAW_VITE_REACT_DEV",
				"[ -x node_modules/.bin/vite ] || npm install; "+
					"npm run dev -- --host 0.0.0.0 --port 5173"),
		})
		webSrv.SetRuntimeManager(rtMgr) // HTTP /runtime endpoints
		gw.SetProjectRuntime(rtMgr)     // agent preview tools
		slog.Info("project runtime enabled",
			"previewBase", os.Getenv("FASTCLAW_PREVIEW_BASE"),
			"templateDir", os.Getenv("FASTCLAW_SHIPANY_TEMPLATE_DIR"))
	}

	bindMode := gwCfg.Bind
	if bindMode == "" {
		bindMode = "loopback"
	}
	slog.Info("gateway starting", "port", port, "bind", bindMode)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := webSrv.Run(ctx); err != nil {
			slog.Error("web server error", "error", err)
		}
	}()

	url := fmt.Sprintf("http://localhost:%d", port)
	slog.Info("web UI available", "url", url)
	// Auto-open the browser when this looks like a fresh install.
	if n, _ := countUsersSafe(gw); n == 0 {
		go openBrowser(url)
	}

	return gw.Run()
}

// envOr returns the value of env var key, or def when it's unset/empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func countUsersSafe(gw *gateway.Gateway) (int, error) {
	st := gw.Store()
	if st == nil {
		return 0, nil
	}
	return st.CountUsers(context.Background())
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	cmd.Run()
}
