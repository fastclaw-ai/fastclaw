package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

func writeLayerSkill(t *testing.T, dir, name, desc string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + desc + "\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
}

// chatter > agent > owner (the product) > global.
func TestSkillLayersChatterAgentOwnerGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FASTCLAW_HOME", home)
	agentDir := filepath.Join(home, "agents", "a1", "agent")
	global := filepath.Join(home, "skills")
	owner := filepath.Join(home, "users", "u_owner", "skills")
	chatter := filepath.Join(home, "users", "u_chat", "skills")

	writeLayerSkill(t, global, "all", "global")
	writeLayerSkill(t, global, "g", "global")
	writeLayerSkill(t, owner, "all", "owner")
	writeLayerSkill(t, owner, "o", "owner")
	writeLayerSkill(t, agentDir+"/skills", "all", "agent")
	writeLayerSkill(t, agentDir+"/skills", "ag", "agent")
	writeLayerSkill(t, owner, "ag", "owner")
	writeLayerSkill(t, chatter, "all", "chatter")
	writeLayerSkill(t, global, "o", "global")

	load := func(uid, ownerID string) map[string]string {
		sl := NewSkillsLoader(home, agentDir, "", config.SkillsConfig{}).WithUserID(uid).WithOwnerID(ownerID)
		out := map[string]string{}
		for _, s := range sl.LoadSkills() {
			out[s.Name] = s.Description
		}
		return out
	}
	got := load("u_chat", "u_owner")
	for name, want := range map[string]string{"all": "chatter", "ag": "agent", "o": "owner", "g": "global"} {
		if got[name] != want {
			t.Errorf("%s: got %q, want %q", name, got[name], want)
		}
	}
	// Another chatter of the same agent: the product's skills, not chatter's.
	if got := load("u_other", "u_owner"); got["all"] != "agent" || got["o"] != "owner" {
		t.Errorf("other chatter: %v", got)
	}
	// No owner layer: the global one.
	if got := load("u_other", ""); got["o"] != "global" {
		t.Errorf("no owner: %v", got)
	}
	// Unsafe owner ids are ignored.
	if got := load("u_other", "../u_owner"); got["o"] != "global" {
		t.Errorf("unsafe owner: %v", got)
	}

	dirs := NewSkillsLoader(home, agentDir, "", config.SkillsConfig{}).WithUserID("u_chat").WithOwnerID("u_owner").AllSkillDirs()
	if dirs[0] != chatter || dirs[1] != filepath.Join(agentDir, "skills") || dirs[2] != owner {
		t.Errorf("dir order: %v", dirs)
	}
}
