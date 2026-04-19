package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

func agentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage agents",
	}

	cmd.AddCommand(agentCreateCmd())
	cmd.AddCommand(agentListCmd())

	return cmd
}

func agentCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new agent from template",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			_ = config.MigrateLegacyLayout()
			userDir, err := config.EnsureUserDir(config.DefaultUserID)
			if err != nil {
				return err
			}

			agentDir := filepath.Join(userDir, "agents", name, "agent")

			if _, err := os.Stat(agentDir); err == nil {
				return fmt.Errorf("agent %q already exists at %s", name, agentDir)
			}

			dirs := []string{
				agentDir,
				filepath.Join(agentDir, "memory"),
				filepath.Join(agentDir, "sessions"),
				filepath.Join(agentDir, "skills"),
			}
			for _, dir := range dirs {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return fmt.Errorf("create directory %s: %w", dir, err)
				}
			}

			agentCfg := config.AgentFileConfig{
				Model: "gpt-4o",
			}
			data, _ := json.MarshalIndent(agentCfg, "", "  ")
			if err := os.WriteFile(filepath.Join(agentDir, "agent.json"), data, 0o644); err != nil {
				return err
			}

			bootstrapFiles := map[string]string{
				"AGENTS.md":    "# Agent Capabilities\n\nDescribe what this agent can do.",
				"IDENTITY.md":  fmt.Sprintf("# Identity\n\nYou are %s, a FastClaw AI agent.", name),
				"SOUL.md":      "# Soul\n\nYour personality and behavioral guidelines.",
				"USER.md":      "# User\n\nInformation about the user you serve.",
				"TOOLS.md":     "# Tools\n\nAdditional tool usage instructions.",
				"BOOTSTRAP.md": "# Bootstrap\n\nStartup instructions loaded on every conversation.",
				"HEARTBEAT.md": "# Heartbeat\n\nPeriodic check-in instructions.",
				"MEMORY.md":    "# Memory\n\nLong-term memory for this agent.",
			}
			for filename, content := range bootstrapFiles {
				path := filepath.Join(agentDir, filename)
				if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
					return err
				}
			}

			fmt.Printf("Agent %q created at %s\n", name, agentDir)
			return nil
		},
	}
}

func agentListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = config.MigrateLegacyLayout()
			userDir, err := config.UserDir(config.DefaultUserID)
			if err != nil {
				return err
			}

			agentsDir := filepath.Join(userDir, "agents")
			entries, err := os.ReadDir(agentsDir)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Println("No agents found. Create one with: fastclaw agent create <name>")
					return nil
				}
				return err
			}

			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				name := entry.Name()
				workspace := filepath.Join(agentsDir, name, "agent")

				agentJSON := filepath.Join(workspace, "agent.json")
				model := "(default)"
				if data, err := os.ReadFile(agentJSON); err == nil {
					var fc config.AgentFileConfig
					if json.Unmarshal(data, &fc) == nil && fc.Model != "" {
						model = fc.Model
					}
				}

				fmt.Printf("  %s  model=%s  workspace=%s\n", name, model, workspace)
			}

			return nil
		},
	}
}
