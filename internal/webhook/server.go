package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// AgentHandler processes messages and returns responses.
type AgentHandler interface {
	HandleMessage(ctx context.Context, agentID string, msg bus.InboundMessage) (string, error)
}

// UserLookup resolves a bearer token to a user ID (cloud mode).
type UserLookup interface {
	LookupByToken(token string) (string, bool)
}

// WebhookRequest is the body of a webhook POST request.
type WebhookRequest struct {
	AgentID string `json:"agentId"`
	UserID  string `json:"userId,omitempty"` // fastclaw user to route to (cloud mode)
	Message string `json:"message"`
	Channel string `json:"channel"`
	ChatID  string `json:"chatId"`
}

// WebhookResponse is the JSON response returned to webhook callers.
type WebhookResponse struct {
	OK      bool   `json:"ok"`
	Reply   string `json:"reply,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Server is the webhook HTTP server.
type Server struct {
	token      string
	path       string
	handler    AgentHandler
	userLookup UserLookup // optional (nil in local mode)
}

// NewServer creates a new webhook server. userLookup may be nil in local mode.
func NewServer(token, path string, handler AgentHandler, userLookup UserLookup) *Server {
	if path == "" {
		path = "/hooks"
	}
	return &Server{
		token:      token,
		path:       path,
		handler:    handler,
		userLookup: userLookup,
	}
}

// Handler returns an http.Handler for the webhook endpoint.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(s.path, s.handleWebhook)
	return mux
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, WebhookResponse{Error: "method not allowed"})
		return
	}

	// Validate bearer token and optionally resolve to a user ID.
	var ownerUserID string
	auth := r.Header.Get("Authorization")
	token := strings.TrimPrefix(auth, "Bearer ")
	if s.token != "" {
		if token == s.token {
			// Admin / local-mode token matches.
		} else if s.userLookup != nil {
			if uid, ok := s.userLookup.LookupByToken(token); ok {
				ownerUserID = uid
			} else {
				writeJSON(w, http.StatusUnauthorized, WebhookResponse{Error: "unauthorized"})
				return
			}
		} else {
			writeJSON(w, http.StatusUnauthorized, WebhookResponse{Error: "unauthorized"})
			return
		}
	}

	var req WebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, WebhookResponse{Error: "invalid request body"})
		return
	}

	if req.AgentID == "" {
		writeJSON(w, http.StatusBadRequest, WebhookResponse{Error: "agentId is required"})
		return
	}
	if req.Message == "" {
		writeJSON(w, http.StatusBadRequest, WebhookResponse{Error: "message is required"})
		return
	}

	channel := req.Channel
	if channel == "" {
		channel = "webhook"
	}
	chatID := req.ChatID
	if chatID == "" {
		chatID = "webhook-default"
	}

	// Prefer explicit userId in request body, then token-derived.
	if req.UserID != "" {
		ownerUserID = req.UserID
	}

	msg := bus.InboundMessage{
		Channel:     channel,
		ChatID:      chatID,
		UserID:      "webhook",
		OwnerUserID: ownerUserID,
		Text:        req.Message,
		PeerKind:    "dm",
	}

	slog.Info("webhook received",
		"agent", req.AgentID,
		"channel", channel,
		"chat_id", chatID,
	)

	reply, err := s.handler.HandleMessage(r.Context(), req.AgentID, msg)
	if err != nil {
		slog.Error("webhook handler error", "agent", req.AgentID, "error", err)
		writeJSON(w, http.StatusInternalServerError, WebhookResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, WebhookResponse{OK: true, Reply: reply})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// ListenAndServe starts the webhook server on the given address.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: s.Handler(),
	}

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	slog.Info("webhook server started", "addr", addr, "path", s.path)
	err := srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return fmt.Errorf("webhook server: %w", err)
}
