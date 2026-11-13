package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// SandboxPool manages sandbox containers per agent, reusing them across exec calls.
type SandboxPool struct {
	sandboxes map[string]*DockerSandbox // agentID -> sandbox
	mu        sync.Mutex
}

// NewPool creates a new sandbox pool.
func NewPool() *SandboxPool {
	return &SandboxPool{
		sandboxes: make(map[string]*DockerSandbox),
	}
}

// Get returns (or lazily creates) a sandbox for the given agent.
//
// On creation we wire BOTH skill dirs into the sandbox so the LLM's
// `python /skills/<name>/main.py` resolves whether the skill lives in
// the global $FASTCLAW_HOME/skills/ tree or this agent's private
// $FASTCLAW_HOME/agents/<agentID>/agent/skills/. Without the per-agent
// mount, skills the operator dropped into agents/<id>/agent/skills/
// (e.g. via SkillsLoader's per-agent layer) silently fail to load
// inside the container.
func (p *SandboxPool) Get(agentID, image, workspace string, policy *Policy) *DockerSandbox {
	p.mu.Lock()
	defer p.mu.Unlock()

	if sb, ok := p.sandboxes[agentID]; ok {
		return sb
	}

	sb := NewDockerSandbox(image, workspace, policy)
	// Best-effort skill mount when the workspace path follows the
	// standard layout (<home>/workspaces/<agentID>); falls through
	// silently if the caller used a custom workspace path.
	if home := homeFromWorkspace(workspace, agentID); home != "" {
		if dirs := skillDirsForAgent(home, agentID, ""); len(dirs) > 0 {
			sb.SetSkillDirs(dirs)
		}
	}
	p.sandboxes[agentID] = sb
	return sb
}

// homeFromWorkspace inverts <home>/workspaces/<agentID> → <home>.
// Returns "" if workspace doesn't follow that convention.
func homeFromWorkspace(workspace, agentID string) string {
	suffix := filepath.Join("workspaces", agentID)
	if strings.HasSuffix(workspace, string(os.PathSeparator)+suffix) {
		return strings.TrimSuffix(workspace, string(os.PathSeparator)+suffix)
	}
	return ""
}

// skillDirsForAgent returns the host paths whose `<dir>/<skill-name>/`
// children should be mounted at /skills/<skill-name>/ inside the
// sandbox, first dir winning: the agent's own skills, then its owner's
// (the product's skills, ~/.fastclaw/users/<owner>/skills), then the
// global ones. A chatter's personal skills are never here: whatever is
// under /skills can be given a skill's configured keys.
//
// home is the resolved FASTCLAW_HOME (the pool's workspaceRoot), not
// the process env — keeps tests / multi-instance debug honest.
func skillDirsForAgent(home, agentID, ownerID string) []string {
	if home == "" {
		return nil
	}
	dirs := []string{filepath.Join(home, "agents", agentID, "agent", "skills")}
	if safeOwnerID(ownerID) {
		dirs = append(dirs, filepath.Join(home, "users", ownerID, "skills"))
	}
	return append(dirs, filepath.Join(home, "skills"))
}

// safeOwnerID: an id that is safe to join into a path.
func safeOwnerID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// Close shuts down and removes all sandbox containers.
func (p *SandboxPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for id, sb := range p.sandboxes {
		sb.Close()
		delete(p.sandboxes, id)
	}
}

// List returns info about all active sandboxes.
func (p *SandboxPool) List() []SandboxInfo {
	p.mu.Lock()
	defer p.mu.Unlock()

	var infos []SandboxInfo
	for agentID, sb := range p.sandboxes {
		infos = append(infos, SandboxInfo{
			AgentID:     agentID,
			ContainerID: sb.ContainerID(),
			Image:       sb.image,
			Workspace:   sb.workspace,
		})
	}
	return infos
}

// Remove destroys a specific sandbox by agent ID.
func (p *SandboxPool) Remove(agentID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	sb, ok := p.sandboxes[agentID]
	if !ok {
		return nil
	}
	err := sb.Close()
	delete(p.sandboxes, agentID)
	return err
}

// SandboxInfo holds display info for a sandbox.
type SandboxInfo struct {
	AgentID     string
	ContainerID string
	Image       string
	Workspace   string
}
