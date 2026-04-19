package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// sessionCmd handles session management subcommands.
func sessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage sessions",
	}
	cmd.AddCommand(sessionListCmd())
	cmd.AddCommand(sessionClearCmd())
	cmd.AddCommand(sessionClearAllCmd())
	return cmd
}

func sessionListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all sessions across all agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			userDir, err := config.UserDir(config.DefaultUserID)
			if err != nil {
				return err
			}
			_ = config.MigrateLegacyLayout()

			agentsDir := filepath.Join(userDir, "agents")
			entries, err := os.ReadDir(agentsDir)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Println("No agents found.")
					return nil
				}
				return err
			}

			found := false
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				sessDir := filepath.Join(agentsDir, entry.Name(), "agent", "sessions")
				sessFiles, err := os.ReadDir(sessDir)
				if err != nil {
					continue
				}
				for _, sf := range sessFiles {
					if sf.IsDir() || !strings.HasSuffix(sf.Name(), ".jsonl") {
						continue
					}
					found = true
					info, _ := sf.Info()
					size := int64(0)
					if info != nil {
						size = info.Size()
					}
					sessKey := strings.TrimSuffix(sf.Name(), ".jsonl")
					fmt.Printf("  agent=%-12s session=%-30s size=%d bytes\n", entry.Name(), sessKey, size)
				}
			}
			if !found {
				fmt.Println("No sessions found.")
			}
			return nil
		},
	}
}

func sessionClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear <session-key>",
		Short: "Clear a specific session (agent:channel_chatid format or just the filename)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			userDir, err := config.UserDir(config.DefaultUserID)
			if err != nil {
				return err
			}
			_ = config.MigrateLegacyLayout()

			agentsDir := filepath.Join(userDir, "agents")
			entries, err := os.ReadDir(agentsDir)
			if err != nil {
				return err
			}

			removed := 0
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				sessFile := filepath.Join(agentsDir, entry.Name(), "agent", "sessions", key+".jsonl")
				if _, err := os.Stat(sessFile); err == nil {
					os.Remove(sessFile)
					fmt.Printf("Cleared session: %s (agent: %s)\n", key, entry.Name())
					removed++
				}
			}
			if removed == 0 {
				return fmt.Errorf("session %q not found", key)
			}
			return nil
		},
	}
}

func sessionClearAllCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear-all",
		Short: "Clear all sessions across all agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			userDir, err := config.UserDir(config.DefaultUserID)
			if err != nil {
				return err
			}
			_ = config.MigrateLegacyLayout()

			agentsDir := filepath.Join(userDir, "agents")
			entries, err := os.ReadDir(agentsDir)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Println("No agents found.")
					return nil
				}
				return err
			}

			removed := 0
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				sessDir := filepath.Join(agentsDir, entry.Name(), "agent", "sessions")
				sessFiles, err := os.ReadDir(sessDir)
				if err != nil {
					continue
				}
				for _, sf := range sessFiles {
					if sf.IsDir() || !strings.HasSuffix(sf.Name(), ".jsonl") {
						continue
					}
					os.Remove(filepath.Join(sessDir, sf.Name()))
					removed++
				}
			}
			fmt.Printf("Cleared %d session(s).\n", removed)
			return nil
		},
	}
}
