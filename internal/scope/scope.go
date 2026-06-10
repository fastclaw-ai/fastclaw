// Package scope reads (user, agent)-keyed rows out of store.configs and
// merges them into the flat shapes the runtime expects.
//
// Every row in configs carries a (kind, user_id, agent_id, name) tuple.
// Resolution walks ownership outer→inner, with inner rows shadowing outer
// rows by `name`:
//
//	system (user='', agent='') →
//	  user (user=X, agent='')   →
//	    agent (user='', agent=Y) →
//	      per-(user, agent) (user=X, agent=Y)
//
// kind="provider": name is the provider key ("openai"). Inner rows
//
//	replace outer entries entirely (no field-level merge).
//
// kind="channel":  name is the channel type ("telegram"). A disabled inner
//
//	row erases the outer entry — lets a user opt out of a system-wide bot.
//
// kind="setting":  name is the namespace ("agents.defaults", "sandbox", …).
//
//	Top-level keys merge field-wise; inner-scope keys win.
package scope

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// HTTP-layer scope identifiers. The storage layer keys configs by
// (user_id, agent_id) directly; these constants exist for the HTTP API
// contract (URL ?scope= params, dashboard scope picker). Translate to
// storage form via OwnershipFromScope.
const (
	System = "system"
	User   = "user"
	Agent  = "agent"
)

// OwnershipFromScope converts the HTTP-side (scope, scopeID) pair into
// the storage (user_id, agent_id) pair. Empty/unknown scope returns
// ("", "") which the store reads as "system / global".
func OwnershipFromScope(sc, scopeID string) (userID, agentID string) {
	switch sc {
	case User:
		return scopeID, ""
	case Agent:
		return "", scopeID
	default:
		return "", ""
	}
}

// ScopeFromOwnership is the inverse, used when emitting (scope, scopeID)
// back to the dashboard JSON. (X, Y) — both filled — is rendered as
// scope="user-agent" so the UI can tell apart per-(user, agent)
// overrides from plain user or agent rows. Today the dashboard only
// reads scope="system"/"user"/"agent"; the new compound keeps the door
// open for the multi-tenant view.
func ScopeFromOwnership(userID, agentID string) (scope, scopeID string) {
	switch {
	case userID != "" && agentID != "":
		return "user-agent", userID + "/" + agentID
	case userID != "":
		return User, userID
	case agentID != "":
		return Agent, agentID
	default:
		return System, ""
	}
}

// Providers returns the merged map of LLM provider configs for a given
// (user, agent). Pass agentID="" to get only the user-level view. Pass
// both empty to get system-only.
func Providers(ctx context.Context, st store.Store, userID, agentID string) (map[string]config.ProviderConfig, error) {
	if st == nil {
		return nil, errors.New("scope.Providers: store is required")
	}
	out := map[string]config.ProviderConfig{}
	apply := func(rows []store.ConfigRecord) {
		for _, r := range rows {
			out[r.Name] = providerToConfig(r)
		}
	}
	// system layer
	if rows, err := st.ListConfigs(ctx, store.KindProvider, "", ""); err != nil {
		return nil, err
	} else {
		apply(rows)
	}
	// user layer
	if userID != "" {
		if rows, err := st.ListConfigs(ctx, store.KindProvider, userID, ""); err != nil {
			return nil, err
		} else {
			apply(rows)
		}
	}
	// agent layer
	if agentID != "" {
		if rows, err := st.ListConfigs(ctx, store.KindProvider, "", agentID); err != nil {
			return nil, err
		} else {
			apply(rows)
		}
	}
	// per-(user, agent) layer
	if userID != "" && agentID != "" {
		if rows, err := st.ListConfigs(ctx, store.KindProvider, userID, agentID); err != nil {
			return nil, err
		} else {
			apply(rows)
		}
	}
	return out, nil
}

// AgentScopeProviders returns providers stored at (user='', agent=Y)
// only — the agent's "official" rows, without system or user layers
// merged in. Use this to overlay an agent's own rows on top of an
// already system+user-merged view: re-running the full Providers walk
// would re-apply outer layers and silently clobber any user-scope
// override the caller already merged in.
func AgentScopeProviders(ctx context.Context, st store.Store, agentID string) (map[string]config.ProviderConfig, error) {
	if st == nil {
		return nil, errors.New("scope.AgentScopeProviders: store is required")
	}
	if agentID == "" {
		return map[string]config.ProviderConfig{}, nil
	}
	rows, err := st.ListConfigs(ctx, store.KindProvider, "", agentID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]config.ProviderConfig, len(rows))
	for _, r := range rows {
		out[r.Name] = providerToConfig(r)
	}
	return out, nil
}

// UserScopeProviders returns providers stored at (user=X, agent='')
// only — the user's personal rows, without the system layer. Used by
// the foreign-agent path so a viewer can fall back to the owner's
// provider credentials without dragging the owner's full merged view
// (which would re-apply system rows on top of the viewer's already-
// merged set).
func UserScopeProviders(ctx context.Context, st store.Store, userID string) (map[string]config.ProviderConfig, error) {
	if st == nil {
		return nil, errors.New("scope.UserScopeProviders: store is required")
	}
	if userID == "" {
		return map[string]config.ProviderConfig{}, nil
	}
	rows, err := st.ListConfigs(ctx, store.KindProvider, userID, "")
	if err != nil {
		return nil, err
	}
	out := make(map[string]config.ProviderConfig, len(rows))
	for _, r := range rows {
		out[r.Name] = providerToConfig(r)
	}
	return out, nil
}

// AgentScopeMCPServers returns enabled MCP servers stored at
// (user='', agent=Y) only. These rows are the dashboard-managed per-agent
// MCP overlay and intentionally do not walk system/user layers.
func AgentScopeMCPServers(ctx context.Context, st store.Store, agentID string) (map[string]config.MCPServerConfig, error) {
	if st == nil || agentID == "" {
		return map[string]config.MCPServerConfig{}, nil
	}
	rows, err := st.ListConfigs(ctx, store.KindMCP, "", agentID)
	if err != nil {
		return nil, err
	}
	return decodeMCPRows(rows)
}

// SystemScopeMCPServers returns enabled MCP servers stored at the system
// layer (user='', agent=''). This is the broadcast base layer inherited
// by every agent; per-agent rows with the same name shadow these.
func SystemScopeMCPServers(ctx context.Context, st store.Store) (map[string]config.MCPServerConfig, error) {
	if st == nil {
		return map[string]config.MCPServerConfig{}, nil
	}
	rows, err := st.ListConfigs(ctx, store.KindMCP, "", "")
	if err != nil {
		return nil, err
	}
	return decodeMCPRows(rows)
}

// decodeMCPRows turns kind="mcp" config rows into the runtime map,
// skipping disabled rows. Shared by the agent- and system-scope readers.
func decodeMCPRows(rows []store.ConfigRecord) (map[string]config.MCPServerConfig, error) {
	out := make(map[string]config.MCPServerConfig, len(rows))
	for _, rec := range rows {
		if !rec.Enabled {
			continue
		}
		blob, err := json.Marshal(rec.Data)
		if err != nil {
			return nil, fmt.Errorf("marshal MCP config %q: %w", rec.Name, err)
		}
		var cfg config.MCPServerConfig
		if err := json.Unmarshal(blob, &cfg); err != nil {
			return nil, fmt.Errorf("decode MCP config %q: %w", rec.Name, err)
		}
		out[rec.Name] = cfg
	}
	return out, nil
}

// Channels returns the merged channel map. Disabled rows in an inner
// scope erase the outer entry.
func Channels(ctx context.Context, st store.Store, userID, agentID string) (map[string]config.ChannelConfig, error) {
	if st == nil {
		return nil, errors.New("scope.Channels: store is required")
	}
	out := map[string]config.ChannelConfig{}
	apply := func(rows []store.ConfigRecord) {
		for _, r := range rows {
			if !r.Enabled {
				delete(out, r.Name)
				continue
			}
			out[r.Name] = channelToConfig(r)
		}
	}
	if rows, err := st.ListConfigs(ctx, store.KindChannel, "", ""); err != nil {
		return nil, err
	} else {
		apply(rows)
	}
	if userID != "" {
		if rows, err := st.ListConfigs(ctx, store.KindChannel, userID, ""); err != nil {
			return nil, err
		} else {
			apply(rows)
		}
	}
	if agentID != "" {
		if rows, err := st.ListConfigs(ctx, store.KindChannel, "", agentID); err != nil {
			return nil, err
		} else {
			apply(rows)
		}
	}
	if userID != "" && agentID != "" {
		if rows, err := st.ListConfigs(ctx, store.KindChannel, userID, agentID); err != nil {
			return nil, err
		} else {
			apply(rows)
		}
	}
	return out, nil
}

// Setting returns the merged JSON for one namespace across the
// system → user → agent → per-(user, agent) chain. Field-level merge on
// the top-level map; inner-ownership fields override outer ones. Unset
// namespaces yield an empty map without erroring — callers Unmarshal
// into typed structs and rely on zero-valued fields.
func Setting(ctx context.Context, st store.Store, namespace, userID, agentID string) (map[string]interface{}, error) {
	if st == nil {
		return nil, errors.New("scope.Setting: store is required")
	}
	out := map[string]interface{}{}
	merge := func(layer map[string]interface{}) {
		for k, v := range layer {
			out[k] = v
		}
	}
	tryGet := func(uid, aid string) error {
		rec, err := st.GetConfigByName(ctx, store.KindSetting, uid, aid, namespace)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		if rec != nil {
			merge(rec.Data)
		}
		return nil
	}
	if err := tryGet("", ""); err != nil {
		return nil, err
	}
	if userID != "" {
		if err := tryGet(userID, ""); err != nil {
			return nil, err
		}
	}
	if agentID != "" {
		if err := tryGet("", agentID); err != nil {
			return nil, err
		}
	}
	if userID != "" && agentID != "" {
		if err := tryGet(userID, agentID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SettingInto resolves Setting and unmarshals the merged JSON into dst.
// Convenience for callers that want a typed config block.
func SettingInto(ctx context.Context, st store.Store, namespace, userID, agentID string, dst interface{}) error {
	merged, err := Setting(ctx, st, namespace, userID, agentID)
	if err != nil {
		return err
	}
	if len(merged) == 0 {
		return nil
	}
	blob, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	return json.Unmarshal(blob, dst)
}

// SaveSettingByScope is the legacy (scope, scopeID) form kept for the
// HTTP layer, which still emits scope strings in URL params and JSON.
// New callers should use SaveSetting with explicit (userID, agentID).
func SaveSettingByScope(ctx context.Context, st store.Store, sc, scopeID, namespace string, data map[string]interface{}) error {
	uid, aid := OwnershipFromScope(sc, scopeID)
	return SaveSetting(ctx, st, uid, aid, namespace, data)
}

// SaveProviderByScope / SaveChannelByScope mirror the same legacy bridge.
func SaveProviderByScope(ctx context.Context, st store.Store, sc, scopeID, name string, p config.ProviderConfig) error {
	uid, aid := OwnershipFromScope(sc, scopeID)
	return SaveProvider(ctx, st, uid, aid, name, p)
}

func SaveChannelByScope(ctx context.Context, st store.Store, sc, scopeID, channelType, credentialKey string, enabled bool, c config.ChannelConfig) error {
	uid, aid := OwnershipFromScope(sc, scopeID)
	return SaveChannel(ctx, st, uid, aid, channelType, credentialKey, enabled, c)
}

// SaveSetting upserts a single namespace at the given (user, agent)
// ownership. Pass nil/empty data to delete the row instead of writing
// {}. Pass empty userID/agentID for system-level.
func SaveSetting(ctx context.Context, st store.Store, userID, agentID, namespace string, data map[string]interface{}) error {
	if st == nil {
		return errors.New("scope.SaveSetting: store is required")
	}
	if len(data) == 0 {
		// Find and drop the row if it exists. Idempotent: missing-row is a no-op.
		if rec, err := st.GetConfigByName(ctx, store.KindSetting, userID, agentID, namespace); err == nil && rec != nil {
			return st.DeleteConfig(ctx, rec.ID)
		}
		return nil
	}
	rec := &store.ConfigRecord{
		Kind:    store.KindSetting,
		UserID:  userID,
		AgentID: agentID,
		Name:    namespace,
		Enabled: true,
		Data:    data,
	}
	return st.SaveConfig(ctx, rec)
}

// SaveProvider upserts a kind="provider" row at the given (user, agent)
// ownership.
func SaveProvider(ctx context.Context, st store.Store, userID, agentID, name string, p config.ProviderConfig) error {
	rec := &store.ConfigRecord{
		Kind:    store.KindProvider,
		UserID:  userID,
		AgentID: agentID,
		Name:    name,
		Enabled: true,
		Data:    providerToData(p),
	}
	return st.SaveConfig(ctx, rec)
}

// SaveChannel upserts a kind="channel" row at the given (user, agent)
// ownership. credentialKey is the stable lookup handle for inbound
// dispatch (bot token tail, app id).
func SaveChannel(ctx context.Context, st store.Store, userID, agentID, channelType, credentialKey string, enabled bool, c config.ChannelConfig) error {
	rec := &store.ConfigRecord{
		Kind:          store.KindChannel,
		UserID:        userID,
		AgentID:       agentID,
		Name:          channelType,
		Enabled:       enabled,
		CredentialKey: credentialKey,
		Data:          channelToData(c),
	}
	return st.SaveConfig(ctx, rec)
}

func providerToConfig(r store.ConfigRecord) config.ProviderConfig {
	pc := config.ProviderConfig{}
	if blob, err := json.Marshal(r.Data); err == nil && len(blob) > 0 {
		_ = json.Unmarshal(blob, &pc)
	}
	return pc
}

func providerToData(p config.ProviderConfig) map[string]interface{} {
	blob, _ := json.Marshal(p)
	var m map[string]interface{}
	_ = json.Unmarshal(blob, &m)
	return m
}

func channelToConfig(r store.ConfigRecord) config.ChannelConfig {
	cc := config.ChannelConfig{Enabled: r.Enabled}
	if blob, err := json.Marshal(r.Data); err == nil && len(blob) > 0 {
		_ = json.Unmarshal(blob, &cc)
	}
	cc.Enabled = r.Enabled
	return cc
}

func channelToData(c config.ChannelConfig) map[string]interface{} {
	blob, _ := json.Marshal(c)
	var m map[string]interface{}
	_ = json.Unmarshal(blob, &m)
	delete(m, "enabled") // enabled lives on the row column, not in data
	return m
}
