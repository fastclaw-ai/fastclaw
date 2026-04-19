package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

const shutdownTimeout = 5 * time.Second

// Manifest is the plugin.json descriptor.
type Manifest struct {
	ID           string                       `json:"id"`
	Name         string                       `json:"name"`
	Version      string                       `json:"version"`
	Description  string                       `json:"description"`
	Type         string                       `json:"type"` // channel, tool, provider, hook
	Command      string                       `json:"command"`
	Capabilities []string                     `json:"capabilities,omitempty"`
	ConfigSchema map[string]ManifestConfigDef `json:"config,omitempty"`

	Dir string `json:"-"` // directory containing the plugin
}

// ManifestConfigDef describes a config field in plugin.json.
type ManifestConfigDef struct {
	Type      string `json:"type"`
	Required  bool   `json:"required,omitempty"`
	Sensitive bool   `json:"sensitive,omitempty"`
	Default   string `json:"default,omitempty"`
}

// PluginInstance holds a loaded plugin's manifest, process, and runtime state.
type PluginInstance struct {
	Manifest *Manifest
	Process  *Process
	Config   map[string]interface{}
	Enabled  bool
}

// Manager discovers, loads, and manages plugin lifecycles.
type Manager struct {
	plugins map[string]*PluginInstance
	bus     *bus.MessageBus
	mu      sync.RWMutex
}

// NewManager creates a plugin manager.
func NewManager(mb *bus.MessageBus) *Manager {
	return &Manager{
		plugins: make(map[string]*PluginInstance),
		bus:     mb,
	}
}

// Discover scans directories for plugin.json files and loads manifests.
func (m *Manager) Discover(paths []string) error {
	for _, dir := range paths {
		dir = expandHome(dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			slog.Warn("plugin: cannot read directory", "path", dir, "error", err)
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			pluginDir := filepath.Join(dir, entry.Name())
			manifest, err := loadManifest(pluginDir)
			if err != nil {
				slog.Warn("plugin: skip directory", "path", pluginDir, "error", err)
				continue
			}

			m.mu.Lock()
			m.plugins[manifest.ID] = &PluginInstance{
				Manifest: manifest,
				Enabled:  true,
			}
			m.mu.Unlock()

			slog.Info("plugin: discovered", "id", manifest.ID, "type", manifest.Type, "version", manifest.Version)
		}
	}
	return nil
}

// ApplyConfig sets per-plugin config and enabled state from the user's configuration.
func (m *Manager) ApplyConfig(entries map[string]PluginEntryCfg) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, entry := range entries {
		inst, ok := m.plugins[id]
		if !ok {
			continue
		}
		inst.Enabled = entry.Enabled
		inst.Config = entry.Config
	}
}

// StartAll starts all enabled plugins and sends initialize.
func (m *Manager) StartAll(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for id, inst := range m.plugins {
		if !inst.Enabled {
			slog.Info("plugin: skipping disabled", "id", id)
			continue
		}

		proc := NewProcess(inst.Manifest)
		inst.Process = proc

		// Set notification handler for inbound messages
		proc.SetNotifyHandler(func(n Notification) {
			m.handleNotification(id, n)
		})

		if err := proc.Start(ctx); err != nil {
			slog.Error("plugin: failed to start", "id", id, "error", err)
			continue
		}

		// Send initialize with config
		cfg := inst.Config
		if cfg == nil {
			cfg = make(map[string]interface{})
		}
		initParams := InitializeParams{Config: cfg}
		if _, err := proc.Call(ctx, MethodInitialize, initParams); err != nil {
			slog.Error("plugin: initialize failed", "id", id, "error", err)
			proc.Stop(shutdownTimeout)
			continue
		}

		slog.Info("plugin: started", "id", id)
	}
	return nil
}

// StopAll gracefully stops all running plugins.
func (m *Manager) StopAll() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for id, inst := range m.plugins {
		if inst.Process != nil && inst.Process.IsRunning() {
			slog.Info("plugin: stopping", "id", id)
			inst.Process.Stop(shutdownTimeout)
		}
	}
}

// Plugins returns all discovered plugin instances.
func (m *Manager) Plugins() []*PluginInstance {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*PluginInstance, 0, len(m.plugins))
	for _, inst := range m.plugins {
		result = append(result, inst)
	}
	return result
}

// Plugin returns a specific plugin by ID.
func (m *Manager) Plugin(id string) *PluginInstance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.plugins[id]
}

// ChannelPlugins returns all running plugins that provide channel capability.
func (m *Manager) ChannelPlugins() []*PluginInstance {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*PluginInstance
	for _, inst := range m.plugins {
		if !inst.Enabled || inst.Process == nil || !inst.Process.IsRunning() {
			continue
		}
		if hasCapability(inst.Manifest, "channel") {
			result = append(result, inst)
		}
	}
	return result
}

// ToolPlugins returns all running plugins that provide tool capability.
func (m *Manager) ToolPlugins() []*PluginInstance {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*PluginInstance
	for _, inst := range m.plugins {
		if !inst.Enabled || inst.Process == nil || !inst.Process.IsRunning() {
			continue
		}
		if hasCapability(inst.Manifest, "tool") {
			result = append(result, inst)
		}
	}
	return result
}

// HookPlugins returns all running plugins that provide hook capability.
func (m *Manager) HookPlugins() []*PluginInstance {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*PluginInstance
	for _, inst := range m.plugins {
		if !inst.Enabled || inst.Process == nil || !inst.Process.IsRunning() {
			continue
		}
		if hasCapability(inst.Manifest, "hook") {
			result = append(result, inst)
		}
	}
	return result
}

// ListTools queries a plugin for its available tools.
func (m *Manager) ListTools(ctx context.Context, pluginID string) ([]ToolDef, error) {
	inst := m.Plugin(pluginID)
	if inst == nil || inst.Process == nil || !inst.Process.IsRunning() {
		return nil, fmt.Errorf("plugin %s not running", pluginID)
	}

	result, err := inst.Process.Call(ctx, MethodToolList, nil)
	if err != nil {
		return nil, err
	}

	var toolResult ToolListResult
	if err := json.Unmarshal(result, &toolResult); err != nil {
		return nil, fmt.Errorf("parse tool.list response: %w", err)
	}
	return toolResult.Tools, nil
}

// ExecuteTool calls a tool on a specific plugin.
func (m *Manager) ExecuteTool(ctx context.Context, pluginID, toolName string, args map[string]interface{}) (string, error) {
	inst := m.Plugin(pluginID)
	if inst == nil || inst.Process == nil || !inst.Process.IsRunning() {
		return "", fmt.Errorf("plugin %s not running", pluginID)
	}

	params := ToolExecuteParams{Name: toolName, Args: args}
	result, err := inst.Process.Call(ctx, MethodToolExecute, params)
	if err != nil {
		return "", err
	}

	var toolResult ToolExecuteResult
	if err := json.Unmarshal(result, &toolResult); err != nil {
		return "", fmt.Errorf("parse tool.execute response: %w", err)
	}
	return toolResult.Result, nil
}

// SendToChannel sends a message through a channel plugin.
func (m *Manager) SendToChannel(ctx context.Context, pluginID, chatID, text string) error {
	inst := m.Plugin(pluginID)
	if inst == nil || inst.Process == nil || !inst.Process.IsRunning() {
		return fmt.Errorf("plugin %s not running", pluginID)
	}

	params := ChannelSendParams{ChatID: chatID, Text: text}
	_, err := inst.Process.Call(ctx, MethodChannelSend, params)
	return err
}

// handleNotification processes notifications from plugins.
func (m *Manager) handleNotification(pluginID string, n Notification) {
	switch n.Method {
	case MethodMessageInbound:
		var params InboundMessageParams
		if err := json.Unmarshal(n.Params, &params); err != nil {
			slog.Warn("plugin: invalid inbound message", "plugin", pluginID, "error", err)
			return
		}

		channel := params.Channel
		if channel == "" {
			channel = "plugin:" + pluginID
		}
		peerKind := params.PeerKind
		if peerKind == "" {
			peerKind = "dm"
		}

		m.bus.Inbound <- bus.InboundMessage{
			Channel:    channel,
			ChatID:     params.ChatID,
			UserID:     params.UserID,
			Text:       params.Text,
			PeerKind:   peerKind,
			SenderName: params.SenderName,
		}

		slog.Info("plugin: inbound message", "plugin", pluginID, "channel", channel, "chat_id", params.ChatID)

	default:
		slog.Debug("plugin: unhandled notification", "plugin", pluginID, "method", n.Method)
	}
}

// PluginEntryCfg is the per-plugin configuration from the user's config file.
type PluginEntryCfg struct {
	Enabled bool                   `json:"enabled"`
	Config  map[string]interface{} `json:"config,omitempty"`
}

// loadManifest reads plugin.json from a directory.
func loadManifest(dir string) (*Manifest, error) {
	path := filepath.Join(dir, "plugin.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if m.ID == "" {
		return nil, fmt.Errorf("%s: missing id field", path)
	}
	if m.Command == "" {
		return nil, fmt.Errorf("%s: missing command field", path)
	}

	m.Dir = dir
	return &m, nil
}

func hasCapability(m *Manifest, cap string) bool {
	// Check capabilities list first
	for _, c := range m.Capabilities {
		if c == cap {
			return true
		}
	}
	// Fall back to type field
	return m.Type == cap
}

func expandHome(path string) string {
	if len(path) > 1 && path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
