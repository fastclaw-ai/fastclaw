package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// Scope-aware CRUD for providers + channels (and a generic settings
// endpoint). Authorization:
//
//   scope=system → READ: any authenticated user (system providers are
//                  designed to be inheritable; api keys are masked on
//                  the way out). WRITE: super_admin only.
//   scope=user   → super_admin OR scopeId == caller's user_id
//   scope=agent  → super_admin OR caller owns the agent
//
// All four routes share the same gating helper so the rules stay aligned.

// scopeOp distinguishes read vs mutating access for authorizeScope. The
// only place this matters today is system scope: regular users may list
// inherited system providers but never edit them.
type scopeOp int

const (
	scopeRead scopeOp = iota
	scopeWrite
)

// authorizeScope returns true if the request is allowed at (scope, scopeID)
// for the given op. Mutating callers should additionally pass through
// `requireWritable` (which rejects super_admin actAs mode).
func (s *Server) authorizeScope(w http.ResponseWriter, r *http.Request, sc, scopeID string, op scopeOp) bool {
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unauthorized"})
		return false
	}
	if ident.Role == users.RoleSuperAdmin {
		return true
	}
	switch sc {
	case scope.System:
		// System scope is broadcast: every agent inherits from it. Reads
		// are open so the dashboard can show a non-admin which providers
		// they're inheriting and the runtime can resolve them. Writes
		// stay locked to super_admin (the upstream business reason for
		// the gate hasn't changed).
		if op == scopeRead {
			return true
		}
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "super_admin required to mutate system scope"})
		return false
	case scope.User:
		if scopeID != ident.UserID {
			jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "cannot manage other users' configs"})
			return false
		}
		// app_user accounts are end-users provisioned by a downstream
		// app — they shouldn't be able to redirect their LLM provider
		// or fork channel bindings out from under the calling app.
		// Reads are still allowed (so the agent runtime can see what
		// the upstream stack configured for them); only mutating
		// callers reach this path via requireWritable, but we hard-
		// reject up front to be unambiguous.
		if ident.Role == users.RoleAppUser {
			jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "app_user cannot manage user-scope configs"})
			return false
		}
		return true
	case scope.Agent:
		// Must own the agent. We do an inexpensive store lookup to verify.
		if s.dataStore == nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "store not configured"})
			return false
		}
		rec, err := s.dataStore.GetAgent(r.Context(), scopeID)
		if err != nil || rec == nil {
			jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "agent not yours"})
			return false
		}
		if rec.UserID == ident.UserID {
			return true
		}
		// Non-owner read access on a shared agent: when the owner has
		// shareModelConfig on (default), the agent's runtime resolution
		// already includes its agent-scope providers for chatters
		// (EnsureAgent overlays them in). The Models tab in the chatter's
		// agent-settings dialog needs to surface those same rows — with
		// masked keys — so the chatter knows which credentials the agent
		// is using and which models are available. Writes stay owner-only.
		if op == scopeRead {
			if agentShareModelConfig(rec) {
				if rec.IsPublic || s.callerOwnsAgent(r, scopeID) {
					return true
				}
				// Mirror requireAgentReadable's apikey-ACL gate so an
				// apikey scoped to this agent can also read.
				if ident.AuthMethod == "apikey" && ident.CanAccessAgent(scopeID) {
					return true
				}
				// Signed-in user on a non-public shared agent: we don't
				// have a separate "this user has been granted access"
				// table beyond IsPublic / apikey ACL, so fall through
				// to the standard 403 below. (If you build sharing
				// invites later, gate them here.)
			}
		}
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "agent not yours"})
		return false
	default:
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid scope"})
		return false
	}
}

// listConfigsByScope is the HTTP-side bridge to store.ListConfigs:
// translates the (scope, scopeID) URL idiom into (userID, agentID).
// New code should call store.ListConfigs directly with explicit
// ownership; this helper exists so the dashboard's scope-keyed routes
// don't have to inline the conversion at every call site.
func (s *Server) listConfigsByScope(ctx context.Context, kind, sc, scopeID string) ([]store.ConfigRecord, error) {
	uid, aid := scope.OwnershipFromScope(sc, scopeID)
	return s.dataStore.ListConfigs(ctx, kind, uid, aid)
}

// getConfigByNameScope is the GetConfigByName variant of the same bridge.
func (s *Server) getConfigByNameScope(ctx context.Context, kind, sc, scopeID, name string) (*store.ConfigRecord, error) {
	uid, aid := scope.OwnershipFromScope(sc, scopeID)
	return s.dataStore.GetConfigByName(ctx, kind, uid, aid, name)
}

// scopeFromQuery reads the scope/scopeId query parameters with sensible
// defaults: missing scope falls through to the caller's user scope so a
// regular user's `GET /api/providers` returns "their" providers.
func scopeFromQuery(r *http.Request) (string, string) {
	sc := r.URL.Query().Get("scope")
	scopeID := r.URL.Query().Get("scopeId")
	if sc == "" {
		ident, _ := auth.FromContext(r.Context())
		if ident.Role == users.RoleSuperAdmin {
			sc = scope.System
		} else {
			sc = scope.User
			scopeID = ident.UserID
		}
	}
	return sc, scopeID
}

// --- Providers ---

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	sc, scopeID := scopeFromQuery(r)
	if !s.authorizeScope(w, r, sc, scopeID, scopeRead) {
		return
	}
	rows, err := s.listConfigsByScope(r.Context(), store.KindProvider, sc, scopeID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		pc := config.ProviderConfig{}
		if blob, err := json.Marshal(r.Data); err == nil {
			_ = json.Unmarshal(blob, &pc)
		}
		out = append(out, map[string]any{
			"id":        r.ID,
			"scope":     r.LegacyScope(),
			"scopeId":   r.LegacyScopeID(),
			"name":      r.Name,
			"apiBase":   pc.APIBase,
			"apiKey":    maskAPIKey(pc.APIKey),
			"apiType":   pc.APIType,
			"authType":  pc.AuthType,
			"models":    pc.Models,
			"updatedAt": r.UpdatedAt,
		})
	}
	jsonResponse(w, http.StatusOK, map[string]any{"providers": out, "scope": sc, "scopeId": scopeID})
}

type writeProviderRequest struct {
	Scope    string              `json:"scope"`
	ScopeID  string              `json:"scopeId"`
	Name     string              `json:"name"`
	APIBase  string              `json:"apiBase"`
	APIKey   string              `json:"apiKey"`
	APIType  string              `json:"apiType"`
	AuthType string              `json:"authType"`
	Models   []config.ModelEntry `json:"models,omitempty"`
}

func (s *Server) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	var req writeProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if req.Name == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "name required"})
		return
	}
	sc, scopeID := req.Scope, req.ScopeID
	if sc == "" {
		sc, scopeID = scopeFromQuery(r)
	}
	if !s.authorizeScope(w, r, sc, scopeID, scopeWrite) {
		return
	}
	pcfg := config.ProviderConfig{
		APIBase:  req.APIBase,
		APIKey:   req.APIKey,
		APIType:  req.APIType,
		AuthType: req.AuthType,
		Models:   req.Models,
	}
	if err := scope.SaveProviderByScope(r.Context(), s.dataStore, sc, scopeID, req.Name, pcfg); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.invalidateScope(sc, scopeID)
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	rec, err := s.dataStore.GetConfig(r.Context(), id)
	if err != nil || rec == nil || rec.Kind != store.KindProvider {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	if !s.authorizeScope(w, r, rec.LegacyScope(), rec.LegacyScopeID(), scopeWrite) {
		return
	}
	var req writeProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	pc := config.ProviderConfig{}
	if blob, err := json.Marshal(rec.Data); err == nil {
		_ = json.Unmarshal(blob, &pc)
	}
	// Patch — preserve apiKey when caller sent the masked sentinel.
	if req.APIBase != "" {
		pc.APIBase = req.APIBase
	}
	if req.APIKey != "" && !isMaskedSecret(req.APIKey) {
		pc.APIKey = req.APIKey
	}
	if req.APIType != "" {
		pc.APIType = req.APIType
	}
	if req.AuthType != "" {
		pc.AuthType = req.AuthType
	}
	// `models` is sent as the full desired set on every PUT (the dialog
	// is the source of truth) — overwrite even when the array is empty so
	// "remove last model" actually persists.
	if req.Models != nil {
		pc.Models = req.Models
	}
	if err := scope.SaveProviderByScope(r.Context(), s.dataStore, rec.LegacyScope(), rec.LegacyScopeID(), rec.Name, pc); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.invalidateScope(rec.LegacyScope(), rec.LegacyScopeID())
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	rec, err := s.dataStore.GetConfig(r.Context(), id)
	if err != nil || rec == nil || rec.Kind != store.KindProvider {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	if !s.authorizeScope(w, r, rec.LegacyScope(), rec.LegacyScopeID(), scopeWrite) {
		return
	}
	if err := s.dataStore.DeleteConfig(r.Context(), id); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.invalidateScope(rec.LegacyScope(), rec.LegacyScopeID())
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// --- Channels ---

func (s *Server) handleListScopedChannels(w http.ResponseWriter, r *http.Request) {
	sc, scopeID := scopeFromQuery(r)
	if !s.authorizeScope(w, r, sc, scopeID, scopeRead) {
		return
	}
	rows, err := s.listConfigsByScope(r.Context(), store.KindChannel, sc, scopeID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		cc := config.ChannelConfig{}
		if blob, err := json.Marshal(r.Data); err == nil {
			_ = json.Unmarshal(blob, &cc)
		}
		out = append(out, map[string]any{
			"id":            r.ID,
			"scope":         r.LegacyScope(),
			"scopeId":       r.LegacyScopeID(),
			"type":          r.Name,
			"enabled":       r.Enabled,
			"botToken":      maskAPIKey(cc.BotToken),
			"appToken":      maskAPIKey(cc.AppToken),
			"credentialKey": r.CredentialKey,
			"updatedAt":     r.UpdatedAt,
		})
	}
	jsonResponse(w, http.StatusOK, map[string]any{"channels": out, "scope": sc, "scopeId": scopeID})
}

type writeChannelRequest struct {
	Scope         string `json:"scope"`
	ScopeID       string `json:"scopeId"`
	Type          string `json:"type"`
	Enabled       bool   `json:"enabled"`
	BotToken      string `json:"botToken"`
	AppToken      string `json:"appToken,omitempty"`
	CredentialKey string `json:"credentialKey,omitempty"`
}

// credentialKeyFor derives a stable lookup handle from the channel's
// credentials. For bot-token channels we use the last 12 chars (matches
// how Telegram / Discord bot tokens are recognizable); falls back to the
// caller-supplied key when it's already populated.
func credentialKeyFor(channelType, botToken, callerKey string) string {
	if callerKey != "" {
		return callerKey
	}
	if botToken == "" {
		return ""
	}
	if len(botToken) <= 12 {
		return botToken
	}
	return botToken[len(botToken)-12:]
}

func (s *Server) handleCreateScopedChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	var req writeChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if req.Type == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "type required"})
		return
	}
	sc, scopeID := req.Scope, req.ScopeID
	if sc == "" {
		sc, scopeID = scopeFromQuery(r)
	}
	if !s.authorizeScope(w, r, sc, scopeID, scopeWrite) {
		return
	}
	credKey := credentialKeyFor(req.Type, req.BotToken, req.CredentialKey)
	uid, aid := scope.OwnershipFromScope(sc, scopeID)
	if err := s.assertChannelCredentialUnique(r, req.Type, credKey, "", uid, aid); err != nil {
		jsonResponse(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	cc := config.ChannelConfig{
		Enabled:  req.Enabled,
		BotToken: req.BotToken,
		AppToken: req.AppToken,
	}
	// Write to the channels table (sole authoritative store).
	chUID, chAID := scope.OwnershipFromScope(sc, scopeID)
	ch := &store.ChannelRecord{
		UserID:    chUID,
		AgentID:   chAID,
		Type:      req.Type,
		AccountID: credKey,
		Enabled:   req.Enabled,
		BotToken:  cc.BotToken,
	}
	chData, _ := json.Marshal(cc)
	var dm map[string]interface{}
	_ = json.Unmarshal(chData, &dm)
	delete(dm, "enabled")
	ch.Data = dm
	if err := s.dataStore.SaveChannel(r.Context(), ch); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.invalidateScope(sc, scopeID)
	if req.Enabled {
		if looked, _ := s.dataStore.LookupChannel(r.Context(), req.Type, credKey); looked != nil {
			s.hotRegisterChannelRecord(*looked)
		}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleUpdateScopedChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	rec, err := s.dataStore.GetConfig(r.Context(), id)
	if err != nil || rec == nil || rec.Kind != store.KindChannel {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	if !s.authorizeScope(w, r, rec.LegacyScope(), rec.LegacyScopeID(), scopeWrite) {
		return
	}
	var req writeChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	cc := config.ChannelConfig{}
	if blob, err := json.Marshal(rec.Data); err == nil {
		_ = json.Unmarshal(blob, &cc)
	}
	if req.BotToken != "" && !isMaskedSecret(req.BotToken) {
		cc.BotToken = req.BotToken
	}
	if req.AppToken != "" && !isMaskedSecret(req.AppToken) {
		cc.AppToken = req.AppToken
	}
	enabled := req.Enabled
	credKey := credentialKeyFor(rec.Name, cc.BotToken, req.CredentialKey)
	if credKey != rec.CredentialKey {
		if err := s.assertChannelCredentialUnique(r, rec.Name, credKey, rec.ID, rec.UserID, rec.AgentID); err != nil {
			jsonResponse(w, http.StatusConflict, map[string]any{"error": err.Error()})
			return
		}
	}
	// Write to the channels table (sole authoritative store).
	ch := &store.ChannelRecord{
		UserID:    rec.UserID,
		AgentID:   rec.AgentID,
		Type:      rec.Name,
		AccountID: credKey,
		Enabled:   enabled,
		BotToken:  cc.BotToken,
	}
	chData, _ := json.Marshal(cc)
	var dm map[string]interface{}
	_ = json.Unmarshal(chData, &dm)
	delete(dm, "enabled")
	ch.Data = dm
	if err := s.dataStore.SaveChannel(r.Context(), ch); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.invalidateScope(rec.LegacyScope(), rec.LegacyScopeID())
	if enabled {
		if updated, _ := s.dataStore.LookupChannel(r.Context(), rec.Name, credKey); updated != nil {
			s.hotRegisterChannelRecord(*updated)
		}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteScopedChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	rec, err := s.dataStore.GetConfig(r.Context(), id)
	if err != nil || rec == nil || rec.Kind != store.KindChannel {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	if !s.authorizeScope(w, r, rec.LegacyScope(), rec.LegacyScopeID(), scopeWrite) {
		return
	}
	// Best-effort: also delete from configs table for backward compat.
	_ = s.dataStore.DeleteConfig(r.Context(), id)

	s.invalidateScope(rec.LegacyScope(), rec.LegacyScopeID())
	// Delete from the channels table (authoritative) and hot-unregister.
	cc := decodeChannelConfigFromRecord(rec)
	for accountID := range cc.Accounts {
		if ch, err := s.dataStore.LookupChannel(r.Context(), rec.Name, accountID); err == nil && ch != nil {
			if err := s.dataStore.DeleteChannel(r.Context(), ch.ID); err != nil {
				jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
		}
		s.hotUnregisterChannel(rec.Name, accountID)
	}
	if len(cc.Accounts) == 0 {
		if ch, err := s.dataStore.LookupChannel(r.Context(), rec.Name, rec.CredentialKey); err == nil && ch != nil {
			if err := s.dataStore.DeleteChannel(r.Context(), ch.ID); err != nil {
				jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
		}
		s.hotUnregisterChannel(rec.Name, "")
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// decodeChannelConfigFromRecord — local mirror of gateway.decodeChannelConfig
// so this package doesn't need to import gateway.
func decodeChannelConfigFromRecord(rec *store.ConfigRecord) config.ChannelConfig {
	cc := config.ChannelConfig{Enabled: rec.Enabled}
	if blob, err := json.Marshal(rec.Data); err == nil && len(blob) > 0 {
		_ = json.Unmarshal(blob, &cc)
	}
	cc.Enabled = rec.Enabled
	return cc
}

// assertChannelCredentialUnique enforces the soft uniqueness invariant the
// schema doesn't have a DB constraint for: two rows with the same
// (kind="channel", credential_key) would race in the inbound dispatcher
// — only one row is returned and which one is undefined.
//
// "Same logical row" exemptions:
//   - excludeID matches: the update path; we're rewriting the row in place
//   - same (kind, user_id, agent_id, name): the upsert path; the
//     connect handler is reconnecting the SAME bot to the SAME (user,
//     agent) it was bound to before. SaveChannel's ON CONFLICT will
//     just refresh the row, no race.
//
// callerUserID / callerAgentID are the (user_id, agent_id) of the row
// the caller is about to write — passing empty strings preserves the
// stricter "global uniqueness" semantics for callers that don't have
// that context handy.
func (s *Server) assertChannelCredentialUnique(r *http.Request, channelType, credKey, excludeID string, callerUserID, callerAgentID string) error {
	return s.assertChannelCredentialUniqueOpt(r, channelType, credKey, excludeID, callerUserID, callerAgentID, false)
}

// assertChannelCredentialUniqueOpt is like assertChannelCredentialUnique but
// accepts autoReplace: when true and the conflicting row belongs to the same
// user, the old row is deleted automatically so the caller can proceed with
// saving the new binding. This lets the dashboard "move" a bot from one agent
// to another without forcing the user to disconnect manually first.
func (s *Server) assertChannelCredentialUniqueOpt(r *http.Request, channelType, credKey, excludeID string, callerUserID, callerAgentID string, autoReplace bool) error {
	if credKey == "" {
		return nil
	}
	existing, err := s.dataStore.LookupChannelByCredential(r.Context(), channelType, credKey)
	if err != nil {
		return nil // not-found is fine; other errors get bubbled by the caller
	}
	if existing == nil || existing.ID == excludeID {
		return nil
	}
	// Same caller reconnecting the same bot to the same agent — the
	// upsert path will refresh the existing row, not create a new one.
	if existing.UserID == callerUserID &&
		existing.AgentID == callerAgentID &&
		existing.Name == channelType {
		return nil
	}
	// Auto-replace: the caller is presenting the bot token (proof of
	// ownership of the credential), so unconditionally tear down the old
	// binding and let the new one take its place. The uniqueness check
	// exists to prevent inbound-dispatcher races, not for authorization.
	if autoReplace {
		slog.Info("[channel-unique] auto-replacing old binding",
			"existing.ID", existing.ID, "existing.UserID", existing.UserID, "existing.AgentID", existing.AgentID,
			"callerUserID", callerUserID, "callerAgentID", callerAgentID)
		cc := decodeChannelConfigFromRecord(existing)
		if err := s.dataStore.DeleteConfig(r.Context(), existing.ID); err != nil {
			return fmt.Errorf("failed to auto-disconnect old binding: %w", err)
		}
		s.invalidateOwner(existing.UserID, existing.AgentID)
		for accountID := range cc.Accounts {
			s.hotUnregisterChannel(existing.Name, accountID)
		}
		if len(cc.Accounts) == 0 {
			s.hotUnregisterChannel(existing.Name, "")
		}
		return nil
	}
	// Surface where the conflict actually lives so the operator knows
	// where to disconnect first instead of staring at a generic message.
	scopeHint := existing.LegacyScope()
	if existing.AgentID != "" {
		scopeHint = "agent " + existing.AgentID
	} else if existing.UserID != "" {
		scopeHint = "user " + existing.UserID
	}
	return fmt.Errorf("this bot is already connected at %s — disconnect it there first", scopeHint)
}

// invalidateOwner is the (userID, agentID) form of invalidateScope —
// preferred for code that's already speaking the new ownership idiom.
// Falls through to invalidateScope so the cache topology stays
// consistent regardless of which entry point the caller used.
func (s *Server) invalidateOwner(userID, agentID string) {
	sc, scopeID := scope.ScopeFromOwnership(userID, agentID)
	// "user-agent" doesn't exist in invalidateScope's switch — for
	// per-(user, agent) writes, dropping the user's cached UserSpace
	// is the right behavior, so map it to scope=user.
	if sc == "user-agent" {
		sc, scopeID = scope.User, userID
	}
	s.invalidateScope(sc, scopeID)
}

// invalidateScope drops cached UserSpaces affected by a scope-level
// write. System changes touch every loaded space; user changes touch one.
func (s *Server) invalidateScope(sc, scopeID string) {
	if s.userResolver == nil {
		return
	}
	type globalInvalidator interface{ ReloadAgents() error }
	type userInvalidator interface{ InvalidateUser(string) }
	type agentInvalidator interface{ InvalidateAgent(string) }
	switch sc {
	case scope.System:
		if r, ok := s.userResolver.(globalInvalidator); ok {
			_ = r.ReloadAgents()
		}
	case scope.User:
		if r, ok := s.userResolver.(userInvalidator); ok {
			r.InvalidateUser(scopeID)
		}
	case scope.Agent:
		// Agent-scoped writes (provider, channel, setting) affect every
		// cached UserSpace that holds the agent — owner plus any foreign
		// caller that lazy-attached it via EnsureAgent. InvalidateAgent
		// walks the registry and drops them all; falling back to the
		// owner-only invalidate keeps behavior consistent for resolvers
		// that don't implement the newer hook.
		if r, ok := s.userResolver.(agentInvalidator); ok {
			r.InvalidateAgent(scopeID)
			return
		}
		ctx := context.Background()
		if all, err := s.dataStore.ListAllAgents(ctx); err == nil {
			for _, ar := range all {
				if ar.ID == scopeID {
					if r, ok := s.userResolver.(userInvalidator); ok {
						r.InvalidateUser(ar.UserID)
					}
					return
				}
			}
		}
	}
}

// tryHotRegisterChannel asks the gateway to start the channel adapter for
// `rec` immediately. A missing optional hook is a successful no-op.
func (s *Server) tryHotRegisterChannel(rec store.ConfigRecord) error {
	if s.userResolver == nil {
		return nil
	}
	type chanRegistrar interface {
		RegisterChannelFromConfig(rec store.ConfigRecord) error
	}
	if r, ok := s.userResolver.(chanRegistrar); ok {
		return r.RegisterChannelFromConfig(rec)
	}
	return nil
}

// hotRegisterChannel preserves the best-effort behavior used by existing
// channel handlers. New flows that require atomic persistence + startup call
// tryHotRegisterChannel directly and roll back on failure.
func (s *Server) hotRegisterChannel(rec store.ConfigRecord) {
	if err := s.tryHotRegisterChannel(rec); err != nil {
		slog.Warn("hot-register channel failed", "type", rec.Name, "error", err)
	}
}

// hotRegisterChannelRecord asks the gateway to start a channel adapter
// from a ChannelRecord. Best-effort — falls back to hotRegisterChannel
// via a synthesized ConfigRecord when the resolver doesn't implement the
// new interface (e.g. older test stubs).
func (s *Server) tryHotRegisterChannelRecord(rec store.ChannelRecord) error {
	if s.userResolver == nil {
		return nil
	}
	type chanRecordRegistrar interface {
		RegisterChannel(rec store.ChannelRecord) error
	}
	if r, ok := s.userResolver.(chanRecordRegistrar); ok {
		return r.RegisterChannel(rec)
	}
	// Fallback: synthesize a ConfigRecord for legacy resolvers.
	cfgRec := store.ConfigRecord{
		ID:            rec.ID,
		Kind:          store.KindChannel,
		UserID:        rec.UserID,
		AgentID:       rec.AgentID,
		Name:          rec.Type,
		Enabled:       rec.Enabled,
		CredentialKey: rec.AccountID,
		Data:          rec.Data,
		CreatedAt:     rec.CreatedAt,
		UpdatedAt:     rec.UpdatedAt,
	}
	return s.tryHotRegisterChannel(cfgRec)
}

func (s *Server) hotRegisterChannelRecord(rec store.ChannelRecord) {
	if err := s.tryHotRegisterChannelRecord(rec); err != nil {
		slog.Warn("hot-register channel record failed", "type", rec.Type, "error", err)
	}
}

// hotUnregisterChannel — paired with hotRegisterChannel for delete paths.
func (s *Server) hotUnregisterChannel(channelType, accountID string) {
	if s.userResolver == nil {
		return
	}
	type chanUnregistrar interface {
		UnregisterChannel(channelType, accountID string)
	}
	if r, ok := s.userResolver.(chanUnregistrar); ok {
		r.UnregisterChannel(channelType, accountID)
	}
}
