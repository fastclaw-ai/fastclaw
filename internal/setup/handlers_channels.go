package setup

import (
	"net/http"
)

// --- Channels ---

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadUserConfig(r)
	if err != nil {
		jsonResponse(w, http.StatusOK, []any{})
		return
	}

	var channels []map[string]any
	for chType, ch := range cfg.Channels {
		status := "disconnected"
		if ch.Enabled {
			status = "connected"
		}
		channels = append(channels, map[string]any{
			"type":    chType,
			"enabled": ch.Enabled,
			"status":  status,
		})
	}
	if channels == nil {
		jsonResponse(w, http.StatusOK, []any{})
		return
	}
	jsonResponse(w, http.StatusOK, channels)
}
