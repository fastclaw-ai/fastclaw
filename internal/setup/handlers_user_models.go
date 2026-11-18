package setup

import (
	"encoding/json"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/usermodels"
	"net/http"
)

func (s *Server) handleUserModels(w http.ResponseWriter, r *http.Request) {
	uid := s.effectiveUserID(r)
	if uid == "" {
		jsonResponse(w, 401, map[string]any{"error": "unauthorized"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == "PUT" {
		if !s.requireWritable(w, r) {
			return
		}
		var c usermodels.Config
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			jsonResponse(w, 400, map[string]any{"error": "invalid model configuration"})
			return
		}
		if err := usermodels.SaveVerified(r.Context(), s.dataStore, uid, c); err != nil {
			jsonResponse(w, 400, map[string]any{"error": err.Error()})
			return
		}
		s.invalidateScope(scope.User, uid)
	}
	c, err := usermodels.Load(r.Context(), s.dataStore, uid)
	if err != nil {
		jsonResponse(w, 500, map[string]any{"error": "could not load models"})
		return
	}
	jsonResponse(w, 200, usermodels.View(c))
}

func (s *Server) handleTestUserModel(w http.ResponseWriter, r *http.Request) {
	uid := s.effectiveUserID(r)
	if uid == "" || !s.requireWritable(w, r) {
		if uid == "" {
			jsonResponse(w, 401, map[string]any{"error": "unauthorized"})
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var candidate usermodels.Provider
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 40<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&candidate); err != nil {
		jsonResponse(w, 400, map[string]any{"error": "invalid model configuration"})
		return
	}
	failed, err := usermodels.TestConnection(r.Context(), s.dataStore, uid, candidate)
	if err != nil {
		jsonResponse(w, 400, map[string]any{"error": "model test failed"})
		return
	}
	jsonResponse(w, 200, map[string]any{"failedModel": failed})
}
