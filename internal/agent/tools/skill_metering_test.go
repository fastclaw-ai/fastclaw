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
		runs              int
		metered           bool
	}{
		// Running a script: metered, gets the keys.
		{"python /skills/seo-tools/scripts/trends.py --q x", "seo-tools", "scripts/trends.py", 1, true},
		{"python3 -u '/skills/seo-tools/main.py' 2>&1 | head -50", "seo-tools", "main.py", 1, true},
		{"node /home/u/agents/a1/skills/seo-tools/run.mjs|jq .", "seo-tools", "run.mjs", 1, true},
		{"/skills/seo-tools/bin/query --kw a > /workspace/out.json", "seo-tools", "bin/query", 1, true},
		{"echo '{}' | SEO_DEBUG=1 timeout 60 python /skills/seo-tools/a.py", "seo-tools", "a.py", 1, true},
		{"bash -lc 'python /skills/seo-tools/a.py && python /skills/seo-tools/b.py'", "seo-tools", "a.py", 2, true},
		{"sh -e /skills/seo-tools/run.sh", "seo-tools", "run.sh", 1, true},
		{"uv run /skills/seo-tools/a.py; npx tsx /skills/seo-tools/b.ts", "seo-tools", "a.py", 2, true},
		{"cd /workspace && x=$(python /skills/seo-tools/a.py) && echo $x", "seo-tools", "a.py", 1, true},
		{"python /skills/no-key/x.py", "no-key", "x.py", 1, false},
		// Reading the skill: free, no keys.
		{"cat /skills/seo-tools/SKILL.md", "", "", 0, false},
		{"ls -la /skills/seo-tools/ && head -100 /skills/seo-tools/references/api.md", "", "", 0, false},
		{"grep -rn trends /skills/seo-tools/", "", "", 0, false},
		{"sed -n 1,80p /skills/seo-tools/scripts/trends.py", "", "", 0, false},
		{"python -c \"print(open('/skills/seo-tools/a.py').read())\"", "", "", 0, false},
		{"python /workspace/mine.py /skills/seo-tools/a.py", "", "", 0, false},
		{"cp /skills/seo-tools/a.py /tmp/a.py && python /tmp/a.py", "", "", 0, false},
		{"python /skills/unknown/x.py", "", "", 0, false},
		{"ls /workspace", "", "", 0, false},
	}
	for _, c := range cases {
		run := resolveSkill(c.cmd, provider, dirs)
		name, script, runs := "", "", 0
		if run != nil {
			name, script, runs = run.name, run.script, run.runs
		}
		if name != c.name || script != c.script || runs != c.runs || run.metered() != c.metered {
			t.Errorf("%q: got (%q, %q, %d, %v), want (%q, %q, %d, %v)", c.cmd, name, script, runs, run.metered(), c.name, c.script, c.runs, c.metered)
		}
	}
	if resolveSkill("python /skills/seo-tools/x.py", nil, dirs) != nil {
		t.Error("nil envProvider must resolve nothing")
	}
}
