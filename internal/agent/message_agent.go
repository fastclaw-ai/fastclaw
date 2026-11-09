package agent

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
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

// AgentResolver finds (and if needed loads) another agent owned by
// ownerUserID — the calling agent's owner — by exact id or
// case-insensitive display name. Agents of any other owner must not
// resolve: an end-user's private agents only reach that end-user's other
// agents, never the account's platform agents or another end-user's.
type AgentResolver func(ctx context.Context, ownerUserID, ref string) (*Agent, error)

// AgentBrief is one entry of the message_agent roster.
type AgentBrief struct {
	ID          string
	Name        string
	Description string
}

// AgentLister lists the agents a caller owned by ownerUserID may reach
// with message_agent, the caller's own first. Same rules as AgentResolver.
type AgentLister func(ctx context.Context, ownerUserID string) ([]AgentBrief, error)

// maxRosterAgents caps the roster put in the prompt; message_agent still
// finds any reachable agent by exact name.
const maxRosterAgents = 20

// registerMessageAgent wires message_agent for this agent: a real private
// message to another of the owner's agents, run in that agent's direct
// chat (where a copy stays), whose reply comes back as the tool result.
func (a *Agent) registerMessageAgent(resolve AgentResolver, list AgentLister, ownerUserID string) {
	if a.registry == nil || resolve == nil {
		return
	}
	if list != nil {
		a.listReachableAgents = func(ctx context.Context) ([]AgentBrief, error) {
			return list(ctx, ownerUserID)
		}
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
	target, err := resolve(ctx, ownerUserID, strings.TrimSpace(ref))
	if err != nil || target == nil {
		why := fmt.Sprintf("no agent named %q exists", ref)
		if err != nil {
			why = err.Error()
		}
		return "", fmt.Errorf("%s. message_agent only reaches FastClaw agents; coding agents such as Codex or Claude Code on the person's computer run through run_local_agent, and a program on the host runs with exec", why)
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
	reply := strings.TrimSpace(target.HandleWebChat(context.WithValue(ctx, messageAgentDepthKey{}, depth+1), sessionID, "", a.messageAgentTurnUser(ownerUserID), prompt, nil, params))
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

// messageAgentTurnUser is the user the recipient's turn runs for. In the
// owner's own space that is the owner (unchanged). An account agent
// running in an end user's space (a product's platform agent chatting
// with one of its users) acts for that end user, so the recipient's turn
// — its session, memory and usage — is theirs, not the account's.
func (a *Agent) messageAgentTurnUser(ownerUserID string) string {
	if a.registry == nil {
		return ownerUserID
	}
	space := a.registry.OwnerUserID()
	if space == "" || space == ownerUserID {
		return ownerUserID
	}
	return a.registry.ChatterUserID()
}

// reachableAgentsNote is the per-turn roster of the other FastClaw agents
// this agent can reach with message_agent, so "which agents do I have?"
// and "ask 小微 …" don't depend on the model guessing names — and so a
// chat that once said an agent didn't exist doesn't keep saying so. Only
// where message_agent is offered (not in group chats); "" when nothing is
// reachable.
func (a *Agent) reachableAgentsNote(ctx context.Context, msg bus.InboundMessage) string {
	if a.listReachableAgents == nil || a.registry == nil || !a.registry.HasTool("message_agent") {
		return ""
	}
	// Customize mode exposes no built-ins, so message_agent isn't offered.
	if allow := builtinAllowForMode(a.promptMode); allow != nil && !slices.Contains(allow, "message_agent") {
		return ""
	}
	if _, group := msg.Params["__fastclawGroupChat"]; group || msg.PeerKind == "group" {
		return ""
	}
	lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	agents, err := a.listReachableAgents(lctx)
	if err != nil {
		slog.Warn("message_agent roster failed", "agent", a.name, "error", err)
		return ""
	}
	var b strings.Builder
	n := 0
	for _, ag := range agents {
		if ag.ID == a.agentID || ag.ID == a.name {
			continue
		}
		if n == maxRosterAgents {
			n++
			break
		}
		n++
		fmt.Fprintf(&b, "\n- %s", ag.Name)
		if d := rosterDescription(ag.Description); d != "" {
			fmt.Fprintf(&b, ": %s", d)
		}
	}
	if n == 0 {
		return ""
	}
	more := ""
	if n > maxRosterAgents {
		more = "\n(More exist; message_agent also finds any other by its exact name.)"
	}
	return "## Other FastClaw agents you can message\n" +
		"These are cloud agents (personas), separate from the coding agents on the person's own computer " +
		"(those, when linked, run through run_local_agent). When the person asks which agents they have, list these " +
		"as their agents here (and the computer's coding agents separately, if any). To involve one, call message_agent " +
		"with its name. This list is current: agents can be added at any time, so if the chat earlier said an agent " +
		"didn't exist, trust this list and message_agent instead." + b.String() + more
}

// rosterDescription is a one-line, short form of an agent description.
func rosterDescription(d string) string {
	d = strings.Join(strings.Fields(d), " ")
	r := []rune(d)
	if len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return d
}
