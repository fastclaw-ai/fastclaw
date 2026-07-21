package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/usage"
)

// slashResult holds the result of a slash command.
//
// continuationQueued flags slashes that pushed a follow-up message onto
// bus.Inbound (currently /goal foo and /goal resume). HandleMessage uses
// it to emit a `turn_pending` event instead of `done`, which keeps the
// caller's SSE stream open until the continuation's own `done` arrives —
// so the typing indicator stays visible during the model-thinking gap.
type slashResult struct {
	handled            bool
	reply              string
	continuationQueued bool
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

	// Owner-only gate for write commands. Read-only inspections (/status,
	// /usage, /insights, /help, /version, /start, /whoami) stay open so
	// any group member can self-serve info. Mutators that change the
	// agent's runtime state (model, personality) or shared group-session
	// history are restricted to the agent owner + per-channel admin
	// allowlist. A DM chatter may start a fresh copy of their own session
	// with /new or /reset; those commands don't affect anybody else there.
	if slashRequiresAdmin(cmd, msg) && !a.isAdminChatter(msg) {
		return slashResult{
			handled: true,
			reply:   fmt.Sprintf("🔒 `%s` 只有 agent owner / admin 能用。让 owner 把你的 platform 用户 ID 加进 agent.json 的 `admins.%s` 里(用 `/whoami` 查自己的 ID)。", cmd, msg.Channel),
		}
	}

	switch cmd {
	case "/start":
		return slashResult{
			handled: true,
			reply:   fmt.Sprintf("👋 Hi! I'm %s, your AI assistant.\n\nJust send me a message to chat. Use /help to see available commands.", a.name),
		}

	case "/new", "/reset":
		// Clear any goal attached to the OLD session_key — design
		// §6 chose "fresh session = clean state" over "goal follows
		// chat". Runs before the web short-circuit too, so frontend-
		// driven /new also reaps the goal row.
		if a.goalStore != nil {
			oldKey := a.resolveSessionKey(msg)
			a.clearGoalForSession(oldKey)
		}
		if msg.Channel == "web" {
			// For web channel, don't delete the session file — frontend handles new session creation
			return slashResult{handled: true, reply: "__NEW_SESSION__"}
		}
		// Mint a fresh session under the same (channel, account, chat)
		// triple so this conversation thread starts blank but the prior
		// thread is preserved as history. Subsequent inbound messages
		// resolve to the new (max updated_at) row via Manager.Get's
		// active-session lookup.
		a.sessions.OpenNewSession(msg.Channel, msg.AccountID, msg.ChatID)
		return slashResult{handled: true, reply: "🔄 New session started. Previous conversation kept as history."}

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

	case "/goal":
		return a.slashGoal(msg, args)

	case "/plan":
		return a.slashPlan(msg, args)

	case "/help":
		return slashResult{handled: true, reply: a.slashHelp()}

	case "/version":
		return slashResult{handled: true, reply: fmt.Sprintf("⚡ FastClaw\nAgent: %s\nModel: %s", a.name, a.model)}

	case "/whoami":
		adminLine := "no — operator-only actions (host shell, agent management, write-slash commands) are unavailable"
		if a.isAdminChatter(msg) {
			adminLine = "yes"
		}
		return slashResult{
			handled: true,
			reply: fmt.Sprintf("Channel: `%s`\nYour user ID: `%s`\nSender name: `%s`\nAdmin: %s\n\n(The operator can add this ID to `admins.%s` in the agent config to grant admin access.)",
				msg.Channel, msg.UserID, msg.SenderName, adminLine, msg.Channel),
		}

	default:
		return slashResult{}
	}
}

// writeSlashCommands are the slash commands that mutate the agent's runtime
// state or session history and therefore need the owner/admin gate. Anything
// not in this set is treated as read-only and runs unrestricted.
var writeSlashCommands = map[string]bool{
	"/new":         true,
	"/reset":       true,
	"/undo":        true,
	"/retry":       true,
	"/compact":     true,
	"/model":       true,
	"/personality": true,
}

// slashRequiresAdmin keeps agent-wide mutations owner/admin-only and also
// protects shared group history. Starting a fresh private session is a
// per-chatter operation, so /new and /reset stay available outside groups.
func slashRequiresAdmin(cmd string, msg bus.InboundMessage) bool {
	if !writeSlashCommands[cmd] {
		return false
	}
	if (cmd == "/new" || cmd == "/reset") && msg.PeerKind != "group" {
		return false
	}
	return true
}

// isAdminChatter decides whether the chatter is allowed to run a write-mode
// slash command on this channel.
//
// Web / api: the chatter's UserID is the FastClaw user UUID — owner is
// identified by direct equality with the agent's ownerUserID. No
// per-platform allowlist needed.
//
// IM channels (discord, telegram, slack, ...): UserID is the platform's
// own user ID (Discord snowflake, Telegram numeric ID, ...), which has
// no inherent link to the agent's FastClaw owner. The owner registers
// platform IDs in agent.json's `admins[channel]` to grant access — and,
// to keep single-user dev installs from being locked out of their own
// agent, an empty/absent allowlist for the channel falls through to
// "anyone can run it" (the legacy behavior). Operators who care about
// group-chat protection populate the list to lock it down.
func (a *Agent) isAdminChatter(msg bus.InboundMessage) bool {
	// WeCom groups are shared conversations whose members retain their own
	// platform identities. Product policy is stricter than the generic IM
	// allowlist: no group member, including an owner/allowlisted identity,
	// may gain admin or host-level trust through the group.
	if msg.Channel == "wecom" && msg.PeerKind == "group" {
		return false
	}
	// Web / api carry FastClaw UUIDs directly; owner check is sufficient.
	if msg.Channel == "web" || msg.Channel == "api" {
		return msg.UserID != "" && msg.UserID == a.ownerUserID
	}
	// Shared-identity channels rewrite EVERY speaker's UserID to the
	// channel owner's id (routing.processInbound), which makes the
	// owner-equality check below meaningless in groups: any group member
	// would pass as the owner. The platform-side sender id is gone by
	// this point, so there's nothing to match against an allowlist
	// either — deny. DMs on a shared-identity channel are fine: the
	// owner marked the channel as personally theirs, and a DM sender on
	// their own bot is them by construction.
	if msg.SharedIdentity && msg.PeerKind == "group" {
		return false
	}
	list, ok := a.admins[msg.Channel]
	if !ok || len(list) == 0 {
		// No allowlist configured for this channel. Fall back to
		// ownership check: if the IM chatter's resolved FastClaw
		// user_id matches the agent owner, they're admin. Otherwise
		// deny — an unconfigured allowlist should NOT grant admin
		// to every anonymous chatter on a public-facing IM channel.
		return msg.UserID != "" && msg.UserID == a.ownerUserID
	}
	for _, id := range list {
		if id == msg.UserID {
			return true
		}
	}
	return false
}

// isTrustedTurn decides whether the current turn may touch the HOST
// (host-shell exec, file access outside the workspace) on a self-hosted
// install. Admin chatters qualify (isAdminChatter). Heartbeat turns do
// too: their instructions come from HEARTBEAT.md, which the
// identity-file gate keeps writable only by admin chatters. Cron
// replays and subagent spawns stay untrusted even though they're
// runtime-originated — their payload text was authored in some earlier
// chat turn whose chatter can't be verified here, so a guest could park
// a hostile command in a cron job and have it replayed with elevated
// rights.
func (a *Agent) isTrustedTurn(msg bus.InboundMessage) bool {
	if msg.Channel == "wecom" && msg.PeerKind == "group" {
		return false
	}
	if msg.Source == bus.SourceHeartbeat {
		return true
	}
	return a.isAdminChatter(msg)
}

// slashRetry re-runs the last user message, discarding the last assistant response.
func (a *Agent) slashRetry(msg bus.InboundMessage) slashResult {
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
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
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)

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
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
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
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
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
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
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

	reply := a.billingUsageText(context.Background())
	if reply != "" {
		reply += "\n\n"
	}
	reply += fmt.Sprintf("📊 Session Usage\n"+
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

func (a *Agent) billingUsageText(ctx context.Context) string {
	if a.meter == nil {
		return ""
	}
	userID := a.ownerUserID
	if userID == "" {
		return ""
	}
	if a.quotaStore != nil {
		if _, qerr := a.quotaStore.GetQuota(ctx, userID); qerr == nil {
			if status, err := usage.CheckQuota(ctx, a.quotaStore, a.meter, userID); err == nil && status != nil {
				return fmt.Sprintf("💳 Billing Usage\n"+
					"Billing user:   %s\n"+
					"Tokens:         %d / %s\n"+
					"Requests:       %d / %s\n"+
					"Remaining:      %s tokens, %s requests\n"+
					"Allowed:        %t\n"+
					"Resets at:      %s",
					userID,
					status.TokensUsed, usageLimitText(status.MonthlyTokenLimit),
					status.RequestsUsed, usageLimitText(status.MonthlyRequestLimit),
					remainingText(status.MonthlyTokenLimit, status.TokensUsed),
					remainingText(status.MonthlyRequestLimit, status.RequestsUsed),
					status.Allowed, emptyDash(status.ResetsAt))
			}
		}
	}
	totals, err := a.meter.TotalsForUser(ctx, userID, usage.LastN(30))
	if err != nil {
		return ""
	}
	tokens := totals.Input + totals.Output + totals.CacheRead + totals.CacheCreation
	return fmt.Sprintf("💳 Billing Usage\n"+
		"Billing user:   %s\n"+
		"Tokens:         %d used in last 30 days\n"+
		"Requests:       %d in last 30 days\n"+
		"Quota:          unlimited / not configured",
		userID, tokens, totals.Requests)
}

func usageLimitText(limit int64) string {
	if limit <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d", limit)
}

func remainingText(limit, used int64) string {
	if limit <= 0 {
		return "unlimited"
	}
	left := limit - used
	if left < 0 {
		left = 0
	}
	return fmt.Sprintf("%d", left)
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
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

Goal (persistent multi-turn objective)
  /goal <objective> — Create a goal; agent self-continues until done
  /goal             — Show current goal status
  /goal pause       — Pause continuation
  /goal resume      — Resume a paused goal
  /goal clear       — Delete the goal

Plan
  /plan <task>      — Run <task> in plan mode: emit a numbered plan, no tool calls

Info
  /help           — Show this help
  /version        — Show version
  /whoami         — Show your platform user ID

🔒 Agent-wide write commands (/undo /retry /compact /model /personality)
   and group-chat /new or /reset are restricted to the agent owner + admins
   listed in agent.json's "admins" field. Private-chat /new and /reset are
   available to the chatter. Use /whoami to find your ID.`
}

// slashPlan handles `/plan <task>`: republish the rest of the message
// onto bus.Inbound with planMode=true so the regular HandleMessage path
// routes it into handlePlanMode. Manual replacement for the auto-plan
// heuristic — users opt in explicitly per turn rather than the server
// guessing from message shape.
func (a *Agent) slashPlan(msg bus.InboundMessage, args []string) slashResult {
	task := strings.TrimSpace(strings.Join(args, " "))
	if task == "" {
		return slashResult{handled: true, reply: "Usage: `/plan <task>`"}
	}

	// Clone the inbound msg so routing fields (channel, account, chat,
	// project, user, sender, owner) carry over verbatim. Rewrite only
	// Text and Params — the plan-mode flag is what handlePlanMode keys
	// on (see isPlanMode in loop.go).
	out := msg
	out.Text = task
	params := map[string]any{}
	for k, v := range msg.Params {
		params[k] = v
	}
	params["planMode"] = true
	out.Params = params

	select {
	case a.messageBus.Inbound <- out:
		return slashResult{handled: true, reply: "", continuationQueued: true}
	default:
		return slashResult{handled: true, reply: "Bus full, try again."}
	}
}

func truncateSlash(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
