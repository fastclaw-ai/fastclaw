package setup

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// turnCancelHandle wraps one in-flight turn's agentCtx cancel. The
// registry stores a pointer so unregister can verify it still owns the
// slot: if a second turn for the same session registered meanwhile
// (send → stop → immediate resend), the first turn's deferred
// unregister must not evict the newcomer.
type turnCancelHandle struct {
	cancel context.CancelFunc
}

// registerTurnCancel makes an in-flight web turn stoppable via
// POST /api/chat/stop. Returns the handle to pass to
// unregisterTurnCancel (defer it next to the ctx cancel).
func (s *Server) registerTurnCancel(uid, agentID, sessionID string, cancel context.CancelFunc) *turnCancelHandle {
	h := &turnCancelHandle{cancel: cancel}
	s.turnCancelsMu.Lock()
	defer s.turnCancelsMu.Unlock()
	if s.turnCancels == nil {
		s.turnCancels = make(map[string]*turnCancelHandle)
	}
	s.turnCancels[uid+"/"+agentID+"/"+sessionID] = h
	return h
}

func (s *Server) unregisterTurnCancel(uid, agentID, sessionID string, h *turnCancelHandle) {
	key := uid + "/" + agentID + "/" + sessionID
	s.turnCancelsMu.Lock()
	defer s.turnCancelsMu.Unlock()
	if s.turnCancels[key] == h {
		delete(s.turnCancels, key)
	}
}

func (s *Server) takeTurnCancel(uid, agentID, sessionID string) context.CancelFunc {
	key := uid + "/" + agentID + "/" + sessionID
	s.turnCancelsMu.Lock()
	defer s.turnCancelsMu.Unlock()
	h := s.turnCancels[key]
	if h == nil {
		return nil
	}
	delete(s.turnCancels, key)
	return h.cancel
}

// handleChatStop cancels the in-flight turn for (agent, session) — the
// web UI's Stop button. Cancelling agentCtx makes the agent loop's
// provider/tool calls fail with context.Canceled, after which the loop
// emits its own terminal `error` + `done` events; subscribers see the
// turn close through the normal event flow. A durable `status` event
// is persisted first so the stop itself is visible in the session log
// (the auto-emitted "context canceled" error is filtered client-side).
//
// 200 {"stopped":true} when a turn was cancelled; 409
// {"stopped":false} when none was registered — mirrors steer's
// contract so clients can fall back (e.g. to aborting their fetch).
// Only turns started via POST /api/chat/stream register; IM- and
// cron-initiated turns are not stoppable here.
func (s *Server) handleChatStop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID   string `json:"agentId"`
		SessionID string `json:"sessionId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	ag := s.resolveAgent(r, req.AgentID)
	if ag == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return
	}
	uid := s.effectiveUserID(r)
	if uid == "" {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	agentID := ag.Name()
	cancel := s.takeTurnCancel(uid, agentID, req.SessionID)
	if cancel == nil {
		jsonResponse(w, http.StatusConflict, map[string]any{"stopped": false})
		return
	}
	// Persist the stop before cancelling so the status row lands ahead
	// of the cancellation-error/done events in session_events order.
	if s.dataStore != nil {
		blob, _ := json.Marshal(map[string]any{"state": "stopped", "at": time.Now().UTC().Format(time.RFC3339)})
		if _, err := s.dataStore.AppendSessionEvent(r.Context(), uid, agentID, req.SessionID, "status", blob); err != nil {
			slog.Warn("persist stop status event failed", "agent", agentID, "session", req.SessionID, "error", err)
		}
	}
	cancel()
	jsonResponse(w, http.StatusOK, map[string]any{"stopped": true})
}
