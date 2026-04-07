package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/fastclaw-ai/fastclaw/internal/daemon"
)

// daemonCmd handles daemon/service management subcommands.
func daemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the FastClaw gateway daemon",
	}
	cmd.AddCommand(daemonStartCmd())
	cmd.AddCommand(daemonStopCmd())
	cmd.AddCommand(daemonRestartCmd())
	cmd.AddCommand(daemonStatusCmd())
	cmd.AddCommand(daemonLogsCmd())
	cmd.AddCommand(daemonInstallCmd())
	cmd.AddCommand(daemonUninstallCmd())
	cmd.AddCommand(daemonRunCmd()) // internal, hidden
	return cmd
}

func daemonStartCmd() *cobra.Command {
	var port int
	var addr string
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the gateway as a background daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			return daemon.Start(addr, port)
		},
	}
	cmd.Flags().IntVar(&port, "port", 18953, "port for gateway")
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1", "listen address for gateway web UI/API")
	return cmd
}

func daemonStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the running daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			return daemon.Stop()
		},
	}
}

func daemonRestartCmd() *cobra.Command {
	var port int
	var addr string
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Stop (ignore error if not running)
			_ = daemon.Stop()
			time.Sleep(500 * time.Millisecond)
			return daemon.Start(addr, port)
		},
	}
	cmd.Flags().IntVar(&port, "port", 18953, "port for gateway")
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1", "listen address for gateway web UI/API")
	return cmd
}

func daemonStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon status",
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := daemon.GetStatus()
			if err != nil {
				return err
			}

			if !st.Running {
				fmt.Println("Status: stopped")
				return nil
			}

			fmt.Printf("Status: running\n")
			fmt.Printf("PID:    %d\n", st.PID)
			fmt.Printf("Uptime: %s\n", st.Uptime.Round(time.Second))

			_, logFile, _, _ := daemon.Paths()
			fmt.Printf("Logs:   %s\n", logFile)
			return nil
		},
	}
}

func daemonLogsCmd() *cobra.Command {
	var follow bool
	var lines int
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show daemon log output",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, logFile, _, err := daemon.Paths()
			if err != nil {
				return err
			}

			if _, err := os.Stat(logFile); os.IsNotExist(err) {
				return fmt.Errorf("no log file found at %s", logFile)
			}

			tailArgs := []string{"-n", fmt.Sprintf("%d", lines)}
			if follow {
				tailArgs = append(tailArgs, "-f")
			}
			tailArgs = append(tailArgs, logFile)

			tailCmd := exec.Command("tail", tailArgs...)
			tailCmd.Stdout = os.Stdout
			tailCmd.Stderr = os.Stderr
			return tailCmd.Run()
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	cmd.Flags().IntVarP(&lines, "lines", "n", 50, "Number of lines to show")
	return cmd
}

func daemonInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install FastClaw as an OS service (launchd/systemd)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return daemon.Install()
		},
	}
}

func daemonUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the FastClaw OS service",
		RunE: func(cmd *cobra.Command, args []string) error {
			return daemon.Uninstall()
		},
	}
}

// daemonRunCmd is the internal command used by 'daemon start' to run the auto-restart loop.
func daemonRunCmd() *cobra.Command {
	var port int
	var addr string
	cmd := &cobra.Command{
		Use:    "__run",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return daemon.RunLoop(addr, port)
		},
	}
	cmd.Flags().IntVar(&port, "port", 18953, "port for gateway")
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1", "listen address for gateway web UI/API")
	return cmd
}
