package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// A creator skill gets the keys its creator set for the agent and never the
// deployment's, even under a name the deployment has keys for. The agent
// owner's own skills keep the old fallback.
func TestSkillEnvVarsCreatorSkill(t *testing.T) {
	home, agentDir := t.TempDir(), t.TempDir()
	mk := func(name string, creator bool) {
		d := filepath.Join(agentDir, "skills", name)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: d\n---\n"), 0o644)
		if creator {
			os.WriteFile(filepath.Join(d, CreatorSkillMarker), nil, 0o644)
		}
	}
	mk("image-tool", true) // shadows a keyed global name
	mk("weather", true)
	mk("owned", false)
	cfg := config.SkillsCfg{
		Entries: map[string]config.SkillEntryCfg{
			"image-tool": {Env: map[string]string{"FAL_KEY": "platform-secret"}},
			"owned":      {Env: map[string]string{"FAL_KEY": "platform-secret"}},
		},
		AgentEntries: map[string]map[string]config.SkillEntryCfg{
			"agt_1": {"weather": {APIKey: "ignored", Env: map[string]string{"MY_KEY": "creator-secret"}}},
		},
	}
	sl := NewSkillsLoaderWithGlobal(home, agentDir, "", config.SkillsConfig{}, cfg)
	sl.agentID = "agt_1"

	if env := sl.SkillEnvVars("image-tool"); env != nil {
		t.Fatalf("creator skill got the deployment's keys: %v", env)
	}
	if env := sl.SkillEnvVars("weather"); len(env) != 1 || env["MY_KEY"] != "creator-secret" {
		t.Fatalf("creator keys: %v", env)
	}
	if env := sl.SkillEnvVars("owned"); env["FAL_KEY"] != "platform-secret" {
		t.Fatalf("owner's skill lost its fallback: %v", env)
	}
	if sl.IsCreatorSkill("../skills/weather") || sl.IsCreatorSkill("..") {
		t.Fatal("path in a skill name")
	}
}
