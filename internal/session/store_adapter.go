package session

import (
	"context"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// StoreAdapter adapts store.Store to the SessionStore interface.
type StoreAdapter struct {
	st store.Store
}

func NewStoreAdapter(st store.Store) *StoreAdapter {
	return &StoreAdapter{st: st}
}

func (a *StoreAdapter) GetSession(ctx context.Context, agentID, sessionKey string) ([]provider.Message, error) {
	rec, err := a.st.GetSession(ctx, agentID, sessionKey)
	if err != nil || rec == nil {
		return nil, err
	}
	msgs := make([]provider.Message, len(rec.Messages))
	for i, m := range rec.Messages {
		msgs[i] = provider.Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
	}
	return msgs, nil
}

func (a *StoreAdapter) SaveSession(ctx context.Context, agentID, sessionKey string, messages []provider.Message) error {
	rec := &store.SessionRecord{
		Messages:  make([]store.SessionMessage, len(messages)),
		UpdatedAt: time.Now(),
	}
	for i, m := range messages {
		rec.Messages[i] = store.SessionMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			Timestamp:  time.Now(),
		}
	}
	return a.st.SaveSession(ctx, agentID, sessionKey, rec)
}

func (a *StoreAdapter) ListWebSessions(ctx context.Context, agentID string) ([]WebSession, error) {
	metas, err := a.st.ListSessions(ctx, agentID)
	if err != nil {
		return nil, err
	}
	var sessions []WebSession
	for _, m := range metas {
		if !strings.HasPrefix(m.Key, "web_") {
			continue
		}
		sessionId := strings.TrimPrefix(m.Key, "web_")
		preview := ""
		rec, err := a.st.GetSession(ctx, agentID, m.Key)
		if err == nil && rec != nil {
			for _, msg := range rec.Messages {
				if msg.Role == "user" && msg.Content != "" {
					preview = msg.Content
					if len(preview) > 100 {
						preview = preview[:100] + "..."
					}
					break
				}
			}
		}
		if preview == "" {
			continue
		}
		sessions = append(sessions, WebSession{
			ID:        sessionId,
			Title:     preview,
			Preview:   preview,
			CreatedAt: m.UpdatedAt.UnixMilli(),
			UpdatedAt: m.UpdatedAt.UnixMilli(),
		})
	}
	return sessions, nil
}

func (a *StoreAdapter) DeleteSession(ctx context.Context, agentID, sessionKey string) error {
	return a.st.DeleteSession(ctx, agentID, sessionKey)
}

func (a *StoreAdapter) RenameSession(ctx context.Context, agentID, sessionKey, title string) error {
	return nil
}
