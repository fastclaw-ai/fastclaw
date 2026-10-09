package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// SubagentRunner is what the delegate_task tool calls to spawn a
// sub-agent. The agent package implements this on Agent so we avoid
// pulling agent into tools (would form an import cycle).
type SubagentRunner interface {
	RunSubagent(ctx context.Context, task string, maxIterations int) (string, error)
}

type delegateTaskArgs struct {
	Task           string `json:"task"`
	ExpectedOutput string `json:"expected_output,omitempty"`
	MaxIterations  int    `json:"max_iterations,omitempty"`
}

// RegisterDelegateTask wires the delegate_task tool. No-op when runner
// is nil so callers can opt out by simply not constructing one (e.g.
// in tests or for agent flavors where sub-agent fan-out doesn't make
// sense).
//
// Registered as SERIAL: two delegate_task calls cannot run
// concurrently within one agent. Sub-agents share the parent's single
// sandbox + single camoufox-cli daemon — running 5 in parallel meant
// they trampled each other's browser navigation state, got back
// snapshots of pages other siblings just navigated to, and produced
// garbage. Serialization trades fan-out wall time (5 × N min instead
// of N min) for actually-correct results.
//
// The tool description explains both the WHY of delegation (parent's
// context stays clean, sub-agent gets a fresh iteration budget) and
// the serial-execution contract so the model doesn't expect parallel
// throughput from fan-out. The "no nesting" line is critical — without
// it flash-tier models try to recursively delegate and burn through
// budgets exponentially.
func RegisterDelegateTask(r *Registry, runner SubagentRunner) {
	if runner == nil {
		return
	}
	r.RegisterSerial("delegate_task",
		"Spawn a sub-agent with its OWN context and OWN iteration budget to run a single bounded sub-task. "+
			"Use this when the user's request decomposes into several large independent chunks "+
			"(e.g. \"find 10 leads matching X\" then \"find another 10 matching Y\" then \"write 5 emails from this data\"). "+
			"Each sub-agent gets a fresh tool-iteration budget so you don't burn yours exploring, and your own context "+
			"stays clean of the dozens of intermediate tool results the sub-agent goes through. "+
			"\n\n**Sub-agents run SERIALLY, not in parallel.** Even if you emit 5 delegate_task calls in one round, "+
			"they execute one at a time — they share the single sandbox + single browser daemon, so parallel "+
			"execution would trample each other's state. Expect the wall-clock time of a fan-out to be N × the single-"+
			"sub-agent time, not 1× it. Plan accordingly: smaller per-sub-agent scope is better than fewer, larger calls.\n\n"+
			"The sub-agent runs against the same tools and provider you have (minus delegate_task itself — no nesting). "+
			"It cannot see your prior conversation, so pass everything it needs in the `task` arg: criteria, search hints, "+
			"earlier findings to build on, output format. Sub-agents are best for tasks that produce a self-contained "+
			"artifact (a table, a draft email, a structured summary). "+
			"\n\nReturn: the sub-agent's final text exactly as it produced it. You then assemble multiple sub-agent "+
			"results into the final deliverable for the user.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"task": map[string]interface{}{
					"type":        "string",
					"description": "Self-contained task description. The sub-agent does NOT see your prior conversation — include all the context it needs to act: criteria, search hints, prior findings it should build on, region / language constraints, anything the sub-agent must respect.",
				},
				"expected_output": map[string]interface{}{
					"type":        "string",
					"description": "Optional concrete format the sub-agent should produce — e.g. \"markdown table with columns: name, city, owner, phone, source_url; one row per business; no preamble\". Appended to the task verbatim so the format spec is unambiguous.",
				},
				"max_iterations": map[string]interface{}{
					"type":        "integer",
					"description": "Optional override for the sub-agent's tool-iteration budget. Default is 20. REALISTIC BUDGETS: for browser-heavy sub-tasks (camoufox-cli — each open/snapshot/click is 1-30s real time, plus a 2-3 min cold-start on the first call), the sub-agent has a 15-minute wall-time cap, so a max_iterations of 12-18 is the practical ceiling — setting 40 just means it'll hit the wall-clock long before iteration 40 and you'll get a partial answer with a 'ran out of budget' note. For quick web_search / web_fetch sub-tasks, 20-30 is fine. For pure synthesis (no tools), 3-5 is enough.",
				},
			},
			"required": []string{"task"},
		},
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args delegateTaskArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			if args.Task == "" {
				return "", fmt.Errorf("task is required")
			}
			taskPrompt := args.Task
			if args.ExpectedOutput != "" {
				taskPrompt += "\n\n## Expected output format\n\n" + args.ExpectedOutput
			}
			out, err := runner.RunSubagent(ctx, taskPrompt, args.MaxIterations)
			if err != nil {
				// Surface the error inside the tool_result so the parent
				// sees it as a normal tool failure (gets the "analyze
				// the error and try a different approach" envelope from
				// the registry) rather than a hard tool-execution error.
				return fmt.Sprintf("[subagent failed: %s]", err.Error()), err
			}
			return out, nil
		})
}
