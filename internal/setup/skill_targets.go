package setup

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/skills"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// skillTarget is where a skill install / upload / delete lands:
//   - AgentID set: the agent's own skills (~/.fastclaw/agents/<id>/skills),
//     only that agent sees them
//   - UserID set: the account's own skills (~/.fastclaw/users/<uid>/skills),
//     loaded for every conversation that account has with any agent
//   - neither: the global skills shared by the whole deployment
type skillTarget struct {
	AgentID string
	UserID  string
}

// storeOwner is the object-store owner key the runtime hydrates from.
func (t skillTarget) storeOwner() string {
	switch {
	case t.AgentID != "":
		return t.AgentID
	case t.UserID != "":
		return skills.UserSkillOwner(t.UserID)
	}
	return skills.GlobalSkillOwner
}

// dir returns (and creates) the target's skills directory.
func (t skillTarget) dir(r *http.Request) (string, error) {
	if t.UserID == "" {
		return resolveInstallTarget(r, t.AgentID)
	}
	dir, err := userSkillsDir(t.UserID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// userSkillsDir mirrors the agent runtime's per-user skill layer
// (agent.SkillsLoader.userSkillsDir).
func userSkillsDir(userID string) (string, error) {
	home, err := config.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "users", userID, "skills"), nil
}

// skillInstallTarget authorizes a mutation against an agent's skills, the
// caller's own skills (userScope) or the global skills, and returns the
// target. It writes the 4xx and returns false when not allowed.
func (s *Server) skillInstallTarget(w http.ResponseWriter, r *http.Request, agentID string, userScope bool) (skillTarget, bool) {
	if agentID != "" || !userScope {
		return skillTarget{AgentID: agentID}, s.authorizeSkillInstallTarget(w, r, agentID)
	}
	if !s.requireWritable(w, r) {
		return skillTarget{}, false
	}
	uid, ok := ownSkillsUser(w, r)
	return skillTarget{UserID: uid}, ok
}

// ownSkillsUser returns the console user whose personal skills a request
// manages. End users never get here: their uploads and deletes go through
// handleUploadMySkill / handleDeleteMySkill (checked and capped), and
// channel accounts have no skills of their own.
func ownSkillsUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	ident, ok := auth.FromContext(r.Context())
	uid := ident.EffectiveUserID()
	if !ok || uid == "" {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unauthorized"})
		return "", false
	}
	if ident.Role == users.RoleAppUser || ident.Role == users.RoleChannelUser {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "forbidden"})
		return "", false
	}
	return uid, true
}

// isEndUserCaller: the request acts for a product's end user.
func isEndUserCaller(r *http.Request) bool {
	ident, ok := auth.FromContext(r.Context())
	return ok && ident.Role == users.RoleAppUser
}
