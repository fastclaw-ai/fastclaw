package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/channels"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Per-agent IM channel CRUD. Wraps the existing scope.SaveChannel +
// bindings setting so the dashboard can present "connect Telegram" as
// one click instead of asking users to wire two separate config rows
// by hand.

// channelOut is the wire shape returned by GET /api/agents/<id>/channels.
// One row per (channelType, accountID); botToken is masked.
//
// Source distinguishes where this binding lives:
//   - "agent" — the agent's "official" channel (visible to every user
//     with read access; only the agent owner / admin can mutate)
//   - "user"  — the caller's own per-user overlay on this agent
//     (only the caller sees + can mutate it)
type channelOut struct {
	Type           string `json:"type"`
	AccountID      string `json:"accountId"`
	BotUsername    string `json:"botUsername,omitempty"`
	BotToken       string `json:"botToken"` // masked
	Enabled        bool   `json:"enabled"`
	SharedIdentity bool   `json:"sharedIdentity"`
	UpdatedAt      string `json:"updatedAt,omitempty"`
	Source         string `json:"source,omitempty"`
}

// resolveChannelBindingScope authorizes a connect/disconnect call and
// returns the (userID, agentID) tuple the channel row should be written
// under. Every channel row now carries the binder's user_id directly —
// there's no "agent-official" overload anymore, because two users
// sharing a public agent can't share one bot anyway (inbound messages
// would route to the wrong space).
//
//   - Owner of the agent (or platform admin): (callerUID, agentID).
//     The owner's bindings are just their personal bindings on their
//     own agent.
//   - Non-owner with read access (public agent or apikey ACL grant):
//     (callerUID, agentID). Each user can bind their own bot to the
//     same public agent without colliding with anyone else's row, and
//     inbound messages route to the binder's UserSpace.
//
// Writes 4xx and returns ok=false on permission/lookup failures.
//
// The legacy return shape was (scope, scopeID, ok). The new shape is
// (userID, agentID, ok). Old callers passed (sc, scopeID) into
// invalidateScope; the migration adapter at the bottom of this file
// keeps that working.
func (s *Server) resolveChannelBindingScope(w http.ResponseWriter, r *http.Request, agentID string) (userID, aid string, ok bool) {
	if agentID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "agent id required"})
		return "", "", false
	}
	rec, err := s.dataStore.GetAgent(r.Context(), agentID)
	if err != nil || rec == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return "", "", false
	}
	uid := s.effectiveUserID(r)
	if uid == "" {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return "", "", false
	}
	ident, _ := auth.FromContext(r.Context())
	if rec.UserID == uid || (ident.AuthMethod != "" && ident.CanAdminPlatform()) {
		return uid, agentID, true
	}
	// Non-owner: must be able to at least read the agent.
	if (ident.AuthMethod == "apikey" && ident.CanAccessAgent(agentID)) || rec.IsPublic {
		return uid, agentID, true
	}
	jsonResponse(w, http.StatusForbidden, map[string]any{"error": "not your agent"})
	return "", "", false
}

// ownsAgent gates channel-management calls. Returns (callerUID, true)
// when the caller is the agent owner OR a platform admin (super_admin
// session, type=admin apikey). Bindings/channel rows are agent-keyed so
// the returned uid is the caller's, not the owner's — that matters for
// per-caller flows like the WeChat QR session whose poll-side equality
// check needs to match the start-side that stored it.
func (s *Server) ownsAgent(r *http.Request, agentID string) (string, bool) {
	if agentID == "" {
		return "", false
	}
	uid := s.effectiveUserID(r)
	if uid == "" {
		return "", false
	}
	rec, err := s.dataStore.GetAgent(r.Context(), agentID)
	if err != nil || rec == nil {
		return "", false
	}
	if rec.UserID == uid {
		return uid, true
	}
	if ident, ok := auth.FromContext(r.Context()); ok && ident.CanAdminPlatform() {
		return uid, true
	}
	return "", false
}

func (s *Server) handleListAgentChannels(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	rec, err := s.dataStore.GetAgent(r.Context(), id)
	if err != nil || rec == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	caller := s.effectiveUserID(r)
	_ = rec // kept around in case future logic gates on agent ownership again

	// Try the new channels table first. If it has rows for this agent,
	// use them exclusively; otherwise fall back to the configs table.
	out := make([]channelOut, 0)
	if caller != "" {
		if chRows, err := s.dataStore.ListChannels(r.Context(), caller, id); err == nil && len(chRows) > 0 {
			out = append(out, flattenChannelRecords(chRows, "agent")...)
		}
	}
	if chRows, err := s.dataStore.ListChannels(r.Context(), "", id); err == nil && len(chRows) > 0 {
		out = append(out, flattenChannelRecords(chRows, "agent")...)
	}
	jsonResponse(w, http.StatusOK, map[string]any{"channels": out})
}

// flattenChannelRows expands one config row per row into one channelOut
// per (channelType, accountID). source stamps where the row came from
// for the UI to render the badge. The (botToken, _, _) trio is unused
// at present — kept variadic-style so a future caller can pre-mask
// without an extra arg.
type accountFilter func(channelType, accountID string) bool

func filterAccounts(allow map[[2]string]bool) accountFilter {
	return func(channelType, accountID string) bool {
		return allow[[2]string{channelType, accountID}]
	}
}

func flattenChannelRows(rows []store.ConfigRecord, source string, _, _ string, filters ...accountFilter) []channelOut {
	out := make([]channelOut, 0, len(rows))
	for _, rec := range rows {
		cc := decodeChannelConfigFromRecord(&rec)
		if len(cc.Accounts) == 0 {
			if len(filters) > 0 && !filters[0](rec.Name, "") {
				continue
			}
			out = append(out, channelOut{
				Type:      rec.Name,
				AccountID: "",
				BotToken:  maskAPIKey(cc.BotToken),
				Enabled:   rec.Enabled,
				UpdatedAt: rec.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
				Source:    source,
			})
			continue
		}
		for accountID, acct := range cc.Accounts {
			if len(filters) > 0 && !filters[0](rec.Name, accountID) {
				continue
			}
			tok := acct.BotToken
			if tok == "" {
				tok = cc.BotToken
			}
			out = append(out, channelOut{
				Type:        rec.Name,
				AccountID:   accountID,
				BotUsername: accountID,
				BotToken:    maskAPIKey(tok),
				Enabled:     rec.Enabled,
				UpdatedAt:   rec.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
				Source:      source,
			})
		}
	}
	return out
}

// flattenChannelRecords builds channelOut entries from ChannelRecord rows.
func flattenChannelRecords(rows []store.ChannelRecord, source string) []channelOut {
	out := make([]channelOut, 0, len(rows))
	for _, rec := range rows {
		cc := config.ChannelConfig{}
		if blob, err := json.Marshal(rec.Data); err == nil {
			_ = json.Unmarshal(blob, &cc)
		}
		if len(cc.Accounts) == 0 {
			out = append(out, channelOut{
				Type:           rec.Type,
				AccountID:      rec.AccountID,
				BotToken:       maskAPIKey(rec.BotToken),
				Enabled:        rec.Enabled,
				SharedIdentity: rec.SharedIdentity,
				UpdatedAt:      rec.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
				Source:         source,
			})
			continue
		}
		for accountID, acct := range cc.Accounts {
			tok := acct.BotToken
			if tok == "" {
				tok = rec.BotToken
			}
			out = append(out, channelOut{
				Type:           rec.Type,
				AccountID:      accountID,
				BotUsername:    accountID,
				BotToken:       maskAPIKey(tok),
				Enabled:        rec.Enabled,
				SharedIdentity: rec.SharedIdentity,
				UpdatedAt:      rec.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
				Source:         source,
			})
		}
	}
	return out
}

type connectTelegramRequest struct {
	BotToken string `json:"botToken"`
}

type connectWeComRequest struct {
	BotID  string `json:"botId"`
	Secret string `json:"secret"`
}

// handleConnectAgentWeCom validates an Intelligent Bot credential pair,
// persists it under the stable Bot ID, and starts its authenticated
// WebSocket immediately. Secrets are never returned to the caller.
func (s *Server) handleConnectAgentWeCom(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	agentID := r.PathValue("id")
	userID, resolvedAgentID, ok := s.resolveChannelBindingScope(w, r, agentID)
	if !ok {
		return
	}

	var req connectWeComRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	botID := strings.TrimSpace(req.BotID)
	secret := strings.TrimSpace(req.Secret)
	if botID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "botId required"})
		return
	}
	if secret == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "secret required"})
		return
	}

	validator := s.weComValidateCredentials
	if validator == nil {
		validator = channels.WeComValidateCredentials
	}
	validationCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := validator(validationCtx, botID, secret); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	// Bot ID is the global routing identity for this persistent socket.
	// Never let SaveChannel's upsert silently move it between users/agents.
	if existing, err := s.dataStore.LookupChannel(r.Context(), "wecom", botID); err == nil && existing != nil {
		jsonResponse(w, http.StatusConflict, map[string]any{
			"error": "this WeCom Bot ID is already connected — disconnect it first",
		})
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	cc := config.ChannelConfig{
		Enabled: true,
		Accounts: map[string]config.AccountConfig{
			botID: {BotToken: secret},
		},
	}
	if err := s.saveChannelRecord(r.Context(), userID, resolvedAgentID, "wecom", botID, true, cc); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := s.appendBinding(r, "", "", config.Binding{
		AgentID: agentID,
		Match:   config.Match{Channel: "wecom", AccountID: botID},
	}); err != nil {
		if stored, lookupErr := s.dataStore.LookupChannel(r.Context(), "wecom", botID); lookupErr == nil && stored != nil {
			_ = s.dataStore.DeleteChannel(r.Context(), stored.ID)
		}
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	stored, err := s.dataStore.LookupChannel(r.Context(), "wecom", botID)
	if err != nil || stored == nil {
		if err == nil {
			err = errors.New("saved WeCom channel could not be loaded")
		}
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	stored.SharedIdentity = false
	if err := s.dataStore.SaveChannel(r.Context(), stored); err != nil {
		_ = s.dataStore.DeleteChannel(r.Context(), stored.ID)
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	s.invalidateOwner(userID, resolvedAgentID)
	if err := s.tryHotRegisterChannelRecord(*stored); err != nil {
		_ = s.dataStore.DeleteChannel(r.Context(), stored.ID)
		_ = s.removeBinding(r, "", "", agentID, "wecom", botID)
		s.invalidateOwner(userID, resolvedAgentID)
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "botId": botID})
}

// handleConnectAgentTelegram validates the bot token by hitting
// Telegram's getMe, then persists a kind=channel + binding pair scoped
// to this agent and hot-starts the adapter so the bot starts polling
// immediately.
func (s *Server) handleConnectAgentTelegram(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	uid, aid, ok := s.resolveChannelBindingScope(w, r, id)
	if !ok {
		return
	}

	var req connectTelegramRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	token := strings.TrimSpace(req.BotToken)
	if token == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "botToken required"})
		return
	}

	// Validate via Telegram getMe; this also gives us the bot username
	// which we use as the binding accountID.
	username, err := telegramGetMe(token)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	// Build channel config: one Account keyed by bot username so multi-
	// bot setups are supported on the same agent if a user adds another
	// bot later. Per-account BotToken so each can have its own.
	cc := config.ChannelConfig{
		Enabled:  true,
		Accounts: map[string]config.AccountConfig{username: {BotToken: token}},
	}
	// credential_key MUST equal the value the Telegram adapter ships as
	// InboundMessage.AccountID — that's the column processInbound uses
	// to find the owning user (LookupChannelByCredential). The adapter
	// is created with accountID = the Accounts-map key, which is the
	// bot's @username, so we mirror that here. Using the token-tail
	// fallback (credentialKeyFor) silently dropped every inbound
	// message because no row matched.
	credKey := username
	if err := s.assertChannelCredentialUniqueOpt(r, "telegram", credKey, "", uid, aid, true); err != nil {
		jsonResponse(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	if err := s.saveChannelRecord(r.Context(), uid, aid, "telegram", username, true, cc); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	// Append a binding so inbound messages route to this agent. Existing
	// bindings (e.g. an earlier Discord bot) are preserved. AgentID
	// inside the entry is always the path-resolved agent (= scopeID
	// when sc=Agent; the foreign agent the caller is binding to when
	// sc=User).
	if err := s.appendBinding(r, "", "", config.Binding{
		AgentID: id,
		Match:   config.Match{Channel: "telegram", AccountID: username},
	}); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	s.invalidateOwner(uid, aid)
	if ch, err := s.dataStore.LookupChannel(r.Context(), "telegram", username); err == nil && ch != nil {
		s.hotRegisterChannelRecord(*ch)
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":          true,
		"botUsername": username,
	})
}

// handleUpdateAgentChannel patches channel-level settings (currently
// only shared_identity). The channel is identified by (type, accountId)
// within the agent's channels.
//
//	PATCH /api/agents/{id}/channels/{type}/{accountId}
//	Body: {"sharedIdentity": true}
func (s *Server) handleUpdateAgentChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	agentID := r.PathValue("id")
	channelType := r.PathValue("type")
	accountID := r.PathValue("accountId")
	uid, aid, ok := s.resolveChannelBindingScope(w, r, agentID)
	if !ok {
		return
	}

	var req struct {
		SharedIdentity *bool `json:"sharedIdentity"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	// Find the channel row.
	chs, err := s.dataStore.ListChannels(r.Context(), uid, aid)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	var target *store.ChannelRecord
	for i := range chs {
		if chs[i].Type == channelType && chs[i].AccountID == accountID {
			target = &chs[i]
			break
		}
	}
	if target == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "channel not found"})
		return
	}

	if channelType == "wecom" {
		if req.SharedIdentity != nil && *req.SharedIdentity {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "shared identity is not supported for WeCom"})
			return
		}
		// Heal any legacy or hand-edited row while accepting an explicit false.
		target.SharedIdentity = false
	} else if req.SharedIdentity != nil {
		target.SharedIdentity = *req.SharedIdentity
	}
	if err := s.dataStore.SaveChannel(r.Context(), target); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDisconnectAgentChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	channelType := r.PathValue("type")
	accountID := r.PathValue("accountId")
	uid, aid, ok := s.resolveChannelBindingScope(w, r, id)
	if !ok {
		return
	}

	// Locate the channel row from the channels table.
	chRows, err := s.dataStore.ListChannels(r.Context(), uid, aid)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	for _, ch := range chRows {
		if ch.Type != channelType || ch.AccountID != accountID {
			continue
		}
		if err := s.dataStore.DeleteChannel(r.Context(), ch.ID); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		// Drop the matching binding too.
		if err := s.removeBinding(r, "", "", id, channelType, accountID); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		s.invalidateOwner(uid, aid)
		s.hotUnregisterChannel(channelType, accountID)
		jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	jsonResponse(w, http.StatusNotFound, map[string]any{"error": "binding not found"})
}

// appendBinding / removeBinding used to maintain a kind=setting,
// name=bindings row that carried (channel, accountID → agentID)
// mappings. Channel rows now carry agent_id directly, so the
// indirection is gone — these stubs are kept so the many connect/
// disconnect handlers don't all have to change shape at once.
//
// The migration drops every existing kind=setting/name=bindings row,
// and the runtime side (gateway/userspace.go::bindingsFromChannelRows)
// rebuilds the routing table by walking channel rows directly.
func (s *Server) appendBinding(r *http.Request, sc, scopeID string, b config.Binding) error {
	_ = r
	_ = sc
	_ = scopeID
	_ = b
	return nil
}

func (s *Server) removeBinding(r *http.Request, sc, scopeID, agentID, channelType, accountID string) error {
	_ = r
	_ = sc
	_ = scopeID
	_ = agentID
	_ = channelType
	_ = accountID
	return nil
}

// telegramGetMe validates the bot token by hitting the Bot API. Returns
// the bot's username on success. We avoid pulling tgbotapi here so this
// handler doesn't drag in the full long-poll bot machinery for what's
// just a HEAD-style validation.
func telegramGetMe(token string) (string, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getMe", token)
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("contact telegram: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		// Telegram returns {"ok":false,"description":"..."} on bad tokens.
		var apiErr struct {
			Description string `json:"description"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Description != "" {
			return "", fmt.Errorf("telegram rejected token: %s", apiErr.Description)
		}
		return "", fmt.Errorf("telegram getMe: HTTP %d", resp.StatusCode)
	}
	var ok struct {
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &ok); err != nil {
		return "", fmt.Errorf("parse telegram response: %w", err)
	}
	if ok.Result.Username == "" {
		return "", errors.New("telegram getMe returned empty username")
	}
	return ok.Result.Username, nil
}

// --- Discord ---

type connectDiscordRequest struct {
	BotToken string `json:"botToken"`
}

// handleConnectAgentDiscord validates a Discord bot token by calling
// /users/@me on the Discord REST API, then persists kind=channel +
// binding rows just like the Telegram flow. accountID = bot user ID
// (Discord's stable identifier, unlike username which can be changed).
func (s *Server) handleConnectAgentDiscord(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	uid, aid, ok := s.resolveChannelBindingScope(w, r, id)
	if !ok {
		return
	}

	var req connectDiscordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	token := strings.TrimSpace(req.BotToken)
	if token == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "botToken required"})
		return
	}

	userID, username, err := discordGetMe(token)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	// accountID = Discord user ID. Stable across username changes
	// and matches what the Discord adapter ships in
	// InboundMessage.AccountID (it's set from the same value).
	cc := config.ChannelConfig{
		Enabled:  true,
		Accounts: map[string]config.AccountConfig{userID: {BotToken: token}},
	}
	credKey := userID
	if err := s.assertChannelCredentialUniqueOpt(r, "discord", credKey, "", uid, aid, true); err != nil {
		jsonResponse(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	if err := s.saveChannelRecord(r.Context(), uid, aid, "discord", userID, true, cc); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := s.appendBinding(r, "", "", config.Binding{
		AgentID: id,
		Match:   config.Match{Channel: "discord", AccountID: userID},
	}); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.invalidateOwner(uid, aid)
	if ch, err := s.dataStore.LookupChannel(r.Context(), "discord", userID); err == nil && ch != nil {
		s.hotRegisterChannelRecord(*ch)
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":          true,
		"botUsername": username,
		"botUserId":   userID,
	})
}

// discordGetMe validates the bot token via the Discord REST API.
// Endpoint docs: GET /users/@me with `Authorization: Bot <token>`
// returns the bot user object (id, username, discriminator). We avoid
// pulling discordgo here so this handler doesn't open a gateway
// connection just to check a token.
func discordGetMe(token string) (string, string, error) {
	req, err := http.NewRequest("GET", "https://discord.com/api/v10/users/@me", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bot "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("contact discord: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		// Discord returns {"message": "...", "code": ...} on auth errors.
		var apiErr struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Message != "" {
			return "", "", fmt.Errorf("discord rejected token: %s", apiErr.Message)
		}
		return "", "", fmt.Errorf("discord users/@me: HTTP %d", resp.StatusCode)
	}
	var me struct {
		ID            string `json:"id"`
		Username      string `json:"username"`
		Discriminator string `json:"discriminator"`
		Bot           bool   `json:"bot"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		return "", "", fmt.Errorf("parse discord response: %w", err)
	}
	if me.ID == "" {
		return "", "", errors.New("discord users/@me returned empty id")
	}
	if !me.Bot {
		return "", "", errors.New("token belongs to a user account, not a bot — connect a bot token from the Discord Developer Portal")
	}
	display := me.Username
	if me.Discriminator != "" && me.Discriminator != "0" {
		// Legacy Discord usernames are user#1234. Modern (post-2023)
		// accounts have discriminator "0" — display just the handle.
		display = me.Username + "#" + me.Discriminator
	}
	return me.ID, display, nil
}

// --- Slack ---

type connectSlackRequest struct {
	BotToken string `json:"botToken"`
	AppToken string `json:"appToken"`
}

// handleConnectAgentSlack persists the Slack bot+app token pair after
// validating via auth.test. Slack needs both: bot token (xoxb-...)
// for posting/reading, app token (xapp-...) for Socket Mode WS.
// accountID = team_id so a workspace's events all route to the same
// agent (per-workspace Slack apps are the common shape).
func (s *Server) handleConnectAgentSlack(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	uid, aid, ok := s.resolveChannelBindingScope(w, r, id)
	if !ok {
		return
	}

	var req connectSlackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	botToken := strings.TrimSpace(req.BotToken)
	appToken := strings.TrimSpace(req.AppToken)
	if botToken == "" || appToken == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "botToken and appToken both required"})
		return
	}
	if !strings.HasPrefix(botToken, "xoxb-") {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "botToken should start with xoxb-"})
		return
	}
	if !strings.HasPrefix(appToken, "xapp-") {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "appToken should start with xapp- (app-level token from Settings → Basic Information)"})
		return
	}

	teamID, teamName, botUserID, err := slackAuthTest(botToken)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	// Slack channel rows put both tokens in top-level BotToken/AppToken
	// (the Slack adapter constructor reads them as a pair). Accounts
	// map keyed by team_id so the inbound side resolves owner via
	// LookupChannelByCredential(channel="slack", credKey=teamID).
	cc := config.ChannelConfig{
		Enabled:  true,
		BotToken: botToken,
		AppToken: appToken,
		Accounts: map[string]config.AccountConfig{teamID: {BotToken: botToken}},
	}
	credKey := teamID
	if err := s.assertChannelCredentialUniqueOpt(r, "slack", credKey, "", uid, aid, true); err != nil {
		jsonResponse(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	if err := s.saveChannelRecord(r.Context(), uid, aid, "slack", teamID, true, cc); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := s.appendBinding(r, "", "", config.Binding{
		AgentID: id,
		Match:   config.Match{Channel: "slack", AccountID: teamID},
	}); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.invalidateOwner(uid, aid)
	if ch, err := s.dataStore.LookupChannel(r.Context(), "slack", teamID); err == nil && ch != nil {
		s.hotRegisterChannelRecord(*ch)
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":        true,
		"teamName":  teamName,
		"teamId":    teamID,
		"botUserId": botUserID,
	})
}

// slackAuthTest hits Slack's auth.test endpoint with the bot token to
// validate it AND capture team_id/team_name/bot_user_id in one call.
// Doc: https://api.slack.com/methods/auth.test
func slackAuthTest(botToken string) (teamID, teamName, botUserID string, err error) {
	req, rerr := http.NewRequest("POST", "https://slack.com/api/auth.test", nil)
	if rerr != nil {
		return "", "", "", rerr
	}
	req.Header.Set("Authorization", "Bearer "+botToken)
	resp, derr := http.DefaultClient.Do(req)
	if derr != nil {
		return "", "", "", fmt.Errorf("contact slack: %w", derr)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// Slack always returns 200 + a JSON body; the `ok` field carries
	// the actual result.
	var ok struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Team   string `json:"team"`
		TeamID string `json:"team_id"`
		User   string `json:"user"`
		UserID string `json:"user_id"`
	}
	if jerr := json.Unmarshal(body, &ok); jerr != nil {
		return "", "", "", fmt.Errorf("parse slack response: %w", jerr)
	}
	if !ok.OK {
		msg := ok.Error
		if msg == "" {
			msg = "unknown error"
		}
		return "", "", "", fmt.Errorf("slack rejected token: %s", msg)
	}
	if ok.TeamID == "" {
		return "", "", "", errors.New("slack auth.test returned empty team_id")
	}
	return ok.TeamID, ok.Team, ok.UserID, nil
}

// --- WeChat (iLink) ---
//
// Unlike Telegram/Discord/Slack, WeChat doesn't take a paste-it-in
// token. The user scans a QR code with the WeChat phone app; on
// confirmation iLink hands back a (bot_token, ilink_bot_id,
// ilink_user_id, baseurl) tuple. Two-step flow:
//
//   POST /api/agents/{id}/channels/wechat/login
//     → fetch a QR token from iLink, render as image on the client.
//       Returns {sessionID, qrCode, qrCodeImg}.
//
//   GET  /api/agents/{id}/channels/wechat/login/status?session=<id>
//     → poll iLink's get_qrcode_status one round-trip.
//       Returns {status: wait|scaned|confirmed|expired, connected,
//       accountId?}. On `confirmed`, persists the channel row +
//       binding and hot-registers the adapter, so the next poll the
//       client makes for sandbox/agent state shows the bot live.

const (
	wechatILinkBase    = "https://ilinkai.weixin.qq.com"
	wechatQRCodeURL    = wechatILinkBase + "/ilink/bot/get_bot_qrcode?bot_type=3"
	wechatQRStatusURL  = wechatILinkBase + "/ilink/bot/get_qrcode_status?qrcode="
	wechatStatusWait   = "wait"
	wechatStatusScaned = "scaned"
	wechatStatusOK     = "confirmed"
	wechatStatusExpire = "expired"
)

// wechatLoginSession tracks an in-flight QR scan. Lives in memory only
// — abandoned sessions get GC'd via the TTL sweep on the registry.
// Saving to the store would let polls survive process restart but the
// QR token itself expires in iLink server-side after a couple of
// minutes anyway, so cross-restart resumption isn't worth the
// complexity.
type wechatLoginSession struct {
	qrCode    string // iLink token, used both as polling key + as QR payload
	qrCodeImg string // optional pre-rendered QR image (base64 or URL)
	agentID   string // which agent the credentials should bind to
	userID    string // initiating caller — verified on every status poll
	scope     string // resolved storage scope ("agent" for owner/admin,
	// "user" for non-owner overlay) — captured at start so persist on
	// confirm uses the same scope the caller initially saw.
	scopeID   string
	createdAt time.Time
}

type wechatLoginRegistry struct {
	mu       sync.Mutex
	sessions map[string]*wechatLoginSession
}

var wechatLogins = &wechatLoginRegistry{sessions: map[string]*wechatLoginSession{}}

func (r *wechatLoginRegistry) put(id string, s *wechatLoginSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[id] = s
	// Opportunistic GC: drop sessions older than 5 minutes (QR codes
	// expire well before this server-side; anything older is dead).
	cutoff := time.Now().Add(-5 * time.Minute)
	for k, v := range r.sessions {
		if v.createdAt.Before(cutoff) {
			delete(r.sessions, k)
		}
	}
}

func (r *wechatLoginRegistry) get(id string) *wechatLoginSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[id]
}

func (r *wechatLoginRegistry) delete(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, id)
}

// handleStartAgentWeChatLogin asks iLink for a fresh QR code, registers
// a server-side session keyed by the returned qrCode token, and hands
// the client back what it needs to render the QR image. The actual
// scan happens out-of-band in the user's WeChat phone app; the client
// then polls handleAgentWeChatLoginStatus to drive the state machine.
func (s *Server) handleStartAgentWeChatLogin(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	uid, aid, ok := s.resolveChannelBindingScope(w, r, id)
	if !ok {
		return
	}

	qr, err := wechatFetchQRCode(r.Context())
	if err != nil {
		jsonResponse(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	sessionID := qr.QRCode // iLink's token is unique enough; reuse it
	wechatLogins.put(sessionID, &wechatLoginSession{
		qrCode:    qr.QRCode,
		qrCodeImg: qr.QRCodeImgContent,
		agentID:   id,
		userID:    uid,
		// `scope`/`scopeID` are kept on the struct for backwards compat
		// with the polling handler — repurposed as (userID, agentID) so
		// persist time uses the same ownership the caller saw at start.
		scope:     uid,
		scopeID:   aid,
		createdAt: time.Now(),
	})
	jsonResponse(w, http.StatusOK, map[string]any{
		"sessionId": sessionID,
		"qrCode":    qr.QRCode,
		"qrCodeImg": qr.QRCodeImgContent,
	})
}

// handleAgentWeChatLoginStatus polls iLink for the current scan state
// of this session's QR code. On `confirmed`, persists the channel row
// + binding + hot-registers the adapter — same shape as the Telegram /
// Discord / Slack connect handlers, but driven by the QR status
// machine instead of an immediate token validation.
func (s *Server) handleAgentWeChatLoginStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, _, ok := s.resolveChannelBindingScope(w, r, id); !ok {
		return
	}
	uid := s.effectiveUserID(r)
	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "session required"})
		return
	}
	sess := wechatLogins.get(sessionID)
	if sess == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "session not found or expired"})
		return
	}
	// Cross-tenant guard: don't let one user's poll observe another
	// user's QR session even with a guessed sessionID.
	if sess.userID != uid || sess.agentID != id {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "session not found"})
		return
	}

	status, err := wechatPollQRStatus(r.Context(), sess.qrCode)
	if err != nil {
		jsonResponse(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}

	switch status.Status {
	case wechatStatusOK:
		// User confirmed on phone. iLink returned credentials; persist
		// + bind + hot-register, then drop the in-flight session.
		creds := wechatCredentials{
			BotToken:    status.BotToken,
			ILinkBotID:  status.ILinkBotID,
			BaseURL:     status.BaseURL,
			ILinkUserID: status.ILinkUserID,
		}
		if creds.BotToken == "" || creds.ILinkBotID == "" {
			jsonResponse(w, http.StatusBadGateway, map[string]any{"error": "ilink confirmed without credentials"})
			return
		}
		if err := s.persistWeChatAccount(r, sess.scope, sess.scopeID, id, creds); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		wechatLogins.delete(sessionID)
		jsonResponse(w, http.StatusOK, map[string]any{
			"status":    "confirmed",
			"connected": true,
			"accountId": creds.ILinkBotID,
		})
		return
	case wechatStatusExpire:
		wechatLogins.delete(sessionID)
		jsonResponse(w, http.StatusOK, map[string]any{
			"status":    "expired",
			"connected": false,
		})
		return
	default:
		// wait / scaned / unknown — keep polling. We surface "scaned"
		// distinctly because the UI flips to "扫描完成,请确认" when the
		// user has tapped the QR but not yet pressed confirm on phone.
		jsonResponse(w, http.StatusOK, map[string]any{
			"status":    status.Status,
			"connected": false,
		})
		return
	}
}

// persistWeChatAccount writes a kind=channel row + binding for a
// freshly-confirmed iLink account. The legacy 3rd/4th args (sc,
// scopeID) are now reused to carry (userID, agentID) — the wechat QR
// flow stashes them on wechatLoginSession at start so persist time
// uses the same ownership.
func (s *Server) persistWeChatAccount(r *http.Request, userID, agentIDArg, agentID string, creds wechatCredentials) error {
	cc := config.ChannelConfig{
		Enabled: true,
		Accounts: map[string]config.AccountConfig{
			creds.ILinkBotID: {
				BotToken: creds.BotToken,
				BaseURL:  creds.BaseURL,
				UserID:   creds.ILinkUserID,
			},
		},
	}
	credKey := creds.ILinkBotID
	if err := s.assertChannelCredentialUniqueOpt(r, "wechat", credKey, "", userID, agentIDArg, true); err != nil {
		return err
	}
	if err := s.saveChannelRecord(r.Context(), userID, agentIDArg, "wechat", creds.ILinkBotID, true, cc); err != nil {
		return err
	}
	if err := s.appendBinding(r, "", "", config.Binding{
		AgentID: agentID,
		Match:   config.Match{Channel: "wechat", AccountID: creds.ILinkBotID},
	}); err != nil {
		return err
	}
	s.invalidateOwner(userID, agentIDArg)
	if ch, err := s.dataStore.LookupChannel(r.Context(), "wechat", creds.ILinkBotID); err == nil && ch != nil {
		s.hotRegisterChannelRecord(*ch)
	}
	return nil
}

// --- iLink HTTP helpers (validation-only; running adapter has its own
//     client in internal/channels/wechat.go) ---

type wechatCredentials struct {
	BotToken    string
	ILinkBotID  string
	BaseURL     string
	ILinkUserID string
}

type wechatQRCodeResp struct {
	QRCode           string `json:"qrcode"`
	QRCodeImgContent string `json:"qrcode_img_content"`
}

type wechatQRStatusResp struct {
	Status      string `json:"status"`
	BotToken    string `json:"bot_token"`
	ILinkBotID  string `json:"ilink_bot_id"`
	BaseURL     string `json:"baseurl"`
	ILinkUserID string `json:"ilink_user_id"`
}

func wechatFetchQRCode(ctx context.Context) (*wechatQRCodeResp, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wechatQRCodeURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contact ilink: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ilink qrcode HTTP %d: %s", resp.StatusCode, string(body))
	}
	var out wechatQRCodeResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse ilink qrcode: %w", err)
	}
	if out.QRCode == "" {
		return nil, errors.New("ilink returned empty qrcode")
	}
	return &out, nil
}

// wechatPollQRStatus does ONE round-trip — returns whatever the server
// says right now. We don't long-poll on the server side because the
// upstream endpoint already does (~40s); doing it on every status
// request would mean a tab refresh stalls 40s. The client polls every
// 3s instead, mirroring the workany-web shape.
func wechatPollQRStatus(ctx context.Context, qrcode string) (*wechatQRStatusResp, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wechatQRStatusURL+qrcode, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contact ilink: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ilink status HTTP %d: %s", resp.StatusCode, string(body))
	}
	var out wechatQRStatusResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse ilink status: %w", err)
	}
	return &out, nil
}

// --- Feishu / Feishu ---

type connectFeishuRequest struct {
	AppID             string `json:"appId"`
	AppSecret         string `json:"appSecret"`
	VerificationToken string `json:"verificationToken"`
	EncryptKey        string `json:"encryptKey"`
	UseLongConn       bool   `json:"useLongConn"`
}

// handleConnectAgentFeishu validates a Feishu custom-app credential triple
// by minting a tenant_access_token (proves app_id+app_secret are
// valid) and fetching /bot/v3/info (captures the bot's display name).
// Stores the triple as kind=channel + binding rows + hot-registers
// the adapter.
//
// Storage layout mirrors slack/wechat: credKey = app_id (also the
// accountID), AccountConfig.BotToken = app_secret, AccountConfig.UserID
// = verification_token (matches the field's "extra account-scoped
// identifier" comment).
func (s *Server) handleConnectAgentFeishu(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	uid, aid, ok := s.resolveChannelBindingScope(w, r, id)
	if !ok {
		return
	}

	var req connectFeishuRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	appID := strings.TrimSpace(req.AppID)
	appSecret := strings.TrimSpace(req.AppSecret)
	verificationToken := strings.TrimSpace(req.VerificationToken)
	encryptKey := strings.TrimSpace(req.EncryptKey)
	useLongConn := req.UseLongConn
	if appID == "" || appSecret == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "appId and appSecret required"})
		return
	}
	if !strings.HasPrefix(appID, "cli_") {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "appId should start with cli_ (Feishu custom-app App ID)"})
		return
	}

	botName, botOpenID, err := channels.FeishuValidateCredentials(r.Context(), appID, appSecret)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	cc := config.ChannelConfig{
		Enabled: true,
		Accounts: map[string]config.AccountConfig{
			appID: {
				BotToken:    appSecret,
				UserID:      verificationToken, // see channels/feishu.go field-mapping note
				EncryptKey:  encryptKey,
				UseLongConn: useLongConn,
			},
		},
	}
	credKey := appID
	if err := s.assertChannelCredentialUniqueOpt(r, "feishu", credKey, "", uid, aid, true); err != nil {
		jsonResponse(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	if err := s.saveChannelRecord(r.Context(), uid, aid, "feishu", appID, true, cc); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := s.appendBinding(r, "", "", config.Binding{
		AgentID: id,
		Match:   config.Match{Channel: "feishu", AccountID: appID},
	}); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.invalidateOwner(uid, aid)
	if ch, err := s.dataStore.LookupChannel(r.Context(), "feishu", credKey); err == nil && ch != nil {
		s.hotRegisterChannelRecord(*ch)
	}
	resp := map[string]any{
		"ok":          true,
		"appId":       appID,
		"botName":     botName,
		"botOpenId":   botOpenID,
		"useLongConn": useLongConn,
	}
	// Webhook URL is only meaningful when the user picked the
	// public-URL transport. Long-connection accounts don't need it
	// (no public ingress required) — omit so the UI doesn't show a
	// step the user can't / shouldn't do.
	if !useLongConn {
		resp["webhookUrl"] = feishuWebhookPathFor(r, appID)
	}
	jsonResponse(w, http.StatusOK, resp)
}

// feishuWebhookPathFor builds the URL the user should paste into the
// Feishu Developer Console's "Event Subscriptions → Request URL" field.
// Best-effort — uses the request's Host so reverse-proxied deployments
// surface the user-facing hostname rather than the bind address.
func feishuWebhookPathFor(r *http.Request, appID string) string {
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") == "" {
		scheme = "http"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host + "/api/feishu/webhook/" + appID
}

// --- LINE ---

type connectLINERequest struct {
	ChannelToken  string `json:"channelToken"`
	ChannelSecret string `json:"channelSecret"`
}

// handleConnectAgentLINE validates a LINE Messaging API channel by
// hitting /v2/bot/info with the channel access token. Captures the
// bot's userId (used as accountID) + display name + basicId. Stores
// channel_access_token in AccountConfig.BotToken, channel_secret in
// AccountConfig.UserID (matching the field-mapping convention used by
// the WeChat / Feishu adapters).
//
// channel_secret is technically optional — the adapter can run without
// signature validation — but webhook traffic flows over the open
// internet so we strongly recommend setting it. The connect handler
// accepts an empty string and warns at validation time only if the
// secret is missing.
func (s *Server) handleConnectAgentLINE(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	uid, aid, ok := s.resolveChannelBindingScope(w, r, id)
	if !ok {
		return
	}

	var req connectLINERequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	channelToken := strings.TrimSpace(req.ChannelToken)
	channelSecret := strings.TrimSpace(req.ChannelSecret)
	if channelToken == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "channelToken required"})
		return
	}

	userID, displayName, basicID, err := channels.LINEValidateCredentials(r.Context(), channelToken)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	cc := config.ChannelConfig{
		Enabled: true,
		Accounts: map[string]config.AccountConfig{
			userID: {
				BotToken: channelToken,
				UserID:   channelSecret,
			},
		},
	}
	credKey := userID
	if err := s.assertChannelCredentialUniqueOpt(r, "line", credKey, "", uid, aid, true); err != nil {
		jsonResponse(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	if err := s.saveChannelRecord(r.Context(), uid, aid, "line", userID, true, cc); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := s.appendBinding(r, "", "", config.Binding{
		AgentID: id,
		Match:   config.Match{Channel: "line", AccountID: userID},
	}); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.invalidateOwner(uid, aid)
	if ch, err := s.dataStore.LookupChannel(r.Context(), "line", credKey); err == nil && ch != nil {
		s.hotRegisterChannelRecord(*ch)
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":         true,
		"botUserId":  userID,
		"botName":    displayName,
		"basicId":    basicID,
		"webhookUrl": lineWebhookPathFor(r, userID),
	})
}

// lineWebhookPathFor returns the URL the user pastes into LINE
// Developers Console under "Messaging API → Webhook URL". Same shape
// as feishuWebhookPathFor — surfaces the public-facing host via the
// usual reverse-proxy headers.
func lineWebhookPathFor(r *http.Request, userID string) string {
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") == "" {
		scheme = "http"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host + "/api/line/webhook/" + userID
}

// saveChannelRecord writes a ChannelRecord to the channels table.
// The channels table is the sole authoritative store for channel data.
func (s *Server) saveChannelRecord(ctx context.Context, userID, agentID, channelType, accountID string, enabled bool, cc config.ChannelConfig) error {
	if s.dataStore == nil {
		return nil
	}
	data := channelConfigToData(cc)
	ch := &store.ChannelRecord{
		UserID:    userID,
		AgentID:   agentID,
		Type:      channelType,
		AccountID: accountID,
		Enabled:   enabled,
		BotToken:  cc.BotToken,
		Data:      data,
	}
	// Extract per-account fields when available.
	if acct, ok := cc.Accounts[accountID]; ok {
		if ch.BotToken == "" {
			ch.BotToken = acct.BotToken
		}
		ch.BaseURL = acct.BaseURL
		ch.PlatformUserID = acct.UserID
	}
	if err := s.dataStore.SaveChannel(ctx, ch); err != nil {
		slog.Error("saveChannelRecord failed",
			"type", channelType, "account", accountID, "error", err)
		return fmt.Errorf("save channel record: %w", err)
	}
	return nil
}

// channelConfigToData converts a ChannelConfig to a JSON data map,
// mirroring scope.channelToData but without the import cycle.
func channelConfigToData(c config.ChannelConfig) map[string]interface{} {
	blob, _ := json.Marshal(c)
	var m map[string]interface{}
	_ = json.Unmarshal(blob, &m)
	delete(m, "enabled")
	return m
}

// deleteChannelRecord removes a row from the channels table. Non-fatal
// if it fails — the configs row is still authoritative.
func (s *Server) deleteChannelRecord(ctx context.Context, channelType, accountID string) {
	if s.dataStore == nil {
		return
	}
	ch, err := s.dataStore.LookupChannel(ctx, channelType, accountID)
	if err != nil || ch == nil {
		return
	}
	if err := s.dataStore.DeleteChannel(ctx, ch.ID); err != nil {
		slog.Warn("deleteChannelRecord failed (non-fatal)",
			"type", channelType, "account", accountID, "error", err)
	}
}
