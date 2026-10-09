package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// GroupContext holds information about the group chat environment for system prompt injection.
type GroupContext struct {
	Instruction string   // authenticated topic transcript and turn instructions
	BotUsername string   // this agent's bot username
	Teammates   []string // other agent names in the group
}

// ContextBuilder assembles the system prompt and runtime context.
type ContextBuilder struct {
	home          string // agent's home: SOUL.md, IDENTITY.md, memory, sessions
	workspace     string // working dir where agent creates user-facing files
	memory        *Memory
	skillsSummary string
	// displayName is the operator-given name from agents.name. Used as
	// a fallback identity line when IDENTITY.md is empty so the model
	// doesn't introduce itself as "Claude" / its base-model name.
	displayName    string
	groupCtx       *GroupContext
	thinking       string // off, low, medium, high, adaptive
	sandboxEnabled bool
	sandboxBackend string
	// sandboxOptional marks the self-hosted "sandbox as a tool" mode: a
	// sandbox pool is wired but the host stays the default execution
	// environment, and the model opts in per call via exec(sandbox:true).
	// Mutually exclusive with sandboxEnabled (the enforced-boundary mode);
	// drives a slimmer prompt section that advertises the opt-in instead
	// of the /workspace filesystem layout. See buildinfo.IsSandboxEnforced.
	sandboxOptional bool
	// promptMode selects how heavily the framework system prompt
	// participates in the assembled prompt. Empty defaults to
	// config.PromptModeAgent for backward compatibility. Chatbot and
	// customize modes drop sections that are off-character for non-agent
	// products (task delegation, todo tracking, tool-use discipline,
	// workspace self-update, scheduling).
	promptMode string
	store      MemoryStore
	userID     string
	agentID    string
	// tzResolver maps a chatterUID to their effective *time.Location
	// (chatter pref → agent default → system default, resolved through
	// scope prefs). Wired by the manager when a relational store is
	// available; nil (or a nil return) falls back to server-local time,
	// which preserves the legacy single-tenant behavior.
	tzResolver func(chatterUID string) *time.Location
}

// ctx returns a context tagged with this builder's user, used when reading
// identity files (SOUL/IDENTITY/USER/...) from a store-backed setup so the
// SQL row scope matches per-(user, agent).
func (cb *ContextBuilder) ctx() context.Context {
	if cb.userID == "" {
		return context.Background()
	}
	return config.WithUserID(context.Background(), cb.userID)
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

// SetSkillsSummary updates the skills summary baked into the system prompt.
// Called from refreshSkillsFromStore so skills hydrated from the object
// store at turn start end up visible to the model without rebuilding the
// whole context builder.
func (cb *ContextBuilder) SetSkillsSummary(s string) { cb.skillsSummary = s }

// SetPromptMode selects the system-prompt assembly profile. Empty / unknown
// values fall back to agent mode (current default). See config.PromptMode*.
func (cb *ContextBuilder) SetPromptMode(m string) { cb.promptMode = m }

// SetDisplayName records the operator-given agent name (agents.name).
// Used as the IDENTITY.md fallback in the system prompt — without
// this the model defaults to its base-model identity ("I am Claude")
// when neither IDENTITY.md nor SOUL.md states a name.
func (cb *ContextBuilder) SetDisplayName(n string) { cb.displayName = n }

// SetTimezoneResolver wires the chatterUID → *time.Location lookup used
// to render the prompt's date line (and runtime context) in the
// chatter's local time. Re-apply after rebuilding the ContextBuilder
// (ReloadWorkspaceFiles), like the other Set* state.
func (cb *ContextBuilder) SetTimezoneResolver(f func(chatterUID string) *time.Location) {
	cb.tzResolver = f
}

// chatterLocation resolves the timezone to render times in for a given
// chatter. Falls back to server-local when no resolver is wired or it
// has nothing for this chatter. The second return value indicates whether
// the timezone was explicitly set by the chatter (true) or is a fallback (false).
func (cb *ContextBuilder) chatterLocation(chatterUID string) (*time.Location, bool) {
	if cb.tzResolver != nil {
		if loc := cb.tzResolver(chatterUID); loc != nil {
			return loc, true
		}
	}
	return time.Local, false
}

// resolvedPromptMode returns the active mode with empty/unknown values
// normalized to PromptModeAgent so callers can switch on the result.
func (cb *ContextBuilder) resolvedPromptMode() string {
	switch cb.promptMode {
	case config.PromptModeChatbot, config.PromptModeCustomize:
		return cb.promptMode
	default:
		return config.PromptModeAgent
	}
}

// BuildSystemPrompt assembles the system prompt from identity, bootstrap files, memory, and skills.
// Reads everything under the agent owner's bucket — equivalent to the
// owner chatting with their own agent. For public-link callers that
// need per-chatter USER.md + memory isolation, use BuildSystemPromptAs.
// Trusted is false: the only production caller is the sub-agent path, and
// sub-agent spawns are deliberately untrusted (their task text was
// authored in an earlier turn whose chatter can't be verified here — see
// Agent.isTrustedTurn).
func (cb *ContextBuilder) BuildSystemPrompt() string {
	return cb.BuildSystemPromptAs(cb.userID, cb.memory, false)
}

// BuildSystemPromptAs is BuildSystemPrompt with explicit chatter identity.
// chatterUID + chatterMem govern reads of the per-user files (USER.md and
// long-term Memory) so a visitor on a public agent sees their own profile
// and memory rather than the owner's. Everything else — SOUL, IDENTITY,
// AGENTS, BOOTSTRAP, HEARTBEAT, TOOLS — still loads from the agent
// owner's bucket because those define what the agent IS, not who is
// talking to it. Pass cb.userID / cb.memory to mimic legacy behavior.
//
// trusted is the turn's operator verdict (Agent.isTrustedTurn): the
// chatter owns this agent / is a listed channel admin, or it's a
// heartbeat. It is passed per call rather than stored on the builder
// because one ContextBuilder serves every chatter of a shared agent —
// stashing it as state would leak one chatter's privileges into the next
// turn. Modules use it to tell the model outright whether host shell and
// platform-management work are available, instead of leaving it to infer
// authorization from an empty USER.md and refuse.
//
// The prompt is assembled from ordered modules defined in prompt_modules.go.
// Each prompt mode (Agent / Chatbot / Customize) declares its own module
// list, and identity files (SOUL.md / IDENTITY.md) are placed early in
// every mode so the model internalizes "who it is" before operational
// instructions.
func (cb *ContextBuilder) BuildSystemPromptAs(chatterUID string, chatterMem *Memory, trusted bool) string {
	return cb.buildSystemPromptAs(chatterUID, chatterMem, trusted, cb.groupCtx)
}

// BuildSystemPromptAsWithGroup supplies group identity for one turn only.
// Unlike SetGroupContext it cannot leak a web group's membership into a
// simultaneous direct-message turn handled by the same Agent instance.
func (cb *ContextBuilder) BuildSystemPromptAsWithGroup(chatterUID string, chatterMem *Memory, trusted bool, groupCtx *GroupContext) string {
	return cb.buildSystemPromptAs(chatterUID, chatterMem, trusted, groupCtx)
}

// BuildTurnSystemPrompt is BuildSystemPromptAsWithGroup plus the turn's
// host working directory (Registry.HostWorkDir — the session workspace).
// Per call for the same reason as trusted/groupCtx: one builder serves
// every chat of the agent concurrently. Empty workdir falls back to the
// agent-level workspace.
func (cb *ContextBuilder) BuildTurnSystemPrompt(chatterUID string, chatterMem *Memory, trusted bool, groupCtx *GroupContext, workdir string) string {
	return cb.buildSystemPrompt(chatterUID, chatterMem, trusted, groupCtx, workdir)
}

func (cb *ContextBuilder) buildSystemPromptAs(chatterUID string, chatterMem *Memory, trusted bool, groupCtx *GroupContext) string {
	return cb.buildSystemPrompt(chatterUID, chatterMem, trusted, groupCtx, "")
}

func (cb *ContextBuilder) buildSystemPrompt(chatterUID string, chatterMem *Memory, trusted bool, groupCtx *GroupContext, workdir string) string {
	if chatterUID == "" {
		chatterUID = cb.userID
	}
	if chatterMem == nil {
		chatterMem = cb.memory
	}

	mode := cb.resolvedPromptMode()
	loc, tzExplicit := cb.chatterLocation(chatterUID)
	now := time.Now().In(loc)

	p := &promptCtx{
		cb:         cb,
		groupCtx:   groupCtx,
		chatterUID: chatterUID,
		chatterMem: chatterMem,
		mode:       mode,
		now:        now,
		loc:        loc,
		dateLine:   buildDateLine(now, tzExplicit),
		trusted:    trusted,
		workdir:    workdir,
	}

	var parts []string
	for _, mod := range modulesForMode(mode) {
		if s := mod.Build(p); s != "" {
			parts = append(parts, s)
		}
	}
	result := strings.Join(parts, sectionSep)
	if config.DebugMode() {
		fmt.Fprintf(os.Stderr, "\n========== SYSTEM PROMPT [mode=%s] ==========\n%s\n========== END SYSTEM PROMPT ==========\n\n", mode, result)
	}
	return result
}

// BuildRuntimeContext returns the runtime context to inject before the user message.
func (cb *ContextBuilder) BuildRuntimeContext(channel, chatID string) string {
	return cb.BuildRuntimeContextAs(cb.userID, channel, chatID)
}

// BuildRuntimeContextAs returns runtime metadata rendered in the same
// chatter-local timezone as the system prompt's Current date/time line.
func (cb *ContextBuilder) BuildRuntimeContextAs(chatterUID, channel, chatID string) string {
	if chatterUID == "" {
		chatterUID = cb.userID
	}
	loc, _ := cb.chatterLocation(chatterUID)
	now := time.Now().In(loc)
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
	return cb.loadFileForUser(name, cb.userID)
}

// loadFileForUser reads a workspace file under an explicit userID.
// Store rows are keyed by (agentID, userID). USER.md is per-chatter
// and goes through the Exact path so a brand-new visitor doesn't
// inherit the owner's profile via the SQL owner-fallback overlay;
// every other identity file (SOUL/IDENTITY/AGENTS/BOOTSTRAP/HEARTBEAT/
// TOOLS) uses the overlay so chatters inherit the owner's setup. The
// on-disk home/ fallback only fires for the agent owner because that's
// the only bucket the legacy FS layout knows about.
func (cb *ContextBuilder) loadFileForUser(name, userID string) string {
	if cb.store != nil {
		ctx := context.Background()
		if userID != "" {
			ctx = config.WithUserID(ctx, userID)
		}
		var data []byte
		var err error
		if name == "USER.md" {
			data, err = cb.store.GetWorkspaceFileExact(ctx, cb.agentID, userID, name)
		} else {
			data, err = cb.store.GetWorkspaceFile(ctx, cb.agentID, userID, name)
		}
		if err == nil && len(data) > 0 {
			return strings.TrimSpace(string(data))
		}
	}
	if userID == cb.userID && cb.home != "" {
		if data, err := os.ReadFile(filepath.Join(cb.home, name)); err == nil && len(data) > 0 {
			return strings.TrimSpace(string(data))
		}
	}
	return ""
}
