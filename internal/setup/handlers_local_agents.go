package setup

import (
	"encoding/json"
	"net/http"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/agl"
)

// A web chat turn's run_local_agent may stop for the person's approval
// (the coding agent on their computer wants to run a command, edit a
// file, …). The page learns about it from a "local_agent" stream event
// and answers here:
//
//	GET  /api/chat/local-agent-approval?agentId=&sessionId=   the open request, or {pending:false}
//	POST /api/chat/local-agent-approval {agentId, sessionId, decision}
//
// decision is allow | allow_session | deny. Only the person whose
// computer it is can see or answer the request.

// localAgentChat resolves the chat a request names, as the web turn
// built it (agent.mountLocalAgents).
func (s *Server) localAgentChat(r *http.Request, agentID, sessionID string) (agl.ToolChat, string, bool) {
	uid := s.effectiveUserID(r)
	ag := s.resolveAgent(r, agentID)
	if uid == "" || ag == nil || sessionID == "" {
		return agl.ToolChat{}, "", false
	}
	return agl.WebToolChat(uid, ag.Name(), s.chatEventSessionID(r, ag.Name(), sessionID)), uid, true
}

func (s *Server) handleLocalAgentApproval(w http.ResponseWriter, r *http.Request) {
	svc := agent.LocalAgentApprovalsService()
	chat, uid, ok := s.localAgentChat(r, r.URL.Query().Get("agentId"), r.URL.Query().Get("sessionId"))
	if svc == nil || !ok {
		jsonResponse(w, http.StatusOK, map[string]any{"pending": false})
		return
	}
	p, pending := svc.PendingApproval(chat, uid)
	if !pending {
		jsonResponse(w, http.StatusOK, map[string]any{"pending": false})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"pending": true, "agent": p.Agent, "machine": p.Machine, "title": p.Title})
}

func (s *Server) handleLocalAgentDecision(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	var req struct {
		AgentID   string `json:"agentId"`
		SessionID string `json:"sessionId"`
		Decision  string `json:"decision"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	switch req.Decision {
	case "allow", "allow_session", "deny":
	default:
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": `decision must be "allow", "allow_session" or "deny"`})
		return
	}
	svc := agent.LocalAgentApprovalsService()
	chat, uid, ok := s.localAgentChat(r, req.AgentID, req.SessionID)
	if svc == nil || !ok {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "no pending request"})
		return
	}
	found, err := svc.Decide(r.Context(), chat, uid, req.Decision)
	if !found {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "no pending request"})
		return
	}
	if err != nil {
		jsonResponse(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}
