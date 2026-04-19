package gateway

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/channels"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/cron"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fsnotify/fsnotify"
)

// startConfigWatcher watches the config file and workspace files for changes,
// triggering a hot-reload when modifications are detected.
func (g *Gateway) startConfigWatcher(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("failed to create file watcher", "error", err)
		return
	}
	defer watcher.Close()

	// Watch the default user's config directory for fastclaw.json changes.
	configPath, err := config.UserConfigPath(config.DefaultUserID)
	if err != nil {
		slog.Error("cannot determine config dir for watcher", "error", err)
		return
	}
	configDir := filepath.Dir(configPath)

	if err := watcher.Add(configDir); err != nil {
		slog.Error("failed to watch config dir", "dir", configDir, "error", err)
		return
	}
	slog.Info("config watcher started", "path", configDir)

	// Also watch agent home directories for SOUL.md, AGENTS.md, etc.
	for _, ag := range g.agents.All() {
		hPath := ag.HomePath()
		if hPath != "" {
			if err := watcher.Add(hPath); err != nil {
				slog.Warn("failed to watch agent home", "path", hPath, "error", err)
			}
		}
	}

	// Debounce: wait for writes to settle before reloading
	var debounceTimer *time.Timer
	var debounceMu sync.Mutex

	for {
		select {
		case <-ctx.Done():
			return

		case event, ok := <-watcher.Events:
			if !ok {
				return
			}

			// Only react to writes and creates
			if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) {
				continue
			}

			filename := filepath.Base(event.Name)

			// Determine what changed
			isConfig := filename == "fastclaw.json"
			isWorkspaceFile := isWatchedWorkspaceFile(filename)

			if !isConfig && !isWorkspaceFile {
				continue
			}

			slog.Info("file change detected", "file", event.Name, "op", event.Op.String())

			// Debounce: many editors write multiple events per save
			debounceMu.Lock()
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(500*time.Millisecond, func() {
				if isConfig {
					g.reloadConfig()
				} else if isWorkspaceFile {
					g.reloadWorkspaceFile(event.Name, filename)
				}
			})
			debounceMu.Unlock()

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			slog.Error("file watcher error", "error", err)
		}
	}
}

// isWatchedWorkspaceFile returns true if this is a file we hot-reload.
func isWatchedWorkspaceFile(filename string) bool {
	switch filename {
	case "SOUL.md", "AGENTS.md", "IDENTITY.md", "TOOLS.md",
		"BOOTSTRAP.md", "HEARTBEAT.md", "MEMORY.md", "USER.md",
		"agent.json":
		return true
	}
	return false
}

// reloadConfig reloads the main config file and applies changes.
func (g *Gateway) reloadConfig() {
	slog.Info("hot-reloading config...")

	newCfg, err := config.Load()
	if err != nil {
		slog.Error("hot-reload: failed to load config", "error", err)
		return
	}

	// 1. Update LLM provider if changed
	g.reloadProvider(newCfg)

	// 2. Update agent configs (model, temperature, etc.)
	g.reloadAgents(newCfg)

	// 3. Update bindings
	g.mu.Lock()
	g.bindings = newCfg.Bindings
	g.config = newCfg
	g.mu.Unlock()

	// 4. Update cron jobs
	g.reloadCron(newCfg)

	// 5. Update teams and group context
	g.reloadTeams(newCfg)

	slog.Info("hot-reload complete ✅")
}

// resolveProviderCfg picks the active provider config from a Config.
func resolveProviderCfg(cfg *config.Config) config.ProviderConfig {
	var pc config.ProviderConfig
	defaultModel := cfg.Agents.Defaults.Model
	if parts := strings.SplitN(defaultModel, "/", 2); len(parts) == 2 {
		if p, ok := cfg.Providers[parts[0]]; ok {
			return p
		}
	}
	for _, key := range []string{"default", "openai", "openrouter"} {
		if p, ok := cfg.Providers[key]; ok {
			return p
		}
	}
	for _, p := range cfg.Providers {
		return p
	}
	return pc
}

// reloadProvider updates the LLM provider if API key/base/type changed.
func (g *Gateway) reloadProvider(newCfg *config.Config) {
	newProvCfg := resolveProviderCfg(newCfg)

	g.mu.RLock()
	oldCfg := g.config
	g.mu.RUnlock()

	oldProvCfg := resolveProviderCfg(oldCfg)

	if newProvCfg.APIKey != oldProvCfg.APIKey || newProvCfg.APIBase != oldProvCfg.APIBase || newProvCfg.APIType != oldProvCfg.APIType {
		llm := provider.NewProvider(newProvCfg.APIKey, newProvCfg.APIBase, newProvCfg.APIType)
		g.agents.UpdateProvider(llm)
		slog.Info("hot-reload: provider updated", "apiBase", newProvCfg.APIBase, "apiType", newProvCfg.APIType)
	}
}

// reloadAgents adds new agents, updates configs on existing ones, and removes
// agents that no longer have a workspace on disk.
func (g *Gateway) reloadAgents(newCfg *config.Config) {
	resolved := config.ResolveAgents(newCfg)
	seen := make(map[string]bool, len(resolved))
	for _, rc := range resolved {
		seen[rc.ID] = true
		ag := g.agents.AgentByID(rc.ID)
		if ag == nil {
			if err := g.agents.AddAgent(rc, g.localSpace.Provider, g.bus); err != nil {
				slog.Error("hot-reload: failed to add agent", "id", rc.ID, "error", err)
			} else {
				slog.Info("hot-reload: new agent added", "id", rc.ID, "model", rc.Model)
			}
			continue
		}
		ag.UpdateConfig(rc)
		slog.Info("hot-reload: agent config updated", "id", rc.ID, "model", rc.Model)
	}
	// Remove agents that disappeared from disk.
	for _, existing := range g.agents.All() {
		if !seen[existing.Name()] {
			g.agents.RemoveAgent(existing.Name())
		}
	}
}

// ReloadAgents is the public entrypoint used by the HTTP API after an agent
// is created / updated / deleted via the web UI. It reloads the local user's
// config and syncs the in-memory agent manager with disk.
func (g *Gateway) ReloadAgents() error {
	newCfg, err := config.Load()
	if err != nil {
		return err
	}
	g.reloadAgents(newCfg)
	g.mu.Lock()
	g.config = newCfg
	g.mu.Unlock()
	return nil
}

// reloadCron updates the cron scheduler with new jobs.
func (g *Gateway) reloadCron(newCfg *config.Config) {
	var cronJobs []cron.Job
	for _, cj := range newCfg.CronJobs {
		cronJobs = append(cronJobs, cron.Job{
			Name:     cj.Name,
			Type:     cron.JobType(cj.Type),
			Schedule: cj.Schedule,
			AgentID:  cj.AgentID,
			Channel:  cj.Channel,
			ChatID:   cj.ChatID,
			Message:  cj.Message,
		})
	}
	g.scheduler.UpdateJobs(cronJobs)
	slog.Info("hot-reload: cron jobs updated", "count", len(cronJobs))
}

// reloadTeams updates team config and group context.
func (g *Gateway) reloadTeams(newCfg *config.Config) {
	teams := newCfg.Teams
	if teams == nil {
		teams = make(map[string]config.TeamEntry)
	}
	g.mu.Lock()
	g.teams = teams
	g.mu.Unlock()

	// Refresh group context for agents in teams
	for _, team := range teams {
		for _, agentID := range team.Agents {
			ag := g.agents.AgentByID(agentID)
			if ag == nil {
				continue
			}
			var teammates []string
			for _, otherID := range team.Agents {
				if otherID != agentID {
					if uname, ok := g.botUsernames[otherID]; ok {
						teammates = append(teammates, "@"+uname)
					} else {
						teammates = append(teammates, otherID)
					}
				}
			}
			if botUname, ok := g.botUsernames[agentID]; ok {
				ag.SetGroupContext(&agent.GroupContext{
					BotUsername: botUname,
					Teammates:  teammates,
				})
			}
		}
	}
}

// reloadWorkspaceFile handles changes to agent identity files (SOUL.md, etc.)
// stored in the agent's home directory.
func (g *Gateway) reloadWorkspaceFile(fullPath, filename string) {
	dir := filepath.Dir(fullPath)

	// Find which agent owns this home dir
	for _, ag := range g.agents.All() {
		if ag.HomePath() == dir {
			ag.ReloadWorkspaceFiles()
			slog.Info("hot-reload: agent home file updated", "agent", ag.Name(), "file", filename)
			return
		}
	}
	slog.Warn("hot-reload: changed file doesn't match any agent home", "path", fullPath)
}

// Minimal channel hot-reload: new channels require restart,
// but we can update existing channel configs.
func (g *Gateway) reloadChannels(newCfg *config.Config) {
	// For now, log which channels changed. Full channel hot-reload
	// (adding/removing Telegram bots) requires restart.
	for name, chCfg := range newCfg.Channels {
		if !chCfg.Enabled {
			continue
		}
		slog.Info("hot-reload: channel config noted (restart needed for new channels)", "channel", name)
	}
}

// registerChannelsHot would add new channels at runtime.
// Currently not implemented — new channels require restart.
func registerChannelsHot(cfg *config.Config, mb *bus.MessageBus, chanMgr *channels.Manager) error {
	_ = cfg
	_ = mb
	_ = chanMgr
	return nil
}
