package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/agl"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

var (
	localAgentsMu  sync.RWMutex
	localAgentsSvc tools.LocalAgentRunner
)

// SetLocalAgents installs the runner behind local_agents /
// run_local_agent (the gateway's *agl.Tools); nil turns them off.
func SetLocalAgents(svc tools.LocalAgentRunner) {
	localAgentsMu.Lock()
	localAgentsSvc = svc
	localAgentsMu.Unlock()
}

func localAgents() tools.LocalAgentRunner {
	localAgentsMu.RLock()
	defer localAgentsMu.RUnlock()
	return localAgentsSvc
}

// LocalAgentApprovals is what the web chat needs to show and answer a
// running run_local_agent's permission request (*agl.Tools).
type LocalAgentApprovals interface {
	PendingApproval(chat agl.ToolChat, userID string) (agl.PendingToolApproval, bool)
	Decide(ctx context.Context, chat agl.ToolChat, userID, decision string) (bool, error)
}

// LocalAgentApprovalsService returns the installed runner's approval
// half, or nil when local agents are off.
func LocalAgentApprovalsService() LocalAgentApprovals {
	a, _ := localAgents().(LocalAgentApprovals)
	return a
}

type localAgentsWebKey struct{}

// ContextWithLocalAgentsUser marks a web chat turn whose chatter (userID,
// the logged-in person) may dispatch work to their own linked computers.
// Only the dashboard's streaming chat sets it: approvals and progress
// reach the person as stream events, which other callers can't show.
func ContextWithLocalAgentsUser(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, localAgentsWebKey{}, userID)
}

func localAgentsWebUser(ctx context.Context) string {
	uid, _ := ctx.Value(localAgentsWebKey{}).(string)
	return uid
}

// localAgentEvent is the stream event of one run_local_agent update.
func localAgentEvent(n agl.ToolNotice) ChatEvent {
	data := map[string]any{"kind": n.Kind, "agent": n.Agent, "machine": n.Machine}
	for k, v := range map[string]string{"text": n.Text, "decision": n.Decision, "error": n.Error} {
		if v != "" {
			data[k] = v
		}
	}
	return ChatEvent{Type: "local_agent", Data: data}
}

// mountLocalAgents puts local_agents / run_local_agent on this turn's
// registry and returns the system hint naming the person's computers,
// or "" when the turn may not use them.
//
// The computers are the CHATTER's, never the agent owner's:
//   - IM: msg.MachineUserID, which the gateway sets only on a 1:1 IM
//     message from the person the channel belongs to. Approvals and
//     progress go to the IM chat (1/2/3 replies).
//   - web: the logged-in person of a dashboard chat stream
//     (ContextWithLocalAgentsUser). Approvals and progress go out as
//     "local_agent" stream events; the page answers with buttons.
//
// Groups, shared sessions, cron and other senders never get them;
// neither does anyone without a machine that allows agents (the turn is
// then exactly as before).
func (a *Agent) mountLocalAgents(ctx context.Context, msg bus.InboundMessage) string {
	svc := localAgents()
	if svc == nil || a.registry == nil || msg.PeerKind == "group" || msg.SharedIdentity || msg.Source != bus.SourceUser {
		return ""
	}
	uid := msg.MachineUserID
	chat := agl.ToolChat{Channel: msg.Channel, AccountID: msg.AccountID, ChatID: msg.ChatID, AgentID: a.name}
	var notify func(agl.ToolNotice)
	switch {
	case uid != "":
		if msg.Channel == "" || msg.Channel == "web" {
			return ""
		}
	case localAgentsWebUser(ctx) != "":
		stream := streamFromContext(ctx)
		if stream == nil || stream.sessionKey == "" {
			return ""
		}
		uid = localAgentsWebUser(ctx)
		chat = agl.WebToolChat(uid, stream.agentID, stream.sessionKey)
		chat.AgentID = a.name
		notify = func(n agl.ToolNotice) { emitEvent(ctx, localAgentEvent(n)) }
	default:
		return ""
	}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	machines, err := svc.Machines(lctx, uid)
	if err != nil {
		slog.Warn("local agents: list machines failed", "agent", a.name, "user", uid, "error", err)
		return ""
	}
	if len(machines) == 0 {
		return ""
	}
	tools.RegisterLocalAgents(a.registry, svc, uid, chat, notify)
	if fetcher, ok := svc.(tools.LocalFileFetcher); ok {
		// Same session key the gateway resolves reply media from.
		sessionID, projectID := msg.ChatID, msg.ProjectID
		save := func(ctx context.Context, f agl.FetchedFile) (string, error) {
			url := "data:" + f.ContentType + ";base64," + base64.StdEncoding.EncodeToString(f.Bytes)
			names := a.WriteSessionAttachments(ctx, sessionID, projectID, []Attachment{{URL: url, Name: f.Name}})
			if len(names) == 0 {
				return "", errors.New("workspace write failed")
			}
			return names[0], nil
		}
		tools.RegisterSendLocalFile(a.registry, fetcher, save, uid)
		if maker, ok := svc.(tools.LocalImageMaker); ok && hasLocalAgent(machines, "codex") {
			tools.RegisterGenerateImage(a.registry, maker, save, uid, chat, notify)
		}
	}
	return tools.LocalAgentsNote(machines)
}

// hasLocalAgent reports whether any of machines reports agent id.
func hasLocalAgent(machines []agl.LocalMachine, id string) bool {
	for _, m := range machines {
		for _, a := range m.Agents {
			if strings.EqualFold(a.ID, id) {
				return true
			}
		}
	}
	return false
}
