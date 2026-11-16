package setup

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/billing"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/gateway"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Console → Tools: the caller's own tool settings (search, page reading,
// image generation, speech). Same categories and providers as System →
// Tools; what a user sets wins over the deployment's, category by
// category and provider by provider — like models and skills. An app
// account's settings also apply to its end users' chats.
//
//   GET /api/my-tools   catalog + the user's own layer (keys never
//                       returned) + what the deployment provides
//   PUT /api/my-tools   {toolProviders, tools}: replaces the user's layer;
//                       a category left out inherits again, an empty
//                       apiKey keeps the saved one
func (s *Server) registerMyToolsRoutes(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/my-tools", auth(s.handleGetMyTools))
	mux.HandleFunc("PUT /api/my-tools", auth(s.handleSaveMyTools))
}

// myToolProvider is a provider of the user's layer as the console sees it.
type myToolProvider struct {
	APIKeySet bool              `json:"apiKeySet"`
	Endpoint  string            `json:"endpoint,omitempty"`
	Options   map[string]string `json:"options,omitempty"`
}

// inheritedTool is a category the deployment provides: its chain only.
type inheritedTool struct {
	Chain []string `json:"chain"`
}

func (s *Server) handleGetMyTools(w http.ResponseWriter, r *http.Request) {
	uid := s.effectiveUserID(r)
	if uid == "" || s.dataStore == nil {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	ctx := r.Context()
	var ownProv map[string]config.ToolProviderCfg
	var ownTools map[string]config.ToolCategoryCfg
	if err := readLayer(r, s.dataStore, gateway.NSToolProviders, uid, &ownProv); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := readLayer(r, s.dataStore, gateway.NSToolCategories, uid, &ownTools); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	providers := map[string]myToolProvider{}
	for name, p := range ownProv {
		providers[name] = myToolProvider{APIKeySet: p.APIKey != "", Endpoint: p.Endpoint, Options: p.Options}
	}
	if ownTools == nil {
		ownTools = map[string]config.ToolCategoryCfg{}
	}

	// What the deployment provides (system layer): chains only, no keys or
	// endpoints. A category counts when one of its providers can run.
	var sysProv map[string]config.ToolProviderCfg
	var sysTools map[string]config.ToolCategoryCfg
	_ = scope.SettingInto(ctx, s.dataStore, gateway.NSToolProviders, "", "", &sysProv)
	_ = scope.SettingInto(ctx, s.dataStore, gateway.NSToolCategories, "", "", &sysTools)
	inherited := map[string]inheritedTool{}
	for cat, c := range sysTools {
		if chain := c.Chain(); len(chain) > 0 && chainUsable(cat, chain, sysProv) {
			inherited[cat] = inheritedTool{Chain: chain}
		}
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"categories":    toolCatalog(),
		"toolProviders": providers,
		"tools":         ownTools,
		"inherited":     inherited,
	})
}

func (s *Server) handleSaveMyTools(w http.ResponseWriter, r *http.Request) {
	uid := s.effectiveUserID(r)
	if uid == "" || s.dataStore == nil {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unauthorized"})
		return
	}
	if !s.requireWritable(w, r) {
		return
	}
	var req struct {
		ToolProviders map[string]config.ToolProviderCfg `json:"toolProviders"`
		Tools         map[string]config.ToolCategoryCfg `json:"tools"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	if err := validateToolChains(req.Tools); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx := r.Context()
	var saved, sysProv map[string]config.ToolProviderCfg
	if err := readLayer(r, s.dataStore, gateway.NSToolProviders, uid, &saved); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	_ = scope.SettingInto(ctx, s.dataStore, gateway.NSToolProviders, "", "", &sysProv)

	providers := map[string]config.ToolProviderCfg{}
	for name, p := range req.ToolProviders {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		p.APIKey = strings.TrimSpace(p.APIKey)
		if p.APIKey == "" {
			p.APIKey = saved[name].APIKey // the form never sees a saved key
		}
		p.Endpoint = strings.TrimSpace(p.Endpoint)
		// The server calls this address with the key: public https only,
		// unless it is the deployment's own address for that provider
		// (an in-cluster service a user layer copied).
		if p.Endpoint != "" && p.Endpoint != sysProv[name].Endpoint {
			if err := billing.ValidateHookURL(ctx, p.Endpoint); err != nil {
				jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": fmt.Sprintf("%s endpoint: %v", name, err)})
				return
			}
		}
		if p.APIKey == "" && p.Endpoint == "" && len(p.Options) == 0 {
			continue
		}
		providers[name] = p
	}
	tools := map[string]config.ToolCategoryCfg{}
	for cat, c := range req.Tools {
		if len(c.Chain()) > 0 {
			tools[cat] = c
		}
	}
	for _, w2 := range []struct {
		ns   string
		data any
	}{{gateway.NSToolProviders, providers}, {gateway.NSToolCategories, tools}} {
		var data map[string]any
		blob, _ := json.Marshal(w2.data)
		_ = json.Unmarshal(blob, &data)
		if len(data) == 0 {
			data = nil // no row: inherit everything
		}
		if err := scope.SaveSetting(ctx, s.dataStore, uid, "", w2.ns, data); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}
	// The account's chats and its end users' pick it up on their next turn.
	if inv, ok := s.userResolver.(interface{ InvalidateAccount(string) }); ok {
		inv.InvalidateAccount(uid)
	} else {
		s.invalidateUser(uid)
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// readLayer reads one user's own row of a namespace (not merged with the
// system's).
func readLayer(r *http.Request, st store.Store, ns, uid string, dst any) error {
	rec, err := st.GetConfigByName(r.Context(), store.KindSetting, uid, "", ns)
	if err != nil || rec == nil || len(rec.Data) == 0 {
		return nil
	}
	blob, err := json.Marshal(rec.Data)
	if err != nil {
		return err
	}
	return json.Unmarshal(blob, dst)
}

// chainUsable: one of the chain's providers has what it needs to run
// (credentials, an endpoint, or none needed) and the chain isn't "none".
func chainUsable(cat string, chain []string, providers map[string]config.ToolProviderCfg) bool {
	reg := gateway.ToolProviderRegistry()
	for _, ref := range chain {
		name, _ := splitRef(ref)
		if name == "none" {
			return false
		}
	}
	for _, ref := range chain {
		name, _ := splitRef(ref)
		p := reg.Get(cat, name)
		if p == nil {
			continue
		}
		pc := providers[name]
		if pc.APIKey != "" || pc.Endpoint != "" || credentialFree(p) {
			return true
		}
	}
	return false
}

func credentialFree(p any) bool {
	cf, ok := p.(interface{ CredentialFree() bool })
	return ok && cf.CredentialFree()
}
