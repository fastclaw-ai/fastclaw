package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fastclaw-ai/fastclaw/internal/agent/goal"
)

// RegisterGoalTools wires the single model-callable goal tool onto r.
//
// Only `update_goal(status="complete")` is exposed to the model: the
// continuation prompt already feeds the model the current objective +
// budget every turn, so there's nothing for `get_goal` to add. We
// deliberately don't let the model start its own goals (`create_goal`)
// either — goals are user-initiated, via the /goal slash.
//
// status is restricted to the literal "complete" at the schema layer.
// Pause / resume / budget_limited are user- or runtime-controlled, not
// model-controlled. Mirrors codex-rs/core/src/tools/handlers/goal_spec.rs.
func RegisterGoalTools(r *Registry, st goal.Store, agentID string) {
	r.Register("update_goal",
		"Mark the active goal complete. Status is restricted to \"complete\"; "+
			"pausing, resuming, and budget_limited transitions are controlled by "+
			"the user or the runtime, not by the model. Only call this when the "+
			"objective has actually been achieved and no required work remains — "+
			"do not call it merely because the budget is nearly exhausted or "+
			"because you want to stop.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"complete"},
					"description": "Must be the literal string \"complete\".",
				},
			},
			"required":             []string{"status"},
			"additionalProperties": false,
		},
		makeUpdateGoal(st, r, agentID),
	)
}

// makeUpdateGoal returns a ToolFunc that flips the active goal to
// Complete. Schema restricts status to "complete"; we re-validate
// here defensively in case a non-OpenAI provider ships through a
// non-conforming model response.
func makeUpdateGoal(st goal.Store, r *Registry, agentID string) ToolFunc {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var a struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("update_goal: parse args: %w", err)
		}
		if a.Status != "complete" {
			return "", fmt.Errorf(
				"update_goal: status must be \"complete\"; pause / resume / budget_limited are user- or runtime-controlled, not model-controlled")
		}

		// The durable session key is per-turn (resolved from the inbound
		// message's session triple); read it from ctx so concurrent turns
		// on the same agent each address their own goal row.
		sessionKey := turnOrZero(ctx).GoalSessionKey
		if sessionKey == "" {
			return "", errors.New("update_goal: no active session context")
		}

		g, err := st.GetGoalBySession(ctx, agentID, sessionKey)
		if errors.Is(err, goal.ErrNotFound) {
			return "", errors.New("update_goal: no active goal for this session")
		}
		if err != nil {
			return "", fmt.Errorf("update_goal: load goal: %w", err)
		}
		if g.Status != goal.StatusActive {
			return "", fmt.Errorf("update_goal: goal status is %q; only an active goal can be marked complete", g.Status)
		}

		g.Status = goal.StatusComplete
		if err := st.UpdateGoal(ctx, g); err != nil {
			return "", fmt.Errorf("update_goal: %w", err)
		}
		b, _ := json.Marshal(map[string]any{
			"ok":                true,
			"final_token_usage": g.TokensUsed,
		})
		return string(b), nil
	}
}
