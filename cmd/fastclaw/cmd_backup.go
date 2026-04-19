package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// backupCmd creates a tar.gz backup of ~/.fastclaw.
func backupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "backup",
		Short: "Create a tar.gz backup of ~/.fastclaw",
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, err := config.HomeDir()
			if err != nil {
				return err
			}

			timestamp := time.Now().Format("20060102-150405")
			backupFile := fmt.Sprintf("fastclaw-backup-%s.tar.gz", timestamp)

			tarCmd := exec.Command("tar", "-czf", backupFile, "-C", filepath.Dir(homeDir), filepath.Base(homeDir))
			tarCmd.Stdout = os.Stdout
			tarCmd.Stderr = os.Stderr
			if err := tarCmd.Run(); err != nil {
				return fmt.Errorf("backup failed: %w", err)
			}

			cwd, _ := os.Getwd()
			fmt.Printf("Backup created: %s\n", filepath.Join(cwd, backupFile))
			return nil
		},
	}
}

// resetCmd deletes sessions and memory but keeps config.
func resetCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Delete sessions and memory (keeps config)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				fmt.Print("This will delete all sessions and memory. Are you sure? (use --yes to confirm): ")
				return fmt.Errorf("aborted: use --yes flag to confirm")
			}

			_ = config.MigrateLegacyLayout()
			userDir, err := config.UserDir(config.DefaultUserID)
			if err != nil {
				return err
			}

			agentsDir := filepath.Join(userDir, "agents")
			entries, err := os.ReadDir(agentsDir)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Println("Nothing to reset.")
					return nil
				}
				return err
			}

			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				agentDir := filepath.Join(agentsDir, entry.Name(), "agent")

				// Clear sessions
				sessDir := filepath.Join(agentDir, "sessions")
				os.RemoveAll(sessDir)
				os.MkdirAll(sessDir, 0o755)

				// Clear memory
				memDir := filepath.Join(agentDir, "memory")
				os.RemoveAll(memDir)
				os.MkdirAll(memDir, 0o755)

				// Clear MEMORY.md content but keep file
				memFile := filepath.Join(agentDir, "MEMORY.md")
				if _, err := os.Stat(memFile); err == nil {
					os.WriteFile(memFile, []byte("# Memory\n\n"), 0o644)
				}

				fmt.Printf("Reset agent: %s\n", entry.Name())
			}

			fmt.Println("Reset complete.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip confirmation")
	return cmd
}
