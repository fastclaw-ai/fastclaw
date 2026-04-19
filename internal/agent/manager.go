package agent

import (
	"fmt"
	"log/slog"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/session"
)

// ManagerOption configures optional Manager behavior.
type ManagerOption func(*managerOpts)

type managerOpts struct {
	sessionStore session.SessionStore
	memoryStore  MemoryStore
}

func WithSessionStore(st session.SessionStore) ManagerOption {
	return func(o *managerOpts) { o.sessionStore = st }
}

func WithMemoryStore(st MemoryStore) ManagerOption {
	return func(o *managerOpts) { o.memoryStore = st }
}

// Manager loads and manages all agent instances.
type Manager struct {
	agents       map[string]*Agent
	defaultAgent *Agent
}

// NewManager creates agents from resolved configs.
func NewManager(resolved []config.ResolvedAgent, prov provider.Provider, mb *bus.MessageBus, opts ...ManagerOption) (*Manager, error) {
	m := &Manager{
		agents: make(map[string]*Agent),
	}
	var mopt managerOpts
	for _, o := range opts {
		o(&mopt)
	}

	homeDir, err := config.HomeDir()
	if err != nil {
		return nil, err
	}

	for _, rc := range resolved {
		ag := NewAgent(rc, prov, mb, homeDir)
		// Inject store-backed session manager if available
		if mopt.sessionStore != nil {
			ag.sessions = session.NewManagerWithStore(rc.Home+"/sessions", mopt.sessionStore, rc.ID)
		}
		if mopt.memoryStore != nil {
			ag.memory = NewMemoryWithStore(rc.Home, mopt.memoryStore, rc.ID)
			ag.ctxBuilder.store = mopt.memoryStore
			ag.ctxBuilder.agentID = rc.ID
		}
		m.agents[rc.ID] = ag

		slog.Info("loaded agent",
			"id", rc.ID,
			"model", rc.Model,
			"home", rc.Home,
			"workspace", rc.Workspace,
		)
	}

	// If only one agent, make it the default
	if len(m.agents) == 1 {
		for _, ag := range m.agents {
			m.defaultAgent = ag
		}
	}

	return m, nil
}

// AddAgent creates and registers a new agent dynamically (for hot-reload).
func (m *Manager) AddAgent(rc config.ResolvedAgent, prov provider.Provider, mb *bus.MessageBus) error {
	if _, exists := m.agents[rc.ID]; exists {
		return fmt.Errorf("agent %q already exists", rc.ID)
	}
	homeDir, _ := config.HomeDir()
	ag := NewAgent(rc, prov, mb, homeDir)
	m.agents[rc.ID] = ag
	slog.Info("agent added dynamically", "id", rc.ID, "model", rc.Model)
	return nil
}

// RemoveAgent unregisters an agent by ID. No-op if the agent is not loaded.
func (m *Manager) RemoveAgent(id string) {
	if _, ok := m.agents[id]; !ok {
		return
	}
	delete(m.agents, id)
	if m.defaultAgent != nil && m.defaultAgent.Name() == id {
		m.defaultAgent = nil
	}
	slog.Info("agent removed dynamically", "id", id)
}

// AgentByID returns an agent by its ID.
func (m *Manager) AgentByID(id string) *Agent {
	return m.agents[id]
}

// DefaultAgent returns the default agent (set when only one agent exists).
func (m *Manager) DefaultAgent() *Agent {
	return m.defaultAgent
}

// All returns all loaded agents.
func (m *Manager) All() []*Agent {
	result := make([]*Agent, 0, len(m.agents))
	for _, ag := range m.agents {
		result = append(result, ag)
	}
	return result
}

// Names returns all agent IDs.
func (m *Manager) Names() []string {
	names := make([]string, 0, len(m.agents))
	for name := range m.agents {
		names = append(names, name)
	}
	return names
}

// UpdateProvider replaces the LLM provider for all agents (hot-reload).
func (m *Manager) UpdateProvider(prov provider.Provider) {
	for _, ag := range m.agents {
		ag.provider = prov
	}
}
