package setup

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/buildinfo"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// agentShareModelConfig reports whether the agent's owner has opted to
// share their model + provider configuration with chatters. Default
// true: when the key is absent from rec.Config, sharing is on. Owners
// explicitly opt OUT by writing `false`. Centralised here so the API
// layer, the runtime overlay gate (EnsureAgent), and the listProviders
// auth relaxation read the flag with one consistent default.
func agentShareModelConfig(rec *store.AgentRecord) bool {
	if rec == nil {
		return true
	}
	v, ok := rec.Config["shareModelConfig"].(bool)
	if !ok {
		return true
	}
	return v
}

// agentScopeModel reads the per-agent model override from the configs
// table — the kind=setting, scope=agent row that supersedes the
// system/user defaults when set.
func (s *Server) agentScopeModel(r *http.Request, agentID string) string {
	rec, err := s.dataStore.GetConfigByName(r.Context(), store.KindSetting, "", agentID, "agents.defaults")
	if err != nil || rec == nil {
		return ""
	}
	if v, ok := rec.Data["model"].(string); ok {
		return v
	}
	return ""
}

// saveAgentScopeModel upserts (model="") or deletes (model=="") the
// agent-scope agents.defaults row.
func (s *Server) saveAgentScopeModel(r *http.Request, agentID, model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return scope.SaveSettingByScope(r.Context(), s.dataStore, scope.Agent, agentID, "agents.defaults", nil)
	}
	return scope.SaveSettingByScope(r.Context(), s.dataStore, scope.Agent, agentID, "agents.defaults", map[string]interface{}{"model": model})
}

// agentScopeDefaultsRead returns the current agent-scope agents.defaults
// row data, or an empty map if the row doesn't exist yet. Callers use
// this as the base for merge-aware patches (read-modify-write) so a
// single PATCH that touches one field doesn't clobber the rest.
func (s *Server) agentScopeDefaultsRead(r *http.Request, agentID string) map[string]interface{} {
	rec, err := s.dataStore.GetConfigByName(r.Context(), store.KindSetting, "", agentID, "agents.defaults")
	if err != nil || rec == nil || rec.Data == nil {
		return map[string]interface{}{}
	}
	// Copy so callers mutating the result don't accidentally write
	// back through the cached store object.
	out := make(map[string]interface{}, len(rec.Data))
	for k, v := range rec.Data {
		out[k] = v
	}
	return out
}

// applyAgentScopeDefaultsPatch merges patch into the current
// agents.defaults row and writes the result. Keys whose value is nil are
// DELETED from the row (the caller's signal for "clear this override").
// A row that ends up empty is removed entirely so MergedAgentConfig
// falls all the way back to system/user defaults.
func (s *Server) applyAgentScopeDefaultsPatch(r *http.Request, agentID string, patch map[string]interface{}) error {
	if len(patch) == 0 {
		return nil
	}
	data := s.agentScopeDefaultsRead(r, agentID)
	for k, v := range patch {
		if v == nil {
			delete(data, k)
			continue
		}
		data[k] = v
	}
	if len(data) == 0 {
		return scope.SaveSettingByScope(r.Context(), s.dataStore, scope.Agent, agentID, "agents.defaults", nil)
	}
	return scope.SaveSettingByScope(r.Context(), s.dataStore, scope.Agent, agentID, "agents.defaults", data)
}

// applyAgentScopePluginsPatch merges per-agent plugin enable
// overrides into the (scope=agent, name=plugins.enabled) row.
//
// patch keys whose value is true/false are written; the rest of the
// row is preserved (so a UI toggle for one plugin doesn't clobber
// overrides for sibling plugins). When reset is true, the entire row
// is dropped — agent falls back to system-wide plugin enable state.
func (s *Server) applyAgentScopePluginsPatch(r *http.Request, agentID string, patch map[string]bool, reset bool) error {
	if reset {
		return scope.SaveSettingByScope(r.Context(), s.dataStore, scope.Agent, agentID, "plugins.enabled", nil)
	}
	if len(patch) == 0 {
		return nil
	}
	data := map[string]interface{}{}
	if rec, err := s.dataStore.GetConfigByName(r.Context(), store.KindSetting, "", agentID, "plugins.enabled"); err == nil && rec != nil {
		for k, v := range rec.Data {
			data[k] = v
		}
	}
	for k, v := range patch {
		data[k] = v
	}
	return scope.SaveSettingByScope(r.Context(), s.dataStore, scope.Agent, agentID, "plugins.enabled", data)
}

// agentScopeSplitReplies reads the per-agent multi-bubble override.
// Returns nil when absent — nil is treated as false by every runtime
// consumer, so the distinction only matters for the GET response (the
// dashboard could choose to render "unset" differently from "off", but
// today the Switch renders both as off and that's fine).
func (s *Server) agentScopeSplitReplies(r *http.Request, agentID string) *bool {
	rec, err := s.dataStore.GetConfigByName(r.Context(), store.KindSetting, "", agentID, "agents.defaults")
	if err != nil || rec == nil {
		return nil
	}
	v, ok := rec.Data["splitReplies"].(bool)
	if !ok {
		return nil
	}
	return &v
}

// agentScopePromptMode reads the per-agent promptMode override.
func (s *Server) agentScopePromptMode(r *http.Request, agentID string) string {
	rec, err := s.dataStore.GetConfigByName(r.Context(), store.KindSetting, "", agentID, "agents.defaults")
	if err != nil || rec == nil {
		return ""
	}
	if v, ok := rec.Data["promptMode"].(string); ok {
		return v
	}
	return ""
}

// agentScopePlugins reads the per-agent plugin enable overlay. Returns
// nil when no row exists. Keyed pluginID → bool; missing keys fall
// through to the system-wide plugin entry's enabled state.
func (s *Server) agentScopePlugins(r *http.Request, agentID string) map[string]bool {
	rec, err := s.dataStore.GetConfigByName(r.Context(), store.KindSetting, "", agentID, "plugins.enabled")
	if err != nil || rec == nil {
		return nil
	}
	out := make(map[string]bool, len(rec.Data))
	for k, v := range rec.Data {
		if b, ok := v.(bool); ok {
			out[k] = b
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// agentScopeAutoPersist reads the per-agent autoPersist override.
// Returns nil when absent — same convention as agentScopeSplitReplies.
// Drives the runPostTurn AutoPersistMemory pass (LLM-distilled writes to
// USER.md / MEMORY.md) which is the only chatter-memory persistence
// path in chatbot mode.
func (s *Server) agentScopeAutoPersist(r *http.Request, agentID string) *bool {
	rec, err := s.dataStore.GetConfigByName(r.Context(), store.KindSetting, "", agentID, "agents.defaults")
	if err != nil || rec == nil {
		return nil
	}
	v, ok := rec.Data["autoPersist"].(bool)
	if !ok {
		return nil
	}
	return &v
}

// agentScopeSharedIdentity returns true when ANY channel bound to this
// agent has shared_identity enabled. The toggle is conceptually agent-
// level (Context page) but physically stored per-channel so the gateway
// routing hot-path can read it without an extra DB lookup.
func (s *Server) agentScopeSharedIdentity(r *http.Request, ownerUserID, agentID string) bool {
	chs, err := s.dataStore.ListChannels(r.Context(), ownerUserID, agentID)
	if err != nil {
		return false
	}
	for _, ch := range chs {
		if ch.SharedIdentity {
			return true
		}
	}
	return false
}

// effectiveUserID returns the resolved user_id for the request: the
// caller's own id, or — for super_admin in actAs mode — the impersonated
// user's id.
func (s *Server) effectiveUserID(r *http.Request) string {
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		return ""
	}
	return ident.EffectiveUserID()
}

// requireWritable returns true if the caller may mutate, writing a 4xx
// response and false otherwise.
func (s *Server) requireWritable(w http.ResponseWriter, r *http.Request) bool {
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unauthorized"})
		return false
	}
	if ident.ReadOnly() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "read-only"})
		return false
	}
	return true
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	uid := s.effectiveUserID(r)
	if uid == "" {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	// ?all=true is the cross-tenant view (replaces /api/admin/agents).
	// Admin-only — for the platform-wide "Agents" admin page that
	// joins owner usernames in.
	if r.URL.Query().Get("all") == "true" {
		ident, _ := auth.FromContext(r.Context())
		if !ident.CanAdminPlatform() {
			jsonResponse(w, http.StatusForbidden, map[string]any{"error": "all=true requires admin"})
			return
		}
		s.respondAllAgents(w, r)
		return
	}
	owned, err := s.dataStore.ListAgents(r.Context(), uid)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(owned))
	for _, ar := range owned {
		desc, _ := ar.Config["description"].(string)
		out = append(out, map[string]any{
			"id":          ar.ID,
			"name":        ar.Name,
			"description": desc,
			"model":       s.agentScopeModel(r, ar.ID),
			"avatarUrl":   "/api/agents/" + ar.ID + "/files/avatar.png",
			"createdAt":   ar.CreatedAt,
			"userId":      ar.UserID,
			"role":        "owner",
			"isPublic":    ar.IsPublic,
		})
	}
	jsonResponse(w, http.StatusOK, map[string]any{"agents": out})
}

type createAgentRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model,omitempty"`
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	ident, _ := auth.FromContext(r.Context())
	if !ident.CanCreateAgent() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"error": "type=agent api keys cannot create agents"})
		return
	}
	uid := s.effectiveUserID(r)
	var req createAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if req.Name == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "name required"})
		return
	}
	// Enforce per-user agent quota. -1 = unlimited (default), 0 = no
	// self-creation (single-tenant customers — admin provisions for
	// them via POST /api/users/{id}/agents under admin caller),
	// N>0 = max N owned at once. Admin path bypasses this check.
	if u, err := s.dataStore.GetUser(r.Context(), uid); err == nil && u != nil && u.AgentQuota >= 0 {
		owned, err := s.dataStore.ListAgents(r.Context(), uid)
		if err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if int64(len(owned)) >= u.AgentQuota {
			jsonResponse(w, http.StatusForbidden, map[string]any{
				"error": fmt.Sprintf("agent quota reached (%d) — contact your admin to provision more", u.AgentQuota),
			})
			return
		}
	}
	id, err := generateID("agt_")
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	rec := &store.AgentRecord{
		ID:     id,
		UserID: uid,
		Name:   req.Name,
	}
	if req.Description != "" {
		// Description lives in the agents.config JSON blob — keeps the
		// schema stable while still surfacing through GetAgentConfig and
		// the agents.config namespace settings overlay.
		rec.Config = map[string]interface{}{"description": req.Description}
	}
	if err := s.dataStore.SaveAgent(r.Context(), rec); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if req.Model != "" {
		if err := s.saveAgentScopeModel(r, id, req.Model); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	}
	s.invalidateUser(uid)
	jsonResponse(w, http.StatusCreated, map[string]any{
		"agent": map[string]any{
			"id":     rec.ID,
			"userId": rec.UserID,
			"name":   rec.Name,
			"model":  req.Model,
			"config": rec.Config,
		},
	})
}

// requireUserOrAdmin gates the /api/users/{id}/* nested routes:
//   - any caller may operate on themselves (pathUserID == ident.UserID)
//   - super_admin / type=admin apikey may operate on any user
//
// Returns true on success; on failure writes a 401/403 and returns false.
// Callers should still validate that the path user actually exists when
// the operation depends on it.
func (s *Server) requireUserOrAdmin(w http.ResponseWriter, r *http.Request, pathUserID string) bool {
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return false
	}
	if pathUserID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "user id required"})
		return false
	}
	if pathUserID == ident.EffectiveUserID() {
		return true
	}
	if ident.CanAdminPlatform() {
		return true
	}
	jsonResponse(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
	return false
}

// requireAgentOwner returns the agent record if the caller owns it (or is
// super_admin), otherwise writes a 403/404 and returns nil.
func (s *Server) requireAgentOwner(w http.ResponseWriter, r *http.Request, agentID string) *store.AgentRecord {
	uid := s.effectiveUserID(r)
	rec, err := s.dataStore.GetAgent(r.Context(), agentID)
	if err != nil || rec == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return nil
	}
	ident, _ := auth.FromContext(r.Context())
	if rec.UserID != uid && ident.Role != users.RoleSuperAdmin {
		jsonResponse(w, http.StatusForbidden, map[string]any{"error": "not your agent"})
		return nil
	}
	return rec
}

// agentReadable reports whether the current request may read an agent.
// A super_admin session is deliberately NOT a blanket bypass: opening a
// private agent's normal chat URL must obey the same privacy boundary as
// every other user. Admin audit remains available through ?actAs=, which is
// read-only and explicitly identifies the user whose session is inspected.
// Admin API keys retain their platform ACL semantics.
func (s *Server) agentReadable(r *http.Request, rec *store.AgentRecord) bool {
	if rec == nil {
		return false
	}
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		return false
	}
	uid := ident.EffectiveUserID()
	if rec.UserID == uid {
		return true
	}
	if ident.AuthMethod == "apikey" && ident.CanAccessAgent(rec.ID) {
		return true
	}
	if ident.IsActingAs() {
		return true
	}
	return rec.IsPublic && uid != ""
}

// callerOwnsAgent returns true when the caller is the agent's owner, a
// super_admin, or an apikey explicitly scoped to the agent. Unlike
// requireAgentReadable this does NOT grant public-agent readers — used
// by file-scope code that needs to distinguish "browse everything"
// (owner) from "scope to your own session" (foreign caller on a public
// agent). Failures are silent: caller decides how to respond.
func (s *Server) callerOwnsAgent(r *http.Request, agentID string) bool {
	rec, err := s.dataStore.GetAgent(r.Context(), agentID)
	if err != nil || rec == nil {
		return false
	}
	uid := s.effectiveUserID(r)
	ident, _ := auth.FromContext(r.Context())
	if rec.UserID == uid || ident.Role == users.RoleSuperAdmin {
		return true
	}
	if ident.AuthMethod == "apikey" && ident.CanAccessAgent(agentID) {
		return true
	}
	return false
}

// requireAgentReadable allows owners, explicit apikey grantees, read-only
// admin audit requests, and authenticated users of public agents. Public
// agents are link-shared: each chatter still gets an isolated user scope.
func (s *Server) requireAgentReadable(w http.ResponseWriter, r *http.Request, agentID string) bool {
	rec, err := s.dataStore.GetAgent(r.Context(), agentID)
	if err != nil || rec == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return false
	}
	if s.agentReadable(r, rec) {
		return true
	}
	jsonResponse(w, http.StatusForbidden, map[string]any{"error": "not your agent"})
	return false
}

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	rec := s.requireAgentOwner(w, r, id)
	if rec == nil {
		return
	}
	var req struct {
		Name             string  `json:"name,omitempty"`
		Description      *string `json:"description,omitempty"` // ptr so empty-string clears it
		Model            *string `json:"model,omitempty"`       // ptr so empty-string clears the agent-scope override
		IsPublic         *bool   `json:"isPublic,omitempty"`    // ptr so caller can leave it unchanged
		ShareModelConfig *bool   `json:"shareModelConfig,omitempty"`
		// PromptMode is a ptr so the caller can distinguish "leave
		// unchanged" (omitted / null) from "clear override" (empty
		// string). Allowed string values: "agent" | "chatbot" |
		// "customize" — empty falls back to system default ("agent").
		// PromptMode also drives the built-in tool surface; there is
		// no separate allowlist field by design (extend via plugins).
		PromptMode *string `json:"promptMode,omitempty"`
		// SplitReplies per-agent override: nil = leave unchanged,
		// non-nil pointer-to-bool = set explicit value (true/false).
		// Distinct from "clear" which is a separate signal — the
		// dashboard sends `splitRepliesReset: true` to delete
		// the override and fall back to system default.
		SplitReplies      *bool `json:"splitReplies,omitempty"`
		SplitRepliesReset bool  `json:"splitRepliesReset,omitempty"`
		// AutoPersist per-agent override — same semantics as SplitReplies.
		// `autoPersistReset:true` clears the override and falls back to
		// system default (currently effectively disabled).
		AutoPersist      *bool `json:"autoPersist,omitempty"`
		AutoPersistReset bool  `json:"autoPersistReset,omitempty"`
		// SharedIdentity toggles cross-channel session/memory sharing.
		// When true, all channels bound to this agent use the channel
		// owner's user_id as the chatter identity, so sessions and
		// memory are shared across web + IM channels. Default false.
		SharedIdentity *bool `json:"sharedIdentity,omitempty"`
		// Plugins per-agent enable overlay. Keys are plugin IDs, values
		// are bool. Patch semantics: only the keys present in this map
		// get written; other keys in the existing row are preserved.
		// To clear all overrides for this agent, send pluginsReset:true.
		Plugins      map[string]bool `json:"plugins,omitempty"`
		PluginsReset bool            `json:"pluginsReset,omitempty"`
		// MCPServers is a whole-map replace: omit to leave untouched,
		// send {} to clear, or send the full desired map to replace.
		MCPServers      map[string]config.MCPServerConfig `json:"mcpServers,omitempty"`
		MCPServersReset bool                              `json:"mcpServersReset,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if req.Name != "" {
		rec.Name = req.Name
	}
	if req.Description != nil {
		if rec.Config == nil {
			rec.Config = map[string]interface{}{}
		}
		if *req.Description == "" {
			delete(rec.Config, "description")
		} else {
			rec.Config["description"] = *req.Description
		}
	}
	if req.IsPublic != nil {
		rec.IsPublic = *req.IsPublic
	}
	// shareModelConfig controls whether a chatter using this agent
	// inherits the owner's model + provider configuration. Default
	// true: sharing is on unless the owner explicitly opts out.
	// Encoding: absent key = on (the new-agent default), explicit
	// `false` = opt-out. We never store `true` — storing absence for
	// the default keeps existing rows minimal and means a future
	// default flip needs only one place to change (agentShareModelConfig
	// above). Stored in the agent's config blob so we don't need a
	// schema migration; runtime reads it back in EnsureAgent to gate
	// the owner-fallback + agent-scope overlays.
	if req.ShareModelConfig != nil {
		if rec.Config == nil {
			rec.Config = map[string]interface{}{}
		}
		if *req.ShareModelConfig {
			delete(rec.Config, "shareModelConfig")
		} else {
			rec.Config["shareModelConfig"] = false
		}
	}
	// MCP servers: whole-map replace into the agent config blob.
	if req.MCPServersReset {
		if rec.Config != nil {
			delete(rec.Config, "mcpServers")
		}
	} else if req.MCPServers != nil {
		if rec.Config == nil {
			rec.Config = map[string]interface{}{}
		}
		if len(req.MCPServers) == 0 {
			delete(rec.Config, "mcpServers")
		} else {
			rec.Config["mcpServers"] = req.MCPServers
		}
	}
	if err := s.dataStore.SaveAgent(r.Context(), rec); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// Per-agent defaults live in one configs row (kind=setting, scope=agent,
	// namespace=agents.defaults). Collect every field the caller touched
	// into a single merge-aware patch so e.g. updating promptMode doesn't
	// clobber an existing model override and vice versa. nil pointer =
	// caller didn't touch the field; ptr-to-empty = "clear this override".
	defaultsPatch := map[string]interface{}{}
	if req.Model != nil {
		m := strings.TrimSpace(*req.Model)
		if m == "" {
			defaultsPatch["model"] = nil
		} else {
			defaultsPatch["model"] = m
		}
	}
	if req.PromptMode != nil {
		pm := strings.TrimSpace(*req.PromptMode)
		// Allow only the documented values plus empty (= clear).
		// Anything else is a 400 — silently coercing to "agent" would
		// mask typos from the dashboard or CLI.
		switch pm {
		case "":
			defaultsPatch["promptMode"] = nil
		case config.PromptModeAgent, config.PromptModeChatbot, config.PromptModeCustomize:
			defaultsPatch["promptMode"] = pm
		default:
			jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "promptMode must be one of: agent, chatbot, customize"})
			return
		}
	}
	if req.SplitRepliesReset {
		// Reset wins over set in the same request — the dashboard's
		// "Inherit" pill writes this flag.
		defaultsPatch["splitReplies"] = nil
	} else if req.SplitReplies != nil {
		defaultsPatch["splitReplies"] = *req.SplitReplies
	}
	if req.AutoPersistReset {
		defaultsPatch["autoPersist"] = nil
	} else if req.AutoPersist != nil {
		defaultsPatch["autoPersist"] = *req.AutoPersist
	}
	if err := s.applyAgentScopeDefaultsPatch(r, rec.ID, defaultsPatch); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// Plugins-enabled overlay: separate config row (scope=agent,
	// name=plugins.enabled), so doesn't go through the agents.defaults
	// patch path. Reset clears the row entirely; otherwise we merge
	// the incoming map keys into the existing data.
	if req.PluginsReset || req.Plugins != nil {
		if err := s.applyAgentScopePluginsPatch(r, rec.ID, req.Plugins, req.PluginsReset); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	}
	// SharedIdentity: batch-update all channels for this agent.
	if req.SharedIdentity != nil {
		chs, _ := s.dataStore.ListChannels(r.Context(), rec.UserID, rec.ID)
		for i := range chs {
			if chs[i].SharedIdentity != *req.SharedIdentity {
				chs[i].SharedIdentity = *req.SharedIdentity
				_ = s.dataStore.SaveChannel(r.Context(), &chs[i])
			}
		}
	}
	// invalidateAgent (not invalidateUser) so super_admin / public-link
	// viewers / apikey callers that lazy-attached this agent into their
	// own UserSpace also drop their stale rc.Model — without this they
	// keep firing the previous model until the 30-min idle eviction.
	s.invalidateAgent(rec.ID)
	share := agentShareModelConfig(rec)
	jsonResponse(w, http.StatusOK, map[string]any{
		"agent": map[string]any{
			"id":               rec.ID,
			"userId":           rec.UserID,
			"name":             rec.Name,
			"model":            s.agentScopeModel(r, rec.ID),
			"promptMode":       s.agentScopePromptMode(r, rec.ID),
			"splitReplies":     s.agentScopeSplitReplies(r, rec.ID),
			"autoPersist":      s.agentScopeAutoPersist(r, rec.ID),
			"sharedIdentity":   s.agentScopeSharedIdentity(r, rec.UserID, rec.ID),
			"plugins":          s.agentScopePlugins(r, rec.ID),
			"config":           rec.Config,
			"isPublic":         rec.IsPublic,
			"shareModelConfig": share,
		},
	})
}

// handleGetAgent returns the basic AgentRecord (id, name, description,
// userId) for one agent. Used by the chat header / sidebar switcher to
// resolve a display name. Permission is read-level — owner, public-link
// viewer, explicit API-key grantee, or super_admin using ?actAs= audit.
func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	rec, err := s.dataStore.GetAgent(r.Context(), id)
	if err != nil || rec == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	desc, _ := rec.Config["description"].(string)
	share := agentShareModelConfig(rec)
	uid := s.effectiveUserID(r)
	role := "owner"
	ident, _ := auth.FromContext(r.Context())
	if rec.UserID != uid || ident.IsActingAs() {
		role = "viewer"
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"agent": map[string]any{
			"id":               rec.ID,
			"name":             rec.Name,
			"description":      desc,
			"userId":           rec.UserID,
			"role":             role,
			"model":            s.agentScopeModel(r, rec.ID),
			"promptMode":       s.agentScopePromptMode(r, rec.ID),
			"splitReplies":     s.agentScopeSplitReplies(r, rec.ID),
			"autoPersist":      s.agentScopeAutoPersist(r, rec.ID),
			"sharedIdentity":   s.agentScopeSharedIdentity(r, rec.UserID, rec.ID),
			"plugins":          s.agentScopePlugins(r, rec.ID),
			"avatarUrl":        "/api/agents/" + rec.ID + "/files/avatar.png",
			"createdAt":        rec.CreatedAt,
			"isPublic":         rec.IsPublic,
			"shareModelConfig": share,
		},
	})
}

func (s *Server) handleGetAgentConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec := s.requireAgentOwner(w, r, id)
	if rec == nil {
		return
	}
	cfg := config.AgentFileConfig{}
	if len(rec.Config) > 0 {
		blob, _ := json.Marshal(rec.Config)
		_ = json.Unmarshal(blob, &cfg)
	}
	jsonResponse(w, http.StatusOK, cfg)
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	rec := s.requireAgentOwner(w, r, id)
	if rec == nil {
		return
	}
	if err := s.dataStore.DeleteAgent(r.Context(), id); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// Drop the agent from every cached UserSpace, not just the owner's,
	// so foreign callers stop resolving the now-deleted agent through
	// EnsureAgent's lazy-attach path.
	s.invalidateAgent(rec.ID)
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// Agent identity / memory files — all live in agent_files, agent-scoped.
// Two classes:
//
//   - identity files (agentIdentityFiles below) are the canonical "shared
//     template" for the agent. They live under a single row keyed by the
//     agent owner's user_id — so admin provisioning, the owner's edits,
//     and the agent's own BOOTSTRAP-flow write_file calls all converge on
//     the same row. Mirrors handlers_admin.forkAgentFiles and
//     internal/agent/tools.identityFiles; keep these three lists in sync.
//
//   - per-user files (USER.md, MEMORY.md) are state that genuinely
//     differs per chatter. They're keyed by the caller's effective
//     user_id; a non-owner caller can author their own override and the
//     read path falls back to the owner's row when none exists.
//
// Filename allowlist gates which files this endpoint can touch at all;
// agent-runtime tool calls go through the workspace store instead.
var agentSystemFileAllowlist = map[string]bool{
	"SOUL.md": true, "IDENTITY.md": true, "AGENTS.md": true,
	"BOOTSTRAP.md": true, "TOOLS.md": true, "MEMORY.md": true,
	"HEARTBEAT.md": true, "USER.md": true, "KNOWLEDGE.md": true, "agent.json": true,
}

var agentIdentityFiles = map[string]bool{
	"SOUL.md": true, "IDENTITY.md": true, "AGENTS.md": true,
	"BOOTSTRAP.md": true, "TOOLS.md": true, "HEARTBEAT.md": true,
	"KNOWLEDGE.md": true, "agent.json": true,
}

func (s *Server) handleGetAgentSystemFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := r.PathValue("name")
	if !agentSystemFileAllowlist[name] {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "filename not allowed"})
		return
	}
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	rec, err := s.dataStore.GetAgent(r.Context(), id)
	if err != nil || rec == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	caller := s.effectiveUserID(r)

	// Identity files: read the owner's row directly — that's the single
	// source of truth, regardless of who's asking.
	if agentIdentityFiles[name] {
		data, err := s.dataStore.GetAgentFileExact(r.Context(), id, rec.UserID, name)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				jsonResponse(w, http.StatusOK, map[string]any{"content": "", "source": "default"})
				return
			}
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		jsonResponse(w, http.StatusOK, map[string]any{"content": string(data), "source": "owner"})
		return
	}

	// Per-user files: prefer caller's own row, fall back to the owner's.
	// `source: "db"` means the caller has authored an override; "owner"
	// means we're showing the agent owner's row by fallback. The
	// frontend uses this to decide whether to show the "Edited" badge
	// and enable the Revert action.
	if data, err := s.dataStore.GetAgentFileExact(r.Context(), id, caller, name); err == nil {
		baseContent := ""
		if rec.UserID != caller {
			if base, err2 := s.dataStore.GetAgentFileExact(r.Context(), id, rec.UserID, name); err2 == nil {
				baseContent = string(base)
			}
		}
		resp := map[string]any{"content": string(data), "source": "db"}
		if baseContent != "" {
			resp["baseContent"] = baseContent
		}
		jsonResponse(w, http.StatusOK, resp)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if rec.UserID != caller {
		if data, err := s.dataStore.GetAgentFileExact(r.Context(), id, rec.UserID, name); err == nil {
			jsonResponse(w, http.StatusOK, map[string]any{"content": string(data), "source": "owner"})
			return
		} else if !errors.Is(err, store.ErrNotFound) {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"content": "", "source": "default"})
}

func (s *Server) handlePutAgentSystemFile(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	name := r.PathValue("name")
	if !agentSystemFileAllowlist[name] {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "filename not allowed"})
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if name == "KNOWLEDGE.md" && len([]byte(body.Content)) > maxKnowledgeFileBytes {
		jsonResponse(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": "KNOWLEDGE.md is too large; maximum size is 256KB",
		})
		return
	}
	target, ok := s.resolveSystemFileTarget(w, r, id, name)
	if !ok {
		return
	}
	if err := s.dataStore.SaveAgentFile(r.Context(), id, target, name, []byte(body.Content)); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.invalidateUser(target)
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteAgentSystemFile(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	name := r.PathValue("name")
	if !agentSystemFileAllowlist[name] {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "filename not allowed"})
		return
	}
	target, ok := s.resolveSystemFileTarget(w, r, id, name)
	if !ok {
		return
	}
	if err := s.dataStore.DeleteAgentFile(r.Context(), id, target, name); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.invalidateUser(target)
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// resolveSystemFileTarget figures out which user_id row a write/delete
// on (agentID, filename) should hit, and gates access:
//
//   - Identity files (SOUL/IDENTITY/AGENTS/BOOTSTRAP/TOOLS/HEARTBEAT/
//     agent.json) always target the agent owner's row — this is the
//     canonical "shared template". Caller must be the owner or hold
//     platform admin (super_admin session, or type=admin apikey).
//   - Per-user files (USER.md, MEMORY.md) target the caller's own row
//     so each chatter has an independent override. Caller just needs
//     read access to the agent.
//
// Writes 4xx and returns ok=false on permission/lookup failures.
func (s *Server) resolveSystemFileTarget(w http.ResponseWriter, r *http.Request, agentID, name string) (string, bool) {
	rec, err := s.dataStore.GetAgent(r.Context(), agentID)
	if err != nil || rec == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return "", false
	}
	caller := s.effectiveUserID(r)
	ident, _ := auth.FromContext(r.Context())
	if agentIdentityFiles[name] {
		if rec.UserID != caller && !ident.CanAdminPlatform() {
			jsonResponse(w, http.StatusForbidden, map[string]any{"error": "not your agent"})
			return "", false
		}
		return rec.UserID, true
	}
	if !s.requireAgentReadable(w, r, agentID) {
		return "", false
	}
	return caller, true
}

// Workspace files — list / get / upload of agent-produced artifacts.
// Backed by the workspace.Store blob backend, whose layout is
//
//   workspaces/<agent_id>/<session_id>/<path>
//
// The HTTP file endpoints below operate at the agent-root level
// (sessionID="") — that's where uploads land and where ListByAgent
// returns objects across every session of that agent. The agent runtime
// passes its own sessionID for in-chat tool calls; those land under the
// session sub-prefix automatically.

// workspaceSessionScope translates the URL `?sessionId=` token into
// the directory name used under workspaces/<agent>/sessions/. The URL
// token is the session_key (so the dashboard can address any session
// uniformly), but workspace artifacts are namespaced by chat_id
// instead — that's what the agent runtime passed at write time.
//
// Returns the chat_id when the session_key resolves under the caller's
// (user_id, agent_id). Returns "" when the lookup fails — including
// the case where the session belongs to a DIFFERENT user — so callers
// don't accidentally widen scope into another user's files. Pre-fix
// behavior was to fall back to the raw URL token; on a public agent
// that let a non-owner caller pass a known chat_id of the owner and
// read its files because the resulting scope was sessions/<their chat>/.
func (s *Server) workspaceSessionScope(ctx context.Context, agentID, urlToken string) string {
	tok := strings.TrimSpace(urlToken)
	if tok == "" || s.dataStore == nil {
		return ""
	}
	uid := config.UserIDFromContext(ctx)
	if uid == "" {
		return ""
	}
	_, _, chatID, err := s.dataStore.LookupSessionTriple(ctx, uid, agentID, tok)
	if err != nil || chatID == "" {
		return ""
	}
	return chatID
}

func (s *Server) handleAgentFileList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.workspaceStore == nil {
		jsonResponse(w, http.StatusOK, map[string]any{"files": []any{}})
		return
	}
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	// Always List with project + session both empty so returned paths
	// stay agent-relative (e.g. "sessions/<sid>/foo.png" or
	// "projects/<pid>/notes.md") — the download endpoint expects that
	// shape, and filtering here is cheaper than two divergent code paths.
	objects, err := s.workspaceStore.List(r.Context(), id, "", "")
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	scope := s.fileScopeForRequest(r, id)
	files := make([]map[string]any, 0, len(objects))
	for _, o := range objects {
		if !scope.acceptPath(o.Path) {
			continue
		}
		files = append(files, map[string]any{
			"path":    o.Path,
			"size":    o.Size,
			"modTime": o.ModTime.Unix(),
		})
	}
	jsonResponse(w, http.StatusOK, map[string]any{"files": files})
}

// fileScope describes which agent-relative paths to surface for the
// file browser / zip filter. acceptPath returns true for paths the
// scope considers in-bounds:
//
//	loose chat:  paths under sessions/<chat_id>/
//	project chat: paths under projects/<pid>/<chat_id>/ (the chat's
//	              own files), PLUS files directly at projects/<pid>/
//	              (project-root "shared/legacy" files — pre-subdir
//	              layout still lives there, and operators may
//	              deliberately drop shared files at the root). Other
//	              chats' subdirs (projects/<pid>/<other-sid>/...)
//	              are excluded — those belong to that chat's panel.
//	no session:  everything (admin browser).
//
// archiveSuffix returns the human-readable scope id used in the zip
// filename — chat_id for loose chats, "<pid>-<chat_id>" for project
// chats so a download names "agent-pid-sid.zip" instead of
// disambiguating by chat_id alone.
type fileScope struct {
	acceptPath    func(string) bool
	archiveSuffix string
}

// stripScopePrefix removes the deepest known scope prefix from an
// agent-relative path so zip entries read as plain filenames. Order
// matters: project chats are tried before session chats so a
// `projects/<pid>/<sid>/foo.md` collapses to `foo.md` rather than
// `<pid>/<sid>/foo.md`. Top-level project files keep the leading
// `projects/<pid>/` strip so they read as bare filenames too.
func stripScopePrefix(p string) string {
	for _, top := range []string{"projects/", "sessions/"} {
		if !strings.HasPrefix(p, top) {
			continue
		}
		rest := p[len(top):]
		// Cut after the scope id (one path segment).
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			rest = rest[i+1:]
			// Project paths can have a second id segment for the
			// per-chat subdir; collapse that too when present.
			if top == "projects/" {
				if j := strings.IndexByte(rest, '/'); j >= 0 {
					// Only treat the first segment as a chat id when it
					// looks like one (s-... prefix). Otherwise keep
					// rest as-is so legacy "subdir/file.md" structures
					// don't get over-stripped.
					if first := rest[:j]; strings.HasPrefix(first, "s-") {
						rest = rest[j+1:]
					}
				}
			}
			return rest
		}
		return ""
	}
	return p
}

// rejectAllScope returns a fileScope that lets nothing through. Used
// when the caller asked for a sessionId we can't resolve for them, so
// a non-owner can't widen into another user's files on a public agent
// just by guessing/leaking a chat_id.
func rejectAllScope() fileScope {
	return fileScope{acceptPath: func(string) bool { return false }}
}

func (s *Server) fileScopeForRequest(r *http.Request, agentID string) fileScope {
	rawSession := r.URL.Query().Get("sessionId")
	rawProject := r.URL.Query().Get("projectId")
	// Project landing page: no specific chat is open, so the panel
	// shows everything under projects/<pid>/ — every chat's subtree
	// plus root-level shared files. The sessionId branch below is
	// the per-chat view; use this branch when the URL is
	// /agents/<aid>/project/<pid> with no chat selected.
	if rawSession == "" && rawProject != "" {
		prefix := "projects/" + rawProject + "/"
		return fileScope{
			acceptPath:    func(p string) bool { return strings.HasPrefix(p, prefix) },
			archiveSuffix: rawProject,
		}
	}
	if rawSession == "" {
		// Agent-wide view (no scope params at all). Owner / super_admin
		// can legitimately browse every file; non-owners (public-agent
		// viewers, foreign apikey callers) must specify a session they
		// own, otherwise we'd hand them other users' files.
		if s.callerOwnsAgent(r, agentID) {
			return fileScope{acceptPath: func(string) bool { return true }}
		}
		return rejectAllScope()
	}
	chatID := s.workspaceSessionScope(r.Context(), agentID, rawSession)
	if chatID == "" {
		// sessionId didn't resolve to a chat THIS caller owns — either
		// it doesn't exist or it belongs to another user. Either way,
		// surface nothing. Pre-fix behavior was to widen back to
		// "accept all", which on a public agent meant non-owners could
		// list every chat's files by passing a junk sessionId.
		return rejectAllScope()
	}
	if pid := s.resolveSessionProject(r.Context(), r, agentID, rawSession); pid != "" {
		ownPrefix := "projects/" + pid + "/" + chatID + "/"
		rootPrefix := "projects/" + pid + "/"
		return fileScope{
			acceptPath: func(p string) bool {
				if strings.HasPrefix(p, ownPrefix) {
					return true
				}
				// Top-level file at projects/<pid>/<file> (no further
				// "/" — i.e. not in any sid subdir).
				if strings.HasPrefix(p, rootPrefix) {
					rest := p[len(rootPrefix):]
					return rest != "" && !strings.Contains(rest, "/")
				}
				return false
			},
			archiveSuffix: pid + "-" + chatID,
		}
	}
	prefix := "sessions/" + chatID + "/"
	return fileScope{
		acceptPath:    func(p string) bool { return strings.HasPrefix(p, prefix) },
		archiveSuffix: chatID,
	}
}

// handleAgentFilesZip streams a zip of every workspace file for the agent
// (or just one session when ?sessionId= is set). Files are added with
// their session-relative path so the archive layout matches what the user
// sees in the chat panel — no enclosing wrapper directory.
func (s *Server) handleAgentFilesZip(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.workspaceStore == nil {
		http.Error(w, "no workspace store", http.StatusServiceUnavailable)
		return
	}
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	objects, err := s.workspaceStore.List(r.Context(), id, "", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	scope := s.fileScopeForRequest(r, id)
	archiveName := fmt.Sprintf("%s.zip", id)
	if scope.archiveSuffix != "" {
		archiveName = fmt.Sprintf("%s-%s.zip", id, scope.archiveSuffix)
	}
	// Wrap entries in a folder named after the archive so extractors
	// (macOS Archive Utility, Windows Explorer, 7zip…) place every
	// file inside one directory instead of dumping them loose next
	// to the zip. Without this, "5 files extracted" looks like
	// "files went missing" because they fan out into Downloads/.
	wrapper := strings.TrimSuffix(archiveName, ".zip") + "/"

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, archiveName))

	zw := zip.NewWriter(w)
	written, skipped, failed := 0, 0, 0
	for _, o := range objects {
		if !scope.acceptPath(o.Path) {
			skipped++
			continue
		}
		// Strip the deepest scope prefix from the archive entry name
		// so the user sees clean filenames in the zip rather than
		// nested `projects/<pid>/<sid>/foo.md` paths.
		entryName := stripScopePrefix(o.Path)
		if entryName == "" {
			skipped++
			continue
		}
		hdr := &zip.FileHeader{
			Name:     wrapper + entryName,
			Method:   zip.Deflate,
			Modified: o.ModTime,
		}
		entry, err := zw.CreateHeader(hdr)
		if err != nil {
			// Continue, not return — finalizing the archive with the
			// rest of the entries is more useful than bailing out and
			// leaving the user with a single file. Pre-fix behavior:
			// any transient hiccup partway through truncated the zip
			// to whatever was already written, surfacing in prod as
			// "only one image came out".
			slog.Warn("zip: create entry failed", "agent", id, "path", o.Path, "err", err)
			failed++
			continue
		}
		rc, err := s.workspaceStore.Get(r.Context(), id, "", "", o.Path)
		if err != nil {
			slog.Warn("zip: open object failed", "agent", id, "path", o.Path, "err", err)
			failed++
			continue
		}
		_, copyErr := io.Copy(entry, rc)
		rc.Close()
		if copyErr != nil {
			slog.Warn("zip: copy failed", "agent", id, "path", o.Path, "err", copyErr)
			failed++
			continue
		}
		written++
	}
	if err := zw.Close(); err != nil {
		slog.Warn("zip: writer close failed", "agent", id, "err", err)
	}
	slog.Info("zip: archive sent", "agent", id, "archive", archiveName,
		"objects", len(objects), "written", written, "skipped", skipped, "failed", failed)
}

// handleAgentWorkspaceReveal opens the chatter's workspace folder in
// the operator's native file browser (Finder/Explorer/xdg-open).
// Self-hosted only — hosted deployments don't have a meaningful
// concept of "the operator's local filesystem" and the chatter
// doesn't own the daemon, so exposing this would be a privilege
// leak. Reads sessionId / projectId from the query string, mirrors
// fileScopeForRequest's resolution (session_key → chat_id, project
// lookup) so the revealed dir matches what the chat-side Workspace
// panel is showing.
//
// Best-effort: returns 200 with the resolved path on success, 4xx
// on bad scope, 503 when the configured workspace store doesn't
// expose a host path (S3 / R2 deploys), 500 if the OS open command
// fails. Non-blocking — we don't wait for Finder to actually
// surface the window.
func (s *Server) handleAgentWorkspaceReveal(w http.ResponseWriter, r *http.Request) {
	if buildinfo.IsHostedDeploy() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"error": "workspace reveal is disabled on hosted deployments"})
		return
	}
	id := r.PathValue("id")
	if s.workspaceStore == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "no workspace store configured"})
		return
	}
	if !s.requireAgentReadable(w, r, id) {
		return
	}

	scoper, ok := s.workspaceStore.(workspace.LocalScoper)
	if !ok {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "workspace store has no local path (e.g. S3-backed) — open in Finder is unavailable"})
		return
	}

	rawSession := r.URL.Query().Get("sessionId")
	rawProject := r.URL.Query().Get("projectId")

	// Resolve to the same (project, chatID) the chat-side panel is
	// scoped to. Empty rawSession + non-empty projectId means project
	// landing — reveal the project root. Empty both means agent root
	// (admin browser); we still allow it because requireAgentReadable
	// has already gated access.
	chatID := ""
	projectID := rawProject
	if rawSession != "" {
		chatID = s.workspaceSessionScope(r.Context(), id, rawSession)
		if pid := s.resolveSessionProject(r.Context(), r, id, rawSession); pid != "" {
			projectID = pid
		}
	}

	dir, ok := scoper.LocalScopeDir(id, projectID, chatID)
	if !ok || dir == "" {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "workspace store did not return a host path"})
		return
	}

	// Pre-create the dir so `open <missing-path>` doesn't error out
	// on a brand-new chat that hasn't written any files yet — empty
	// folder still feels like progress to the user.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	if err := openInFileBrowser(dir); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "path": dir})
}

// openInFileBrowser shells out to the platform-appropriate "open"
// command. macOS and Linux behave consistently (open the directory
// in the default file manager); Windows uses explorer.exe. We
// deliberately don't wait on the child — Finder in particular
// returns immediately, and there's no useful exit code to surface
// either way.
func openInFileBrowser(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		// `explorer` returns exit code 1 even on success, so we
		// don't check err. The only real failure mode is "binary
		// not on PATH", which Start() reports.
		cmd = exec.Command("explorer", path)
		return cmd.Start()
	default:
		// Linux / *BSD — xdg-open is the freedesktop standard.
		cmd = exec.Command("xdg-open", path)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	// Detach: we don't care about the file manager's lifetime.
	go func() { _ = cmd.Wait() }()
	return nil
}

func (s *Server) handleAgentFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rel := r.PathValue("path")
	if rel == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "path required"})
		return
	}
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	// Never serve a file named SKILL.md as a downloadable artifact. Skill
	// manifests are the agent's IP and never legitimately land in the
	// workspace (skill-creator writes them to the skills bucket, not here),
	// so a SKILL.md showing up under /workspace is the tail of the
	// `cat /skills/foo/SKILL.md > /workspace/foo.md` exfil chain. This is a
	// name-level guard only — it does not catch manifest content saved
	// under a different filename; that residual is the model-cooperation
	// case the load_skill / system-prompt confidentiality directives cover.
	if strings.EqualFold(filepath.Base(filepath.Clean(rel)), "SKILL.md") {
		jsonResponse(w, http.StatusForbidden, map[string]any{"error": "refused: skill manifests are not downloadable"})
		return
	}
	if s.workspaceStore != nil {
		s.serveFileFromWorkspaceStore(w, r, id, rel)
		return
	}
	// Workspace store not configured — fall back to direct FS read.
	// The local FS layout mirrors the workspace store's:
	// ~/.fastclaw/workspaces/<agent_id>/<path>.
	home, err := config.HomeDir()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	root := filepath.Join(home, "workspaces", id)
	abs := filepath.Join(root, filepath.Clean("/"+rel))
	if !strings.HasPrefix(abs, root+string(os.PathSeparator)) && abs != root {
		jsonResponse(w, http.StatusForbidden, map[string]any{"error": "path escape"})
		return
	}
	// ServeFile sets Content-Type from the mime database itself; we just
	// add the CSP sandbox for HTML on top — same rationale as in
	// setFileResponseHeaders above.
	if ext := strings.ToLower(filepath.Ext(rel)); ext == ".html" || ext == ".htm" {
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, abs)
}

func (s *Server) serveFileFromWorkspaceStore(w http.ResponseWriter, r *http.Request, agentID, path string) {
	rc, err := s.workspaceStore.Get(r.Context(), agentID, "", "", path)
	if err != nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	defer rc.Close()
	setFileResponseHeaders(w, path)
	io.Copy(w, rc)
}

// setFileResponseHeaders picks the right Content-Type for a user-produced
// workspace file and locks down agent-generated HTML so it can't reach the
// app's cookies/storage even if the user opens the URL in a bare tab. The
// Content-Type derived from the extension is what lets iframes render the
// file (octet-stream → about:blank, since iframes don't sniff). The CSP
// `sandbox` header is the same protection the chat preview gets via the
// iframe `sandbox` attribute, but applied at the HTTP layer so it kicks in
// no matter how the file is loaded.
func setFileResponseHeaders(w http.ResponseWriter, path string) {
	ext := strings.ToLower(filepath.Ext(path))
	ctype := mime.TypeByExtension(ext)
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if ext == ".html" || ext == ".htm" {
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts")
	}
}

func (s *Server) handleAgentFileUpload(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	if s.workspaceStore == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "no workspace store"})
		return
	}
	if rec := s.requireAgentOwner(w, r, id); rec == nil {
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	// The chat client sends one form field "file" per attachment, so the
	// multipart payload often carries several entries under the same key.
	// r.FormFile only returns the first — iterate over MultipartForm.File
	// so multi-attach uploads land all of their files, not just one.
	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "no file"})
		return
	}
	// sessionId scopes the upload to the sandbox mount the agent actually
	// sees. We resolve the session to find its project_id so uploads in
	// a project chat land in projects/<pid>/ alongside the agent's own
	// writes; loose chats keep the legacy sessions/<chat>/ subdir.
	sessionKey := r.URL.Query().Get("sessionId")
	sessionID := s.workspaceSessionScope(r.Context(), id, sessionKey)
	projectID := s.resolveSessionProject(r.Context(), r, id, sessionKey)
	if projectID != "" {
		// Project sessions don't use the per-chat subdir — clear it so
		// the workspace store routes to projects/<pid>/.
		sessionID = ""
	}
	saved := make([]map[string]any, 0, len(headers))
	for _, h := range headers {
		fh, err := h.Open()
		if err != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		data, err := io.ReadAll(fh)
		fh.Close()
		if err != nil {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if err := s.workspaceStore.Put(r.Context(), id, projectID, sessionID, h.Filename, strings.NewReader(string(data)), int64(len(data)), ""); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		saved = append(saved, map[string]any{"name": h.Filename, "size": len(data)})
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "files": saved})
}

func defaultIfEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// invalidateUser drops the user's lazy-loaded UserSpace so the next
// access reloads it from the DB. The gateway implements InvalidateUser
// behind the api.UserResolver interface.
func (s *Server) invalidateUser(userID string) {
	if userID == "" || s.userResolver == nil {
		return
	}
	if r, ok := s.userResolver.(interface{ InvalidateUser(string) }); ok {
		r.InvalidateUser(userID)
	}
	slog.Debug("invalidated user space", "user", userID)
}

// invalidateAgent drops every cached UserSpace that holds this agent —
// owner plus any foreign caller that lazy-attached via EnsureAgent
// (super_admin chat, public-link viewer, apikey user). Use this after
// writes that mutate the agent's resolved runtime (agents.defaults,
// agent-scope providers); plain user-scope writes can stick with
// invalidateUser.
func (s *Server) invalidateAgent(agentID string) {
	if agentID == "" || s.userResolver == nil {
		return
	}
	if r, ok := s.userResolver.(interface{ InvalidateAgent(string) }); ok {
		r.InvalidateAgent(agentID)
	}
	slog.Debug("invalidated user spaces holding agent", "agent", agentID)
}

// requireOwnerOrSuperAdmin guards endpoints that mutate another user's
// resources.
func (s *Server) requireOwnerOrSuperAdmin(w http.ResponseWriter, r *http.Request, ownerID string) bool {
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return false
	}
	if ident.UserID == ownerID || ident.Role == users.RoleSuperAdmin {
		return true
	}
	jsonResponse(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
	return false
}

var _ workspace.Store = (workspace.Store)(nil)

// handleListAgentRegisteredTools returns the live tool registry for the
// specified agent. Drives the Tools tab's allowlist checkbox picker —
// the operator clicks rather than typing tool names from memory.
//
// Permission is read-level (owner / super_admin / shared-link viewer)
// rather than owner-only because viewers might want to see what they
// have access to, even if they can't change the allowlist. The PUT
// path stays owner-gated.
func (s *Server) handleListAgentRegisteredTools(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	ag := s.resolveAgent(r, id)
	if ag == nil {
		// Agent isn't loaded in the caller's UserSpace and lazy-attach
		// also failed. We could fall back to the DB record, but the
		// whole point of this endpoint is the LIVE registry (MCP tools
		// only exist once the agent is attached), so a 404 here is
		// honest rather than misleadingly returning just the builtins.
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not loaded"})
		return
	}
	toolList := ag.RegisteredTools()
	if toolList == nil {
		toolList = []tools.ToolInfo{}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"tools": toolList})
}
