package tools

import (
	"context"
	"strings"
	"testing"
)

func TestIdentityFileBlockedRespectsCallerFlag(t *testing.T) {
	cases := []struct {
		path  string
		admin bool
		want  bool
	}{
		// Identity files: admin OK, chatter blocked.
		{"SOUL.md", false, true},
		{"SOUL.md", true, false},
		{"IDENTITY.md", false, true},
		{"BOOTSTRAP.md", false, true},
		{"AGENTS.md", false, true},
		{"TOOLS.md", false, true},
		{"HEARTBEAT.md", false, true},
		{"agent.json", false, true},

		// Absolute path with identity basename still gets caught — model
		// often produces "/data/.fastclaw/agents/<id>/SOUL.md" from the
		// Working Directory hint in the system prompt.
		{"/var/lib/fastclaw/agents/xyz/SOUL.md", false, true},
		{"./SOUL.md", false, true},

		// Per-chatter files: NOT gated even for non-admin — they're the
		// chatter's own state and they should be able to inspect them.
		{"MEMORY.md", false, false},
		{"USER.md", false, false},

		// Workspace artifacts: never gated.
		{"report.md", false, false},
		{"notes/SOUL.md", false, false}, // nested, not the identity file
		{"output.png", false, false},
		{"", false, false},
	}
	for _, c := range cases {
		// callerIsAdmin now lives on TurnContext (per-turn), not the
		// shared registry field — mirror the production path by attaching
		// it to ctx.
		ctx := WithTurnContext(context.Background(), &TurnContext{CallerIsAdmin: c.admin})
		r := &Registry{}
		got := r.identityFileBlocked(ctx, c.path)
		if got != c.want {
			t.Errorf("identityFileBlocked(%q) admin=%v = %v, want %v",
				c.path, c.admin, got, c.want)
		}
	}
}

func TestIdentityFileRefusalMessageShape(t *testing.T) {
	// Lock the contract: the refusal must (1) be tool-output safe (no
	// outer quotes) and (2) tell the model NOT to paraphrase — without
	// that, a chatter who asks "summarize your SOUL" still wins.
	if !strings.HasPrefix(IdentityFileRefusal, "[refused:") {
		t.Errorf("refusal should be a [refused: …] tool message; got %q", IdentityFileRefusal)
	}
	if !strings.Contains(IdentityFileRefusal, "paraphrase") {
		t.Errorf("refusal must instruct the model not to paraphrase; got %q", IdentityFileRefusal)
	}
	if !strings.Contains(IdentityFileRefusal, "stay in character") {
		t.Errorf("refusal must tell the model to stay in character; got %q", IdentityFileRefusal)
	}
}

func TestNestedIdentityNameIsNotBlocked(t *testing.T) {
	// "notes/SOUL.md" is a workspace artifact named like an identity
	// file — it's the chatter's own note, not the agent's persona. The
	// gate must not block it.
	ctx := WithTurnContext(context.Background(), &TurnContext{CallerIsAdmin: false})
	r := &Registry{}
	if r.identityFileBlocked(ctx, "notes/SOUL.md") {
		t.Errorf("nested SOUL.md must not be gated as the agent's identity")
	}
}
