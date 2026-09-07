package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
)

type teamChatRequest struct {
	TeamID      string              `json:"teamId"`
	SessionID   string              `json:"sessionId"`
	Message     string              `json:"message"`
	ImageURLs   []string            `json:"imageUrls,omitempty"`
	Images      []string            `json:"images,omitempty"`
	Attachments []attachmentRequest `json:"attachments,omitempty"`
	Members     []teamChatMember    `json:"members"`
	Params      map[string]any      `json:"params,omitempty"`
}

type teamChatMember struct {
	AgentID   string `json:"agentId"`
	SessionID string `json:"sessionId"`
}

func (s *Server) handleTeamChatStream(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	var req teamChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if strings.TrimSpace(req.TeamID) == "" || len(req.Members) == 0 {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "teamId and members required"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": "streaming unsupported"})
		return
	}
	uid := s.effectiveUserID(r)
	if uid == "" {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	members := s.resolveTeamMembers(r, req)
	if len(members) == 0 {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "no accessible team agents"})
		return
	}
	selected := s.selectTeamMembers(req.Message, members)
	if len(selected) == 0 {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "no routed team agents"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	for _, member := range selected {
		if !s.runTeamAgentTurn(w, flusher, r, uid, req, member) {
			return
		}
	}
	fmt.Fprintf(w, "data: %s\n\n", `{"type":"done"}`)
	flusher.Flush()
}

type resolvedTeamMember struct {
	AgentID   string
	SessionID string
	Handle    AgentHandle
	Name      string
}

func (s *Server) resolveTeamMembers(r *http.Request, req teamChatRequest) []resolvedTeamMember {
	seen := map[string]bool{}
	out := make([]resolvedTeamMember, 0, len(req.Members))
	for _, m := range req.Members {
		agentID := strings.TrimSpace(m.AgentID)
		if agentID == "" || seen[agentID] {
			continue
		}
		ag := s.resolveAgent(r, agentID)
		if ag == nil {
			continue
		}
		sessionID := strings.TrimSpace(m.SessionID)
		if sessionID == "" {
			sessionID = teamAgentSessionID(req.SessionID, req.TeamID, agentID)
		}
		name := agentID
		if s.dataStore != nil {
			if rec, err := s.dataStore.GetAgent(r.Context(), agentID); err == nil && rec != nil && strings.TrimSpace(rec.Name) != "" {
				name = strings.TrimSpace(rec.Name)
			}
		}
		seen[agentID] = true
		out = append(out, resolvedTeamMember{AgentID: agentID, SessionID: sessionID, Handle: ag, Name: name})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func teamAgentSessionID(baseSessionID, teamID, agentID string) string {
	base := strings.TrimSpace(baseSessionID)
	if base == "" {
		base = "team-" + strings.TrimSpace(teamID)
	}
	if strings.Contains(base, "-agent-"+agentID) {
		return base
	}
	return base + "-agent-" + agentID
}

func (s *Server) selectTeamMembers(message string, members []resolvedTeamMember) []resolvedTeamMember {
	text := strings.ToLower(strings.TrimSpace(message))
	if strings.Contains(text, "@all") || strings.Contains(text, "所有人") || strings.Contains(text, "大家") {
		return members
	}
	var mentioned []resolvedTeamMember
	for _, m := range members {
		name := strings.ToLower(m.Name)
		id := strings.ToLower(m.AgentID)
		if strings.Contains(text, "@"+name) || strings.Contains(text, "@"+id) || strings.Contains(text, name) {
			mentioned = append(mentioned, m)
		}
	}
	if len(mentioned) > 0 {
		return mentioned
	}
	best := members[0]
	bestScore := -1
	for _, m := range members {
		score := teamRouteScore(text, m)
		if score > bestScore {
			bestScore = score
			best = m
		}
	}
	return []resolvedTeamMember{best}
}

func teamRouteScore(text string, member resolvedTeamMember) int {
	score := 0
	for _, token := range strings.Fields(strings.ToLower(member.Name + " " + member.AgentID)) {
		token = strings.Trim(token, ".,，。:：;；()（）[]【】")
		if len([]rune(token)) < 2 {
			continue
		}
		if strings.Contains(text, token) {
			score += len([]rune(token))
		}
	}
	return score
}

func (s *Server) runTeamAgentTurn(w http.ResponseWriter, flusher http.Flusher, r *http.Request, uid string, req teamChatRequest, member resolvedTeamMember) bool {
	chatReq := chatRequest{
		AgentID:     member.AgentID,
		SessionID:   member.SessionID,
		Message:     req.Message,
		Images:      req.Images,
		ImageURLs:   req.ImageURLs,
		Attachments: req.Attachments,
		Params:      req.Params,
	}
	atts := chatReq.allAttachments()
	imageURLs := chatReq.inlineImageURLs()
	msgText := chatReq.Message
	if !chatReq.preMaterialized() {
		projectID := s.resolveSessionProject(r.Context(), r, member.AgentID, member.SessionID)
		paths := member.Handle.WriteSessionAttachments(r.Context(), member.SessionID, projectID, atts)
		msgText = annotateMessageWithAttachments(chatReq.Message, paths)
	}

	hub := s.chatEventHub()
	sub, unsubscribe := hub.Subscribe(uid, member.AgentID, member.SessionID)
	defer unsubscribe()

	agentCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), agentTurnTimeout)
	defer cancel()
	agentCtx = agent.ContextWithStream(agentCtx, nil, s.dataStore, hub, uid, member.AgentID, member.SessionID)

	agentDone := make(chan struct{})
	go func() {
		defer close(agentDone)
		_ = member.Handle.HandleWebChatStream(agentCtx, member.SessionID, req.TeamID, uid, msgText, imageURLs, req.Params, nil)
	}()

	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()
	clientGone := r.Context().Done()
	turnPending := false
	for {
		select {
		case <-clientGone:
			return false
		case <-agentDone:
		drain:
			for {
				select {
				case env, ok := <-sub:
					if !ok {
						return false
					}
					if env.Event.Type == "turn_pending" {
						turnPending = true
						continue
					}
					if env.Event.Type == "done" {
						return true
					}
					forwardTeamEvent(w, flusher, member.AgentID, env)
				default:
					break drain
				}
			}
			if turnPending {
				agentDone = nil
				continue
			}
			return true
		case <-agentCtx.Done():
			return false
		case <-keepalive.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case env, ok := <-sub:
			if !ok {
				return false
			}
			if env.Event.Type == "turn_pending" {
				turnPending = true
				continue
			}
			if env.Event.Type == "done" {
				return true
			}
			forwardTeamEvent(w, flusher, member.AgentID, env)
		}
	}
}

func forwardTeamEvent(w http.ResponseWriter, flusher http.Flusher, agentID string, env agent.EventEnvelope) {
	payload := map[string]any{
		"seq":     env.Seq,
		"type":    env.Event.Type,
		"agentId": agentID,
	}
	if env.Event.Data != nil {
		payload["data"] = env.Event.Data
	}
	data, _ := json.Marshal(payload)
	if env.Seq >= 0 {
		fmt.Fprintf(w, "id: %d\n", env.Seq)
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}
