package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent/goal"
)

// memGoalStore is the in-memory goal.Store implementation the tool
// tests use. Keyed by (agentID, sessionKey) to mirror the production
// UNIQUE index.
type memGoalStore struct {
	mu   sync.Mutex
	rows map[string]*goal.Goal // key = agentID + "|" + sessionKey
}

func newMemGoalStore() *memGoalStore {
	return &memGoalStore{rows: map[string]*goal.Goal{}}
}

func (m *memGoalStore) key(agent, sess string) string { return agent + "|" + sess }

func (m *memGoalStore) CreateGoal(_ context.Context, g *goal.Goal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(g.AgentID, g.SessionKey)
	if _, exists := m.rows[k]; exists {
		return goal.ErrAlreadyExists
	}
	clone := *g
	m.rows[k] = &clone
	return nil
}
func (m *memGoalStore) GetGoalBySession(_ context.Context, agentID, sessionKey string) (*goal.Goal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.rows[m.key(agentID, sessionKey)]
	if !ok {
		return nil, goal.ErrNotFound
	}
	clone := *g
	return &clone, nil
}
func (m *memGoalStore) UpdateGoal(_ context.Context, g *goal.Goal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(g.AgentID, g.SessionKey)
	if _, ok := m.rows[k]; !ok {
		return goal.ErrNotFound
	}
	clone := *g
	m.rows[k] = &clone
	return nil
}
func (m *memGoalStore) DeleteGoal(context.Context, string) error { return nil }

// fixture builds a Registry pre-bound to a session and a memGoalStore,
// with update_goal registered. (agentID, ownerUserID, sessionKey) match
// what every test exercises.
func fixture(t *testing.T) (*Registry, *memGoalStore) {
	t.Helper()
	r := NewRegistry("", "")
	st := newMemGoalStore()
	RegisterGoalTools(r, st, "agent-A")
	return r, st
}

// goalFixtureSessionKey is the per-turn goal session key attached to ctx
// in tests — the same value the old r.SetGoalSessionKey wired. Kept as a
// constant so seedGoal and callTool agree on it.
const goalFixtureSessionKey = "s-fixture-1"

// callTool runs the named tool with the given args JSON and returns
// (result, err). It attaches a TurnContext carrying the fixture session
// key so update_goal can resolve its goal row — mirroring how the agent
// loop threads per-turn state through ctx instead of registry fields.
func callTool(t *testing.T, r *Registry, name, argsJSON string) (string, error) {
	t.Helper()
	fn := r.GetFunc(name)
	if fn == nil {
		t.Fatalf("tool %q not registered", name)
	}
	ctx := WithTurnContext(context.Background(), &TurnContext{GoalSessionKey: goalFixtureSessionKey})
	return fn(ctx, json.RawMessage(argsJSON))
}

// seedGoal directly inserts an Active goal into the store, bypassing
// any model-facing tool surface (which no longer ships create_goal).
func seedGoal(t *testing.T, st *memGoalStore) {
	t.Helper()
	if err := st.CreateGoal(context.Background(), &goal.Goal{
		ID:          "g-test",
		AgentID:     "agent-A",
		SessionKey:  "s-fixture-1",
		OwnerUserID: "user-1",
		Objective:   "do it",
		Status:      goal.StatusActive,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestUpdateGoalCompleteFlipsStatus: happy path flips active → complete
// and returns the final token tally.
func TestUpdateGoalCompleteFlipsStatus(t *testing.T) {
	r, st := fixture(t)
	seedGoal(t, st)
	g, _ := st.GetGoalBySession(context.Background(), "agent-A", "s-fixture-1")
	g.TokensUsed = 12345
	_ = st.UpdateGoal(context.Background(), g)

	out, err := callTool(t, r, "update_goal", `{"status":"complete"}`)
	if err != nil {
		t.Fatalf("update_goal: %v", err)
	}
	if !strings.Contains(out, `"final_token_usage":12345`) {
		t.Errorf("expected final_token_usage in response, got %s", out)
	}
	final, _ := st.GetGoalBySession(context.Background(), "agent-A", "s-fixture-1")
	if final.Status != goal.StatusComplete {
		t.Errorf("status = %q, want complete", final.Status)
	}
}

// TestUpdateGoalRejectsBadStatus pins the "model can only mark complete"
// contract. Anything else must error out.
func TestUpdateGoalRejectsBadStatus(t *testing.T) {
	r, st := fixture(t)
	seedGoal(t, st)
	for _, bad := range []string{`"pause"`, `"paused"`, `"budget_limited"`, `"unmet"`, `"active"`, `""`} {
		args := `{"status":` + bad + `}`
		if _, err := callTool(t, r, "update_goal", args); err == nil {
			t.Errorf("update_goal accepted forbidden status %s", bad)
		}
	}
}

// TestUpdateGoalNoActiveGoal: calling update_goal without a goal in
// place is a user error the model should surface.
func TestUpdateGoalNoActiveGoal(t *testing.T) {
	r, _ := fixture(t)
	_, err := callTool(t, r, "update_goal", `{"status":"complete"}`)
	if err == nil {
		t.Fatal("expected error when no goal exists")
	}
}

// TestUpdateGoalOnlyTransitionsFromActive: a goal that's already
// complete / paused / budget_limited can't be re-completed by the model.
func TestUpdateGoalOnlyTransitionsFromActive(t *testing.T) {
	r, st := fixture(t)
	seedGoal(t, st)
	for _, blocked := range []goal.Status{goal.StatusPaused, goal.StatusBudgetLimited, goal.StatusComplete} {
		g, _ := st.GetGoalBySession(context.Background(), "agent-A", "s-fixture-1")
		g.Status = blocked
		_ = st.UpdateGoal(context.Background(), g)

		_, err := callTool(t, r, "update_goal", `{"status":"complete"}`)
		if err == nil {
			t.Errorf("update_goal should reject from status %q", blocked)
		}
	}
}

// TestUpdateGoalRegistered: trivial guard that the only tool we ship
// stays registered under its expected name.
func TestUpdateGoalRegistered(t *testing.T) {
	r, _ := fixture(t)
	if r.GetFunc("update_goal") == nil {
		t.Error("update_goal not registered")
	}
}

// TestUpdateGoalNoSessionContext: when the tool fires outside a chat
// turn (no TurnContext attached to ctx), it must surface a recoverable
// error instead of dereferencing a nil session. This is the boot-time /
// out-of-context path.
func TestUpdateGoalNoSessionContext(t *testing.T) {
	r := NewRegistry("", "")
	// Deliberately use a bare background ctx — no TurnContext attached.
	st := newMemGoalStore()
	RegisterGoalTools(r, st, "agent-A")

	fn := r.GetFunc("update_goal")
	if fn == nil {
		t.Fatalf("update_goal not registered")
	}
	_, err := fn(context.Background(), json.RawMessage(`{"status":"complete"}`))
	if err == nil || !strings.Contains(err.Error(), "no active session") {
		t.Fatalf("expected no-session error, got %v", err)
	}
}

// TestUpdateGoalMalformedArgs: invalid JSON or wrong-typed status
// must come back as a Go error, not a panic and not a silent success.
// Non-OpenAI providers can ship through non-conforming model output;
// this is the defensive parse boundary.
func TestUpdateGoalMalformedArgs(t *testing.T) {
	r, st := fixture(t)
	seedGoal(t, st)

	for _, bad := range []string{
		`{`,                       // truncated JSON
		`{"status": 123}`,         // wrong type
		`{}`,                      // missing status
		`{"status":["complete"]}`, // array, not string
	} {
		if _, err := callTool(t, r, "update_goal", bad); err == nil {
			t.Errorf("update_goal accepted malformed args %q", bad)
		}
	}
	// Goal must not have been mutated by any of the failed calls.
	g, _ := st.GetGoalBySession(context.Background(), "agent-A", "s-fixture-1")
	if g.Status != goal.StatusActive {
		t.Errorf("status mutated to %q despite all calls failing", g.Status)
	}
}
