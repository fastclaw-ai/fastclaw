package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/agl"
)

// Tool names for dispatching work to the person's own computer.
const (
	LocalAgentsToolName   = "local_agents"
	RunLocalAgentToolName = "run_local_agent"
	SendLocalFileToolName = "send_local_file"
)

// LocalFileFetcher reads a file from the person's computer (*agl.Tools).
type LocalFileFetcher interface {
	Fetch(ctx context.Context, r agl.FetchRun) (agl.FetchedFile, error)
}

// SaveFileFunc stores fetched bytes in this turn's session workspace and
// returns the /workspace-relative name.
type SaveFileFunc func(ctx context.Context, f agl.FetchedFile) (string, error)

// LocalAgentRunner is what the tools need from internal/agl (*agl.Tools).
type LocalAgentRunner interface {
	Machines(ctx context.Context, userID string) ([]agl.LocalMachine, error)
	Run(ctx context.Context, r agl.ToolRun) (string, error)
}

// RegisterLocalAgents mounts local_agents and run_local_agent on one
// turn's registry. The machines' owner (userID) and the chat are bound
// here from the turn — the model only names agents and machines among
// that person's own. Only for 1:1 IM turns of the person the chat
// belongs to (see Agent.mountLocalAgents).
func RegisterLocalAgents(r *Registry, svc LocalAgentRunner, userID string, chat agl.ToolChat) {
	t := &localAgentsTool{svc: svc, userID: userID, chat: chat}

	r.RegisterFrom(LocalAgentsToolName,
		"List the person's own linked computers (name, online) and the coding agents available on each (claude, codex, gemini, …). "+
			"Use it before run_local_agent when you are not sure which agent or computer to use.",
		map[string]any{"type": "object", "properties": map[string]any{}},
		t.list, SourceLocalAgent)

	r.RegisterSerialFrom(RunLocalAgentToolName, strings.Join([]string{
		"Run a task with a coding agent (Claude Code, Codex, …) on the PERSON'S OWN computer, linked to this chat, and get its final answer back.",
		"Use it when the request needs their computer: reading or changing code, files or repositories there, running commands, builds or tests, checking something on their machine.",
		"Do NOT use it for chit-chat, general knowledge, writing or anything you can answer yourself.",
		"Write \"task\" as a complete, self-contained instruction for the coding agent (it does not see this chat), in the person's language. Follow-ups in this chat continue the same local session, so you can refer to earlier work.",
		"The run can take minutes; the person may be asked in this chat to approve actions (they reply 1/2/3). Summarize the result for the person in your own words; do not paste huge outputs.",
		"If it returns an error (computer offline, agent missing, timeout), tell the person plainly what happened and what they can do.",
	}, "\n"), map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent":   map[string]any{"type": "string", "description": `Coding agent id on the computer, e.g. "claude" or "codex" (see local_agents).`},
			"task":    map[string]any{"type": "string", "description": "The full instruction for the coding agent."},
			"machine": map[string]any{"type": "string", "description": "Computer name or id; omit to use the first online computer that has the agent."},
			"cwd":     map[string]any{"type": "string", "description": `Working directory on the computer, e.g. "~/code/app"; omit for the computer's default.`},
		},
		"required": []string{"agent", "task"},
	}, t.run, SourceLocalAgent)
}

// RegisterSendLocalFile mounts send_local_file: fetch a file a coding
// agent produced on the person's computer into the workspace, so the
// reply can show it (image) or attach it (document) in this chat.
func RegisterSendLocalFile(r *Registry, svc LocalFileFetcher, save SaveFileFunc, userID string) {
	t := &sendLocalFileTool{svc: svc, save: save, userID: userID}
	r.RegisterSerialFrom(SendLocalFileToolName, strings.Join([]string{
		"Fetch a file from the PERSON'S OWN computer (one a coding agent created or that they ask for) so you can show or send it in this chat.",
		"Use it after run_local_agent made an image, chart, PDF or document the person wants to see: the chat can display images and attach files, the person can't open paths on their computer from here.",
		"Allowed: png, jpg, gif, webp, pdf, docx, xlsx, pptx, txt, md, csv, zip, mp4, mp3 (max 20 MB), not files in hidden folders. SVG can't be shown: ask the coding agent to export a PNG first.",
		"It returns a workspace path. To show an image, put ![](/workspace/<name>) in your reply; to attach any other file, put [<name>](/workspace/<name>). Don't describe the path to the person.",
	}, "\n"), map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": `File path on the computer, e.g. "~/Downloads/cat.png" (absolute, ~/…, or relative to the computer's default folder).`},
			"machine": map[string]any{"type": "string", "description": "Computer name or id; omit for the first online computer."},
		},
		"required": []string{"path"},
	}, t.run, SourceLocalAgent)
}

type sendLocalFileTool struct {
	svc    LocalFileFetcher
	save   SaveFileFunc
	userID string
}

func (t *sendLocalFileTool) run(ctx context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Path    string `json:"path"`
		Machine string `json:"machine"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	f, err := t.svc.Fetch(ctx, agl.FetchRun{UserID: t.userID, Machine: a.Machine, Path: a.Path})
	if err != nil {
		var te *agl.ToolError
		if errors.As(err, &te) {
			return "", te
		}
		return "", fmt.Errorf("send_local_file: %w", err)
	}
	name, err := t.save(ctx, f)
	if err != nil {
		return "", fmt.Errorf("send_local_file: could not store %s: %w", f.Name, err)
	}
	ref := fmt.Sprintf("[%s](/workspace/%s)", name, name)
	if f.IsImage() {
		ref = fmt.Sprintf("![](/workspace/%s)", name)
	}
	blob, _ := json.Marshal(map[string]any{
		"workspacePath": "/workspace/" + name,
		"bytes":         len(f.Bytes),
		"contentType":   f.ContentType,
		"putInReply":    ref,
	})
	return string(blob), nil
}

type localAgentsTool struct {
	svc    LocalAgentRunner
	userID string
	chat   agl.ToolChat
}

func (t *localAgentsTool) list(ctx context.Context, _ json.RawMessage) (string, error) {
	machines, err := t.svc.Machines(ctx, t.userID)
	if err != nil {
		return "", fmt.Errorf("could not list the person's computers: %w", err)
	}
	type agentView struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type machineView struct {
		ID     string      `json:"id"`
		Name   string      `json:"name"`
		Online bool        `json:"online"`
		Access string      `json:"access"`
		Agents []agentView `json:"agents"`
	}
	out := make([]machineView, 0, len(machines))
	for _, m := range machines {
		v := machineView{ID: m.ID, Name: m.Name, Online: m.Online, Access: m.DefaultAccess, Agents: []agentView{}}
		for _, a := range m.Agents {
			v.Agents = append(v.Agents, agentView{ID: a.ID, Name: a.Name})
		}
		out = append(out, v)
	}
	blob, _ := json.Marshal(map[string]any{"computers": out})
	return string(blob), nil
}

func (t *localAgentsTool) run(ctx context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Agent   string `json:"agent"`
		Task    string `json:"task"`
		Machine string `json:"machine"`
		Cwd     string `json:"cwd"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	out, err := t.svc.Run(ctx, agl.ToolRun{UserID: t.userID, Chat: t.chat, Agent: a.Agent, Task: a.Task, Machine: a.Machine, Cwd: a.Cwd})
	if err != nil {
		var te *agl.ToolError
		if errors.As(err, &te) {
			return out, te
		}
		return out, fmt.Errorf("run_local_agent: %w", err)
	}
	return out, nil
}

// LocalAgentsNote is the one-line system hint for a turn that has the
// tools: which computers and agents the person has.
func LocalAgentsNote(machines []agl.LocalMachine) string {
	var parts []string
	for _, m := range machines {
		state := "online"
		if !m.Online {
			state = "offline"
		}
		ids := make([]string, 0, len(m.Agents))
		for _, a := range m.Agents {
			ids = append(ids, a.ID)
		}
		agents := strings.Join(ids, ", ")
		if agents == "" {
			agents = "agents not reported yet"
		}
		parts = append(parts, fmt.Sprintf("%s (%s: %s)", m.Name, state, agents))
	}
	return "The person has linked their own computer(s): " + strings.Join(parts, "; ") +
		". When a request needs their computer (code, files, repos, running commands), use run_local_agent; answer everything else yourself." +
		" When the result is an image or file the person should see, fetch it with send_local_file and put it in your reply (ask for PNG instead of SVG)."
}
