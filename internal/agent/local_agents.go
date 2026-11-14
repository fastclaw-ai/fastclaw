package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
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
	// Same session key the gateway resolves reply media from.
	sessionID, projectID := msg.ChatID, msg.ProjectID
	load := func(ctx context.Context, p string) (agl.ToolFile, error) {
		return a.loadSessionFile(ctx, sessionID, projectID, p)
	}
	tools.RegisterLocalAgents(a.registry, svc, uid, chat, notify, load)
	if fetcher, ok := svc.(tools.LocalFileFetcher); ok {
		save := func(ctx context.Context, f agl.FetchedFile) (string, error) {
			url := "data:" + f.ContentType + ";base64," + base64.StdEncoding.EncodeToString(f.Bytes)
			names := a.WriteSessionAttachments(ctx, sessionID, projectID, []Attachment{{URL: url, Name: f.Name}})
			if len(names) == 0 {
				return "", errors.New("workspace write failed")
			}
			return names[0], nil
		}
		tools.RegisterSendLocalFile(a.registry, fetcher, save, uid)
		if maker, ok := svc.(tools.LocalImageMaker); ok && tools.CanGenerateImage(machines) {
			tools.RegisterGenerateImage(a.registry, maker, save, load, uid, chat, notify)
		}
	}
	return tools.LocalAgentsNote(machines) + a.mountComputerTools(lctx, svc, uid, chat, notify, machines)
}

// mountComputerTools puts computer_* (agl's built-in read / write / edit /
// exec) on the turn for the person's online computers whose agl offers
// them, coding agent or not, and returns the hint for them ("" when there
// are none). The model chooses between them and run_local_agent.
func (a *Agent) mountComputerTools(ctx context.Context, svc tools.LocalAgentRunner, uid string, chat agl.ToolChat, notify func(agl.ToolNotice), machines []agl.LocalMachine) string {
	runner, ok := svc.(tools.LocalBuiltinRunner)
	if !ok {
		return ""
	}
	offered := map[string]agl.BuiltinTool{}
	var names, agentless []string
	for _, m := range machines {
		if !agl.UsesBuiltinTools(m) {
			continue
		}
		list := runner.BuiltinTools(ctx, m)
		if len(list) == 0 {
			continue
		}
		names = append(names, m.Name)
		if len(m.Agents) == 0 {
			agentless = append(agentless, m.Name)
		}
		for _, bt := range list {
			if _, seen := offered[bt.Name]; !seen {
				offered[bt.Name] = bt
			}
		}
	}
	if len(offered) == 0 {
		return ""
	}
	tools.RegisterComputerTools(a.registry, runner, uid, chat, notify, offered, names)
	return tools.ComputerToolsNote(names, agentless, offered)
}

// loadSessionFile reads one file of the session workspace for handing
// over to the person's computer: p is its /workspace path (or relative
// to it). The workspace store first (attachments and files the sandbox
// made are there), then the host workspace dir.
func (a *Agent) loadSessionFile(ctx context.Context, sessionID, projectID, p string) (agl.ToolFile, error) {
	rel, ok := workspaceRel(p)
	if !ok {
		return agl.ToolFile{}, errors.New("not a file in /workspace")
	}
	if a.workspaceStore != nil {
		if rc, err := a.workspaceStore.Get(ctx, a.agentID, projectID, sessionID, rel); err == nil {
			defer rc.Close()
			data, err := io.ReadAll(io.LimitReader(rc, agl.MaxFetchBytes+1))
			if err != nil {
				return agl.ToolFile{}, err
			}
			return sessionFile(rel, data)
		}
	}
	if a.workspacePath != "" {
		full := filepath.Join(a.workspacePath, filepath.FromSlash(rel))
		if info, err := os.Stat(full); err == nil && info.Mode().IsRegular() {
			if info.Size() > agl.MaxFetchBytes {
				return agl.ToolFile{}, fmt.Errorf("larger than %d MB", agl.MaxFetchBytes>>20)
			}
			data, err := os.ReadFile(full)
			if err != nil {
				return agl.ToolFile{}, err
			}
			return sessionFile(rel, data)
		}
	}
	return agl.ToolFile{}, errors.New("no such file")
}

func sessionFile(rel string, data []byte) (agl.ToolFile, error) {
	if len(data) > agl.MaxFetchBytes {
		return agl.ToolFile{}, fmt.Errorf("larger than %d MB", agl.MaxFetchBytes>>20)
	}
	if len(data) == 0 {
		return agl.ToolFile{}, errors.New("empty file")
	}
	return agl.ToolFile{Name: path.Base(rel), Bytes: data}, nil
}

// workspaceRel turns "/workspace/a/b.png", "workspace/a/b.png" or
// "a/b.png" into "a/b.png"; false for anything outside the workspace or
// in a hidden folder.
func workspaceRel(p string) (string, bool) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" || strings.ContainsRune(p, 0) {
		return "", false
	}
	switch {
	case p == "/workspace" || p == "workspace":
		return "", false
	case strings.HasPrefix(p, "/workspace/"):
		p = strings.TrimPrefix(p, "/workspace/")
	case strings.HasPrefix(p, "workspace/"):
		p = strings.TrimPrefix(p, "workspace/")
	case strings.HasPrefix(p, "/"):
		return "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || strings.HasPrefix(seg, ".") && seg != "." {
			return "", false
		}
	}
	rel := path.Clean(p)
	if rel == "." || rel == "" || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return rel, true
}
