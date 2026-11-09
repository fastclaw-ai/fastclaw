package api

// This file implements Agent Communication Protocol (ACP) 0.2, the
// HTTP/REST agent-to-agent protocol originally published by the BeeAI project.
// It intentionally does not implement the unrelated Agent Client Protocol
// (also abbreviated ACP), whose primary transport is JSON-RPC over stdio.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/drain"
)

const (
	acpRunSync   = "sync"
	acpRunAsync  = "async"
	acpRunStream = "stream"

	acpStatusCreated    = "created"
	acpStatusInProgress = "in-progress"
	acpStatusCancelling = "cancelling"
	acpStatusCancelled  = "cancelled"
	acpStatusCompleted  = "completed"
	acpStatusFailed     = "failed"

	acpRunTTL     = 24 * time.Hour
	acpRunLimit   = 1000
	acpRunTimeout = 45 * time.Minute
)

var acpAgentNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

type acpError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

type acpMessagePart struct {
	Name            string         `json:"name,omitempty"`
	ContentType     string         `json:"content_type"`
	Content         string         `json:"content,omitempty"`
	ContentEncoding string         `json:"content_encoding,omitempty"`
	ContentURL      string         `json:"content_url,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

type acpMessage struct {
	Role        string           `json:"role"`
	Parts       []acpMessagePart `json:"parts"`
	CreatedAt   *time.Time       `json:"created_at,omitempty"`
	CompletedAt *time.Time       `json:"completed_at,omitempty"`
}

type acpSession struct {
	ID      string   `json:"id"`
	History []string `json:"history"`
	State   string   `json:"state,omitempty"`
}

type acpRunCreateRequest struct {
	AgentName string       `json:"agent_name"`
	SessionID string       `json:"session_id,omitempty"`
	Session   *acpSession  `json:"session,omitempty"`
	Input     []acpMessage `json:"input"`
	Mode      string       `json:"mode,omitempty"`
}

type acpRun struct {
	AgentName    string         `json:"agent_name"`
	SessionID    string         `json:"session_id,omitempty"`
	RunID        string         `json:"run_id"`
	Status       string         `json:"status"`
	AwaitRequest map[string]any `json:"await_request,omitempty"`
	Output       []acpMessage   `json:"output"`
	Error        *acpError      `json:"error,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	FinishedAt   *time.Time     `json:"finished_at,omitempty"`
}

type acpEvent map[string]any

type acpRunState struct {
	mu      sync.Mutex
	ownerID string
	run     acpRun
	events  []acpEvent
	cancel  context.CancelFunc
}

type acpPreparedInput struct {
	text        string
	images      []string
	attachments []agent.Attachment
}

func (s *Server) registerACPRoutes(mux *http.ServeMux, getUserID func(*http.Request) string) {
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return s.authMiddleware(rateLimitMiddleware(s.limiter, getUserID, h))
	}
	mux.HandleFunc("GET /acp/ping", s.handleACPPing)
	mux.HandleFunc("GET /acp/agents", wrap(s.handleACPListAgents))
	mux.HandleFunc("GET /acp/agents/{name}", wrap(s.handleACPGetAgent))
	mux.HandleFunc("POST /acp/runs", wrap(s.handleACPCreateRun))
	mux.HandleFunc("GET /acp/runs/{runID}", wrap(s.handleACPGetRun))
	mux.HandleFunc("POST /acp/runs/{runID}", wrap(s.handleACPResumeRun))
	mux.HandleFunc("GET /acp/runs/{runID}/events", wrap(s.handleACPListRunEvents))
	mux.HandleFunc("POST /acp/runs/{runID}/cancel", wrap(s.handleACPCancelRun))
	mux.HandleFunc("GET /acp/session/{sessionID}", wrap(s.handleACPGetSession))
	// The published OpenAPI document used singular /session, while the
	// reference SDKs shipped /sessions. Accept both spellings.
	mux.HandleFunc("GET /acp/sessions/{sessionID}", wrap(s.handleACPGetSession))
}

func (s *Server) handleACPPing(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "protocol": "acp/0.2"})
}

func (s *Server) handleACPListAgents(w http.ResponseWriter, r *http.Request) {
	space, ident, err := s.acpUserSpace(r)
	if err != nil {
		writeACPError(w, http.StatusUnauthorized, "server_error", err.Error())
		return
	}
	manifests := s.acpManifests(space, ident)
	offset, err := acpQueryInt(r, "offset", 0, 0, len(manifests))
	if err != nil {
		writeACPError(w, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}
	limit, err := acpQueryInt(r, "limit", 10, 1, 1000)
	if err != nil {
		writeACPError(w, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}
	end := offset + limit
	if end > len(manifests) {
		end = len(manifests)
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": manifests[offset:end]})
}

func (s *Server) handleACPGetAgent(w http.ResponseWriter, r *http.Request) {
	space, ident, err := s.acpUserSpace(r)
	if err != nil {
		writeACPError(w, http.StatusUnauthorized, "server_error", err.Error())
		return
	}
	ag := s.resolveACPAgent(r, space, ident, r.PathValue("name"))
	if ag == nil {
		writeACPError(w, http.StatusNotFound, "not_found", "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, acpManifestFor(ag))
}

func (s *Server) handleACPCreateRun(w http.ResponseWriter, r *http.Request) {
	space, ident, err := s.acpUserSpace(r)
	if err != nil {
		writeACPError(w, http.StatusUnauthorized, "server_error", err.Error())
		return
	}
	var req acpRunCreateRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20))
	if err := dec.Decode(&req); err != nil {
		writeACPError(w, http.StatusBadRequest, "invalid_input", "invalid request body")
		return
	}
	if req.AgentName == "" || len(req.Input) == 0 {
		writeACPError(w, http.StatusBadRequest, "invalid_input", "agent_name and input are required")
		return
	}
	if req.Mode == "" {
		req.Mode = acpRunSync
	}
	if req.Mode != acpRunSync && req.Mode != acpRunAsync && req.Mode != acpRunStream {
		writeACPError(w, http.StatusBadRequest, "invalid_input", "mode must be sync, async, or stream")
		return
	}
	ag := s.resolveACPAgent(r, space, ident, req.AgentName)
	if ag == nil {
		writeACPError(w, http.StatusNotFound, "not_found", "agent not found")
		return
	}
	prepared, err := prepareACPInput(req.Input)
	if err != nil {
		writeACPError(w, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}
	sessionID, err := acpSessionID(req)
	if err != nil {
		writeACPError(w, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}

	state := &acpRunState{
		ownerID: config.UserIDFromContext(r.Context()),
		run: acpRun{
			AgentName: acpAgentName(ag.Name()),
			SessionID: sessionID,
			RunID:     uuid.NewString(),
			Status:    acpStatusCreated,
			Output:    []acpMessage{},
			CreatedAt: time.Now().UTC(),
		},
	}
	s.storeACPRun(state)
	w.Header().Set("Run-ID", state.run.RunID)
	w.Header().Set("Access-Control-Expose-Headers", "Run-ID")

	switch req.Mode {
	case acpRunAsync:
		created := state.snapshot()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), acpRunTimeout)
		state.setCancel(cancel)
		go func() {
			defer cancel()
			s.executeACPRun(ctx, state, ag, prepared, nil)
		}()
		writeJSON(w, http.StatusAccepted, created)
	case acpRunStream:
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeACPError(w, http.StatusInternalServerError, "server_error", "streaming unsupported")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		ctx, cancel := context.WithTimeout(r.Context(), acpRunTimeout)
		state.setCancel(cancel)
		defer cancel()
		s.executeACPRun(ctx, state, ag, prepared, func(evt acpEvent) {
			data, _ := json.Marshal(evt)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		})
	default:
		ctx, cancel := context.WithTimeout(r.Context(), acpRunTimeout)
		state.setCancel(cancel)
		defer cancel()
		s.executeACPRun(ctx, state, ag, prepared, nil)
		writeJSON(w, http.StatusOK, state.snapshot())
	}
}

func (s *Server) executeACPRun(ctx context.Context, state *acpRunState, ag *agent.Agent, input acpPreparedInput, emit func(acpEvent)) {
	if state.isCancelling() {
		s.finishACPRun(state, acpStatusCancelled, nil, emit)
		return
	}
	s.emitACPRunEvent(state, acpEvent{"type": "run.created", "run": state.snapshot()}, emit)
	state.setStatus(acpStatusInProgress)
	s.emitACPRunEvent(state, acpEvent{"type": "run.in-progress", "run": state.snapshot()}, emit)

	text := input.text
	paths := ag.WriteSessionAttachments(ctx, state.sessionID(), "", input.attachments)
	if len(paths) > 0 {
		var b strings.Builder
		for _, path := range paths {
			fmt.Fprintf(&b, "[Attached: /workspace/%s]\n", path)
		}
		b.WriteString(text)
		text = b.String()
	}
	if strings.TrimSpace(text) == "" {
		text = "Please process the attached content."
	}
	msg := bus.InboundMessage{
		Channel: "acp", ChatID: state.sessionID(), UserID: state.ownerID,
		OwnerUserID: state.ownerID, AgentID: ag.Name(), Text: text,
		PeerKind: "dm", PhotoURLs: input.images,
	}

	defer drain.Track()() // the stream is consumed in this handler
	stream := ag.HandleMessageStream(ctx, msg)
	messageStarted := false
	var messageCreatedAt time.Time
	messageParts := make([]acpMessagePart, 0)
	for {
		chunk, more := stream.Next()
		if chunk.Content != "" {
			now := time.Now().UTC()
			part := acpMessagePart{ContentType: "text/plain", Content: chunk.Content, ContentEncoding: "plain"}
			if !messageStarted {
				messageStarted = true
				messageCreatedAt = now
				message := acpMessage{Role: "agent/" + state.agentName(), Parts: []acpMessagePart{}, CreatedAt: &messageCreatedAt}
				s.emitACPRunEvent(state, acpEvent{"type": "message.created", "message": message}, emit)
			}
			messageParts = append(messageParts, part)
			s.emitACPRunEvent(state, acpEvent{"type": "message.part", "part": part}, emit)
		}
		if chunk.Done || !more {
			break
		}
	}

	if err := stream.Err(); err != nil && ctx.Err() == nil {
		s.finishACPRun(state, acpStatusFailed, &acpError{Code: "server_error", Message: err.Error()}, emit)
		return
	}
	if ctx.Err() != nil || state.isCancelling() {
		s.finishACPRun(state, acpStatusCancelled, nil, emit)
		return
	}
	if messageStarted {
		now := time.Now().UTC()
		message := acpMessage{
			Role:        "agent/" + state.agentName(),
			Parts:       messageParts,
			CreatedAt:   &messageCreatedAt,
			CompletedAt: &now,
		}
		state.setOutput([]acpMessage{message})
		s.emitACPRunEvent(state, acpEvent{"type": "message.completed", "message": message}, emit)
	}
	s.finishACPRun(state, acpStatusCompleted, nil, emit)
}

func (s *Server) finishACPRun(state *acpRunState, status string, runErr *acpError, emit func(acpEvent)) {
	state.finish(status, runErr)
	eventType := "run." + status
	if status == acpStatusFailed {
		eventType = "run.failed"
	}
	s.emitACPRunEvent(state, acpEvent{"type": eventType, "run": state.snapshot()}, emit)
}

func (s *Server) emitACPRunEvent(state *acpRunState, evt acpEvent, emit func(acpEvent)) {
	state.addEvent(evt)
	if emit != nil {
		emit(evt)
	}
}

func (s *Server) handleACPGetRun(w http.ResponseWriter, r *http.Request) {
	state := s.findACPRun(r)
	if state == nil {
		writeACPError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	writeJSON(w, http.StatusOK, state.snapshot())
}

func (s *Server) handleACPListRunEvents(w http.ResponseWriter, r *http.Request) {
	state := s.findACPRun(r)
	if state == nil {
		writeACPError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": state.eventSnapshot()})
}

func (s *Server) handleACPCancelRun(w http.ResponseWriter, r *http.Request) {
	state := s.findACPRun(r)
	if state == nil {
		writeACPError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	if !state.requestCancel() {
		writeACPError(w, http.StatusForbidden, "invalid_input", "a terminal run cannot be cancelled")
		return
	}
	writeJSON(w, http.StatusAccepted, state.snapshot())
}

func (s *Server) handleACPResumeRun(w http.ResponseWriter, r *http.Request) {
	state := s.findACPRun(r)
	if state == nil {
		writeACPError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	writeACPError(w, http.StatusForbidden, "invalid_input", "run is not awaiting input")
}

func (s *Server) handleACPGetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	if _, err := uuid.Parse(sessionID); err != nil {
		writeACPError(w, http.StatusBadRequest, "invalid_input", "session_id must be a UUID")
		return
	}
	ownerID := config.UserIDFromContext(r.Context())
	history := make([]string, 0)
	s.acpMu.Lock()
	for id, state := range s.acpRuns {
		if state.ownerID == ownerID && state.sessionID() == sessionID {
			history = append(history, "/acp/runs/"+id)
		}
	}
	s.acpMu.Unlock()
	if len(history) == 0 {
		writeACPError(w, http.StatusNotFound, "not_found", "session not found")
		return
	}
	sort.Strings(history)
	writeJSON(w, http.StatusOK, acpSession{ID: sessionID, History: history})
}

func (s *Server) acpUserSpace(r *http.Request) (*UserSpaceView, auth.Identity, error) {
	space, err := s.userSpaceFor(r)
	if err != nil {
		return nil, auth.Identity{}, err
	}
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		return nil, auth.Identity{}, errors.New("unauthorized")
	}
	return space, ident, nil
}

func (s *Server) acpManifests(space *UserSpaceView, ident auth.Identity) []map[string]any {
	agents := space.Agents.All()
	manifests := make([]map[string]any, 0, len(agents))
	for _, ag := range agents {
		if ident.CanAccessAgent(ag.Name()) {
			manifests = append(manifests, acpManifestFor(ag))
		}
	}
	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i]["name"].(string) < manifests[j]["name"].(string)
	})
	return manifests
}

func acpManifestFor(ag *agent.Agent) map[string]any {
	description := "FastClaw agent"
	if ag.Model() != "" {
		description += " powered by " + ag.Model()
	}
	return map[string]any{
		"name":                 acpAgentName(ag.Name()),
		"description":          description,
		"input_content_types":  []string{"text/plain", "text/markdown", "application/json", "image/*", "application/octet-stream"},
		"output_content_types": []string{"text/plain", "text/markdown"},
		"metadata": map[string]any{
			"framework":            "FastClaw",
			"programming_language": "Go",
			"tags":                 []string{"Chat", "Orchestrator"},
			"annotations":          map[string]any{"fastclaw_agent_id": ag.Name(), "model": ag.Model()},
			"capabilities": []map[string]string{
				{"name": "Multi-turn conversation", "description": "Maintains state through ACP session_id."},
				{"name": "Tool use", "description": "Runs the tools and skills configured on this FastClaw agent."},
			},
		},
	}
}

func (s *Server) resolveACPAgent(r *http.Request, space *UserSpaceView, ident auth.Identity, name string) *agent.Agent {
	for _, ag := range space.Agents.All() {
		if !ident.CanAccessAgent(ag.Name()) {
			continue
		}
		if ag.Name() == name || acpAgentName(ag.Name()) == name {
			return ag
		}
	}
	// Apps with many agents load them on demand, so an agent addressed
	// by its exact id may not be loaded yet.
	if name != "" {
		if ag, err := s.resolveChatAgent(r, name); err == nil {
			return ag
		}
	}
	return nil
}

func acpAgentName(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 63 && acpAgentNamePattern.MatchString(id) {
		return id
	}
	original := id
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(id) {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if valid {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" {
		base = "agent"
	}
	sum := sha256.Sum256([]byte(original))
	suffix := "-" + hex.EncodeToString(sum[:4])
	maxBase := 63 - len(suffix)
	if len(base) > maxBase {
		base = strings.TrimRight(base[:maxBase], "-")
	}
	return base + suffix
}

func prepareACPInput(messages []acpMessage) (acpPreparedInput, error) {
	var out acpPreparedInput
	var groups []string
	for _, message := range messages {
		if message.Role != "user" && message.Role != "agent" && !strings.HasPrefix(message.Role, "agent/") {
			return out, fmt.Errorf("invalid message role %q", message.Role)
		}
		if len(message.Parts) == 0 {
			return out, errors.New("every input message must contain at least one part")
		}
		var content []string
		for _, part := range message.Parts {
			if part.ContentType == "" {
				return out, errors.New("content_type is required")
			}
			if part.Content != "" && part.ContentURL != "" {
				return out, errors.New("a message part cannot contain both content and content_url")
			}
			encoding := part.ContentEncoding
			if encoding == "" {
				encoding = "plain"
			}
			if encoding != "plain" && encoding != "base64" {
				return out, fmt.Errorf("unsupported content_encoding %q", encoding)
			}

			switch {
			case part.ContentURL != "":
				att := agent.Attachment{URL: part.ContentURL, Name: part.Name}
				out.attachments = append(out.attachments, att)
				if strings.HasPrefix(strings.ToLower(part.ContentType), "image/") {
					out.images = append(out.images, part.ContentURL)
				} else {
					content = append(content, fmt.Sprintf("[%s resource: %s]", part.ContentType, part.ContentURL))
				}
			case encoding == "base64":
				if _, err := base64.StdEncoding.DecodeString(part.Content); err != nil {
					return out, fmt.Errorf("invalid base64 content: %w", err)
				}
				dataURL := "data:" + part.ContentType + ";base64," + part.Content
				out.attachments = append(out.attachments, agent.Attachment{URL: dataURL, Name: part.Name})
				if strings.HasPrefix(strings.ToLower(part.ContentType), "image/") {
					out.images = append(out.images, dataURL)
				}
			case strings.HasPrefix(strings.ToLower(part.ContentType), "text/") || part.ContentType == "application/json":
				content = append(content, part.Content)
			case part.Content != "":
				content = append(content, fmt.Sprintf("[%s content: %s]", part.ContentType, part.Content))
			}
		}
		joined := strings.Join(content, "\n")
		if len(messages) > 1 || message.Role != "user" {
			joined = "[Message from " + message.Role + "]\n" + joined
		}
		if strings.TrimSpace(joined) != "" {
			groups = append(groups, joined)
		}
	}
	out.text = strings.Join(groups, "\n\n")
	return out, nil
}

func acpSessionID(req acpRunCreateRequest) (string, error) {
	id := req.SessionID
	if req.Session != nil {
		if id != "" && req.Session.ID != "" && id != req.Session.ID {
			return "", errors.New("session_id and session.id must match")
		}
		if id == "" {
			id = req.Session.ID
		}
	}
	if id == "" {
		return uuid.NewString(), nil
	}
	if _, err := uuid.Parse(id); err != nil {
		return "", errors.New("session_id must be a UUID")
	}
	return id, nil
}

func acpQueryInt(r *http.Request, name string, def, min, max int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be between %d and %d", name, min, max)
	}
	return n, nil
}

func (s *Server) storeACPRun(state *acpRunState) {
	s.acpMu.Lock()
	defer s.acpMu.Unlock()
	if s.acpRuns == nil {
		s.acpRuns = make(map[string]*acpRunState)
	}
	cutoff := time.Now().Add(-acpRunTTL)
	for id, existing := range s.acpRuns {
		if existing.createdAt().Before(cutoff) {
			delete(s.acpRuns, id)
		}
	}
	if len(s.acpRuns) >= acpRunLimit {
		var oldestID string
		var oldest time.Time
		for id, existing := range s.acpRuns {
			created := existing.createdAt()
			if oldestID == "" || created.Before(oldest) {
				oldestID, oldest = id, created
			}
		}
		delete(s.acpRuns, oldestID)
	}
	s.acpRuns[state.run.RunID] = state
}

func (s *Server) findACPRun(r *http.Request) *acpRunState {
	id := r.PathValue("runID")
	ownerID := config.UserIDFromContext(r.Context())
	s.acpMu.Lock()
	state := s.acpRuns[id]
	s.acpMu.Unlock()
	if state == nil || state.ownerID != ownerID {
		return nil
	}
	return state
}

func (r *acpRunState) snapshot() acpRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.run
	out.Output = append([]acpMessage(nil), r.run.Output...)
	return out
}

func (r *acpRunState) eventSnapshot() []acpEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]acpEvent(nil), r.events...)
}

func (r *acpRunState) addEvent(evt acpEvent) {
	r.mu.Lock()
	r.events = append(r.events, evt)
	r.mu.Unlock()
}

func (r *acpRunState) setStatus(status string) {
	r.mu.Lock()
	r.run.Status = status
	r.mu.Unlock()
}

func (r *acpRunState) setOutput(output []acpMessage) {
	r.mu.Lock()
	r.run.Output = output
	r.mu.Unlock()
}

func (r *acpRunState) setCancel(cancel context.CancelFunc) {
	r.mu.Lock()
	r.cancel = cancel
	cancelling := r.run.Status == acpStatusCancelling
	r.mu.Unlock()
	if cancelling {
		cancel()
	}
}

func (r *acpRunState) requestCancel() bool {
	r.mu.Lock()
	var cancel context.CancelFunc
	accepted := false
	if r.run.Status == acpStatusCreated || r.run.Status == acpStatusInProgress {
		r.run.Status = acpStatusCancelling
		cancel = r.cancel
		accepted = true
	}
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return accepted
}

func (r *acpRunState) finish(status string, runErr *acpError) {
	now := time.Now().UTC()
	r.mu.Lock()
	r.run.Status = status
	r.run.Error = runErr
	r.run.FinishedAt = &now
	r.cancel = nil
	r.mu.Unlock()
}

func (r *acpRunState) isCancelling() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.run.Status == acpStatusCancelling
}

func (r *acpRunState) sessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.run.SessionID
}

func (r *acpRunState) agentName() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.run.AgentName
}

func (r *acpRunState) createdAt() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.run.CreatedAt
}

func writeACPError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, acpError{Code: code, Message: message})
}
