package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/api"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/daemon"
	"github.com/fastclaw-ai/fastclaw/internal/gateway"
	"github.com/fastclaw-ai/fastclaw/internal/setup"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "fastclaw",
		Short: "FastClaw - Lightweight AI Agent Framework",
		// No args = default to gateway (so double-click on Windows works)
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGateway("127.0.0.1", 18953)
		},
	}

	rootCmd.AddCommand(gatewayCmd())
	rootCmd.AddCommand(agentCmd())
	rootCmd.AddCommand(skillCmd())
	rootCmd.AddCommand(sessionCmd())
	rootCmd.AddCommand(versionCmd())
	rootCmd.AddCommand(upgradeCmd())
	rootCmd.AddCommand(doctorCmd())
	rootCmd.AddCommand(backupCmd())
	rootCmd.AddCommand(resetCmd())
	rootCmd.AddCommand(pluginCmd())
	rootCmd.AddCommand(providerCmd())
	rootCmd.AddCommand(sandboxCmd())
	rootCmd.AddCommand(policyCmd())
	rootCmd.AddCommand(daemonCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func gatewayCmd() *cobra.Command {
	var port int
	var addr string
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Start the FastClaw gateway (loads all agents)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGateway(addr, port)
		},
	}
	cmd.Flags().IntVar(&port, "port", 18953, "port for setup wizard / web UI")
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1", "listen address for setup wizard / web UI / API")
	return cmd
}

func runGateway(addr string, port int) error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Check if config exists
	cfg, err := config.Load()
	if err != nil {
		// Config doesn't exist — run setup wizard
		slog.Info("no config found, starting setup wizard", "url", fmt.Sprintf("http://localhost:%d", port))
		return runSetupWizard(addr, port)
	}

	slog.Info("starting gateway")

	// Write PID file for daemon management
	if err := daemon.WritePIDFile(); err != nil {
		slog.Warn("failed to write PID file", "error", err)
	}
	defer daemon.RemovePIDFile()

	gw, err := gateway.New(cfg)
	if err != nil {
		return fmt.Errorf("create gateway: %w", err)
	}

	// Start web UI server alongside gateway
	gwCfg := &cfg.Gateway
	if gwCfg.Port > 0 {
		port = gwCfg.Port
	}

	webSrv := setup.NewServer(port, nil)
	webSrv.SetAgentProvider(&agentProviderAdapter{mgr: gw.AgentManager()})
	webSrv.SetTaskQueue(gw.TaskQueue())
	webSrv.SetGatewayConfig(gwCfg)
	webSrv.SetListenAddr(addr)

	// Set up OpenAI-compatible API and WebSocket gateway
	gatewayToken := cfg.Gateway.Auth.Token
	apiSrv := api.NewServer(gw.AgentManager(), gatewayToken, gwCfg)
	webSrv.SetAPIServer(apiSrv)

	bindMode := gwCfg.Bind
	if bindMode == "" {
		bindMode = "loopback"
	}
	authMode := gwCfg.Auth.Mode
	if authMode == "" {
		authMode = "token"
	}
	slog.Info("gateway API enabled",
		"port", port,
		"addr", addr,
		"bind", bindMode,
		"auth", authMode,
		"chatCompletions", gwCfg.HTTP.Endpoints.ChatCompletions.Enabled,
	)

	// Write fastclaw.gateway.json for ChatClaw auto-detect
	writeFastClawGatewayConfig(port, gatewayToken)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := webSrv.Run(ctx); err != nil {
			slog.Error("web server error", "error", err)
		}
	}()

	slog.Info("web UI available", "url", fmt.Sprintf("http://localhost:%d", port))

	return gw.Run()
}

// agentProviderAdapter adapts agent.Manager to setup.AgentProvider.
type agentProviderAdapter struct {
	mgr *agent.Manager
}

func (a *agentProviderAdapter) AllAgents() []setup.AgentHandle {
	agents := a.mgr.All()
	result := make([]setup.AgentHandle, len(agents))
	for i, ag := range agents {
		result[i] = ag
	}
	return result
}

func (a *agentProviderAdapter) AgentByID(id string) setup.AgentHandle {
	ag := a.mgr.AgentByID(id)
	if ag == nil {
		return nil
	}
	return ag
}

// writeFastClawGatewayConfig writes ~/.fastclaw/fastclaw.gateway.json for ChatClaw auto-detect.
func writeFastClawGatewayConfig(port int, token string) {
	if token == "" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".fastclaw")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}

	cfg := map[string]any{
		"gateway": map[string]any{
			"port": port,
			"auth": map[string]string{
				"token": token,
			},
		},
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "fastclaw.gateway.json"), data, 0o644); err != nil {
		slog.Warn("failed to write fastclaw.gateway.json", "error", err)
	} else {
		slog.Info("wrote fastclaw.gateway.json for ChatClaw auto-detect")
	}
}

func runSetupWizard(addr string, port int) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := setup.NewServer(port, func(cfg *config.Config) {
		slog.Info("setup complete, config saved")
		// Stop the setup wizard and restart as gateway
		go func() {
			cancel()
		}()
	})
	srv.SetListenAddr(addr)

	// Open browser
	url := fmt.Sprintf("http://localhost:%d", port)
	go openBrowser(url)

	if err := srv.Run(ctx); err != nil {
		return err
	}

	// Config was saved, now start the gateway
	slog.Info("restarting as gateway")
	return runGateway(addr, port)
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
