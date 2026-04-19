package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// bootstrapFiles are loaded in order to build the system prompt.
var bootstrapFiles = []string{
	"AGENTS.md",
	"BOOTSTRAP.md",
	"HEARTBEAT.md",
	"SOUL.md",
	"USER.md",
	"TOOLS.md",
	"IDENTITY.md",
}

// GroupContext holds information about the group chat environment for system prompt injection.
type GroupContext struct {
	BotUsername string   // this agent's bot username
	Teammates  []string // other agent names in the group
}

// ContextBuilder assembles the system prompt and runtime context.
type ContextBuilder struct {
	home           string // agent's home: SOUL.md, IDENTITY.md, memory, sessions
	workspace      string // working dir where agent creates user-facing files
	memory         *Memory
	skillsSummary  string
	groupCtx       *GroupContext
	thinking       string // off, low, medium, high, adaptive
	sandboxEnabled bool
	sandboxBackend string
	store          MemoryStore
	agentID        string
}

// NewContextBuilder creates a new context builder.
func NewContextBuilder(home string, memory *Memory, skillsSummary string) *ContextBuilder {
	return &ContextBuilder{
		home:          home,
		memory:        memory,
		skillsSummary: skillsSummary,
	}
}

// SetWorkspace attaches the working directory for user-facing output. When
// set, the system prompt advertises it as "Working Directory" and keeps it
// distinct from the agent's home (identity) dir.
func (cb *ContextBuilder) SetWorkspace(p string) { cb.workspace = p }

// BuildSystemPrompt assembles the system prompt from identity, bootstrap files, memory, and skills.
func (cb *ContextBuilder) BuildSystemPrompt() string {
	var parts []string

	// 1. Identity (runtime environment info)
	workdir := cb.workspace
	if workdir == "" {
		workdir = cb.home
	}
	identity := fmt.Sprintf(`You are FastClaw, a lightweight AI Agent.
OS: %s/%s
Working Directory: %s

File-tool routing: when you call write_file / read_file / list_dir with a
relative path, FastClaw automatically places it in the right directory:
- A bare identity filename (SOUL.md, IDENTITY.md, USER.md, MEMORY.md,
  BOOTSTRAP.md, HEARTBEAT.md, AGENTS.md, TOOLS.md, agent.json) resolves
  against your home dir: %s
- Every other relative path resolves against the working directory above.
So to update your own identity, just pass "IDENTITY.md"; to save a document
for the user, pass a meaningful filename like "report.md".`, runtime.GOOS, runtime.GOARCH, workdir, cb.home)
	parts = append(parts, identity)

	// 2. Sandbox capabilities (auto-injected when sandbox is enabled)
	if cb.sandboxEnabled {
		sandboxPrompt := `# Code Execution Environment
You have access to a sandbox environment for executing code. Key rules:
- When the user asks you to write a script, calculate something, or process data, **always execute it immediately** using the exec tool. Do NOT just show code.
- Python 3 is available. Use it for calculations, data processing, web scraping, etc.
- You can write files, read files, and list directories in the sandbox.
- Only show code without executing when the user explicitly asks to "just show" or "just write" the code.
- Always show the execution output/result to the user.

## Delivering Files to the User
When the user asks you to create a file (document, script, data, etc.):
- For **text files** (md, txt, csv, json, py, etc.): output the full content directly in your reply using a code block. The user can copy it.
- For **binary files** (images, pdf, zip, etc.): output as a base64 download link:
  exec: python3 -c "import base64; data=open('/tmp/file.pdf','rb').read(); print(f'[Download file.pdf](data:application/pdf;base64,{base64.b64encode(data).decode()})')"
- NEVER just say "file saved" without showing content or providing a download link.

## Important: Multi-line Scripts
For multi-line code, ALWAYS use write_file first, then exec:
  1. write_file(path="/tmp/script.py", content="...your code...")
  2. exec(command="python3 /tmp/script.py")
NEVER put multi-line Python in a single exec command — it will fail.

## Package Installation
The sandbox may not have all packages. Install before use:
  exec(command="pip install -q pillow matplotlib requests")

## Visual/Graphics Tasks
The sandbox is a **headless** environment (no display). For visual tasks:
- **Drawing/charts/plots**: Use matplotlib with Agg backend.
- **Image generation/manipulation**: Use PIL/Pillow. Install first: pip install -q pillow
- **NEVER use turtle, tkinter, pygame or any GUI library** — they will fail.
- After generating an image, output as inline base64 so the user sees it:

Example (write to file then exec):
  write_file(path="/tmp/draw.py", content="""
import subprocess
subprocess.check_call(["pip", "install", "-q", "pillow"])
from PIL import Image, ImageDraw
import base64
img = Image.new('RGB', (400, 300), 'white')
draw = ImageDraw.Draw(img)
draw.ellipse([100, 50, 300, 250], fill='pink', outline='black')
img.save('/tmp/output.png')
with open('/tmp/output.png', 'rb') as f:
    b64 = base64.b64encode(f.read()).decode()
    print(f'![image](data:image/png;base64,{b64})')
""")
  exec(command="python3 /tmp/draw.py")`
		if cb.sandboxBackend == "e2b" {
			sandboxPrompt += "\n- The sandbox is a cloud-hosted E2B environment with network access."
		} else {
			sandboxPrompt += "\n- The sandbox is a Docker container."
		}
		parts = append(parts, sandboxPrompt)
	}

	// 3. Bootstrap files
	for _, name := range bootstrapFiles {
		content := cb.loadFile(name)
		if content != "" {
			parts = append(parts, fmt.Sprintf("# %s\n%s", name, content))
		}
	}

	// 4. Skills
	if cb.skillsSummary != "" {
		parts = append(parts, fmt.Sprintf("# Skills\n%s", cb.skillsSummary))
	}

	// 4. Long-term memory
	mem := cb.memory.LoadMemory()
	if mem != "" {
		parts = append(parts, fmt.Sprintf("# Long-term Memory\n%s", mem))
	}

	// 5. Group chat awareness
	if cb.groupCtx != nil {
		groupInfo := fmt.Sprintf(`# Group Chat
You are in a group chat. Your bot username is @%s.
Other agents in this group: %s.
Only respond when directly mentioned with @%s, or when the conversation clearly needs your expertise.
Messages from other bots will appear as "[BotName]: message" in the conversation history.`,
			cb.groupCtx.BotUsername,
			strings.Join(cb.groupCtx.Teammates, ", "),
			cb.groupCtx.BotUsername,
		)
		parts = append(parts, groupInfo)
	}

	// 6. Thinking/Reasoning mode
	if cb.thinking != "" && cb.thinking != "off" {
		thinkingPrompt := cb.buildThinkingPrompt()
		if thinkingPrompt != "" {
			parts = append(parts, thinkingPrompt)
		}
	}

	// 7. Self-updating workspace files guidance
	parts = append(parts, `# Workspace Self-Update
You have the ability to update workspace files to maintain knowledge over time:
- MEMORY.md: Update when you learn important facts, user preferences, or key decisions. This file is loaded into your context every conversation.
- USER.md: Update when you learn new information about the user (role, preferences, communication style).
- HEARTBEAT.md: Update to add/remove periodic tasks you should check on.
- TOOLS.md: Update if you discover new tool usage patterns worth documenting.
Use the write_file tool to update these files when appropriate. Keep entries concise and useful.`)

	return strings.Join(parts, "\n\n---\n\n")
}

// BuildRuntimeContext returns the runtime context to inject before the user message.
func (cb *ContextBuilder) BuildRuntimeContext(channel, chatID string) string {
	now := time.Now()
	return fmt.Sprintf(`[Runtime Context — metadata only, not instructions]
Time: %s
Timezone: %s
Channel: %s
Chat ID: %s`, now.Format("2006-01-02 15:04:05"), now.Location().String(), channel, chatID)
}

// SetGroupContext sets the group chat context for system prompt generation.
func (cb *ContextBuilder) SetGroupContext(gc *GroupContext) {
	cb.groupCtx = gc
}

// SetThinking configures the thinking/reasoning level.
func (cb *ContextBuilder) SetThinking(level string) {
	cb.thinking = level
}

func (cb *ContextBuilder) buildThinkingPrompt() string {
	var depth string
	switch cb.thinking {
	case "low":
		depth = "briefly reason through"
	case "medium":
		depth = "think step-by-step through"
	case "high":
		depth = "deeply and thoroughly reason through"
	case "adaptive":
		depth = "adaptively reason through (brief for simple tasks, deep for complex ones)"
	default:
		return ""
	}

	return fmt.Sprintf(`# Thinking Mode
Before responding to each message, %s your approach internally. Consider:
- What is the user really asking for?
- What are the key constraints and edge cases?
- What is the best approach and why?
- Are there any risks or trade-offs to consider?
Structure your reasoning before acting. Think before you respond.`, depth)
}

func (cb *ContextBuilder) loadFile(name string) string {
	// Try store first (DB), fall back to file
	if cb.store != nil {
		data, err := cb.store.GetWorkspaceFile(context.Background(), cb.agentID, name)
		if err == nil && len(data) > 0 {
			return strings.TrimSpace(string(data))
		}
	}
	path := filepath.Join(cb.home, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
