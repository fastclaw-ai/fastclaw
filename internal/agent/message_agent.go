package agent

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
)

// PrivateFromParamKey carries the source of a private message ({kind,
// id, name, content}) into the turn it starts. It is persisted as UI-only
// metadata on the user-role message so the recipient's chat renders a
// "received privately from …" card instead of a plain user bubble.
const PrivateFromParamKey = "__fastclawPrivateFrom"

// maxMessageAgentDepth bounds A → B → C relay chains so two agents can't
// keep messaging each other forever.
const maxMessageAgentDepth = 2

type messageAgentDepthKey struct{}

// AgentResolver finds (and if needed loads) an agent owned by the same
// account, by exact id or case-insensitive display name.
type AgentResolver func(ctx context.Context, ref string) (*Agent, error)

// registerMessageAgent wires message_agent for this agent: a real private
// message to another of the owner's agents, run in that agent's direct
// chat (where a copy stays), whose reply comes back as the tool result.
func (a *Agent) registerMessageAgent(resolve AgentResolver, ownerUserID string) {
	if a.registry == nil || resolve == nil {
		return
	}
	tools.RegisterMessageAgent(a.registry, func(ctx context.Context, ref, message string) (string, error) {
		return a.messageAgent(ctx, resolve, ownerUserID, ref, message)
	})
}

func (a *Agent) messageAgent(ctx context.Context, resolve AgentResolver, ownerUserID, ref, message string) (string, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return "", fmt.Errorf("message is required")
	}
	depth, _ := ctx.Value(messageAgentDepthKey{}).(int)
	if depth >= maxMessageAgentDepth {
		return "", fmt.Errorf("handoff depth reached; continue with the context already available")
	}
	target, err := resolve(ctx, strings.TrimSpace(ref))
	if err != nil || target == nil {
		return "", fmt.Errorf("no agent named %q exists. message_agent only reaches the user's FastClaw agents; if %q is a program on the host (e.g. the codex or claude CLI), run it with exec instead", ref, ref)
	}
	if target == a || (target.agentID != "" && target.agentID == a.agentID) {
		return "", fmt.Errorf("you cannot message yourself")
	}
	senderName := a.displayName
	if senderName == "" {
		senderName = a.name
	}
	targetName := target.displayName
	if targetName == "" {
		targetName = target.name
	}
	sessionID := target.latestDirectSession()
	prompt := fmt.Sprintf("[Message from %s] %s\nRespond as yourself. %s will use your answer to report back to the human, and a copy of this exchange stays in your own private chat.", senderName, message, senderName)
	params := map[string]any{PrivateFromParamKey: map[string]any{
		"kind": "agent", "id": a.agentID, "name": senderName, "content": message,
	}}
	reply := strings.TrimSpace(target.HandleWebChat(context.WithValue(ctx, messageAgentDepthKey{}, depth+1), sessionID, "", ownerUserID, prompt, nil, params))
	if reply == "" {
		return "", fmt.Errorf("%s received the message but returned no reply", targetName)
	}
	// The first line and the "Recipient reply:" marker are parsed by the
	// web UI to render the delivery card; keep the shape stable.
	return fmt.Sprintf("Delivered to %s (%s). %s replied; the exchange is saved in their private chat. Use this as source material to answer the human in your own voice and attribute the findings; do not paste the reply verbatim or say you are still waiting.\nRecipient reply:\n%s", targetName, target.agentID, targetName, reply), nil
}

// latestDirectSession is this agent's direct chat as the sidebar sees it:
// the most recently active plain web session (no project, not a group
// member or inbox session). With none yet, a fresh session id.
func (a *Agent) latestDirectSession() string {
	best, bestAt := "", int64(-1)
	for _, entry := range a.WebChatSessions() {
		id := entry.ChatID
		if id == "" {
			id = entry.ID
		}
		if entry.ProjectID != "" || (entry.Channel != "" && entry.Channel != "web") ||
			strings.HasPrefix(id, "team-") || strings.HasPrefix(id, "group-inbox-") {
			continue
		}
		if entry.UpdatedAt > bestAt {
			best, bestAt = id, entry.UpdatedAt
		}
	}
	if best != "" {
		return best
	}
	return fmt.Sprintf("s-%d-%s", time.Now().UnixMilli(), strconv.FormatInt(rand.Int63n(36*36*36*36*36*36), 36))
}
