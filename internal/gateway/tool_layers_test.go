package gateway

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// Tool settings resolve like models: system → the space's user → the
// agent's owner (when the owner's config applies) → the agent, each
// category and provider taken from the most specific layer that has it.
func TestToolConfigLayersOwnerAndAgentOverTheSpace(t *testing.T) {
	st := newOnDemandStore(t)
	ctx := context.Background()
	mustUser(t, st, "acct", "user", "")
	mustUser(t, st, "end_1", users.RoleAppUser, "acct")
	mustAgent(t, st, "agt_1", "acct")
	save := func(uid, aid, ns string, data map[string]any) {
		t.Helper()
		if err := scope.SaveSetting(ctx, st, uid, aid, ns, data); err != nil {
			t.Fatal(err)
		}
	}
	save("", "", NSToolCategories, map[string]any{
		"web_search": map[string]any{"primary": "searxng/default"},
		"tts":        map[string]any{"primary": "openai/tts-1"},
	})
	save("", "", NSToolProviders, map[string]any{"searxng": map[string]any{"endpoint": "http://searxng"}})
	save("end_1", "", NSToolCategories, map[string]any{"web_search": map[string]any{"primary": "duckduckgo/default"}})
	save("acct", "", NSToolCategories, map[string]any{"image_gen": map[string]any{"primary": "fal/seedream"}})
	save("acct", "", NSToolProviders, map[string]any{"fal": map[string]any{"apiKey": "fal_owner"}})
	save("", "agt_1", NSToolCategories, map[string]any{"tts": map[string]any{"primary": "none/default"}})

	base, err := assembleConfig(ctx, st, "end_1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := base.Tools["image_gen"]; ok {
		t.Fatal("the end user's own view already has the owner's image_gen")
	}

	got := toolConfigFor(ctx, st, base, "acct", "agt_1")
	if got.Tools["image_gen"].Primary != "fal/seedream" || got.ToolProviders["fal"].APIKey != "fal_owner" {
		t.Fatalf("owner layer missing: %+v %+v", got.Tools, got.ToolProviders)
	}
	if got.Tools["web_search"].Primary != "duckduckgo/default" {
		t.Fatalf("the space user's own category was lost: %+v", got.Tools["web_search"])
	}
	if got.Tools["tts"].Primary != "none/default" {
		t.Fatalf("agent layer didn't win: %+v", got.Tools["tts"])
	}
	if got.ToolProviders["searxng"].Endpoint != "http://searxng" {
		t.Fatalf("system provider lost: %+v", got.ToolProviders)
	}
	if _, ok := base.Tools["image_gen"]; ok || base.ToolProviders["fal"].APIKey != "" {
		t.Fatal("base was modified")
	}

	// Not the owner's config (a foreign agent that doesn't share it): only
	// the agent's own layer is added.
	if got := toolConfigFor(ctx, st, base, "", "agt_1"); got.Tools["image_gen"].Primary != "" || got.Tools["tts"].Primary != "none/default" {
		t.Fatalf("without the owner: %+v", got.Tools)
	}
	// Nothing to add: the same config back.
	if got := toolConfigFor(ctx, st, base, "", "agt_none"); got != base {
		t.Fatal("expected base unchanged")
	}
}
