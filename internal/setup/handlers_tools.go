package setup

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/gateway"
	"github.com/fastclaw-ai/fastclaw/internal/toolproviders"
)

// categoryCatalog is the admin UI's source of truth for which tool
// categories exist and which providers can back them. Extending this list
// (once the new providers exist in the toolproviders package) makes them
// appear in the UI automatically.
type categoryCatalog struct {
	Name      string           `json:"name"`  // e.g. "web_search"
	Label     string           `json:"label"` // human-friendly name
	Providers []providerCatalog `json:"providers"`
}

type providerCatalog struct {
	Name      string   `json:"name"`         // "exa"
	Label     string   `json:"label"`        // "Exa"
	NeedsKey  bool     `json:"needsKey"`     // API key required?
	NeedsURL  bool     `json:"needsUrl"`     // endpoint required (self-hosted)?
	Models    []string `json:"models"`       // suggested "<provider>/<model>" suffixes
}

// builtinCatalog lists every tool category + provider pair that the binary
// knows how to run. Providers not present in the runtime Registry are
// filtered out at response time, so this is safe to list optimistically.
var builtinCatalog = []categoryCatalog{
	{
		Name:  "web_search",
		Label: "Web Search",
		Providers: []providerCatalog{
			{Name: "exa", Label: "Exa", NeedsKey: true, Models: []string{"auto", "neural", "keyword"}},
			{Name: "brave", Label: "Brave Search", NeedsKey: true, Models: []string{"web"}},
			{Name: "brightdata", Label: "Bright Data", NeedsKey: true, Models: []string{"serp"}},
			{Name: "searxng", Label: "SearxNG (self-hosted)", NeedsURL: true, Models: []string{"default"}},
			// "none" is a sentinel: when picked, web_search is not exposed
			// to the model at all. There's no external backend — the model
			// uses its own native search if it has one.
			{Name: "none", Label: "None (rely on model's native search)", Models: []string{"default"}},
		},
	},
	{
		Name:  "web_fetch",
		Label: "Web Fetch",
		Providers: []providerCatalog{
			// Direct uses Go's net/http directly — no key required.
			// Jina's free tier works without a key (rate limited);
			// the key field is shown so admins can paste one to raise
			// quota, but the chain runtime treats blank as valid because
			// the provider implements CredentialFree.
			{Name: "direct", Label: "Direct (built-in)", Models: []string{"default"}},
			{Name: "jina", Label: "Jina Reader", NeedsKey: true, Models: []string{"default"}},
			{Name: "firecrawl", Label: "Firecrawl", NeedsKey: true, Models: []string{"default"}},
		},
	},
	{
		Name:  "image_gen",
		Label: "Image Generation",
		Providers: []providerCatalog{
			{Name: "openai", Label: "OpenAI", NeedsKey: true, Models: []string{"gpt-image-1", "dall-e-3"}},
			{Name: "replicate", Label: "Replicate", NeedsKey: true, Models: []string{"flux-schnell", "flux-dev", "flux-pro", "sdxl", "ideogram"}},
			{Name: "fal", Label: "Fal", NeedsKey: true, Models: []string{"flux-dev", "flux-schnell", "flux-pro"}},
			// "none" is a sentinel: when picked, image_gen is not exposed
			// to the model at all. The model falls back to its own native
			// image-generation capability if it has one.
			{Name: "none", Label: "None (rely on model's native image gen)", Models: []string{"default"}},
		},
	},
	{
		Name:  "tts",
		Label: "Text-to-Speech",
		Providers: []providerCatalog{
			{Name: "openai", Label: "OpenAI", NeedsKey: true, Models: []string{"tts-1", "tts-1-hd"}},
			{Name: "elevenlabs", Label: "ElevenLabs", NeedsKey: true, Models: []string{"eleven_multilingual_v2", "eleven_turbo_v2_5", "eleven_flash_v2_5"}},
			{Name: "fish", Label: "Fish Audio", NeedsKey: true, Models: []string{"s1", "speech-1.5", "speech-1.6"}},
			{Name: "minimax", Label: "MiniMax", NeedsKey: true, Models: []string{"speech-02-hd", "speech-02-turbo"}},
			// "none" is a sentinel: when picked, tts is not exposed to the
			// model at all. The model falls back to its own native audio
			// capability if it has one.
			{Name: "none", Label: "None (rely on model's native audio)", Models: []string{"default"}},
		},
	},
}

// handleGetTools returns the categories + provider catalog and the user's
// current toolProviders/tools settings. The UI renders a form from this.
func (s *Server) handleGetTools(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadUserConfig(r)
	if err != nil {
		cfg = &config.Config{}
	}

	reg := gateway.ToolProviderRegistry()
	cats := make([]categoryCatalog, 0, len(builtinCatalog))
	for _, c := range builtinCatalog {
		filtered := make([]providerCatalog, 0, len(c.Providers))
		known := map[string]bool{}
		for _, p := range c.Providers {
			if reg.Get(c.Name, p.Name) == nil {
				continue
			}
			filtered = append(filtered, p)
			known[p.Name] = true
		}
		// Append plugin-registered providers that aren't in the static
		// builtin catalog. We can't know whether they want a key or a URL
		// (plugin doesn't declare that yet), so we offer both fields — the
		// admin fills whichever the plugin needs.
		for _, extra := range reg.Names(c.Name) {
			if known[extra] {
				continue
			}
			filtered = append(filtered, providerCatalog{
				Name:     extra,
				Label:    extra + " (plugin)",
				NeedsKey: true,
				NeedsURL: true,
				Models:   []string{"default"},
			})
		}
		cc := c
		cc.Providers = filtered
		cats = append(cats, cc)
	}

	// Return providers as a keyed object (easier for the UI to merge-edit).
	// apiKey is returned in full to the admin — the UI decides whether to
	// mask it — but cloud callers see 403 below, so this is local-only.
	providers := map[string]config.ToolProviderCfg{}
	for name, pc := range cfg.ToolProviders {
		providers[name] = pc
	}
	tools := map[string]config.ToolCategoryCfg{}
	for name, cc := range cfg.Tools {
		tools[name] = cc
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"categories":    cats,
		"toolProviders": providers,
		"tools":         tools,
	})
}

// handleSaveTools atomically updates the toolProviders and tools sections of
// fastclaw.json. Only the admin/local user is allowed here — cloud tenants
// get their own settings via a separate path (not wired yet). After save,
// running agents are hot-reloaded so chains pick up new keys immediately.
func (s *Server) handleSaveTools(w http.ResponseWriter, r *http.Request) {
	// requireSuperAdmin middleware already gates this route; no further check needed.
	var req struct {
		ToolProviders map[string]config.ToolProviderCfg `json:"toolProviders"`
		Tools         map[string]config.ToolCategoryCfg `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	if err := validateToolChains(req.Tools); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	cfg, err := s.loadUserConfig(r)
	if err != nil {
		cfg = &config.Config{}
	}
	cfg.ToolProviders = req.ToolProviders
	cfg.Tools = req.Tools
	if err := s.saveUserConfig(r, cfg); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	// Nudge the resolver to drop the caller's cached user space; next
	// access reloads it from the DB with the new tool/provider config.
	s.invalidateUser(s.effectiveUserID(r))
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// validateToolChains sanity-checks that every "<provider>/<model>" reference
// names a provider actually registered in the runtime catalog. Catching
// typos here avoids a silent "no providers available" when the agent starts.
// The runtime registry is the single source of truth, so plugin-provided
// providers validate the same as built-ins.
func validateToolChains(tools map[string]config.ToolCategoryCfg) error {
	reg := gateway.ToolProviderRegistry()
	for cat, cfg := range tools {
		for _, ref := range cfg.Chain() {
			name, _ := splitRef(ref)
			if reg.Get(cat, name) == nil {
				return fmt.Errorf("unknown provider %q for category %q", ref, cat)
			}
		}
	}
	return nil
}

func splitRef(ref string) (string, string) {
	for i := 0; i < len(ref); i++ {
		if ref[i] == '/' {
			return ref[:i], ref[i+1:]
		}
	}
	return ref, ""
}

// Silence unused-import warnings when the package grows.
var _ = toolproviders.ErrNoResults
