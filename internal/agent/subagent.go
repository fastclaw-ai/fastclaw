package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// subagentDefaultTimeout caps the wall time one subagent can spend on
// its loop, independent of how long the parent's overall turn has left.
// Big enough for ~15-20 camoufox-cli-driven iterations including cold-
// start. Smaller than agentTurnTimeout so a parallel fan-out where one
// subagent goes slow doesn't take the rest down with it; parent ctx
// cancel still propagates so a genuinely killed parent kills every
// subagent.
const subagentDefaultTimeout = 15 * time.Minute

// RunSubagent implements tools.SubagentRunner so the delegate_task tool
// can call back into the Agent without creating an import cycle.
//
// Always emits a `subagent_progress` event with phase="done" on exit
// (success, error, or panic-via-defer) so the frontend's "currently
// delegating" indicator can clear cleanly between serial sub-agent
// runs — even when the next sub-agent doesn't start immediately or
// the parent decides to handle the result before issuing another
// delegate_task.
func (a *Agent) RunSubagent(ctx context.Context, task string, maxIterations int) (out string, err error) {
	defer func() {
		emitEvent(ctx, ChatEvent{Type: "subagent_progress", Data: map[string]any{
			"phase": "done",
		}})
	}()
	return a.runSubagentLoop(ctx, task, maxIterations)
}

// runSubagentLoop is a self-contained ReAct loop used by delegate_task.
//
// What it shares with HandleMessage:
//   - the parent's provider, model, tool registry, and SDK engine
//   - the same loop-detection, all-failed-rounds-disable-tools, and
//     cap-reached forced-delivery patterns
//
// What it deliberately does NOT do (vs HandleMessage):
//   - no session persistence — the sub-agent's working messages live in
//     a private slice and never touch session_messages
//   - no chat-event emission — the parent's chat UI sees the
//     delegate_task tool call + final tool_result only, not the sub-
//     agent's intermediate steps
//   - no hooks, no skill-store refresh, no compaction, no runPostTurn
//   - no slash-command / plan-mode short-circuit (caller is the parent
//     model via the delegate_task tool, not a human composer)
//
// delegate_task itself is filtered out of the sub-agent's toolset so
// sub-agents can't spawn further sub-agents (v1 nesting limit).
//
// Return contract: the final synthesized text in all "we got something"
// cases — clean exit, cap-hit forced delivery, or loop-detection abort.
// A non-nil error is returned only for plumbing failures (no provider,
// transient API error during a Chat call); callers fold that into the
// tool_result so the parent agent can react.
func (a *Agent) runSubagentLoop(ctx context.Context, task string, maxIterations int) (string, error) {
	if a.provider == nil {
		return "", fmt.Errorf("agent has no provider configured")
	}
	if maxIterations <= 0 {
		maxIterations = a.maxToolIterations
	}
	if maxIterations <= 0 {
		maxIterations = 20
	}

	// Each subagent gets its own bounded ctx so a slow sibling can't
	// drain the rest of a parallel fan-out. Parent cancel still wins —
	// we're wrapping, not detaching.
	subCtx, cancel := context.WithTimeout(ctx, subagentDefaultTimeout)
	defer cancel()
	ctx = subCtx

	systemPrompt := a.ctxBuilder.BuildSystemPrompt() + subagentSystemSuffix()
	messages := []provider.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: task},
	}

	// Filter delegate_task out of the sub-agent's toolset — no nesting
	// in v1. Other tools (web_fetch, exec, file ops, MCP, …) flow
	// through unchanged.
	var toolDefs []provider.Tool
	for _, t := range a.registry.Definitions() {
		if t.Function.Name == "delegate_task" {
			continue
		}
		toolDefs = append(toolDefs, t)
	}

	var loopDetector toolLoopDetector
	allFailedRounds := 0
	const failedRoundsLimit = 3

	for i := 0; i < maxIterations; i++ {
		slog.Info("subagent iteration",
			"agent", a.name,
			"iteration", i+1,
			"max", maxIterations,
		)
		// Heartbeat to the parent's chat stream so the UI can show
		// the user that a sub-agent is making progress and where it
		// is. Without this the delegate_task tool card looks frozen
		// for the entire sub-agent run (often 5-15 min). We emit at
		// the start of every iteration plus right before tool execution
		// (with the tool name) so the user sees both "thinking" and
		// "running web_search" phases.
		emitEvent(ctx, ChatEvent{Type: "subagent_progress", Data: map[string]any{
			"iteration": i + 1,
			"max":       maxIterations,
			"phase":     "thinking",
		}})

		callTools := toolDefs
		llmMsgs := messages
		if allFailedRounds >= failedRoundsLimit {
			slog.Warn("subagent disabling tools after consecutive failed rounds",
				"agent", a.name, "failed_rounds", allFailedRounds)
			callTools = nil
			llmMsgs = append(llmMsgs, provider.Message{
				Role: "system",
				Content: fmt.Sprintf(
					"The last %d rounds of tool calls all failed (HTTP 4xx/5xx or empty results). Stop calling tools and produce the deliverable from what you already gathered, with explicit gaps marked.",
					allFailedRounds,
				),
			})
		}

		resp, err := a.provider.Chat(ctx, llmMsgs, callTools, a.model, a.maxTokens, a.temperature)
		if err != nil {
			// If the ctx itself expired, the parent caller has more
			// useful framing than "context deadline exceeded" mid-
			// stream — surface the timeout explicitly so the parent
			// agent can decide to retry with a tighter task scope.
			if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				return "", fmt.Errorf(
					"subagent ran out of its %s wall-time budget at iteration %d — task was too large; the parent should retry with a tighter scope or lower max_iterations",
					subagentDefaultTimeout, i+1)
			}
			return "", fmt.Errorf("subagent chat failed at iteration %d: %w", i+1, err)
		}

		if !resp.HasToolCalls() {
			return resp.Content, nil
		}

		messages = append(messages, provider.Message{
			Role:         "assistant",
			Content:      resp.Content,
			ToolCalls:    resp.ToolCalls,
			Thinking:     resp.Thinking,
			RawAssistant: resp.RawAssistant,
		})

		// Second heartbeat: tools are about to run. Surface their names
		// so the UI can show "running web_search" / "running exec
		// (camoufox-cli open …)" instead of just a spinner.
		toolNames := make([]string, 0, len(resp.ToolCalls))
		for _, tc := range resp.ToolCalls {
			toolNames = append(toolNames, tc.Function.Name)
		}
		emitEvent(ctx, ChatEvent{Type: "subagent_progress", Data: map[string]any{
			"iteration": i + 1,
			"max":       maxIterations,
			"phase":     "running",
			"tools":     toolNames,
		}})

		results := a.engine.executeToolsConcurrently(ctx, a.registry, resp.ToolCalls, a.workspacePath)
		roundAllFailed := true
		loopDetected := false
		for idx, r := range results {
			tc := resp.ToolCalls[idx]
			resultContent, _ := extractToolMeta(r.result)
			resultContent = capToolResult(resultContent, a.registry.HostWorkDir(), tc.ID)
			if !isFailedToolResult(r.err, resultContent) {
				roundAllFailed = false
			}
			messages = append(messages, provider.Message{
				Role:       "tool",
				Content:    resultContent,
				ToolCallID: tc.ID,
				Name:       r.toolName,
			})
			if loopDetector.Observe(tc, resultContent) && !loopDetected {
				slog.Warn("subagent tool loop detected", "agent", a.name, "tool", tc.Function.Name)
				loopDetected = true
			}
		}
		if loopDetected {
			messages = append(messages, repeatedToolCallWarning("Loop detected: same tool with same arguments and same result 3 times. Stop and produce the deliverable from what you have."))
		}
		if roundAllFailed {
			allFailedRounds++
		} else {
			allFailedRounds = 0
		}
		if loopDetected {
			break
		}
	}

	// Cap reached — forced-delivery turn with tools off. Same nudge as
	// HandleMessage; the system message reads naturally in both contexts.
	slog.Warn("subagent max iterations reached — forcing final delivery",
		"agent", a.name, "max", maxIterations)
	emitEvent(ctx, ChatEvent{Type: "subagent_progress", Data: map[string]any{
		"iteration": maxIterations,
		"max":       maxIterations,
		"phase":     "final-delivery",
	}})
	finalMessages := append(messages, capReachedNudge(maxIterations))
	finalResp, err := a.provider.Chat(ctx, finalMessages, nil, a.model, a.maxTokens, a.temperature)
	if err != nil {
		return "", fmt.Errorf("subagent forced final delivery failed: %w", err)
	}
	if finalResp.Content == "" {
		return fmt.Sprintf("[subagent reached %d-iteration limit without producing a final answer]", maxIterations), nil
	}
	return finalResp.Content, nil
}

// subagentSystemSuffix is appended to the agent's normal system prompt
// when running under runSubagentLoop. Spells out the contract: the
// reply is a tool result for the parent, not chat with a human. Without
// this, sub-agents kept producing chatty "Hi! I'll help you find …"
// preambles that the parent then had to strip before splicing.
func subagentSystemSuffix() string {
	return "\n\n# Subagent mode\n\n" +
		"You are running as a delegated sub-agent invoked by a parent " +
		"agent via the `delegate_task` tool. Your reply is consumed as a " +
		"tool result, not displayed to a human as chat. Follow these " +
		"rules strictly:\n\n" +
		"- Output **only** the deliverable the task asks for. No " +
		"preamble (\"Sure, I'll help…\"), no reassurance, no follow-up " +
		"questions, no offers to continue.\n" +
		"- If the task specifies an output format (table, JSON, " +
		"markdown rows), produce exactly that format — the parent " +
		"splices your output into a larger result.\n" +
		"- If you can't complete the task, return a brief note " +
		"explaining what you got and what blocked you. Partial " +
		"structured output beats no output.\n" +
		"- You have the parent's full tool set except `delegate_task` " +
		"itself (no nesting). Use them as normal.\n" +
		"- You don't see the parent's prior conversation. Everything " +
		"you need to do this task is in the user message below."
}
