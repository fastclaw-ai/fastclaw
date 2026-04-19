package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// slashResult holds the result of a slash command.
type slashResult struct {
	handled bool
	reply   string
}

// handleSlashCommand checks if the message is a slash command and handles it.
func (a *Agent) handleSlashCommand(msg bus.InboundMessage) slashResult {
	text := strings.TrimSpace(msg.Text)
	if !strings.HasPrefix(text, "/") {
		return slashResult{}
	}

	parts := strings.Fields(text)
	cmd := strings.ToLower(parts[0])
	// Strip @botname suffix: /status@mybot → /status
	if idx := strings.Index(cmd, "@"); idx > 0 {
		cmd = cmd[:idx]
	}
	args := parts[1:]

	switch cmd {
	case "/start":
		return slashResult{
			handled: true,
			reply:   fmt.Sprintf("👋 Hi! I'm %s, your AI assistant.\n\nJust send me a message to chat. Use /help to see available commands.", a.name),
		}

	case "/new", "/reset":
		if msg.Channel == "web" {
			// For web channel, don't delete the session file — frontend handles new session creation
			return slashResult{handled: true, reply: "__NEW_SESSION__"}
		}
		sess := a.sessions.Get(msg.Channel, msg.ChatID)
		sess.Clear()
		return slashResult{handled: true, reply: "🔄 Session cleared. Starting fresh."}

	case "/retry":
		return a.slashRetry(msg)

	case "/undo":
		return a.slashUndo(msg)

	case "/compact":
		return a.slashCompact(msg)

	case "/status":
		return a.slashStatus(msg)

	case "/usage":
		return a.slashUsage(msg)

	case "/insights":
		days := 7
		if len(args) > 0 {
			fmt.Sscanf(args[0], "%d", &days)
		}
		return a.slashInsights(msg, days)

	case "/personality":
		if len(args) == 0 {
			return a.slashPersonalityList(msg)
		}
		return a.slashPersonalitySet(msg, args[0])

	case "/model":
		if len(args) == 0 {
			return slashResult{handled: true, reply: fmt.Sprintf("Current model: `%s`\n\nUsage: /model <model-name>\nExample: /model gpt-4o-mini", a.model)}
		}
		return a.slashModel(msg, args[0])

	case "/help":
		return slashResult{handled: true, reply: a.slashHelp()}

	case "/version":
		return slashResult{handled: true, reply: fmt.Sprintf("⚡ FastClaw\nAgent: %s\nModel: %s", a.name, a.model)}

	default:
		return slashResult{}
	}
}

// slashRetry re-runs the last user message, discarding the last assistant response.
func (a *Agent) slashRetry(msg bus.InboundMessage) slashResult {
	sess := a.sessions.Get(msg.Channel, msg.ChatID)
	msgs := sess.GetMessages()

	// Find the last user message
	lastUserIdx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx < 0 {
		return slashResult{handled: true, reply: "No previous message to retry."}
	}

	// Save snapshot for undo
	sess.Snapshot()

	// Trim to just before the last user message
	sess.ReplaceMessages(msgs[:lastUserIdx])

	// Re-inject the user message as a new inbound
	lastUserText := msgs[lastUserIdx].Content
	retryMsg := msg
	retryMsg.Text = lastUserText

	// Signal that we want to re-process this message (return not-handled so gateway retries)
	// But we return handled here to avoid double-processing — gateway should re-send
	return slashResult{
		handled: true,
		reply:   fmt.Sprintf("🔁 Retrying: *%s*", truncateSlash(lastUserText, 80)),
	}
}

// slashUndo reverts the last assistant response.
func (a *Agent) slashUndo(msg bus.InboundMessage) slashResult {
	sess := a.sessions.Get(msg.Channel, msg.ChatID)

	if !sess.HasSnapshot() {
		// No snapshot — try to remove last user+assistant turn manually
		msgs := sess.GetMessages()
		if len(msgs) < 2 {
			return slashResult{handled: true, reply: "Nothing to undo."}
		}
		// Trim trailing assistant messages + the user message before them
		end := len(msgs)
		for end > 0 && msgs[end-1].Role == "assistant" {
			end--
		}
		if end > 0 && msgs[end-1].Role == "user" {
			end--
		}
		sess.ReplaceMessages(msgs[:end])
		return slashResult{handled: true, reply: "↩️ Undid last turn."}
	}

	if sess.Undo() {
		return slashResult{handled: true, reply: "↩️ Undid last action."}
	}
	return slashResult{handled: true, reply: "Nothing to undo."}
}

func (a *Agent) slashCompact(msg bus.InboundMessage) slashResult {
	sess := a.sessions.Get(msg.Channel, msg.ChatID)
	sessionMsgs := sess.GetMessages()

	if len(sessionMsgs) == 0 {
		return slashResult{handled: true, reply: "No messages to compact."}
	}

	result, err := CompactMessages(sessionMsgs, a.homePath, a.provider, a.model)
	if err != nil {
		return slashResult{handled: true, reply: fmt.Sprintf("Compaction error: %v", err)}
	}
	if result != nil && result.Pruned {
		sess.ReplaceMessages(result.Messages)
		return slashResult{handled: true, reply: fmt.Sprintf("✅ Compacted: %d → %d messages.", len(sessionMsgs), len(result.Messages))}
	}
	return slashResult{handled: true, reply: "Session is within limits, no compaction needed."}
}

func (a *Agent) slashStatus(msg bus.InboundMessage) slashResult {
	sess := a.sessions.Get(msg.Channel, msg.ChatID)
	sessionMsgs := sess.GetMessages()

	memContent := a.memory.LoadMemory()
	memLines := 0
	if memContent != "" {
		memLines = strings.Count(memContent, "\n") + 1
	}

	soul := a.loadSoulName()

	status := fmt.Sprintf("⚡ FastClaw Status\n"+
		"─────────────────\n"+
		"Agent:       %s\n"+
		"Model:       %s\n"+
		"Personality: %s\n"+
		"Max Tokens:  %d\n"+
		"Temperature: %.1f\n"+
		"Max Iter:    %d\n"+
		"Session Msgs:%d\n"+
		"Memory:      %d lines\n"+
		"Workspace:   %s",
		a.name, a.model, soul,
		a.maxTokens, a.temperature, a.maxToolIterations,
		len(sessionMsgs), memLines, a.homePath,
	)
	return slashResult{handled: true, reply: status}
}

func (a *Agent) slashUsage(msg bus.InboundMessage) slashResult {
	sess := a.sessions.Get(msg.Channel, msg.ChatID)
	msgs := sess.GetMessages()

	userTurns, asstTurns, toolTurns := 0, 0, 0
	for _, m := range msgs {
		switch m.Role {
		case "user":
			userTurns++
		case "assistant":
			asstTurns++
		case "tool":
			toolTurns++
		}
	}

	reply := fmt.Sprintf("📊 Session Usage\n"+
		"User turns:      %d\n"+
		"Assistant turns: %d\n"+
		"Tool calls:      %d\n"+
		"Total messages:  %d",
		userTurns, asstTurns, toolTurns, len(msgs),
	)

	// Append cost tracking info from SDK engine
	if a.costTracker != nil {
		stats := a.costTracker.Stats()
		reply += fmt.Sprintf("\n─────────────────\n"+
			"Cost:            %s\n"+
			"Input tokens:    %v\n"+
			"Output tokens:   %v\n"+
			"API duration:    %vms\n"+
			"Tool duration:   %vms",
			a.costTracker.FormatCost(),
			stats["totalInputTokens"],
			stats["totalOutputTokens"],
			stats["totalAPIDurationMs"],
			stats["totalToolDurationMs"],
		)
	}

	return slashResult{handled: true, reply: reply}
}

func (a *Agent) slashInsights(msg bus.InboundMessage, days int) slashResult {
	logDir := filepath.Join(a.homePath, "memory", "logs")
	cutoff := time.Now().AddDate(0, 0, -days)

	files, _ := filepath.Glob(filepath.Join(logDir, "*.jsonl"))
	totalFiles, recentFiles := 0, 0
	for _, f := range files {
		totalFiles++
		info, err := os.Stat(f)
		if err == nil && info.ModTime().After(cutoff) {
			recentFiles++
		}
	}

	reply := fmt.Sprintf("🔍 Insights (last %d days)\n"+
		"─────────────────────────\n"+
		"Log files:       %d total, %d recent\n"+
		"Memory file:     %s\n"+
		"Workspace:       %s\n\n"+
		"Tip: Use /status for session info, /usage for token stats.",
		days, totalFiles, recentFiles,
		func() string {
			info, err := os.Stat(filepath.Join(a.homePath, "MEMORY.md"))
			if err != nil {
				return "not found"
			}
			return fmt.Sprintf("%.1f KB, updated %s", float64(info.Size())/1024, info.ModTime().Format("2006-01-02 15:04"))
		}(),
		a.homePath,
	)
	return slashResult{handled: true, reply: reply}
}

// slashPersonalityList lists available SOUL.md presets.
func (a *Agent) slashPersonalityList(msg bus.InboundMessage) slashResult {
	presets := a.listPersonalities()
	if len(presets) == 0 {
		return slashResult{handled: true, reply: "No personality presets found.\n\nCreate files named SOUL-<name>.md in your workspace to add presets.\nExample: SOUL-assistant.md, SOUL-dev.md"}
	}
	current := a.loadSoulName()
	var sb strings.Builder
	sb.WriteString("🎭 Personalities\n")
	sb.WriteString("─────────────────\n")
	for _, p := range presets {
		if p == current {
			sb.WriteString(fmt.Sprintf("• %s ← current\n", p))
		} else {
			sb.WriteString(fmt.Sprintf("• %s\n", p))
		}
	}
	sb.WriteString("\nUsage: /personality <name>")
	return slashResult{handled: true, reply: sb.String()}
}

// slashPersonalitySet switches the active SOUL.md.
func (a *Agent) slashPersonalitySet(msg bus.InboundMessage, name string) slashResult {
	// Look for SOUL-<name>.md in workspace
	srcPath := filepath.Join(a.homePath, fmt.Sprintf("SOUL-%s.md", name))
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		return slashResult{handled: true, reply: fmt.Sprintf("Personality '%s' not found.\nExpected: %s", name, srcPath)}
	}

	data, err := os.ReadFile(srcPath)
	if err != nil {
		return slashResult{handled: true, reply: fmt.Sprintf("Error reading personality: %v", err)}
	}

	destPath := filepath.Join(a.homePath, "SOUL.md")
	if err := os.WriteFile(destPath, data, 0o644); err != nil {
		return slashResult{handled: true, reply: fmt.Sprintf("Error applying personality: %v", err)}
	}

	return slashResult{handled: true, reply: fmt.Sprintf("🎭 Personality set to: **%s**\nSOUL.md updated. Takes effect on the next message.", name)}
}

// slashModel switches the active model for this agent session.
func (a *Agent) slashModel(msg bus.InboundMessage, model string) slashResult {
	old := a.model
	a.model = model
	return slashResult{handled: true, reply: fmt.Sprintf("🤖 Model switched: `%s` → `%s`", old, model)}
}

// listPersonalities finds SOUL-<name>.md files in workspace.
func (a *Agent) listPersonalities() []string {
	pattern := filepath.Join(a.homePath, "SOUL-*.md")
	files, _ := filepath.Glob(pattern)
	var names []string
	for _, f := range files {
		base := filepath.Base(f)
		// SOUL-<name>.md → <name>
		name := strings.TrimPrefix(base, "SOUL-")
		name = strings.TrimSuffix(name, ".md")
		names = append(names, name)
	}
	return names
}

// loadSoulName returns the current personality name (default if standard SOUL.md).
func (a *Agent) loadSoulName() string {
	// Check if current SOUL.md is a known preset
	for _, p := range a.listPersonalities() {
		srcPath := filepath.Join(a.homePath, fmt.Sprintf("SOUL-%s.md", p))
		soulPath := filepath.Join(a.homePath, "SOUL.md")
		srcData, err1 := os.ReadFile(srcPath)
		soulData, err2 := os.ReadFile(soulPath)
		if err1 == nil && err2 == nil && string(srcData) == string(soulData) {
			return p
		}
	}
	return "default"
}

func (a *Agent) slashHelp() string {
	return `⚡ FastClaw Commands

Conversation
  /new, /reset    — Clear session history
  /retry          — Re-run last message
  /undo           — Undo last turn

Context
  /compact        — Compress context window
  /status         — Agent status & memory info
  /usage          — Session token/turn stats
  /insights [N]   — Activity insights (last N days, default 7)

Personality & Model
  /personality        — List available personalities
  /personality <name> — Switch personality (SOUL-<name>.md)
  /model <name>       — Switch LLM model

Info
  /help           — Show this help
  /version        — Show version`
}

func truncateSlash(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
