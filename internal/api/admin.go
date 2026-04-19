package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

// RegisterAdminRoutes adds /v1/admin/apikeys/* endpoints to the mux.
// Protected by the gateway admin token.
func (s *Server) RegisterAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/admin/apikeys", s.adminAuth(s.handleCreateAPIKey))
	mux.HandleFunc("GET /v1/admin/apikeys", s.adminAuth(s.handleListAPIKeys))
	mux.HandleFunc("DELETE /v1/admin/apikeys/{id}", s.adminAuth(s.handleDeleteAPIKey))
	mux.HandleFunc("POST /v1/admin/apikeys/{id}/rotate", s.adminAuth(s.handleRotateAPIKey))

	// Backward compat: old /v1/admin/users/* routes
	mux.HandleFunc("POST /v1/admin/users", s.adminAuth(s.handleCreateAPIKey))
	mux.HandleFunc("GET /v1/admin/users", s.adminAuth(s.handleListAPIKeys))
	mux.HandleFunc("DELETE /v1/admin/users/{id}", s.adminAuth(s.handleDeleteAPIKey))
	mux.HandleFunc("POST /v1/admin/users/{id}/token", s.adminAuth(s.handleRotateAPIKey))
}

func (s *Server) adminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" {
			writeUnauth(w, "admin endpoints require a gateway auth token")
			return
		}
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		if token == auth || token != s.token {
			writeUnauth(w, "invalid admin token")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		next(w, r)
	}
}

func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "API key management not available"})
		return
	}
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id is required"})
		return
	}

	ak, key, err := s.registry.Add(req.ID, req.Name)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	if err := s.registry.Save(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"apikey": ak,
		"key":    key,
	})
}

func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		writeJSON(w, http.StatusOK, map[string]any{"apikeys": []any{}})
		return
	}
	list := s.registry.List()
	// Mask keys
	for _, ak := range list {
		if len(ak.Key) > 10 {
			ak.Key = ak.Key[:6] + "****" + ak.Key[len(ak.Key)-4:]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"apikeys": list})
}

func (s *Server) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "not available"})
		return
	}
	id := r.PathValue("id")
	if err := s.registry.Remove(id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	s.registry.Save()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRotateAPIKey(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "not available"})
		return
	}
	id := r.PathValue("id")
	key, err := s.registry.IssueToken(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	s.registry.Save()
	writeJSON(w, http.StatusOK, map[string]any{"key": key})
}
