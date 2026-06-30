package tools

import (
	"context"
	"testing"
)

// codingRootScope must collapse the session segment so file tools address
// the project root the dev server serves; off, it preserves per-chat
// isolation. This is the invariant the coding-agent preview relies on.
//
// Per-turn scoping now flows through TurnContext on ctx (not shared
// registry fields), so these tests attach a TurnContext to a background
// ctx — the same path the agent loop uses — instead of mutating fields.
func TestScopeSessionIDCodingRoot(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())

	// Default mode: session segment preserved.
	ctx := WithTurnContext(context.Background(), &TurnContext{
		SessionID: "sess-123",
	})
	if got := r.scopeSessionID(ctx); got != "sess-123" {
		t.Fatalf("default mode: want session segment preserved, got %q", got)
	}

	// coding-root mode: session segment collapses to "".
	ctx = WithTurnContext(context.Background(), &TurnContext{
		SessionID:      "sess-123",
		CodingRootScope: true,
	})
	if got := r.scopeSessionID(ctx); got != "" {
		t.Fatalf("coding-root mode: want empty session segment, got %q", got)
	}

	// Back to default: restored.
	ctx = WithTurnContext(context.Background(), &TurnContext{
		SessionID: "sess-123",
	})
	if got := r.scopeSessionID(ctx); got != "sess-123" {
		t.Fatalf("after disabling: want session segment restored, got %q", got)
	}
}

func TestWsPathSubdirRedirect(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())

	// No subdir → passthrough.
	ctx := context.Background()
	if got := r.wsPath(ctx, "src/x.tsx"); got != "src/x.tsx" {
		t.Fatalf("no subdir: want passthrough, got %q", got)
	}

	ctxApp := WithTurnContext(context.Background(), &TurnContext{CodingSubdir: "app"})
	cases := map[string]string{
		"src/x.tsx":     "app/src/x.tsx", // bare path gets prefixed
		"app/src/x.tsx": "app/src/x.tsx", // already-prefixed is idempotent
		"/src/x.tsx":    "app/src/x.tsx", // leading slash tolerated
		"app":           "app",           // the subdir itself
		"package.json":  "app/package.json",
	}
	for in, want := range cases {
		if got := r.wsPath(ctxApp, in); got != want {
			t.Fatalf("wsPath(%q): want %q, got %q", in, want, got)
		}
	}

	// Empty subdir → passthrough again.
	if got := r.wsPath(ctx, "src/x.tsx"); got != "src/x.tsx" {
		t.Fatalf("after disabling: want passthrough, got %q", got)
	}
}

// EffectiveUserID now resolves the per-turn chatter from ctx, falling back
// to the boot-time owner when no TurnContext is attached.
func TestEffectiveUserIDFallback(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	r.SetOwnerUserID("owner-1")

	// No TurnContext → boot owner.
	if got := r.EffectiveUserIDFromCtx(context.Background()); got != "owner-1" {
		t.Fatalf("no chatter: want owner fallback, got %q", got)
	}
	// TurnContext with a chatter → chatter wins.
	ctx := WithTurnContext(context.Background(), &TurnContext{ChatterUserID: "chatter-9"})
	if got := r.EffectiveUserIDFromCtx(ctx); got != "chatter-9" {
		t.Fatalf("with chatter: want chatter, got %q", got)
	}
}
