package setup

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// --- Plugins ---

func (s *Server) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	homeDir, err := config.HomeDir()
	if err != nil {
		jsonResponse(w, http.StatusOK, []any{})
		return
	}

	cfg, _ := s.loadUserConfig(r)
	pluginsDir := filepath.Join(homeDir, "plugins")
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		jsonResponse(w, http.StatusOK, []any{})
		return
	}

	var plugins []map[string]any
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()

		// Read plugin.json for metadata
		pluginType := "unknown"
		version := ""
		manifestPath := filepath.Join(pluginsDir, id, "plugin.json")
		if data, readErr := os.ReadFile(manifestPath); readErr == nil {
			var manifest map[string]any
			if json.Unmarshal(data, &manifest) == nil {
				if t, ok := manifest["type"].(string); ok {
					pluginType = t
				}
				if v, ok := manifest["version"].(string); ok {
					version = v
				}
			}
		}

		enabled := false
		if cfg != nil && cfg.Plugins.Entries != nil {
			if pe, ok := cfg.Plugins.Entries[id]; ok {
				enabled = pe.Enabled
			}
		}

		status := "stopped"
		if enabled {
			status = "running"
		}

		plugins = append(plugins, map[string]any{
			"id":      id,
			"type":    pluginType,
			"version": version,
			"status":  status,
			"enabled": enabled,
		})
	}
	if plugins == nil {
		jsonResponse(w, http.StatusOK, []any{})
		return
	}
	jsonResponse(w, http.StatusOK, plugins)
}

func (s *Server) handleUpdatePlugin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Enabled *bool                  `json:"enabled,omitempty"`
		Config  map[string]interface{} `json:"config,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}

	cfg, err := s.loadUserConfig(r)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	if cfg.Plugins.Entries == nil {
		cfg.Plugins.Entries = make(map[string]config.PluginEntryCfg)
	}
	entry := cfg.Plugins.Entries[id]
	if req.Enabled != nil {
		entry.Enabled = *req.Enabled
	}
	if req.Config != nil {
		entry.Config = req.Config
	}
	cfg.Plugins.Entries[id] = entry

	if err := s.saveUserConfig(r, cfg); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}
