package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// DecideGroup runs a tool-free, stateless coordination call with this member's
// configured provider/model. It never reads or appends private/member history.
func (a *Agent) DecideGroup(ctx context.Context, scope, prompt string) (string, error) {
	if a.provider == nil {
		return "", fmt.Errorf("group coordinator has no provider")
	}
	started := time.Now()
	response, err := a.provider.Chat(ctx, []provider.Message{{Role: "user", Content: prompt}}, nil, a.model, 2048, 0)
	if err != nil {
		return "", err
	}
	if response == nil {
		return "", fmt.Errorf("empty coordination response")
	}
	a.meterTokens(ctx, scope, a.model, response.Usage, time.Since(started).Milliseconds())
	if len(response.ToolCalls) > 0 {
		return "", fmt.Errorf("coordination must not call tools")
	}
	if len(response.Content) > 16000 {
		return "", fmt.Errorf("coordination response too large")
	}
	return strings.TrimSpace(response.Content), nil
}

// DeliverPrivate appends a private message from this agent to the
// human into the given direct-chat session, the way a proactive message
// lands there. The source (e.g. {kind:"group", id, name}) is UI-only
// metadata for the "received privately from …" label; the body is not
// published to the group stream or other members' sessions.
func (a *Agent) DeliverPrivate(ctx context.Context, sessionID, userID string, source map[string]any, content string) {
	channel, accountID, chatID, projectID := a.recoverWebTriple(sessionID)
	sess := a.sessions.Get(channel, accountID, chatID, projectID)
	sess.SetChatter(userID)
	sess.Append(provider.Message{
		Role:      "assistant",
		Content:   content,
		Timestamp: time.Now().UnixMilli(),
		Metadata:  map[string]any{"privateFrom": source},
	})
}
