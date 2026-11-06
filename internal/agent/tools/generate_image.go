package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agl"
)

// GenerateImageToolName makes a picture with Codex's built-in image
// generation on the person's computer and puts it in the chat.
const GenerateImageToolName = "generate_image"

// imageBriefs are the local agents FastClaw can brief for a picture, in
// order of preference, each with its task. Only agents that report
// agl.CapImageGeneration on the machine are used.
var imageBriefs = []struct {
	agent string
	task  func(prompt, aspect, path string) string
}{
	{"codex", imageTask},
}

func imageAgents() []string {
	out := make([]string, 0, len(imageBriefs))
	for _, b := range imageBriefs {
		out = append(out, b.agent)
	}
	return out
}

func imageBrief(agent string) func(prompt, aspect, path string) string {
	for _, b := range imageBriefs {
		if strings.EqualFold(b.agent, agent) {
			return b.task
		}
	}
	return nil
}

// CanGenerateImage reports whether one of machines has a local agent
// that can generate images (generate_image is mounted then).
func CanGenerateImage(machines []agl.LocalMachine) bool {
	return len(agl.FindCapable(machines, agl.CapImageGeneration, "", imageAgents())) > 0
}

// imageUnavailable is what the task asks Codex to answer with when it
// can't generate images, instead of drawing one some other way.
const imageUnavailable = "IMAGEGEN_UNAVAILABLE"

// LocalImageMaker is what generate_image needs (*agl.Tools).
type LocalImageMaker interface {
	LocalAgentRunner
	LocalFileFetcher
}

// RegisterGenerateImage mounts generate_image. Fastclaw writes Codex's
// task itself — "use your image generation tool, never code" — because
// models asked to brief Codex keep telling it to draw with Pillow/SVG,
// especially once a chat has done that before.
func RegisterGenerateImage(r *Registry, svc LocalImageMaker, save SaveFileFunc, userID string, chat agl.ToolChat, notify func(agl.ToolNotice)) {
	t := &generateImageTool{svc: svc, save: save, userID: userID, chat: chat, notify: notify, now: time.Now}
	r.RegisterSerialFrom(GenerateImageToolName, strings.Join([]string{
		"Generate a picture (photo, illustration, portrait, logo, poster, sticker, …) with the image generation model of Codex on the PERSON'S OWN computer, and get it ready to show in this chat.",
		"Use it for EVERY request to draw, paint, generate or make an image — including \"让 codex 画…\" — instead of run_local_agent. Don't describe how to draw it; describe what the picture shows.",
		"Write \"prompt\" as a rich visual description: subject, style, composition, colors, mood, and any text that must appear in the image. Never ask for code, SVG or a specific drawing library.",
		"It can take a minute or two. It returns putInReply: put that exactly in your reply so the image is shown, plus a short line of your own.",
	}, "\n"), map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt":  map[string]any{"type": "string", "description": "What the picture shows, in detail."},
			"aspect":  map[string]any{"type": "string", "enum": []string{"square", "portrait", "landscape"}, "description": "Shape of the picture; default square."},
			"machine": map[string]any{"type": "string", "description": "Computer name or id; omit to use the first online computer that can generate images."},
		},
		"required": []string{"prompt"},
	}, t.run, SourceLocalAgent)
}

type generateImageTool struct {
	svc    LocalImageMaker
	save   SaveFileFunc
	userID string
	chat   agl.ToolChat
	notify func(agl.ToolNotice)
	now    func() time.Time
}

// imageTask is Codex's instruction for one picture saved at path.
func imageTask(prompt, aspect, path string) string {
	switch aspect {
	case "portrait":
		aspect = "portrait (taller than wide)"
	case "landscape":
		aspect = "landscape (wider than tall)"
	default:
		aspect = "square"
	}
	return strings.Join([]string{
		"Generate this picture with your built-in image generation tool (the imagegen / image_gen tool).",
		"Do NOT draw it with code: no Python, Pillow, matplotlib, cairo, SVG, HTML or canvas, and don't download an image from the web. If the image generation tool is not available or fails, reply with the single line " + imageUnavailable + ": <reason> and stop — don't make the picture any other way.",
		"",
		"Picture: " + prompt,
		"Shape: " + aspect + ".",
		"",
		"When it is generated, copy the PNG to " + path + " (create the folder if needed) and reply with only that absolute path.",
	}, "\n")
}

func (t *generateImageTool) run(ctx context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Prompt  string `json:"prompt"`
		Aspect  string `json:"aspect"`
		Machine string `json:"machine"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	a.Prompt = strings.TrimSpace(a.Prompt)
	if a.Prompt == "" {
		return "", &agl.ToolError{Msg: `"prompt" is required: describe the picture.`}
	}
	machines, err := t.svc.Machines(ctx, t.userID)
	if err != nil {
		return "", fmt.Errorf("generate_image: %w", err)
	}
	found := agl.FindCapable(machines, agl.CapImageGeneration, a.Machine, imageAgents())
	if len(found) == 0 {
		return "", &agl.ToolError{Msg: "None of the person's computers has a local agent that can generate images (Codex with image generation turned on). Tell them; don't draw it another way unless they ask."}
	}
	pick := found[0]
	if !pick.Machine.Online {
		return "", &agl.ToolError{Msg: fmt.Sprintf("The computer %s is offline. Tell the person to make sure it is on, connected, and agl is running (agl status), then try again.", pick.Machine.Name)}
	}
	path := "~/Downloads/" + t.now().Format("20060102-150405") + "-image.png"
	out, err := t.svc.Run(ctx, agl.ToolRun{
		UserID: t.userID, Chat: t.chat, Agent: pick.Agent, Machine: pick.Machine.ID,
		Task: imageBrief(pick.Agent)(a.Prompt, a.Aspect, path), Notify: t.notify,
	})
	if err != nil {
		var te *agl.ToolError
		if errors.As(err, &te) {
			return out, te
		}
		return out, fmt.Errorf("generate_image: %w", err)
	}
	if i := strings.Index(out, imageUnavailable); i >= 0 {
		reason := strings.TrimSpace(strings.TrimPrefix(out[i+len(imageUnavailable):], ":"))
		return "", &agl.ToolError{Msg: "Codex on the person's computer can't generate images right now: " + reason +
			". Tell the person; updating Codex (npm i -g @openai/codex) and signing in again usually fixes it. Don't draw it another way unless they ask."}
	}
	f, err := t.svc.Fetch(ctx, agl.FetchRun{UserID: t.userID, Machine: pick.Machine.ID, Path: path})
	if err != nil {
		var te *agl.ToolError
		if errors.As(err, &te) {
			return "", &agl.ToolError{Msg: "Codex finished but the picture isn't at " + path + " (" + te.Msg + "). Codex said: " + clipOut(out)}
		}
		return "", fmt.Errorf("generate_image: %w", err)
	}
	name, err := t.save(ctx, f)
	if err != nil {
		return "", fmt.Errorf("generate_image: could not store the picture: %w", err)
	}
	blob, _ := json.Marshal(map[string]any{
		"workspacePath":   "/workspace/" + name,
		"savedOnComputer": path,
		"putInReply":      fmt.Sprintf("![](/workspace/%s)", name),
	})
	return string(blob), nil
}

func clipOut(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 300 {
		return string(r[:300]) + "…"
	}
	return s
}
