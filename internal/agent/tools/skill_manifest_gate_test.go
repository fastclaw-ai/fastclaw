package tools

import (
	"context"
	"strings"
	"testing"
)

func TestSkillManifestBlockedRespectsCallerFlag(t *testing.T) {
	cases := []struct {
		name           string
		path           string
		admin          bool
		userSkillsRoot string
		want           bool
	}{
		// Absolute sandbox-mount manifest: the documented exfil vector
		// (`read_file("/skills/foo/SKILL.md")`). Blocked for a chatter,
		// allowed for the owner/admin who maintains skills.
		{"mount manifest, chatter", "/skills/foo/SKILL.md", false, "", true},
		{"mount manifest, admin", "/skills/foo/SKILL.md", true, "", false},
		{"nested mount manifest", "/var/x/skills/foo/SKILL.md", false, "", true},

		// Relative manifest with NO per-user bucket resolves to the agent's
		// home skill set — the agent's IP, so blocked.
		{"relative manifest, no user bucket", "skills/foo/SKILL.md", false, "", true},
		{"relative manifest, admin", "skills/foo/SKILL.md", true, "", false},

		// Relative manifest WITH a per-user bucket is the chatter's OWN
		// skill — their content, stays readable.
		{"relative manifest, user bucket", "skills/foo/SKILL.md", false, "/home/u/skills", false},

		// Not a manifest / unrelated files: never gated.
		{"skill script", "/skills/foo/main.py", false, "", false},
		{"workspace lookalike", "/workspace/SKILL.md", false, "", false},
		{"nested workspace note", "notes/SKILL.md", false, "", false},
		{"plain report", "report.md", false, "", false},
		{"empty", "", false, "", false},
	}
	for _, c := range cases {
		// callerIsAdmin is per-turn (TurnContext on ctx); userSkillsRoot
		// is boot-stable (stays on the registry).
		ctx := WithTurnContext(context.Background(), &TurnContext{CallerIsAdmin: c.admin})
		r := &Registry{userSkillsRoot: c.userSkillsRoot}
		if got := r.skillManifestBlocked(ctx, c.path); got != c.want {
			t.Errorf("%s: skillManifestBlocked(%q) admin=%v userRoot=%q = %v, want %v",
				c.name, c.path, c.admin, c.userSkillsRoot, got, c.want)
		}
	}
}

func TestSkillManifestRefusalMessageShape(t *testing.T) {
	// Same contract as the identity refusal: tool-output safe and an
	// explicit "don't paraphrase" so summarizing the manifest is refused
	// too.
	if !strings.HasPrefix(SkillManifestRefusal, "[refused:") {
		t.Errorf("refusal should be a [refused: …] tool message; got %q", SkillManifestRefusal)
	}
	if !strings.Contains(SkillManifestRefusal, "paraphrase") {
		t.Errorf("refusal must instruct the model not to paraphrase; got %q", SkillManifestRefusal)
	}
	if !strings.Contains(SkillManifestRefusal, "stay in character") {
		t.Errorf("refusal must tell the model to stay in character; got %q", SkillManifestRefusal)
	}
}
