package setup

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
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
	Name        string              `json:"-"`
	Description string              `json:"-"`
	HumanName   string              `json:"-"`
}

type teamChatMember struct {
	AgentID   string `json:"agentId"`
	SessionID string `json:"sessionId"`
}

type resolvedTeamMember struct {
	AgentID     string
	SessionID   string
	Handle      AgentHandle
	Name        string
	Description string
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
		sessionID := teamAgentSessionID(req.SessionID, req.TeamID, agentID)
		name := agentID
		description := ""
		if s.dataStore != nil {
			rec, err := s.dataStore.GetAgent(r.Context(), agentID)
			if err == nil && rec != nil && !s.privateAgentMemberAllowed(r, rec) {
				continue
			}
			if err == nil && rec != nil {
				if strings.TrimSpace(rec.Name) != "" {
					name = strings.TrimSpace(rec.Name)
				}
				if value, ok := rec.Config["description"].(string); ok {
					description = strings.TrimSpace(value)
				}
			}
		}
		seen[agentID] = true
		out = append(out, resolvedTeamMember{AgentID: agentID, SessionID: sessionID, Handle: ag, Name: name, Description: description})
	}
	return out
}

// privateAgentMemberAllowed keeps an end-user's private agent (one owned
// by an app_user) out of every group but its owner's own: groups and team
// topics only connect agents of one owner.
func (s *Server) privateAgentMemberAllowed(r *http.Request, rec *store.AgentRecord) bool {
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		return false
	}
	if rec.UserID == ident.EffectiveUserID() {
		return true
	}
	owner, err := s.dataStore.GetUser(r.Context(), rec.UserID)
	return err == nil && owner != nil && owner.Role != users.RoleAppUser
}

func teamAgentSessionID(baseSessionID, teamID, agentID string) string {
	base := strings.TrimSpace(baseSessionID)
	if base == "" {
		base = "team-" + strings.TrimSpace(teamID)
	}
	if strings.HasSuffix(base, "-agent-"+agentID) {
		return base
	}
	return base + "-agent-" + agentID
}

func (s *Server) selectTeamMembers(message string, members []resolvedTeamMember) []resolvedTeamMember {
	if len(members) == 0 {
		return nil
	}
	if mentioned, all := teamMentions(message, members); all {
		return members
	} else {
		return mentioned
	}
}

func teamMemberParams(base map[string]any, member resolvedTeamMember, members []resolvedTeamMember) map[string]any {
	params := make(map[string]any, len(base)+1)
	for key, value := range base {
		params[key] = value
	}
	teammates := make([]string, 0, len(members)-1)
	for _, candidate := range members {
		if candidate.AgentID != member.AgentID {
			teammates = append(teammates, candidate.Name)
		}
	}
	var instruction string
	if previous, ok := base["__fastclawGroupChat"].(map[string]any); ok {
		instruction, _ = previous["instruction"].(string)
	}
	params["__fastclawGroupChat"] = map[string]any{
		"instruction": instruction,
		"botUsername": member.Name,
		"teammates":   teammates,
	}
	return params
}

type teamMessageInjector interface {
	InjectGroupMessage(context.Context, bus.InboundMessage)
}

func injectTeamMessage(ctx context.Context, target resolvedTeamMember, uid, teamID, senderName, text string, bot bool) {
	injector, ok := target.Handle.(teamMessageInjector)
	if !ok || strings.TrimSpace(text) == "" {
		return
	}
	injector.InjectGroupMessage(ctx, bus.InboundMessage{
		Channel:      "web",
		ChatID:       target.SessionID,
		ProjectID:    teamID,
		UserID:       uid,
		OwnerUserID:  uid,
		AgentID:      target.AgentID,
		Text:         text,
		PeerKind:     "group",
		SenderName:   senderName,
		IsBotMessage: bot,
	})
}

func (s *Server) runTeamAgentTurn(r *http.Request, uid string, req teamChatRequest, member resolvedTeamMember, members []resolvedTeamMember, emit func(agent.EventEnvelope)) (bool, string) {
	chatReq := chatRequest{
		AgentID:     member.AgentID,
		SessionID:   member.SessionID,
		Message:     req.Message,
		Images:      req.Images,
		ImageURLs:   req.ImageURLs,
		Attachments: req.Attachments,
		Params:      teamMemberParams(req.Params, member, members),
	}
	atts := chatReq.allAttachments()
	imageURLs := chatReq.inlineImageURLs()
	msgText := chatReq.Message
	if !chatReq.preMaterialized() {
		projectID := req.TeamID
		paths := member.Handle.WriteSessionAttachments(r.Context(), member.SessionID, projectID, atts)
		msgText = annotateMessageWithAttachments(chatReq.Message, paths)
	}

	// The execution owner consumes a reliable channel. EventHub observers may
	// drop bursts, which must never drop final private deliveries or handoffs.
	events := make(chan agent.ChatEvent, 32)
	agentCtx, cancel := context.WithTimeout(r.Context(), agentTurnTimeout)
	defer cancel()
	agentCtx = agent.ContextWithStream(agentCtx, events, s.dataStore, s.chatEventHub(), uid, member.AgentID, member.SessionID)
	type result struct {
		reply  string
		failed bool
	}
	completed := make(chan result, 1)
	go func() {
		defer func() {
			if recover() != nil {
				completed <- result{failed: true}
			}
		}()
		reply := member.Handle.HandleWebChatStream(agentCtx, member.SessionID, req.TeamID, uid, msgText, imageURLs, chatReq.Params, events)
		completed <- result{reply: reply}
	}()
	pending, done, failed := false, false, false
	response := ""
	inactivity := time.NewTimer(teamInactivityTimeout)
	defer inactivity.Stop()
	consume := func(event agent.ChatEvent) {
		if !inactivity.Stop() {
			select {
			case <-inactivity.C:
			default:
			}
		}
		inactivity.Reset(teamInactivityTimeout)
		if event.Type == "turn_pending" {
			pending = true
			return
		}
		if event.Type == "done" {
			done = true
			return
		}
		if event.Type == "error" {
			failed = true
		}
		env := agent.EventEnvelope{Seq: -1, Event: event}
		response = teamEventContent(response, env)
		emit(env)
	}
	for {
		select {
		case <-agentCtx.Done():
			return false, response
		case <-inactivity.C:
			return false, response
		case event := <-events:
			consume(event)
			if done && completed == nil {
				return !failed, response
			}
		case result := <-completed:
			for len(events) > 0 {
				consume(<-events)
			}
			if result.reply != "" {
				response = result.reply
			}
			failed = failed || result.failed
			if !pending || done {
				return !failed, response
			}
			completed = nil
		}
	}
}

func teamEventContent(current string, env agent.EventEnvelope) string {
	if env.Event.Data == nil {
		return current
	}
	if env.Event.Type == "content" {
		if content, ok := env.Event.Data["content"].(string); ok && content != "" {
			return content
		}
	}
	if env.Event.Type == "content_delta" {
		if delta, ok := env.Event.Data["delta"].(string); ok {
			return current + delta
		}
	}
	return current
}
