package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/agl"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
)

// Local agents over /v1: a product (e.g. WeClaw relaying WeChat) lets the
// agent run the end user's own coding agents on their linked computers.
//
//	POST /v1/chat/completions {"stream": true, "local_agents": true, …}
//	     with X-Fastclaw-End-User and x-fastclaw-session-key
//	     → run_local_agent is offered; updates stream as `local_agent` chunks
//	GET  /v1/local-agents/approval?agent_id=&session_key=   the open request, or {"pending": false}
//	POST /v1/local-agents/approval {"agent_id", "session_key", "decision"}
//
// The turn is addressed like a web chat (agl.WebToolChat: end user, agent,
// session key), so only the same end user can see or answer its approval.

type localTurnKey struct{}

type localTurn struct {
	userID     string
	sessionKey string
}

func withLocalTurn(ctx context.Context, userID, sessionKey string) context.Context {
	return context.WithValue(ctx, localTurnKey{}, localTurn{userID: userID, sessionKey: sessionKey})
}

func localTurnFrom(ctx context.Context) (localTurn, bool) {
	lt, ok := ctx.Value(localTurnKey{}).(localTurn)
	return lt, ok && lt.userID != "" && lt.sessionKey != ""
}

// localAgentsUser is the end user whose computers a /v1 turn may use:
// streaming (approvals need a live channel back), made as an end user
// (never the app account itself), with a caller-chosen session key (the
// approval endpoint addresses the turn by it).
func localAgentsUser(ident auth.Identity, stream bool, headerSessionKey string) (string, bool) {
	uid := ident.EffectiveUserID()
	if !stream || strings.TrimSpace(headerSessionKey) == "" || ident.AppUserID == "" || uid == "" || uid == ident.AccountID() {
		return "", false
	}
	return uid, true
}

// localApprovalChat resolves the chat an approval request names.
func (s *Server) localApprovalChat(r *http.Request, agentID, sessionKey string) (agl.ToolChat, string, bool) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.AppUserID == "" {
		return agl.ToolChat{}, "", false
	}
	uid := ident.EffectiveUserID()
	agentID, sessionKey = strings.TrimSpace(agentID), strings.TrimSpace(sessionKey)
	if uid == "" || uid == ident.AccountID() || agentID == "" || sessionKey == "" {
		return agl.ToolChat{}, "", false
	}
	ag, err := s.resolveChatAgent(r, agentID)
	if err != nil || ag == nil {
		return agl.ToolChat{}, "", false
	}
	return agl.WebToolChat(uid, ag.Name(), sessionKey), uid, true
}

// HandleGetLocalAgentApproval handles GET /v1/local-agents/approval.
func (s *Server) HandleGetLocalAgentApproval(w http.ResponseWriter, r *http.Request) {
	svc := agent.LocalAgentApprovalsService()
	q := r.URL.Query()
	chat, uid, ok := s.localApprovalChat(r, q.Get("agent_id"), q.Get("session_key"))
	if svc == nil || !ok {
		writeJSON(w, http.StatusOK, map[string]any{"pending": false})
		return
	}
	p, pending := svc.PendingApproval(chat, uid)
	if !pending {
		writeJSON(w, http.StatusOK, map[string]any{"pending": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": true, "agent": p.Agent, "machine": p.Machine, "title": p.Title})
}

// HandleLocalAgentDecision handles POST /v1/local-agents/approval.
// decision: allow | allow_session | deny. 404 when nothing is pending.
func (s *Server) HandleLocalAgentDecision(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID    string `json:"agent_id"`
		SessionKey string `json:"session_key"`
		Decision   string `json:"decision"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeBadRequest(w, "invalid request body")
		return
	}
	switch req.Decision {
	case "allow", "allow_session", "deny":
	default:
		writeBadRequest(w, `decision must be "allow", "allow_session" or "deny"`)
		return
	}
	svc := agent.LocalAgentApprovalsService()
	chat, uid, ok := s.localApprovalChat(r, req.AgentID, req.SessionKey)
	if svc == nil || !ok {
		writeAPIError(w, http.StatusNotFound, errTypeNotFound, codeNoPendingApproval, "no pending request")
		return
	}
	found, err := svc.Decide(r.Context(), chat, uid, req.Decision)
	if !found {
		writeAPIError(w, http.StatusNotFound, errTypeNotFound, codeNoPendingApproval, "no pending request")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, errTypeServer, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "decision": req.Decision})
}
