package setup

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// --- Cron Jobs ---

func (s *Server) handleListCronJobs(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadUserConfig(r)
	if err != nil {
		jsonResponse(w, http.StatusOK, []any{})
		return
	}

	var jobs []map[string]any
	for i, job := range cfg.CronJobs {
		jobs = append(jobs, map[string]any{
			"id":       fmt.Sprintf("%d", i),
			"name":     job.Name,
			"type":     job.Type,
			"schedule": job.Schedule,
			"agentId":  job.AgentID,
			"channel":  job.Channel,
			"chatId":   job.ChatID,
			"message":  job.Message,
			"enabled":  true,
		})
	}
	if jobs == nil {
		jsonResponse(w, http.StatusOK, []any{})
		return
	}
	jsonResponse(w, http.StatusOK, jobs)
}

func (s *Server) handleCreateCronJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Schedule string `json:"schedule"`
		AgentID  string `json:"agentId"`
		Channel  string `json:"channel"`
		ChatID   string `json:"chatId"`
		Message  string `json:"message"`
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

	cfg.CronJobs = append(cfg.CronJobs, config.CronJob{
		Name:     req.Name,
		Type:     req.Type,
		Schedule: req.Schedule,
		AgentID:  req.AgentID,
		Channel:  req.Channel,
		ChatID:   req.ChatID,
		Message:  req.Message,
	})

	if err := s.saveUserConfig(r, cfg); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleUpdateCronJob(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	var idx int
	if _, err := fmt.Sscanf(idStr, "%d", &idx); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid id"})
		return
	}

	var req struct {
		Enabled *bool `json:"enabled,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}

	// For now, just acknowledge — cron enable/disable would need scheduler integration
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteCronJob(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	var idx int
	if _, err := fmt.Sscanf(idStr, "%d", &idx); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid id"})
		return
	}

	cfg, err := s.loadUserConfig(r)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	if idx < 0 || idx >= len(cfg.CronJobs) {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": "job not found"})
		return
	}

	cfg.CronJobs = append(cfg.CronJobs[:idx], cfg.CronJobs[idx+1:]...)

	if err := s.saveUserConfig(r, cfg); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}
