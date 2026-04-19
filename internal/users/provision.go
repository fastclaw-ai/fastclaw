package users

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// ProvisionWorkspace seeds a new user's directory with a minimal config
// and a default agent so they can start using the API immediately.
// Safe to call multiple times — skips if already provisioned.
func ProvisionWorkspace(userID string) error {
	userDir, err := config.EnsureUserDir(userID)
	if err != nil {
		return err
	}

	cfgPath := filepath.Join(userDir, "fastclaw.json")
	if _, err := os.Stat(cfgPath); err == nil {
		return nil // already provisioned
	}

	cfg := config.Config{
		Providers: map[string]config.ProviderConfig{},
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Model:             "gpt-4o",
				MaxTokens:         8192,
				Temperature:       0.7,
				MaxToolIterations: 20,
			},
		},
		Channels: map[string]config.ChannelConfig{},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		return err
	}

	// Seed the default agent workspace.
	agentDir := filepath.Join(userDir, "agents", "default", "agent")
	for _, sub := range []string{"", "memory", "sessions", "skills"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0o755); err != nil {
			return err
		}
	}
	agentJSON, _ := json.MarshalIndent(config.AgentFileConfig{Model: "gpt-4o"}, "", "  ")
	if err := os.WriteFile(filepath.Join(agentDir, "agent.json"), agentJSON, 0o644); err != nil {
		return err
	}
	bootstrap := map[string]string{
		"SOUL.md":     fmt.Sprintf("# %s\n\nYou are a helpful AI agent for user %s.\n", userID, userID),
		"IDENTITY.md": fmt.Sprintf("# Identity\n\nYou are %s's personal FastClaw agent.\n", userID),
		"AGENTS.md":   "# Agent Capabilities\n\nDescribe what this agent can do.\n",
		"MEMORY.md":   "# Memory\n\nLong-term memory for this agent.\n",
	}
	for name, content := range bootstrap {
		os.WriteFile(filepath.Join(agentDir, name), []byte(content), 0o644)
	}
	return nil
}
