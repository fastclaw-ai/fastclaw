package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	coderuntime "github.com/fastclaw-ai/fastclaw/internal/runtime"
)

// SetProjectRuntime turns this agent into a coding agent: it wires the
// project runtime manager and registers the preview tools. Called once at
// boot by attachProjectRuntimeToAgents. Passing nil is a no-op (the agent
// stays a plain assistant). Safe to call repeatedly — tool registration
// overwrites by name.
func (a *Agent) SetProjectRuntime(m *coderuntime.Manager) {
	a.projectRuntime = m
	if m == nil {
		return
	}
	a.registerProjectRuntimeTools()
}

// registerProjectRuntimeTools adds start_app_preview + app_preview_logs to
// the agent's tool registry. The closures read the in-flight turn's
// identity (user / project) off the registry at call time, so a single
// registration serves every turn.
func (a *Agent) registerProjectRuntimeTools() {
	reg := a.registry

	// Surface the registered templates so the model can pick one by name
	// (and knows the default). Built at registration time — the manager has
	// every RegisterTemplate applied before SetProjectRuntime runs.
	tmplDesc := "Template ref to scaffold from on first boot (e.g. \"shipany-tanstack\"). Optional once a runtime already exists; the deployment's default is used when omitted."
	if refs := a.projectRuntime.Templates(); len(refs) > 0 {
		tmplDesc = fmt.Sprintf("Template ref to scaffold from on first boot. Available: %s — the first is the default, used when omitted. Optional once a runtime already exists.",
			strings.Join(refs, ", "))
	}

	reg.Register(
		"start_app_preview",
		"PRIMARY tool for building a web app / website / landing page / dashboard — INCLUDING requests like 'use template X to make Y' or '用某模板做个…'. "+
			"Call this FIRST for such requests. Do NOT search for skills (find-skills / `npx skills find`) and do NOT hand-run a dev server for this — the template name (e.g. shipany) is a scaffold target, not a skill. "+
			"It scaffolds the app from the template on first call, starts its dev server, and returns a live preview URL that hot-reloads as you edit files — so call it ONCE early, then just edit files and the preview updates itself. "+
			"Works in any chat: if the chat is inside a project the app is homed there (shared/persistent); otherwise it lives in this chat's own workspace. Safe to call again to re-fetch the URL.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"template": map[string]any{
					"type":        "string",
					"description": tmplDesc,
				},
			},
		},
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			if a.projectRuntime == nil {
				return "App preview is not enabled on this deployment.", nil
			}
			userID := reg.EffectiveUserIDFromCtx(ctx)
			if userID == "" {
				return "", fmt.Errorf("start_app_preview: no user resolved for this turn")
			}
			// Home the app in the project when there is one, else in this
			// chat's own session workspace — so a preview works without a
			// pre-created project.
			projectID := reg.ProjectIDFromCtx(ctx)
			sessionID := reg.SessionIDFromCtx(ctx)
			if projectID == "" && sessionID == "" {
				return "", fmt.Errorf("start_app_preview: no project or chat session to home the app in")
			}
			var args struct {
				Template string `json:"template"`
			}
			_ = json.Unmarshal(raw, &args)
			tmpl := strings.TrimSpace(args.Template)
			if tmpl == "" {
				tmpl = a.projectRuntime.DefaultTemplate()
			}
			rec, err := a.projectRuntime.Up(ctx, userID, a.name, projectID, sessionID, tmpl)
			if err != nil {
				return "", fmt.Errorf("start_app_preview: %w", err)
			}
			// Redirect this turn's file tools into the app subfolder so the
			// agent's edits land where the dev server serves (subsequent
			// turns get this from bindSession). Done by re-attaching an
			// updated TurnContext to ctx — mutating a shared registry field
			// here would race with concurrent turns on the same agent.
			if tc := tools.FromContext(ctx); tc != nil {
				tc.CodingSubdir = coderuntime.AppSubdir
			}
			return fmt.Sprintf(
				"Preview is live: %s (status: %s, template: %s).\n"+
					"The app lives in the `%s/` folder of the workspace. Your file tools are already "+
					"scoped there, so just edit files normally (e.g. write_file \"src/routes/index.tsx\") — "+
					"they land in the app and the dev server hot-reloads. "+
					"Use app_preview_logs if a change doesn't show up (build error).\n"+
					"IMPORTANT: do NOT change the dev server port or the framework config's server.port "+
					"(e.g. vite.config.ts) — the preview is mapped to port 3000; changing it breaks the link.",
				rec.PreviewURL, rec.Status, rec.TemplateRef, coderuntime.AppSubdir), nil
		},
	)

	reg.Register(
		"app_preview_logs",
		"Tail the dev-server log of the current project's preview. Use this to diagnose why an edit didn't render (compile error, crash) after start_app_preview.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tail": map[string]any{
					"type":        "integer",
					"description": "How many trailing log lines to return (default 200).",
				},
			},
		},
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			if a.projectRuntime == nil {
				return "App preview is not enabled on this deployment.", nil
			}
			userID := reg.EffectiveUserIDFromCtx(ctx)
			projectID := reg.ProjectIDFromCtx(ctx)
			sessionID := reg.SessionIDFromCtx(ctx)
			if projectID == "" && sessionID == "" {
				return "No project or chat session, so there's no preview to read logs from.", nil
			}
			var args struct {
				Tail int `json:"tail"`
			}
			_ = json.Unmarshal(raw, &args)
			if args.Tail <= 0 {
				args.Tail = 200
			}
			out, err := a.projectRuntime.Logs(ctx, userID, a.name, projectID, sessionID, args.Tail)
			if err != nil {
				return fmt.Sprintf("Couldn't read preview logs: %v (is the preview running? call start_app_preview first).", err), nil
			}
			if strings.TrimSpace(out) == "" {
				return "Dev server log is empty so far.", nil
			}
			return out, nil
		},
	)
}
