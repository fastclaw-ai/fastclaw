package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSkillRegisteredByDefaultAndLoadsFullContent(t *testing.T) {
	home := t.TempDir()
	skillDir := filepath.Join(home, "skills", "chart-maker")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `---
name: chart-maker
description: Build charts from tabular data.
---

Run {baseDir}/scripts/render.py with JSON input.`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry(t.TempDir(), t.TempDir())
	RegisterLoadSkill(r, []string{filepath.Join(home, "skills")}, "")

	fn := r.GetFunc("load_skill")
	if fn == nil {
		t.Fatal("load_skill was not registered")
	}
	rawArgs, err := json.Marshal(map[string]string{"name": "chart-maker"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := fn(context.Background(), rawArgs)
	if err != nil {
		t.Fatal(err)
	}

	// {baseDir} must resolve to the IN-CONTAINER path, not the host path.
	// The sandbox mounts per-agent/managed skills at /skills/<name>, so the
	// placeholder should become /skills/chart-maker — never the host dir.
	if !strings.Contains(got, "Run /skills/chart-maker/scripts/render.py") {
		t.Fatalf("load_skill did not return full content with baseDir replaced by container path:\n%s", got)
	}
	if strings.Contains(got, skillDir+"/scripts/render.py") {
		t.Fatalf("load_skill leaked the HOST skill path into output:\n%s", got)
	}
	if !strings.Contains(got, "INTERNAL CONTEXT") {
		t.Fatalf("load_skill output missing internal wrapper:\n%s", got)
	}
}

func TestLoadSkillUsesDirectoryPrecedence(t *testing.T) {
	agentSkills := filepath.Join(t.TempDir(), "skills")
	userSkills := filepath.Join(t.TempDir(), "skills")
	for _, dir := range []string{agentSkills, userSkills} {
		if err := os.MkdirAll(filepath.Join(dir, "shared"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(userSkills, "shared", "SKILL.md"), []byte("user version"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentSkills, "shared", "SKILL.md"), []byte("agent version"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry(t.TempDir(), t.TempDir())
	RegisterLoadSkill(r, []string{agentSkills, userSkills}, "")
	rawArgs, err := json.Marshal(map[string]string{"name": "shared"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.GetFunc("load_skill")(context.Background(), rawArgs)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(got, "agent version") {
		t.Fatalf("load_skill did not use first matching directory:\n%s", got)
	}
	if strings.Contains(got, "user version") {
		t.Fatalf("load_skill should not include lower-priority skill:\n%s", got)
	}
}

func TestLoadSkillMarksMissingEnvRequirement(t *testing.T) {
	home := t.TempDir()
	skillDir := filepath.Join(home, "skills", "deepcoin-trade")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `---
name: deepcoin-trade
description: Place orders.
metadata:
  openclaw:
    requires:
      env: ["DC_API_KEY", "DC_SECRET_KEY"]
---

Authenticated instructions.`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry(t.TempDir(), t.TempDir())
	RegisterLoadSkill(r, []string{filepath.Join(home, "skills")}, "")
	rawArgs, err := json.Marshal(map[string]string{"name": "deepcoin-trade"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.GetFunc("load_skill")(context.Background(), rawArgs)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(got, "SKILL CURRENTLY UNAVAILABLE") {
		t.Fatalf("load_skill output missing unavailable warning:\n%s", got)
	}
	if !strings.Contains(got, "DC_API_KEY, DC_SECRET_KEY") {
		t.Fatalf("load_skill output missing env names:\n%s", got)
	}
}

// TestLoadSkillPerUserLayerMapsBaseDirToAgentSkills verifies that a skill
// installed at the per-user layer resolves {baseDir} to the per-user in-container
// mount path /root/.agents/skills/<name>, not the default /skills/<name> used by
// per-agent/managed skills. The per-user layer is bind-mounted RW as a whole
// directory at /root/.agents/skills (the location `npx skills add -g` writes to),
// so its scripts are NOT under /skills/.
func TestLoadSkillPerUserLayerMapsBaseDirToAgentSkills(t *testing.T) {
	home := t.TempDir()
	// managed layer dir (for contrast — should still map to /skills/<name>)
	managedSkills := filepath.Join(home, "skills")
	// per-user layer dir — mimics ~/.fastclaw/users/<uid>/skills
	userSkills := filepath.Join(home, "users", "u_test", "skills")

	for _, tc := range []struct {
		name        string
		skillLayer  string // host dir holding the skill folder
		skillName   string
		wantSubstr string // expected in-container path the placeholder resolves to
	}{
		{
			name:        "managed layer maps to /skills/<name>",
			skillLayer:  managedSkills,
			skillName:   "managed-tool",
			wantSubstr: "/skills/managed-tool/scripts/run.py",
		},
		{
			name:        "per-user layer maps to /root/.agents/skills/<name>",
			skillLayer:  userSkills,
			skillName:   "user-tool",
			wantSubstr: "/root/.agents/skills/user-tool/scripts/run.py",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skillDir := filepath.Join(tc.skillLayer, tc.skillName)
			if err := os.MkdirAll(skillDir, 0o755); err != nil {
				t.Fatal(err)
			}
			body := "---\nname: " + tc.skillName + "\ndescription: test.\n---\n\nRun {baseDir}/scripts/run.py"
			if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			r := NewRegistry(t.TempDir(), t.TempDir())
			// Order mirrors SkillsLoader.allSkillDirs: per-user dir is passed
			// AND identified via the userSkillsHostDir argument.
			RegisterLoadSkill(r, []string{managedSkills, userSkills}, userSkills)

			rawArgs, err := json.Marshal(map[string]string{"name": tc.skillName})
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.GetFunc("load_skill")(context.Background(), rawArgs)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, tc.wantSubstr) {
				t.Fatalf("expected %q in output, got:\n%s", tc.wantSubstr, got)
			}
		})
	}
}
