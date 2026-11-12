package tools

import "testing"

func TestResolveSkill(t *testing.T) {
	envs := map[string]map[string]string{
		"seo-tools": {"SEO_API_KEY": "k"},
		"no-key":    {"SOME_VAR": ""},
	}
	provider := func(name string) map[string]string { return envs[name] }
	dirs := []string{"/home/u/agents/a1/skills"}

	cases := []struct {
		cmd, name, script string
		metered           bool
	}{
		{"python /skills/seo-tools/scripts/trends.py --q x", "seo-tools", "scripts/trends.py", true},
		{"node /home/u/agents/a1/skills/seo-tools/run.mjs|jq .", "seo-tools", "run.mjs", true},
		{"python3 '/skills/seo-tools/main.py'", "seo-tools", "main.py", true},
		{"python /skills/no-key/x.py", "no-key", "x.py", false},
		{"python /skills/unknown/x.py", "", "", false},
		{"ls /workspace", "", "", false},
	}
	for _, c := range cases {
		run := resolveSkill(c.cmd, provider, dirs)
		name, script := "", ""
		if run != nil {
			name, script = run.name, run.script
		}
		if name != c.name || script != c.script || run.metered() != c.metered {
			t.Errorf("%q: got (%q, %q, %v), want (%q, %q, %v)", c.cmd, name, script, run.metered(), c.name, c.script, c.metered)
		}
	}
	if resolveSkill("python /skills/seo-tools/x.py", nil, dirs) != nil {
		t.Error("nil envProvider must resolve nothing")
	}
}
