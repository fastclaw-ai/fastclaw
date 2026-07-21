package setup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/api"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/buildinfo"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/session"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

type agentChatEvent = agent.ChatEvent

// loadUserConfig reads the merged Config view for the request's user.
// Walks system → user setting namespaces + the scope-aware provider /
// channel rows. The result is the same shape gateway.assembleConfig
// produces — UI-only fields like Storage/Gateway are filled by env
// overlay, not by the DB.
func (s *Server) loadUserConfig(r *http.Request) (*config.Config, error) {
	if s.dataStore == nil {
		return &config.Config{}, nil
	}
	uid := config.UserIDFromContext(r.Context())
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{},
		Channels:  map[string]config.ChannelConfig{},
	}
	for _, ns := range settingNamespaces {
		if err := scope.SettingInto(r.Context(), s.dataStore, ns.namespace, uid, "", ns.dst(cfg)); err != nil {
			return nil, err
		}
	}
	if err := scope.SettingInto(r.Context(), s.dataStore, scope.PrefsNamespace, uid, "", &cfg.Prefs); err != nil {
		return nil, err
	}
	if provs, err := scope.Providers(r.Context(), s.dataStore, uid, ""); err == nil {
		for k, v := range provs {
			cfg.Providers[k] = v
		}
	}
	if chs, err := scope.Channels(r.Context(), s.dataStore, uid, ""); err == nil {
		for k, v := range chs {
			cfg.Channels[k] = v
		}
	}
	if ae, err := loadAgentSkillEntriesForUser(r.Context(), s.dataStore, uid); err == nil && len(ae) > 0 {
		cfg.Skills.AgentEntries = ae
	}
	config.LoadEnv().ApplyToConfig(cfg)
	config.ApplyDefaults(cfg)
	return cfg, nil
}

// loadAgentSkillEntriesForUser collects every agent-scope skills.entries
// row owned by this user. Replaces the legacy single-row keyed-by-agent
// blob — each agent now persists its own row, so the JSON we hand back
// is rebuilt by listing the user's agents and pulling each one's row.
func loadAgentSkillEntriesForUser(ctx context.Context, st store.Store, userID string) (map[string]map[string]config.SkillEntryCfg, error) {
	if st == nil || userID == "" {
		return nil, nil
	}
	agents, err := st.ListAgents(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]config.SkillEntryCfg{}
	for _, ar := range agents {
		entries, err := loadAgentSkillEntries(ctx, st, ar.ID)
		if err != nil || len(entries) == 0 {
			continue
		}
		out[ar.ID] = entries
	}
	return out, nil
}

// loadAgentSkillEntries reads one agent-scope skills.entries row.
// A missing row is not an error — nil entries means "no overrides".
func loadAgentSkillEntries(ctx context.Context, st store.Store, agentID string) (map[string]config.SkillEntryCfg, error) {
	rec, err := st.GetConfigByName(ctx, store.KindSetting, "", agentID, "skills.entries")
	if err != nil || rec == nil || len(rec.Data) == 0 {
		return nil, err
	}
	blob, _ := json.Marshal(rec.Data)
	var entries map[string]config.SkillEntryCfg
	if err := json.Unmarshal(blob, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// saveAgentSkillEntries upserts the agent-scope skills.entries row.
// Empty inner map deletes the row (no overrides → no row, keeps the
// configs table tight). Authorization is the caller's responsibility;
// we just persist what was requested.
func saveAgentSkillEntries(ctx context.Context, st store.Store, agentID string, entries map[string]config.SkillEntryCfg) error {
	if len(entries) == 0 {
		return scope.SaveSetting(ctx, st, "", agentID, "skills.entries", nil)
	}
	blob, _ := json.Marshal(entries)
	var asMap map[string]interface{}
	_ = json.Unmarshal(blob, &asMap)
	return scope.SaveSetting(ctx, st, "", agentID, "skills.entries", asMap)
}

// saveUserConfig persists the namespaced setting rows for the calling
// user's scope. Providers/Channels live in their own configs rows
// and are NOT touched here — the dedicated /api/providers and /api/channels
// endpoints (and the onboard handler) write those.
func (s *Server) saveUserConfig(r *http.Request, cfg *config.Config) error {
	if s.dataStore == nil {
		return errors.New("store not configured")
	}
	ident, ok := authIdentity(r)
	// Decide who owns the rows we're about to save:
	//   - super_admin without ?actAs=  → system rows (user_id='')
	//   - super_admin with ?actAs=X    → write into user X's scope
	//   - regular user                 → write into their own scope
	uid := ""
	if ok && ident.Role == "super_admin" {
		if ident.IsActingAs() {
			uid = ident.EffectiveUserID()
		}
	} else if ok {
		uid = ident.UserID
	}
	for _, ns := range settingNamespaces {
		data := ns.collect(cfg)
		if err := scope.SaveSetting(r.Context(), s.dataStore, uid, "", ns.namespace, data); err != nil {
			return err
		}
	}
	return nil
}

// settingNamespaces is the table that drives loadUserConfig /
// saveUserConfig. Adding a new sub-block of Config to the round-trip is
// a single append here.
var settingNamespaces = []settingNamespace{
	{namespace: "agents.defaults",
		dst:     func(c *config.Config) interface{} { return &c.Agents.Defaults },
		collect: func(c *config.Config) map[string]interface{} { return toMap(c.Agents.Defaults) }},
	{namespace: "sandbox",
		dst:     func(c *config.Config) interface{} { return &c.Sandbox },
		collect: func(c *config.Config) map[string]interface{} { return toMap(c.Sandbox) }},
	{namespace: "objectstore",
		dst:     func(c *config.Config) interface{} { return &c.ObjectStore },
		collect: func(c *config.Config) map[string]interface{} { return toMap(c.ObjectStore) }},
	{namespace: "hooks",
		dst:     func(c *config.Config) interface{} { return &c.Hooks },
		collect: func(c *config.Config) map[string]interface{} { return toMap(c.Hooks) }},
	{namespace: "plugins",
		dst:     func(c *config.Config) interface{} { return &c.Plugins },
		collect: func(c *config.Config) map[string]interface{} { return toMap(c.Plugins) }},
	{namespace: "taskqueue",
		dst:     func(c *config.Config) interface{} { return &c.TaskQueue },
		collect: func(c *config.Config) map[string]interface{} { return toMap(c.TaskQueue) }},
	{namespace: "tools.providers",
		dst:     func(c *config.Config) interface{} { return &c.ToolProviders },
		collect: func(c *config.Config) map[string]interface{} { return wrapKeyed(c.ToolProviders) }},
	{namespace: "tools.categories",
		dst:     func(c *config.Config) interface{} { return &c.Tools },
		collect: func(c *config.Config) map[string]interface{} { return wrapKeyed(c.Tools) }},
	{namespace: "skills.install",
		dst:     func(c *config.Config) interface{} { return &c.Skills.Install },
		collect: func(c *config.Config) map[string]interface{} { return toMap(c.Skills.Install) }},
	{namespace: "skills.entries",
		dst:     func(c *config.Config) interface{} { return &c.Skills.Entries },
		collect: func(c *config.Config) map[string]interface{} { return wrapKeyed(c.Skills.Entries) }},
	// Per-agent skill env/key overrides have been split off this table
	// into one row per agent at scope=agent, name=skills.entries — see
	// loadAgentSkillEntriesForUser / saveAgentSkillEntries below. Lumping
	// every agent's overrides into a single user/system-scope row let
	// the JSON blob grow with every agent × skill, and forced a full
	// rewrite on every patch.
	{namespace: "memory",
		dst:     func(c *config.Config) interface{} { return &c.Memory },
		collect: func(c *config.Config) map[string]interface{} { return toMap(c.Memory) }},
	{namespace: "privacy",
		dst:     func(c *config.Config) interface{} { return &c.Privacy },
		collect: func(c *config.Config) map[string]interface{} { return toMap(c.Privacy) }},
	{namespace: "skillsLearner",
		dst:     func(c *config.Config) interface{} { return &c.SkillsLearner },
		collect: func(c *config.Config) map[string]interface{} { return toMap(c.SkillsLearner) }},
	{namespace: "heartbeat",
		dst:     func(c *config.Config) interface{} { return &c.Heartbeat },
		collect: func(c *config.Config) map[string]interface{} { return toMap(c.Heartbeat) }},
	{namespace: "teams",
		dst:     func(c *config.Config) interface{} { return &c.Teams },
		collect: func(c *config.Config) map[string]interface{} { return wrapKeyed(c.Teams) }},
	{namespace: "bindings",
		dst: func(c *config.Config) interface{} { return &c.Bindings },
		collect: func(c *config.Config) map[string]interface{} {
			if len(c.Bindings) == 0 {
				return nil
			}
			return map[string]interface{}{"list": c.Bindings}
		}},
}

type settingNamespace struct {
	namespace string
	dst       func(*config.Config) interface{}
	collect   func(*config.Config) map[string]interface{}
}

func toMap(v interface{}) map[string]interface{} {
	blob, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]interface{}
	_ = json.Unmarshal(blob, &m)
	if len(m) == 0 {
		return nil
	}
	return m
}

// wrapKeyed marshals a map[string]X into map[string]interface{} so it
// fits the configs.data column. Empty maps return nil so
// SaveSetting deletes the row instead of writing {}.
func wrapKeyed(v interface{}) map[string]interface{} {
	blob, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]interface{}
	_ = json.Unmarshal(blob, &m)
	if len(m) == 0 {
		return nil
	}
	return m
}

func authIdentity(r *http.Request) (auth.Identity, bool) {
	return auth.FromContext(r.Context())
}

// resolveAgent returns the AgentHandle for the given agent within the
// caller's user space. Apikey callers are additionally checked against
// their access list before the handle is returned. super_admin without
// an actAs override gets the foreign agent injected into their OWN
// UserSpace — sessions, memory, and provider scope stay caller-keyed
// (admin doesn't see the owner's chats), while the agent's persistent
// resolveSessionProject reads sessions.project_id for the chat
// the request is targeting so attachments and other workspace IO can
// route to projects/<pid>/ when the chat belongs to a project. Returns
// "" on any failure (caller treats as loose chat) — non-existent
// session, no auth context, and "no datastore" all collapse to the
// same outcome and we don't want a path lookup to break the chat
// hot-path.
func (s *Server) resolveSessionProject(ctx context.Context, r *http.Request, agentID, sessionKey string) string {
	if sessionKey == "" || s.dataStore == nil {
		return ""
	}
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		return ""
	}
	uid := ident.EffectiveUserID()
	if uid == "" {
		return ""
	}
	pid, err := s.dataStore.LookupSessionProject(ctx, uid, agentID, sessionKey)
	if err != nil {
		return ""
	}
	return pid
}

// identity (system prompt, agent-scope config, skills, files — all
// keyed by agent_id) is reused.
func (s *Server) resolveAgent(r *http.Request, agentID string) AgentHandle {
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		return nil
	}
	if !ident.CanAccessAgent(agentID) {
		return nil
	}
	if s.userResolver == nil {
		return nil
	}
	uid := ident.EffectiveUserID()
	space, err := s.userResolver.UserSpaceFor(uid)
	if err != nil || space == nil || space.Agents == nil {
		return nil
	}
	ag := space.Agents.AgentByID(agentID)
	// Lazy-attach when the agent isn't in the caller's UserSpace but
	// the caller is otherwise authorized to use it. Concrete scenarios:
	//
	//   1. super_admin browsing another user's agent.
	//   2. api_key whose ACL grants this agent — typically the key
	//      owner == agent owner, but this path also handles the
	//      app_user case where SwitchToAppUser flipped the identity
	//      to a fresh app_user whose UserSpace has no agents at all.
	//      Sessions/files written under that UserSpace then partition
	//      per end-user, which is the desired isolation.
	//   3. session user accessing a public agent owned by someone else
	//      (link-based sharing — gated on agents.is_public).
	//
	// For the public-agent path we DO need a DB hit to confirm
	// is_public; everything else (super_admin, apikey ACL) is already
	// answered by Identity. EnsureAgent is idempotent so the lookup
	// only fires before the agent lands in the user's Manager — once
	// attached, AgentByID succeeds on subsequent requests.
	if ag == nil {
		injector, hasInjector := s.userResolver.(api.AgentInjector)
		// super_admin can lazy-attach foreign agents regardless of actAs
		// mode. In actAs mode, EffectiveUserID() is the impersonated user
		// — attaching the agent to THAT user's UserSpace is exactly what
		// the admin Chats "Open" flow needs to read sessions written
		// under user_id=impersonated. The previous `!ident.IsActingAs()`
		// gate blocked this case and the chat panel rendered empty even
		// though the session_messages rows existed in the DB.
		canAttach := hasInjector &&
			(ident.AuthMethod == "apikey" || ident.Role == users.RoleSuperAdmin)
		if !canAttach && hasInjector && uid != "" && s.dataStore != nil {
			if rec, err := s.dataStore.GetAgent(r.Context(), agentID); err == nil && rec != nil && rec.IsPublic {
				canAttach = true
			}
		}
		if canAttach {
			if err := injector.EnsureAgent(r.Context(), uid, agentID); err == nil {
				ag = space.Agents.AgentByID(agentID)
			}
		}
	}
	if ag == nil {
		return nil
	}
	return ag
}

func (s *Server) resolveAllAgents(r *http.Request) []AgentHandle {
	ident, ok := auth.FromContext(r.Context())
	if !ok || s.userResolver == nil {
		return nil
	}
	space, err := s.userResolver.UserSpaceFor(ident.EffectiveUserID())
	if err != nil || space == nil || space.Agents == nil {
		return nil
	}
	all := space.Agents.All()
	out := make([]AgentHandle, 0, len(all))
	for _, ag := range all {
		if !ident.CanAccessAgent(ag.Name()) {
			continue
		}
		out = append(out, ag)
	}
	return out
}

// --- /api/status ---

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	configured := false
	if s.accounts != nil {
		if n, err := s.accounts.Count(r.Context()); err == nil && n > 0 {
			configured = true
		}
	}
	resp := map[string]any{
		"configured":       configured,
		"registrationOpen": s.registrationOpen(r),
		"running":          s.userResolver != nil,
		"port":             s.port,
		"version":          buildinfo.Version,
		"agents":           []any{},
		"channels":         []any{},
		"provider":         nil,
		"uptime":           formatDuration(time.Since(s.startedAt)),
	}
	ident, authed := auth.FromContext(r.Context())
	if !authed {
		jsonResponse(w, http.StatusOK, resp)
		return
	}
	resp["userId"] = ident.UserID
	resp["role"] = ident.Role
	resp["isAdmin"] = ident.Role == "super_admin"
	if resp["isAdmin"].(bool) && s.accounts != nil {
		if n, err := s.accounts.Count(r.Context()); err == nil {
			resp["users"] = n
		}
	}

	if !configured {
		jsonResponse(w, http.StatusOK, resp)
		return
	}
	cfg, err := s.loadUserConfig(r)
	if err == nil {
		// Pick the provider that actually backs the default model. The
		// model id is "<providerName>/<modelID>" (split on first slash —
		// modelIDs themselves can contain slashes, e.g.
		// "openrouter/xiaomi/mimo-v2-flash"). Falling back to "first
		// provider in the map" produced a mismatched panel where the
		// header said one provider but the default model belonged to
		// another.
		defaultModel := cfg.Agents.Defaults.Model
		var provName string
		if i := strings.IndexByte(defaultModel, '/'); i > 0 {
			provName = defaultModel[:i]
		}
		if prov, ok := cfg.Providers[provName]; ok {
			resp["provider"] = map[string]string{
				"name":    provName,
				"model":   defaultModel,
				"apiBase": prov.APIBase,
				"apiKey":  maskAPIKey(prov.APIKey),
			}
		} else {
			for name, prov := range cfg.Providers {
				resp["provider"] = map[string]string{
					"name":    name,
					"model":   defaultModel,
					"apiBase": prov.APIBase,
					"apiKey":  maskAPIKey(prov.APIKey),
				}
				break
			}
		}
		var chs []map[string]string
		for chType, ch := range cfg.Channels {
			if !ch.Enabled {
				continue
			}
			chs = append(chs, map[string]string{"type": chType})
		}
		if len(chs) > 0 {
			resp["channels"] = chs
		}
	}
	allAgents := s.resolveAllAgents(r)
	if len(allAgents) > 0 {
		var agentList []map[string]string
		for _, ag := range allAgents {
			id := ag.Name() // AgentHandle.Name() returns the agent id
			entry := map[string]string{"id": id}
			// Surface the human-friendly name from the agents row so the
			// dashboard list reads "default" / "ImgAny" instead of
			// "agt_…". Look-up failures fall back to id-only so a
			// transient store error doesn't black out the panel.
			if s.dataStore != nil {
				if rec, _ := s.dataStore.GetAgent(r.Context(), id); rec != nil && rec.Name != "" {
					entry["name"] = rec.Name
				}
			}
			agentList = append(agentList, entry)
		}
		resp["agents"] = agentList
	}
	jsonResponse(w, http.StatusOK, resp)
}

// --- /api/config (GET / POST) ---

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadUserConfig(r)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	masked := *cfg
	masked.Providers = make(map[string]config.ProviderConfig)
	for k, v := range cfg.Providers {
		v.APIKey = maskAPIKey(v.APIKey)
		masked.Providers[k] = v
	}
	if len(cfg.Skills.Entries) > 0 {
		me := make(map[string]config.SkillEntryCfg, len(cfg.Skills.Entries))
		for k, v := range cfg.Skills.Entries {
			me[k] = maskSkillEntry(v)
		}
		masked.Skills.Entries = me
	}
	if len(cfg.Skills.AgentEntries) > 0 {
		ma := make(map[string]map[string]config.SkillEntryCfg, len(cfg.Skills.AgentEntries))
		for aid, inner := range cfg.Skills.AgentEntries {
			out := make(map[string]config.SkillEntryCfg, len(inner))
			for k, v := range inner {
				out[k] = maskSkillEntry(v)
			}
			ma[aid] = out
		}
		masked.Skills.AgentEntries = ma
	}
	// Compute the system-only resolution of agents.defaults so the
	// dashboard can tell apart "inheriting from system" vs "overriding
	// at my user scope" — `cfg` already merges user over system, so
	// without this hint the UI sees the same value either way and
	// can't render an Inheriting/Override badge.
	sysDefaults := config.AgentsConfig{}.Defaults
	if s.dataStore != nil {
		_ = scope.SettingInto(r.Context(), s.dataStore, "agents.defaults", "", "", &sysDefaults)
	}
	serverTimezone := time.Local.String()
	// Marshal-then-extend keeps the response shape compatible (existing
	// callers ignore the extra `meta` key) without forcing a refactor of
	// config.Config to carry presentation metadata.
	blob, _ := json.Marshal(masked)
	out := map[string]any{}
	_ = json.Unmarshal(blob, &out)
	out["meta"] = map[string]any{
		"systemDefaultModel": sysDefaults.Model,
		"serverTimezone":     serverTimezone,
	}
	jsonResponse(w, http.StatusOK, out)
}

func (s *Server) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.ReadOnly() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "read-only"})
		return
	}
	// PATCH semantics: load existing cfg, then decode the request into
	// it. Go's json.Unmarshal leaves struct fields and map entries that
	// aren't present in the JSON untouched, so /settings POSTing just
	// `{"sandbox":{...}}` no longer wipes agents.defaults / skills.* /
	// every other namespace via saveUserConfig's namespace sweep.
	buf, err := io.ReadAll(r.Body)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var raw struct {
		Prefs   *config.PrefsCfg `json:"prefs"`
		Sandbox *json.RawMessage `json:"sandbox"`
		Skills  *struct {
			Entries      map[string]config.SkillEntryCfg            `json:"entries"`
			AgentEntries map[string]map[string]config.SkillEntryCfg `json:"agentEntries"`
		} `json:"skills"`
	}
	_ = json.Unmarshal(buf, &raw)
	if raw.Prefs != nil {
		raw.Prefs.Timezone = strings.TrimSpace(raw.Prefs.Timezone)
		if raw.Prefs.Timezone != "" {
			if _, err := time.LoadLocation(raw.Prefs.Timezone); err != nil {
				jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid timezone: use an IANA name like Asia/Shanghai"})
				return
			}
		}
	}
	merged, err := s.loadUserConfig(r)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// Avoid merging stale per-agent skill entries back into the saved
	// state — those are persisted via the per-agent loop below, not
	// through the namespace sweep, and re-writing them here would
	// re-build the legacy shape we just split apart.
	merged.Skills.AgentEntries = nil
	// Snapshot the stored global skill entries before the request body
	// is unmarshalled over them. The dashboard round-trips masked
	// secrets ("sk-1****abcd") — mergeSkillEntry resolves those back to
	// the stored originals so a no-op save can't clobber real keys.
	existingSkillEntries := make(map[string]config.SkillEntryCfg, len(merged.Skills.Entries))
	for name, entry := range merged.Skills.Entries {
		existingSkillEntries[name] = entry
	}
	if err := json.Unmarshal(buf, merged); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if raw.Skills != nil {
		for name, in := range raw.Skills.Entries {
			merged.Skills.Entries[name] = mergeSkillEntry(existingSkillEntries[name], in)
		}
	}
	if err := s.saveUserConfig(r, merged); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// Per-agent skill env overrides (one row per agent at scope=agent,
	// name=skills.entries). Pull from the raw body — not from the
	// merged Config — so we only touch agents the caller actually
	// patched, and don't echo every existing override back as a write.
	if raw.Prefs != nil {
		sc, scopeID := s.scopeForSave(r)
		uid, aid := scope.OwnershipFromScope(sc, scopeID)
		data := map[string]interface{}{}
		if raw.Prefs.Timezone != "" {
			data["timezone"] = raw.Prefs.Timezone
		}
		if err := scope.SaveSetting(r.Context(), s.dataStore, uid, aid, scope.PrefsNamespace, data); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}
	if raw.Skills != nil && raw.Skills.AgentEntries != nil {
		for agentID, entries := range raw.Skills.AgentEntries {
			rec, err := s.dataStore.GetAgent(r.Context(), agentID)
			if err != nil || rec == nil {
				jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": "agent not found: " + agentID})
				return
			}
			if !s.authorizeScope(w, r, scope.Agent, agentID, scopeWrite) {
				return
			}
			// Resolve masked secrets back to the stored originals before
			// persisting — same protection as the global entries path.
			if existing, err := loadAgentSkillEntries(r.Context(), s.dataStore, agentID); err == nil {
				for name, in := range entries {
					entries[name] = mergeSkillEntry(existing[name], in)
				}
			}
			if err := saveAgentSkillEntries(r.Context(), s.dataStore, agentID, entries); err != nil {
				jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
				return
			}
		}
	}
	// Cached UserSpaces hold a snapshot of the merged config (including
	// agents.defaults.model and the provider chain). Without this, an
	// agent loaded before the change keeps seeing the stale model and
	// surfaces "no usable LLM provider" in chat.
	sc, scopeID := s.scopeForSave(r)
	if sc == scope.System && raw.Sandbox != nil {
		if err := s.reloadSystemSandbox(); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}
	s.invalidateScope(sc, scopeID)
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) reloadSystemSandbox() error {
	type sandboxReloader interface{ ReloadSandbox() error }
	if s.userResolver == nil {
		return nil
	}
	if r, ok := s.userResolver.(sandboxReloader); ok {
		return r.ReloadSandbox()
	}
	return nil
}

// scopeForSave mirrors the scope-resolution logic in saveUserConfig so
// callers can invalidate exactly the UserSpaces that were just touched.
func (s *Server) scopeForSave(r *http.Request) (string, string) {
	ident, ok := authIdentity(r)
	if ok && ident.Role == "super_admin" {
		if !ident.IsActingAs() {
			return scope.System, ""
		}
		return scope.User, ident.EffectiveUserID()
	}
	if ok {
		return scope.User, ident.UserID
	}
	return scope.User, ""
}

// --- /api/test-provider ---

type testProviderRequest struct {
	APIBase  string `json:"apiBase"`
	APIKey   string `json:"apiKey"`
	Model    string `json:"model"`
	APIType  string `json:"apiType"`
	AuthType string `json:"authType"`
}

func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	var req testProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	jsonResponse(w, http.StatusOK, runProviderTest(r.Context(), req))
}

// handleTestStoredProvider runs the same connection check, but reads the
// apiKey + apiBase + apiType + authType from a saved provider row instead
// of taking them from the request body. Lets the Edit dialog test against
// the stored secret so users don't have to re-paste the key on every edit.
func (s *Server) handleTestStoredProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, err := s.dataStore.GetConfig(r.Context(), id)
	if err != nil || rec == nil || rec.Kind != store.KindProvider {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}
	// Test = read-equivalent: any user that can read the row can verify
	// it works. They'll be using it via their agent runtime anyway, so a
	// dashboard-side dry run shouldn't be more restrictive.
	if !s.authorizeScope(w, r, rec.LegacyScope(), rec.LegacyScopeID(), scopeRead) {
		return
	}
	// The browser never receives the unmasked API key, so it stays
	// server-side via the stored row. But everything else (apiBase,
	// apiType, authType) is freely editable in the form and the user
	// expects Test to exercise *what they typed*, not the saved row —
	// otherwise tweaking the URL and clicking Test silently re-pings
	// the old URL and reports green. Honor any overrides the client
	// sends; fall back to the stored values when a field is omitted.
	var body struct {
		Model    string  `json:"model"`
		APIBase  *string `json:"apiBase,omitempty"`
		APIType  *string `json:"apiType,omitempty"`
		AuthType *string `json:"authType,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	pc := config.ProviderConfig{}
	if blob, err := json.Marshal(rec.Data); err == nil {
		_ = json.Unmarshal(blob, &pc)
	}
	apiBase := pc.APIBase
	if body.APIBase != nil {
		apiBase = *body.APIBase
	}
	apiType := pc.APIType
	if body.APIType != nil {
		apiType = *body.APIType
	}
	authType := pc.AuthType
	if body.AuthType != nil {
		authType = *body.AuthType
	}
	jsonResponse(w, http.StatusOK, runProviderTest(r.Context(), testProviderRequest{
		APIBase:  apiBase,
		APIKey:   pc.APIKey,
		Model:    body.Model,
		APIType:  apiType,
		AuthType: authType,
	}))
}

// runProviderTest issues a lightweight chat completion against the
// upstream provider. Shared between the inline test (key supplied in
// the request body, used during create / re-key) and the stored test
// (key looked up server-side, used during edit).
//
// We're deliberately stricter than "HTTP 2xx = ok" because some
// upstreams (one-api / new-api gateways, generic reverse proxies, even
// a misconfigured nginx) happily return 200 with HTML on a wrong path.
// A bare 2xx check there reports green for a URL that the runtime will
// later 404 on. So after the request we also require the response to
// look like a real Messages / ChatCompletion object.
func runProviderTest(ctx context.Context, req testProviderRequest) map[string]any {
	base := provider.NormalizeAPIBase(req.APIBase, req.APIType)
	var testURL string
	var payload string
	if req.APIType == "anthropic-messages" {
		testURL = base + "/v1/messages"
		model := req.Model
		if model == "" {
			model = "claude-sonnet-4-20250514"
		}
		payload = fmt.Sprintf(`{"model":"%s","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, model)
	} else {
		testURL = base + "/chat/completions"
		model := req.Model
		if model == "" {
			model = "gpt-4o-mini"
		}
		payload = openAIProviderTestPayload(model, false)
	}
	respBody, statusCode, err := sendProviderTestRequest(ctx, req, testURL, payload)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	if req.APIType != "anthropic-messages" && statusCode >= 400 && shouldRetryProviderTestWithMaxCompletionTokens(respBody) {
		model := req.Model
		if model == "" {
			model = "gpt-4o-mini"
		}
		respBody, statusCode, err = sendProviderTestRequest(ctx, req, testURL, openAIProviderTestPayload(model, true))
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
	}

	if statusCode < 200 || statusCode >= 300 {
		return map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("HTTP %d: %s", statusCode, truncate(strings.TrimSpace(string(respBody)), 240)),
		}
	}
	if err := validateProviderTestBody(req.APIType, respBody); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true}
}

func openAIProviderTestPayload(model string, maxCompletionTokens bool) string {
	if maxCompletionTokens {
		return fmt.Sprintf(`{"model":"%s","max_completion_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, model)
	}
	return fmt.Sprintf(`{"model":"%s","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, model)
}

func sendProviderTestRequest(ctx context.Context, req testProviderRequest, testURL, payload string) ([]byte, int, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", testURL, strings.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if req.APIType == "anthropic-messages" {
		httpReq.Header.Set("x-api-key", req.APIKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")
	} else if req.AuthType == "api-key" {
		httpReq.Header.Set("api-key", req.APIKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode, nil
}

func shouldRetryProviderTestWithMaxCompletionTokens(body []byte) bool {
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "max_tokens") && strings.Contains(lower, "max_completion_tokens")
}

// validateProviderTestBody confirms the 2xx body is a real Messages /
// ChatCompletion object rather than an HTML splash page or a generic
// gateway "ok" payload. Returns nil if the shape matches.
func validateProviderTestBody(apiType string, body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return fmt.Errorf("empty response body")
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return fmt.Errorf("response is not JSON: %s", truncate(string(trimmed), 120))
	}
	var probe map[string]any
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return fmt.Errorf("response is not valid JSON: %v", err)
	}
	if apiType == "anthropic-messages" {
		if _, ok := probe["content"].([]any); ok {
			return nil
		}
		if t, _ := probe["type"].(string); t == "message" {
			return nil
		}
		return fmt.Errorf("response missing Anthropic Messages fields (content/type=message)")
	}
	if _, ok := probe["choices"].([]any); ok {
		return nil
	}
	return fmt.Errorf("response missing OpenAI Chat Completion field 'choices'")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// --- /api/tasks ---

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	if s.taskQueue == nil {
		jsonResponse(w, http.StatusOK, []any{})
		return
	}
	tasks := s.taskQueue.RecentTasks(50)
	out := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		entry := map[string]any{
			"id":        t.ID,
			"agentId":   t.AgentID,
			"chatKey":   t.ChatKey,
			"status":    string(t.Status),
			"createdAt": t.CreatedAt.Format(time.RFC3339),
		}
		if t.StartedAt != nil && t.DoneAt != nil {
			entry["duration"] = t.DoneAt.Sub(*t.StartedAt).Milliseconds()
		}
		if t.Error != nil {
			entry["error"] = t.Error.Error()
		}
		out = append(out, entry)
	}
	jsonResponse(w, http.StatusOK, out)
}

// --- chat handlers (delegate to per-user agent) ---

type chatRequest struct {
	AgentID   string `json:"agentId,omitempty"`
	SessionID string `json:"sessionId"`
	// ProjectID, when non-empty AND the session row doesn't yet exist,
	// is the "this chat belongs to project X" hint the URL carries
	// (`?project=<pid>`) before the first message. Once the row exists
	// it's authoritative — the server reads project_id from the row
	// and ignores any later hint.
	ProjectID string `json:"projectId,omitempty"`
	Message   string `json:"message"`
	// Images carries data URLs / HTTPS URLs for image attachments. The
	// web client historically sends them under `imageUrls` (camelCase)
	// while the API path uses `images`; we accept both and merge below
	// so server-side plumbing has one canonical slice. Without this the
	// web's image_url content parts never reach the agent (empty slice
	// → no ContentParts persisted → history reload shows no image, and
	// vision LLMs see only the text breadcrumb).
	Images    []string `json:"images,omitempty"`
	ImageURLs []string `json:"imageUrls,omitempty"`
	// Attachments is the typed, general-purpose attachment field. Each
	// entry can carry an optional Name which is sanitized and used as
	// the on-disk filename so the LLM sees `report.pdf` instead of
	// `image_3jk7l_0.pdf`. Unlike Images / ImageURLs, entries here are
	// NOT inlined as vision content parts — they only land in
	// /workspace and reach the LLM via the `[Attached: /workspace/X]`
	// breadcrumb. Use Images / ImageURLs (not Attachments) when you
	// want the bytes shown directly to a vision model.
	Attachments []attachmentRequest `json:"attachments,omitempty"`
	Params      map[string]any      `json:"params,omitempty"`
}

// attachmentRequest is the wire form of a single attachment. URL is the
// data: or http(s) URL; Name is the optional caller-supplied filename.
type attachmentRequest struct {
	URL  string `json:"url"`
	Name string `json:"name,omitempty"`
}

// allAttachments flattens the three input shapes (Images, ImageURLs,
// Attachments) into one ordered slice for materialization into
// /workspace. Order: Images → ImageURLs → Attachments. Clients
// normally pick one; mixing is allowed and not de-dup'd.
func (r chatRequest) allAttachments() []agent.Attachment {
	n := len(r.Images) + len(r.ImageURLs) + len(r.Attachments)
	if n == 0 {
		return nil
	}
	out := make([]agent.Attachment, 0, n)
	for _, u := range r.Images {
		out = append(out, agent.Attachment{URL: u})
	}
	for _, u := range r.ImageURLs {
		out = append(out, agent.Attachment{URL: u})
	}
	for _, a := range r.Attachments {
		out = append(out, agent.Attachment{URL: a.URL, Name: a.Name})
	}
	return out
}

// inlineImageURLs returns just the URLs that should be inlined as
// vision content parts (PhotoURLs → image_url content blocks). Only
// the legacy image-only fields qualify: Images and ImageURLs are by
// contract caller-asserted images, so wrapping them as image_url is
// safe. The newer Attachments field is general-purpose (pdf / zip /
// txt are all legal), and feeding a non-image URL to provider vision
// channels fails the whole turn upstream (Anthropic returns 400 for
// `{type:image, source:{type:url, url:<pdf>}}`). Attachments reach
// the LLM via the `[Attached: /workspace/<file>]` breadcrumb instead.
func (r chatRequest) inlineImageURLs() []string {
	if len(r.Images) == 0 && len(r.ImageURLs) == 0 {
		return nil
	}
	out := make([]string, 0, len(r.Images)+len(r.ImageURLs))
	out = append(out, r.Images...)
	out = append(out, r.ImageURLs...)
	return out
}

// preMaterialized reports whether the caller already uploaded the
// attachments + prefixed `[Attached: /workspace/...]` breadcrumb into
// the message. The web client does this end-to-end (uploadAgentFiles +
// inline breadcrumb in chat/page.tsx); doing it again server-side
// double-writes the file under a generated name and emits a second
// breadcrumb, which the LLM reads as two distinct images and tries to
// edit each separately. API callers that just send raw images via the
// chat-completions extension have no breadcrumb, so the server has to
// materialize on their behalf.
func (r chatRequest) preMaterialized() bool {
	return strings.HasPrefix(r.Message, "[Attached:")
}

// annotateMessageWithAttachments prepends one `[Attached: /workspace/<file>]`
// line per attachment to the user message — same breadcrumb format the web
// UI uses (see web/src/app/agents/[id]/chat/page.tsx:639-645), so the wire
// shape the LLM sees is identical regardless of whether the turn arrived
// via the web chat or the chat API. provider.StripAttachedPrefix scrubs
// these tags from stored history before they hit UI bubbles / page titles.
//
// We deliberately do NOT add a trailing "do not probe" block. An earlier
// pass tried that — but the explicit negative directive triggered the
// opposite of its intent (models reflexively `which`/`ls`/`file`'d the
// path "to confirm" before using it). The web path proves a single bare
// breadcrumb is enough; mirror that exactly.
func annotateMessageWithAttachments(message string, paths []string) string {
	if len(paths) == 0 {
		return message
	}
	var b strings.Builder
	for _, p := range paths {
		b.WriteString("[Attached: /workspace/")
		b.WriteString(p)
		b.WriteString("]\n")
	}
	if message != "" {
		b.WriteString(message)
	}
	return b.String()
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	ag := s.resolveAgent(r, req.AgentID)
	if ag == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return
	}
	atts := req.allAttachments()
	msgText := req.Message
	if !req.preMaterialized() {
		// Resolve the chat's project so attachments land in
		// projects/<pid>/ when the session belongs to one. Best-effort:
		// failure → empty pid → loose-chat scope (the historical
		// behavior).
		projectID := s.resolveSessionProject(r.Context(), r, ag.Name(), req.SessionID)
		paths := ag.WriteSessionAttachments(r.Context(), req.SessionID, projectID, atts)
		msgText = annotateMessageWithAttachments(req.Message, paths)
	}
	reply := ag.HandleWebChat(r.Context(), req.SessionID, req.ProjectID, s.effectiveUserID(r), msgText, req.inlineImageURLs(), req.Params)
	jsonResponse(w, http.StatusOK, map[string]any{"reply": reply})
}

// handleChatSteer buffers a message into an in-flight turn for the
// session. It does NOT open a stream or emit an event — the running
// turn (started by the earlier /api/chat/stream POST) folds the message
// in between tool rounds and emits the "steer" event on its existing
// SSE. 200 {"buffered":true} when a turn was active; 409
// {"buffered":false} when none is running, so the client falls back to
// a normal /api/chat/stream send.
func (s *Server) handleChatSteer(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	ag := s.resolveAgent(r, req.AgentID)
	if ag == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return
	}
	if s.effectiveUserID(r) == "" {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "empty message"})
		return
	}
	if ag.SteerWeb(req.SessionID, req.ProjectID, req.Message) {
		jsonResponse(w, http.StatusOK, map[string]any{"buffered": true})
		return
	}
	jsonResponse(w, http.StatusConflict, map[string]any{"buffered": false})
}

// agentTurnTimeout is the upper bound on how long an agent goroutine
// is allowed to run after the client connection drops. Bumped to 45m
// after fan-out delegate_task work (6 parallel subagents × ~10m each
// driving camoufox-cli) routinely blew through the prior 15m budget
// in the middle of a Chat call, surfacing as "context deadline
// exceeded" to every sibling at once. 45m is comfortably above a
// realistic max-parallel fan-out with browser automation; still
// bounded so a genuine runaway loop doesn't pin a goroutine forever.
const agentTurnTimeout = 45 * time.Minute

func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	ag := s.resolveAgent(r, req.AgentID)
	if ag == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": "streaming unsupported"})
		return
	}
	uid := s.effectiveUserID(r)
	if uid == "" {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Defeat nginx / Cloudflare buffering of long-lived responses; the
	// agent loop emits chunks at human-typing pace and we want them on
	// the wire immediately, not held until the response closes.
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()
	atts := req.allAttachments()
	imageURLs := req.inlineImageURLs()
	msgText := req.Message
	if !req.preMaterialized() {
		projectID := s.resolveSessionProject(r.Context(), r, ag.Name(), req.SessionID)
		paths := ag.WriteSessionAttachments(r.Context(), req.SessionID, projectID, atts)
		msgText = annotateMessageWithAttachments(req.Message, paths)
	}

	// Subscribe to the hub BEFORE starting the agent so we don't race
	// the first emitted event. The hub buffers in-flight events so
	// dispatch from emitEvent never blocks even if we're slow to drain.
	hub := s.chatEventHub()
	agentID := ag.Name()
	sub, unsubscribe := hub.Subscribe(uid, agentID, req.SessionID)
	defer unsubscribe()

	// Detach the agent's ctx from the request: when the browser tab
	// disconnects (refresh, close, network blip) we want the agent to
	// keep running so its already-paid-for LLM call finishes and the
	// reply lands in session_events. The agentTurnTimeout cap is the only
	// thing that can kill it.
	//
	// WithoutCancel severs the request→agent cancel propagation, but that
	// alone is not enough: this handler also owns `cancel`, and the
	// clientGone branch below used to `return` on disconnect, which fired
	// `defer cancel()` and killed the agent anyway. The detach is only
	// real because clientGone no longer returns — it parks the handler
	// until the turn ends naturally. Do NOT reintroduce an early return on
	// client disconnect; that is exactly the "工具调用莫名其妙 stopped" bug.
	agentCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), agentTurnTimeout)
	// cancel stays on the handler defer (not a per-goroutine scope) on
	// purpose: a slash that queues a continuation keeps the SSE open past
	// HandleMessage's return, and the loop reuses agentCtx.Done() as the
	// upper bound of that wait. Cancelling the moment HandleMessage returns
	// would tear agentCtx down before the continuation's events reach this
	// handler's safety-net check. The handler only returns — and thus only
	// cancels — at a true turn terminal (`done` / agentDone-without-
	// continuation / the timeout).
	defer cancel()
	// Make this turn stoppable via POST /api/chat/stop. The handle is
	// per-turn so a stop→resend race can't evict the new turn's entry.
	stopHandle := s.registerTurnCancel(uid, agentID, req.SessionID, cancel)
	defer s.unregisterTurnCancel(uid, agentID, req.SessionID, stopHandle)
	agentCtx = agent.ContextWithStream(agentCtx, nil, s.dataStore, hub, uid, agentID, req.SessionID)

	agentDone := make(chan struct{})
	go func() {
		defer close(agentDone)
		// events param stays nil — emitEvent now fans out via the
		// streamCtx attached above (persist + hub). The legacy channel
		// path is no longer needed for this handler.
		_ = ag.HandleWebChatStream(agentCtx, req.SessionID, req.ProjectID, uid, msgText, imageURLs, req.Params, nil)
	}()

	// Heartbeat keeps proxies (nginx 60s default, Cloudflare 100s, ELB
	// 60s) from killing an idle SSE connection while the agent is
	// thinking but not yet emitting content.
	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()

	clientGone := r.Context().Done()
	forwardedAny := false
	// clientDropped latches when the receiving SSE goes away (tab refresh,
	// conversation switch, network blip). Once set, we stop touching the
	// dead response writer but keep this handler parked — see the
	// clientGone case for why returning here would kill the agent.
	clientDropped := false
	// turnPending flips on when the slash handler reports it queued a
	// continuation via bus.Inbound (`turn_pending` event). The POST
	// goroutine's HandleMessage has already returned, but the real
	// reply is still 10–15s away on a different goroutine — we keep
	// the SSE open so the browser's typing indicator stays visible
	// and the continuation's content_delta/content events stream into
	// the same connection. Cleared when the continuation's own `done`
	// arrives, at which point the loop returns normally.
	turnPending := false
	for {
		select {
		case <-clientGone:
			// The client dropped the receiving stream. This must NOT
			// cancel the agent: returning here would fire `defer cancel()`
			// and tear down agentCtx mid-turn, killing in-flight tool
			// calls / sub-agents and losing the rest of the reply. Instead
			// latch clientDropped and keep looping until the turn ends
			// naturally (`done` / agentDone / the agentCtx timeout). The
			// agent's events still persist to session_events via the stream
			// ctx, so a reloaded page picks up the full reply through
			// /api/chat/subscribe?since=N. Nil the channel so this case
			// stops selecting (r.Context().Done() stays closed forever) and
			// stop the keepalive — nothing is listening on the wire.
			clientDropped = true
			clientGone = nil
			keepalive.Stop()
		case <-agentDone:
			// Race: HandleMessage publishes `turn_pending` to the hub
			// AND `defer close(agentDone)` fires from the same goroutine.
			// Go's select picks at random when both are ready, so
			// agentDone can win even when a turn_pending event is
			// sitting in the sub buffer. Drain pending events first to
			// make the decision deterministic.
		drain:
			for {
				select {
				case env, ok := <-sub:
					if !ok {
						return
					}
					if env.Event.Type == "turn_pending" {
						turnPending = true
						continue
					}
					if env.Event.Type == "done" {
						if !clientDropped {
							forwardEvent(w, flusher, env)
						}
						forwardedAny = true
						return
					}
					if !clientDropped {
						forwardEvent(w, flusher, env)
					}
					forwardedAny = true
				default:
					break drain
				}
			}
			if turnPending {
				// HandleMessage returned silent after queueing a
				// continuation. Don't close; wait for the continuation's
				// `done` event over the hub instead. agentCtx.Done()
				// (agentTurnTimeout) is the upper bound if it never lands.
				agentDone = nil
				continue
			}
			if !forwardedAny && !clientDropped {
				forwardSyntheticEvent(w, flusher, agent.ChatEvent{
					Type: "error",
					Data: map[string]any{"message": "agent finished without emitting a response"},
				})
			}
			return
		case <-agentCtx.Done():
			// Safety net for the turnPending path above: bail out at
			// the agent context's hard timeout even if no `done` event
			// ever arrives.
			return
		case <-keepalive.C:
			// keepalive.Stop() was called once the client dropped, but a
			// tick may already be buffered — skip the write either way.
			if clientDropped {
				continue
			}
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case env, ok := <-sub:
			if !ok {
				return
			}
			if env.Event.Type == "turn_pending" {
				turnPending = true
				continue
			}
			if !clientDropped {
				forwardEvent(w, flusher, env)
			}
			forwardedAny = true
			if env.Event.Type == "done" {
				return
			}
		}
	}
}

// forwardEvent writes one EventEnvelope to the SSE response. Includes
// seq inline in the JSON payload (in addition to the SSE `id:` line)
// so the fetch-based parser used by the frontend's POST sendChatStream
// can dedup against the parallel /api/chat/subscribe SSE connection.
// Without this dedup, every chunk renders twice during an active turn.
func forwardEvent(w http.ResponseWriter, flusher http.Flusher, env agent.EventEnvelope) {
	payload := map[string]any{
		"seq":  env.Seq,
		"type": env.Event.Type,
	}
	if env.Event.Data != nil {
		payload["data"] = env.Event.Data
	}
	data, _ := json.Marshal(payload)
	if env.Seq >= 0 {
		fmt.Fprintf(w, "id: %d\n", env.Seq)
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

func forwardSyntheticEvent(w http.ResponseWriter, flusher http.Flusher, evt agent.ChatEvent) {
	forwardEvent(w, flusher, agent.EventEnvelope{Seq: -1, Event: evt})
}

// handleChatSubscribe holds an SSE connection open for one (agent,
// session) pair and forwards three kinds of traffic:
//
//  1. Replay: session_events rows with seq > since (or > Last-Event-ID)
//     that the client missed before connecting. Lets a freshly
//     reloaded page pick up an in-flight turn without the rest of the
//     reply disappearing.
//
//  2. Live agent chat events from the hub — every emitEvent call from
//     the agent loop fans through here. This covers both the
//     synchronous POST /api/chat/stream path AND turns started by
//     other tabs / cron firings, so any open chat panel sees them
//     regardless of who triggered the work.
//
//  3. Legacy WebChannel bus messages — cron-fired final replies that
//     route through bus.Outbound rather than the chat-event path.
//     Kept so we don't lose pre-existing functionality during the
//     transition.
//
// Auth gating reuses resolveAgent, so the caller must already have
// permission to chat with this agent. The subscription doesn't
// generate any traffic on its own — closes are silent (client gone).
func (s *Server) handleChatSubscribe(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agentId")
	sessionID := r.URL.Query().Get("sessionId")
	if agentID == "" || sessionID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "agentId and sessionId required"})
		return
	}
	if ag := s.resolveAgent(r, agentID); ag == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return
	}
	uid := s.effectiveUserID(r)
	if uid == "" {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	// Initial flush so the client EventSource fires `open` immediately.
	fmt.Fprintf(w, ": ok\n\n")
	flusher.Flush()

	// Resume point: prefer Last-Event-ID (browser-managed reconnect),
	// fall back to ?since=N for callers that pass it explicitly. -1
	// means "stream live only, no replay".
	sinceSeq := int64(-1)
	if hdr := r.Header.Get("Last-Event-ID"); hdr != "" {
		if v, err := strconv.ParseInt(hdr, 10, 64); err == nil {
			sinceSeq = v
		}
	}
	if q := r.URL.Query().Get("since"); q != "" {
		if v, err := strconv.ParseInt(q, 10, 64); err == nil {
			sinceSeq = v
		}
	}

	hub := s.chatEventHub()
	// Snapshot the in-flight turn BEFORE subscribing: any content_delta
	// published after Subscribe lands in the live channel, so taking
	// the snapshot first guarantees no delta is both inside the
	// snapshot text AND replayed live (duplicated text). The reverse
	// gap — a delta emitted between snapshot and Subscribe is simply
	// missing — self-heals when the round's `content` seal replaces
	// the partial text with the full round text client-side.
	turnPartial, turnActive := hub.TurnSnapshot(uid, agentID, sessionID)
	// Subscribe BEFORE replay so any event that lands while we're
	// scanning the DB ends up either in the replayed range OR in the
	// live channel — never both, never lost.
	live, unsubscribeLive := hub.Subscribe(uid, agentID, sessionID)
	defer unsubscribeLive()

	// Replay missed events from the persistent log.
	if s.dataStore != nil {
		rows, err := s.dataStore.ListSessionEventsSince(r.Context(), uid, agentID, sessionID, sinceSeq)
		if err != nil {
			slog.Warn("session_events replay failed", "agent", agentID, "session", sessionID, "since", sinceSeq, "error", err)
		}
		for _, rec := range rows {
			fmt.Fprintf(w, "id: %d\n", rec.Seq)
			if len(rec.Data) == 0 || string(rec.Data) == "null" {
				fmt.Fprintf(w, "data: {\"seq\":%d,\"type\":%q}\n\n", rec.Seq, rec.Type)
			} else {
				fmt.Fprintf(w, "data: {\"seq\":%d,\"type\":%q,\"data\":%s}\n\n", rec.Seq, rec.Type, string(rec.Data))
			}
			flusher.Flush()
			if rec.Seq > sinceSeq {
				sinceSeq = rec.Seq
			}
		}
	}

	// A turn is in flight for this session: hand the client the
	// half-generated text of the current round (content_delta is never
	// persisted, so replay above cannot contain it) plus a "running"
	// signal so the UI can attach visibly — typing bubble, Stop button
	// — instead of sitting silent until the turn's `done`. Sent even
	// with empty partial text: the running flag alone is information
	// the client has no other way to get.
	if turnActive {
		forwardSyntheticEvent(w, flusher, agent.ChatEvent{
			Type: "content_snapshot",
			Data: map[string]any{"content": turnPartial, "running": true},
		})
	}

	// Legacy webChan path: cron-fired bus.Outbound messages. Kept until
	// the cron path is refactored to emit through the chat-event hub
	// (then this can go away).
	var outbound <-chan bus.OutboundMessage
	var unsubscribeOutbound func() = func() {}
	if s.webChan != nil {
		outbound, unsubscribeOutbound = s.webChan.Subscribe(agentID, sessionID)
	}
	defer unsubscribeOutbound()

	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case env, ok := <-live:
			if !ok {
				return
			}
			// content_delta is the high-volume token-by-token stream
			// that drives the active turn's bubble. It is intentionally
			// NOT persisted (see emitEvent), arrives with seq=-1, and is
			// already delivered to the initiating tab via the POST
			// /api/chat/stream subscription on the same hub. Forwarding
			// it here would double-render on the active tab; reloaders
			// who join mid-turn miss the partial reveal but still get
			// the trailing `content` event with the full text.
			if env.Event.Type == "content_delta" {
				continue
			}
			// Drop replay-overlap events: any event with seq <= the
			// highest seq we already streamed during replay. Without
			// this, a browser that reconnects at exactly the wrong
			// moment would render the same content chunk twice.
			if env.Seq >= 0 && env.Seq <= sinceSeq {
				continue
			}
			if env.Seq >= 0 {
				sinceSeq = env.Seq
				fmt.Fprintf(w, "id: %d\n", env.Seq)
			}
			payload := map[string]any{
				"seq":  env.Seq,
				"type": env.Event.Type,
			}
			if env.Event.Data != nil {
				payload["data"] = env.Event.Data
			}
			data, _ := json.Marshal(payload)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case msg, ok := <-outbound:
			if !ok {
				outbound = nil
				continue
			}
			payload := map[string]any{
				"text":      msg.Text,
				"parseMode": msg.ParseMode,
			}
			if len(msg.MediaItems) > 0 {
				items := make([]map[string]any, 0, len(msg.MediaItems))
				for _, m := range msg.MediaItems {
					items = append(items, map[string]any{
						"filename":    m.Filename,
						"contentType": m.ContentType,
					})
				}
				payload["mediaItems"] = items
			}
			data, _ := json.Marshal(payload)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// handleChatTodo reads the per-session todo.md the agent maintains and
// returns it as both raw markdown and a parsed checklist. We resolve
// the session key → (chatID, projectID) here so the frontend doesn't
// need to know the on-disk path layout (`sessions/<chat>/todo.md` vs
// `projects/<pid>/<chat>/todo.md`).
//
// A missing file is not an error — fresh sessions or runs that don't
// use the todo convention return {items: [], raw: ""}. Frontend hides
// the panel when items is empty.
func (s *Server) handleChatTodo(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agentId")
	sessionID := r.URL.Query().Get("sessionId")
	ag := s.resolveAgent(r, agentID)
	if ag == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return
	}
	if sessionID == "" {
		jsonResponse(w, http.StatusOK, map[string]any{"items": []any{}, "raw": ""})
		return
	}

	// Build the agent-relative path. Project chats live under
	// projects/<pid>/<chat>/, plain chats under sessions/<chat>/. The
	// agent's workdir resolves bare filenames to its session subdir, so
	// a `write_file("todo.md", ...)` from the agent lands at one of
	// these two paths — same shape that handleAgentFileList already
	// surfaces.
	chatID := s.workspaceSessionScope(r.Context(), ag.Name(), sessionID)
	projectID := s.resolveSessionProject(r.Context(), r, ag.Name(), sessionID)
	var relPath string
	switch {
	case projectID != "" && chatID != "":
		relPath = "projects/" + projectID + "/" + chatID + "/todo.md"
	case chatID != "":
		relPath = "sessions/" + chatID + "/todo.md"
	default:
		jsonResponse(w, http.StatusOK, map[string]any{"items": []any{}, "raw": ""})
		return
	}

	raw, err := s.readWorkspaceFileBytes(r.Context(), ag.Name(), relPath)
	if err != nil {
		// 404 / not-yet-written / FS miss — return empty rather than
		// surfacing the error; the panel just stays hidden until the
		// agent writes one.
		jsonResponse(w, http.StatusOK, map[string]any{"items": []any{}, "raw": ""})
		return
	}
	items := parseTodoMarkdown(string(raw))
	jsonResponse(w, http.StatusOK, map[string]any{
		"items": items,
		"raw":   string(raw),
	})
}

// readWorkspaceFileBytes reads a single agent-relative file via the
// workspace store, falling back to the local FS layout when no store
// is wired. Bare path-string interface used only by the todo endpoint
// — workspaceStore.Get expects (projectID, chatID) but here we already
// baked them into the path, so pass empties.
func (s *Server) readWorkspaceFileBytes(ctx context.Context, agentID, relPath string) ([]byte, error) {
	if s.workspaceStore != nil {
		rc, err := s.workspaceStore.Get(ctx, agentID, "", "", relPath)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	home, err := config.HomeDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, "workspaces", agentID)
	abs := filepath.Join(root, filepath.Clean("/"+relPath))
	if !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return nil, fmt.Errorf("path escape")
	}
	return os.ReadFile(abs)
}

// parseTodoMarkdown extracts checkbox lines from a todo.md body and
// returns them as structured items. Conventions:
//
//   - [ ] text   → pending
//   - [x] text   → completed
//   - [X] text   → completed (case-insensitive)
//
// Anything else (heading lines, blank lines, non-checkbox bullets) is
// ignored — todo.md doubles as a human-readable plan document, so we
// don't force a rigid schema. Indented checkboxes are NOT supported
// in v1 (no sub-tasks); flatten them in the model's nudge if needed.
//
// Duplicate-text entries get merged: first occurrence wins the slot,
// `done` is OR'd across all occurrences. This is defensive — the
// convention says use edit_file to flip a single item, but if the
// model accidentally re-runs write_file and stacks old + new lists,
// we'd otherwise show the same step twice. Progress (done=true) is
// sticky on merge so a later pending duplicate can't regress an
// already-checked item.
func parseTodoMarkdown(s string) []map[string]any {
	out := []map[string]any{}
	idx := map[string]int{}
	for _, line := range strings.Split(s, "\n") {
		trim := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(trim, "- [") && !strings.HasPrefix(trim, "* [") {
			continue
		}
		if len(trim) < 6 {
			continue
		}
		box := trim[3]
		rest := strings.TrimSpace(trim[5:])
		if rest == "" {
			continue
		}
		done := box == 'x' || box == 'X'
		if i, ok := idx[rest]; ok {
			if done {
				out[i]["done"] = true
			}
			continue
		}
		idx[rest] = len(out)
		out = append(out, map[string]any{
			"text": rest,
			"done": done,
		})
	}
	return out
}

func (s *Server) handleChatHistory(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agentId")
	sessionID := r.URL.Query().Get("sessionId")
	ag := s.resolveAgent(r, agentID)
	if ag == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return
	}
	resp := map[string]any{"history": ag.WebChatHistory(sessionID)}
	// latestEventSeq is the resume cursor for /api/chat/subscribe — the
	// client opens that endpoint with `since=<latestEventSeq>` so a
	// fresh page load picks up only deltas it hasn't already rendered.
	// Best-effort: a missing/zero value just means "stream live only,
	// no replay", which is the right fallback when the session has no
	// in-flight turn or when session_events isn't backfilled.
	if s.dataStore != nil {
		uid := s.effectiveUserID(r)
		if uid != "" {
			if seq, err := s.dataStore.LatestSessionEventSeq(r.Context(), uid, ag.Name(), sessionID); err == nil {
				resp["latestEventSeq"] = seq
			}
		}
	}
	jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleChatSessions(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agentId")
	ag := s.resolveAgent(r, agentID)
	if ag == nil {
		jsonResponse(w, http.StatusOK, map[string]any{"sessions": []session.WebSession{}})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"sessions": ag.WebChatSessions()})
}

// handleChats returns chat sessions scoped by the caller's API key type:
//   - admin key: all sessions across all users and agents
//   - user key:  all sessions for agents owned by the key's user
//   - agent key: all sessions for agents in the key's ACL
//
// Session-based callers (browser) get the same scoping as user keys.
func (s *Server) handleChats(w http.ResponseWriter, r *http.Request) {
	if s.dataStore == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "no data store"})
		return
	}
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	// Pagination: ?page=1&pageSize=30 (1-based, defaults to page 1, 30 per page).
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	if pageSize < 1 || pageSize > 100 {
		pageSize = 30
	}

	// Resolve which agent IDs the caller may see.
	var agentIDs []string // nil = all (admin)
	switch {
	case ident.AuthMethod == "apikey" && ident.APIKeyType == users.APIKeyTypeAdmin:
		agentIDs = nil // admin sees everything
	case ident.AuthMethod == "apikey" && ident.APIKeyType == users.APIKeyTypeAgent:
		agentIDs = ident.APIKeyAgents
	default:
		uid := ident.EffectiveUserID()
		agents, agentsErr := s.dataStore.ListAgents(r.Context(), uid)
		if agentsErr != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": agentsErr.Error()})
			return
		}
		agentIDs = make([]string, len(agents))
		for i, a := range agents {
			agentIDs[i] = a.ID
		}
	}

	offset := (page - 1) * pageSize
	metas, total, err := s.dataStore.ListSessionsPaginated(r.Context(), agentIDs, offset, pageSize)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	ownerCache := map[string]*users.Account{}
	resolveOwner := func(uid string) *users.Account {
		if uid == "" {
			return nil
		}
		if a, ok := ownerCache[uid]; ok {
			return a
		}
		a, _ := s.accounts.Get(r.Context(), uid)
		ownerCache[uid] = a
		return a
	}
	agentCache := map[string]*store.AgentRecord{}
	resolveAgentRec := func(agentID string) *store.AgentRecord {
		if agentID == "" {
			return nil
		}
		if a, ok := agentCache[agentID]; ok {
			return a
		}
		a, _ := s.dataStore.GetAgent(r.Context(), agentID)
		agentCache[agentID] = a
		return a
	}

	out := make([]map[string]any, 0, len(metas))
	for _, m := range metas {
		ag := resolveAgentRec(m.AgentID)
		if ag == nil {
			continue
		}
		// Build preview from first user message.
		adapter := session.NewStoreAdapter(s.dataStore, m.UserID)
		ws := adapter.BuildWebSession(r.Context(), m)
		if ws == nil {
			continue
		}
		owner := resolveOwner(m.UserID)
		entry := map[string]any{
			"id":           ws.ID,
			"agentId":      m.AgentID,
			"agentName":    ag.Name,
			"userId":       m.UserID,
			"channel":      ws.Channel,
			"accountId":    ws.AccountID,
			"chatId":       ws.ChatID,
			"projectId":    ws.ProjectID,
			"title":        ws.Title,
			"preview":      ws.Preview,
			"thumbnailUrl": ws.ThumbnailURL,
			"createdAt":    ws.CreatedAt,
			"updatedAt":    ws.UpdatedAt,
		}
		if ws.ChatterUserID != "" {
			entry["chatterUserId"] = ws.ChatterUserID
			if chatter := resolveOwner(ws.ChatterUserID); chatter != nil {
				if chatter.ExternalID != "" {
					entry["chatterExternalId"] = chatter.ExternalID
				}
				if chatter.DisplayName != "" {
					entry["chatterDisplayName"] = chatter.DisplayName
				}
			}
		}
		if owner != nil {
			entry["ownerUsername"] = owner.Username
			entry["ownerEmail"] = owner.Email
			if owner.ExternalID != "" {
				entry["ownerExternalId"] = owner.ExternalID
			}
			if owner.DisplayName != "" {
				entry["ownerDisplayName"] = owner.DisplayName
			}
		}
		out = append(out, entry)
	}
	totalPages := (total + pageSize - 1) / pageSize
	jsonResponse(w, http.StatusOK, map[string]any{
		"sessions":   out,
		"page":       page,
		"pageSize":   pageSize,
		"total":      total,
		"totalPages": totalPages,
	})
}

func (s *Server) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	// Body-or-query for agentId — the frontend sends it in the JSON body
	// (see renameChatSession in web/src/lib/api.ts), matching the
	// handleMoveSessionProject convention. The earlier query-only path
	// always saw "" and bailed at resolveAgent with a silent 404, so
	// "Edit chat title" looked like a no-op even though the dialog
	// submitted cleanly.
	agentID := r.URL.Query().Get("agentId")
	var req struct {
		AgentID string `json:"agentId"`
		Title   string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if agentID == "" {
		agentID = req.AgentID
	}
	ag := s.resolveAgent(r, agentID)
	if ag == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return
	}
	if err := ag.RenameWebChatSession(r.PathValue("key"), req.Title); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agentId")
	ag := s.resolveAgent(r, agentID)
	if ag == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return
	}
	if err := ag.DeleteWebChatSession(r.PathValue("key")); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// handleMoveSessionProject reassigns one chat to a different project
// (or detaches it back to the loose-chat list when projectId is "").
// Backs the sidebar drag-and-drop affordance: dragging a chat row
// onto a project header / out of one fires this endpoint.
//
// Request body: { "agentId": "...", "projectId": "<pid>" | "" }
//
// Side effects beyond the sessions.project_id flip:
//   - Workspace files are moved between sessions/<sid>/ and
//     projects/<pid>/<sid>/ so the next turn sees its own artifacts
//     under the new scope. Empty source dir = no-op.
//   - Any active sandbox bound to this chat is released so the
//     replacement container starts with the new bind-mount path.
//
// Returns 409 with code="destination_exists" when the target dir
// already has files (defensive — session_keys are unique so this
// shouldn't happen organically, but better than silent merge).
func (s *Server) handleMoveSessionProject(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agentId")
	var req struct {
		AgentID   string `json:"agentId"`
		ProjectID string `json:"projectId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if agentID == "" {
		agentID = req.AgentID
	}
	if agentID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "agentId required"})
		return
	}
	// Owner-only — moving a chat changes its workspace path, which a
	// read-only viewer should never trigger.
	if rec := s.requireAgentOwner(w, r, agentID); rec == nil {
		return
	}
	if !s.requireWritable(w, r) {
		return
	}
	uid := s.effectiveUserID(r)
	// Validate the target project exists and belongs to this caller.
	// Empty projectId is the "detach" case — always allowed.
	if req.ProjectID != "" && s.dataStore != nil {
		p, err := s.dataStore.GetProject(r.Context(), uid, agentID, req.ProjectID)
		if err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if p == nil {
			jsonResponse(w, http.StatusNotFound, map[string]any{"error": "project not found"})
			return
		}
	}
	ag := s.resolveAgent(r, agentID)
	if ag == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return
	}
	if err := ag.MoveWebChatSession(r.Context(), r.PathValue("key"), req.ProjectID); err != nil {
		if errors.Is(err, workspace.ErrMoveDestinationExists) {
			jsonResponse(w, http.StatusConflict, map[string]any{
				"error": "destination workspace already exists",
				"code":  "destination_exists",
			})
			return
		}
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// handleFeishuWebhook receives Feishu / Feishu event POSTs. The route is
// public (Feishu doesn't auth via fastclaw bearer); per-event security
// is enforced inside the Feishu adapter by validating the payload's
// header.token against the verification token stored at connect time.
//
// Hands the raw body to the gateway (via type-asserted dispatcher
// hook) which finds the right adapter by accountID. The adapter
// returns an HTTP body + status — handler just relays it. URL
// verification challenges and real events both go through this same
// path; the adapter discriminates internally.
func (s *Server) handleFeishuWebhook(w http.ResponseWriter, r *http.Request) {
	appID := r.PathValue("appId")
	if appID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "appId required"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	type feishuDispatcher interface {
		DispatchFeishuWebhook(accountID string, body []byte) ([]byte, int, error)
	}
	d, ok := s.userResolver.(feishuDispatcher)
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "feishu webhook dispatch not available"})
		return
	}
	respBody, status, derr := d.DispatchFeishuWebhook(appID, body)
	if derr != nil {
		slog.Warn("feishu webhook dispatch error", "appId", appID, "status", status, "error", derr)
		if respBody == nil {
			respBody = []byte(`{"ok":false}`)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(respBody)
}

// handleLINEWebhook receives LINE Messaging API event POSTs. The route
// is public (LINE doesn't auth via fastclaw bearer); per-event security
// comes from the HMAC-SHA256 signature in `x-line-signature` which the
// adapter validates against channel_secret + the raw body.
//
// Reads the body once, hands the raw bytes + signature to the gateway
// dispatcher (re-encoding the JSON would change the bytes the HMAC was
// computed over and break verification).
func (s *Server) handleLINEWebhook(w http.ResponseWriter, r *http.Request) {
	accountID := r.PathValue("accountId")
	if accountID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "accountId required"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	signature := r.Header.Get("x-line-signature")
	type lineDispatcher interface {
		DispatchLINEWebhook(accountID string, body []byte, signature string) ([]byte, int, error)
	}
	d, ok := s.userResolver.(lineDispatcher)
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "line webhook dispatch not available"})
		return
	}
	respBody, status, derr := d.DispatchLINEWebhook(accountID, body, signature)
	if derr != nil {
		slog.Warn("line webhook dispatch error", "accountId", accountID, "status", status, "error", derr)
		if respBody == nil {
			respBody = []byte(`{"ok":false}`)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(respBody)
}

// --- Helpers ---

func jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func maskAPIKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if hours < 24 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	days := hours / 24
	hours = hours % 24
	return fmt.Sprintf("%dd %dh", days, hours)
}

func looksLikeSecret(name string) bool {
	upper := strings.ToUpper(name)
	for _, marker := range []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

func isMaskedSecret(s string) bool {
	if s == "" {
		return false
	}
	return s == "****" || strings.Contains(s, "****")
}

func maskSkillEntry(v config.SkillEntryCfg) config.SkillEntryCfg {
	out := config.SkillEntryCfg{Enabled: v.Enabled, APIKey: maskAPIKey(v.APIKey)}
	if len(v.Env) > 0 {
		out.Env = make(map[string]string, len(v.Env))
		for ek, ev := range v.Env {
			if looksLikeSecret(ek) {
				out.Env[ek] = maskAPIKey(ev)
			} else {
				out.Env[ek] = ev
			}
		}
	}
	return out
}

func mergeSkillEntry(existing, in config.SkillEntryCfg) config.SkillEntryCfg {
	out := config.SkillEntryCfg{Enabled: in.Enabled, APIKey: in.APIKey, Env: in.Env}
	if isMaskedSecret(out.APIKey) {
		out.APIKey = existing.APIKey
	}
	if out.Env != nil {
		for k, v := range out.Env {
			if isMaskedSecret(v) {
				out.Env[k] = existing.Env[k]
			}
		}
	}
	return out
}

func newRandID() (string, error) {
	var buf [10]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func generateRandomToken(length int) string {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "fastclaw-default-token"
	}
	return hex.EncodeToString(b)
}

// debugLog is used from various handlers for diagnostic events; kept as a
// thin wrapper so handler files don't import slog directly.
func debugLog(msg string, kv ...any) { slog.Debug(msg, kv...) }
