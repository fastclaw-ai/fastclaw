package agl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Local agents of one machine, as the Devices page manages them: re-read
// the list, list an agent's models, and test that it answers.

// TestPrompt is what a test run asks; any answer counts.
const TestPrompt = "Reply with just: OK"

const (
	testTimeout     = 90 * time.Second
	maxTestReply    = 500
	agentsListLimit = 30 * time.Second
)

// RefreshAgents asks the machine for its agents now (agents.list) and
// stores them, returning the updated machine.
func (h *Hub) RefreshAgents(ctx context.Context, machineID string) (*store.MachineRecord, error) {
	m, err := h.store.GetMachine(ctx, machineID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, agentsListLimit)
	defer cancel()
	raw, err := h.Request(ctx, machineID, "agents.list", map[string]any{"check_updates": false})
	if err != nil {
		return nil, err
	}
	var list []agentInfo
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("bad agents.list response: %w", err)
	}
	m.Agents = machineAgents(list)
	m.LastSeenAt = h.now()
	if err := h.store.UpdateMachineHello(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

// AgentModel is one model a local agent offers (agents.models).
type AgentModel struct {
	ID          string   `json:"id"`
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	Default     bool     `json:"default,omitempty"`
	Efforts     []string `json:"efforts,omitempty"`
}

// AgentModels lists the models agent offers on machineID.
func (h *Hub) AgentModels(ctx context.Context, machineID, agent string) ([]AgentModel, error) {
	ctx, cancel := context.WithTimeout(ctx, agentsListLimit)
	defer cancel()
	raw, err := h.Request(ctx, machineID, "agents.models", map[string]any{"agent": agent})
	if err != nil {
		return nil, err
	}
	var out struct {
		Models []AgentModel `json:"models"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("bad agents.models response: %w", err)
	}
	if out.Models == nil {
		out.Models = []AgentModel{}
	}
	return out.Models, nil
}

// AgentTest is the outcome of TestAgent.
type AgentTest struct {
	DurationMs int64  `json:"durationMs"`
	Reply      string `json:"reply"`
	Model      string `json:"model,omitempty"` // what the runtime reported running
}

// TestAgent starts agent on machineID read-only in a throwaway session,
// asks TestPrompt, and returns its answer. It uses the agent's own
// account (a little quota). model / effort "" = the agent's default.
func (h *Hub) TestAgent(ctx context.Context, machineID, agent, model, effort string) (AgentTest, error) {
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	start := time.Now()
	events, unsubscribe := h.Subscribe(machineID)
	defer unsubscribe()

	params := map[string]any{"agent": agent, "access": AccessReadOnly, "name": "fc:test:" + newTurnID()}
	if model != "" {
		params["model"] = model
	}
	if effort != "" {
		params["effort"] = effort
	}
	raw, err := h.Request(ctx, machineID, "session.create", params)
	if err != nil {
		return AgentTest{}, err
	}
	var meta struct {
		ID           string `json:"id"`
		CurrentModel string `json:"current_model"`
		Model        string `json:"model"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil || meta.ID == "" {
		return AgentTest{}, errors.New("bad session.create response")
	}
	defer func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		_, _ = h.Request(cctx, machineID, "session.close", map[string]any{"session_id": meta.ID})
	}()
	turnID := newTurnID()
	if _, err := h.Request(ctx, machineID, "session.prompt", map[string]any{
		"session_id": meta.ID, "text": TestPrompt, "turn_id": turnID,
	}); err != nil {
		return AgentTest{}, err
	}
	var buf strings.Builder
	for {
		select {
		case <-ctx.Done():
			return AgentTest{}, fmt.Errorf("no answer within %s", testTimeout)
		case ev := <-events:
			if ev.Disconnected {
				return AgentTest{}, ErrOffline
			}
			if ev.SessionID != meta.ID {
				continue
			}
			e := ev.Event
			switch e.Kind {
			case "text_delta":
				if e.Phase != "commentary" && buf.Len() < maxTestReply {
					buf.WriteString(e.Text)
				}
			case "permission_request":
				// Read-only: nothing to approve in a test.
				if e.RequestID != "" && e.DecidedBy == "" {
					_ = approve(ctx, h, machineID, &pendingApproval{sessionID: meta.ID, requestID: e.RequestID}, "deny")
				}
			case "turn_ended":
				if e.TurnID != turnID {
					continue
				}
				if e.Outcome == "failed" || e.Outcome == "aborted" {
					msg := e.Error
					if msg == "" {
						msg = e.Outcome
					}
					return AgentTest{}, errors.New(clipText(msg, 300))
				}
				reply := clipText(strings.TrimSpace(buf.String()), maxTestReply)
				m := meta.CurrentModel
				if m == "" {
					m = meta.Model
				}
				return AgentTest{DurationMs: time.Since(start).Milliseconds(), Reply: reply, Model: m}, nil
			}
		}
	}
}
