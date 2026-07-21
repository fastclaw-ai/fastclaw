package gateway

import (
	"context"
	"errors"
	"log/slog"

	"github.com/fastclaw-ai/fastclaw/internal/channels"
	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// InvalidateUser drops a user's cached UserSpace so the next access reloads
// it from the DB. Called by admin handlers after agent / provider /
// channel writes so changes take effect without a process restart.
func (g *Gateway) InvalidateUser(userID string) {
	if g.users == nil || userID == "" {
		return
	}
	g.users.invalidate(userID)
	slog.Info("user space invalidated; will reload on next access", "user", userID)
}

// InvalidateAgent drops every cached UserSpace that currently holds the
// given agent — owner's space (always preloaded by loadUserSpace) plus
// any foreign space that lazy-attached via EnsureAgent (super_admin
// browsing, public-link viewer, apikey caller). Used after agent-scope
// settings / provider writes so non-owner viewers don't keep stale
// rc.Model / providers until the 30-minute idle eviction kicks in.
func (g *Gateway) InvalidateAgent(agentID string) {
	if g.users == nil || agentID == "" {
		return
	}
	for _, sp := range g.users.all() {
		if sp.Agents == nil {
			continue
		}
		if sp.Agents.AgentByID(agentID) != nil {
			g.users.invalidate(sp.UserID)
		}
	}
	slog.Info("agent invalidated; affected user spaces dropped", "agent", agentID)
}

// ReloadAgents is kept on Gateway for callers (admin API after agent CRUD)
// that want to force a refresh of every loaded space. The new model lazy-
// loads on every auth, so the practical effect is just dropping caches.
func (g *Gateway) ReloadAgents() error {
	if g.users == nil {
		return nil
	}
	for _, sp := range g.users.all() {
		g.users.invalidate(sp.UserID)
	}
	slog.Info("hot-reload: invalidated all loaded user spaces")
	return nil
}

// ReloadSandbox rebuilds the gateway-wide sandbox executor pool from the
// current system-scope sandbox config. It is called after the admin/settings
// UI saves sandbox changes so exec tools can pick them up without a process
// restart.
func (g *Gateway) ReloadSandbox() error {
	if g.store == nil {
		return nil
	}
	cfg := readSystemSandboxCfg(g.store)
	next := buildSystemSandboxPool(cfg, g.workspace)

	g.mu.Lock()
	prev := g.sandboxPool
	g.sandboxPool = next
	if g.projectRuntime != nil {
		g.projectRuntime.SetSandboxPool(cfg.Backend, next)
	}
	evicted := 0
	if g.users != nil {
		evicted = g.users.setSystemSandboxPool(next)
	}
	g.mu.Unlock()

	if prev != nil && prev != next {
		prev.CloseAll()
	}
	slog.Info("hot-reload: sandbox executor pool rebuilt",
		"backend", sandboxPoolBackend(next),
		"evictedUserSpaces", evicted)
	return nil
}

func sandboxPoolBackend(p sandbox.ExecutorPool) string {
	if p == nil {
		return ""
	}
	return p.Backend()
}

// reloadAgentForUser is a finer-grained invalidate used by setup handlers
// after a single user mutates their own agents.
func (g *Gateway) reloadAgentForUser(_ context.Context, userID string) {
	g.InvalidateUser(userID)
}

// RegisterChannelFromConfig hot-starts a channel adapter for a freshly-
// saved configs row without restarting the process. Called by the
// dashboard's per-agent channel handlers after a successful save so a
// new Telegram bot starts polling immediately. Idempotent at the
// chanMgr level — a re-save of the same accountID swaps the adapter.
func (g *Gateway) RegisterChannelFromConfig(rec store.ConfigRecord) error {
	if g.chanMgr == nil || g.bus == nil {
		return nil
	}
	return registerChannelInstance(rec, g.bus, g.chanMgr, g.store, true)
}

// RegisterChannel hot-starts a channel adapter from a ChannelRecord.
// New-table equivalent of RegisterChannelFromConfig.
func (g *Gateway) RegisterChannel(rec store.ChannelRecord) error {
	if g.chanMgr == nil || g.bus == nil {
		return nil
	}
	return registerChannelFromRecord(rec, g.bus, g.chanMgr, g.store, true)
}

// UnregisterChannel removes a channel from the routing table and cancels the
// adapter's active child context. Inbound routing stops immediately.
func (g *Gateway) UnregisterChannel(channelType, accountID string) {
	if g.chanMgr == nil {
		return
	}
	g.chanMgr.Unregister(channelType, accountID)
}

// DispatchLINEWebhook hands a raw LINE webhook POST body off to the
// adapter for accountID. Signature is the value of the `x-line-signature`
// header — the adapter checks it against HMAC-SHA256(channel_secret,
// body). Returns the response body + status the HTTP handler should
// write back; LINE retries on non-2xx.
func (g *Gateway) DispatchLINEWebhook(accountID string, body []byte, signature string) (responseBody []byte, status int, err error) {
	if g.chanMgr == nil {
		return nil, 503, errors.New("channel manager not running")
	}
	ch := g.chanMgr.Get("line", accountID)
	if ch == nil {
		return nil, 404, errors.New("no line channel for account")
	}
	ln, ok := ch.(*channels.LINE)
	if !ok {
		return nil, 500, errors.New("registered channel is not a LINE adapter")
	}
	return ln.HandleWebhook(body, signature)
}

// DispatchFeishuWebhook hands a raw Feishu webhook POST body off to the
// adapter registered for accountID (= Feishu App ID in URL path). The
// adapter handles URL-verification challenges + im.message.receive_v1
// dispatch + token validation; the HTTP handler just relays response
// body / status. Returns ErrUnknownAccount when no adapter is
// registered (Feishu configured to push to a non-existent app id).
func (g *Gateway) DispatchFeishuWebhook(accountID string, body []byte) (responseBody []byte, status int, err error) {
	if g.chanMgr == nil {
		return nil, 503, errors.New("channel manager not running")
	}
	ch := g.chanMgr.Get("feishu", accountID)
	if ch == nil {
		return nil, 404, errors.New("no feishu channel for account")
	}
	lk, ok := ch.(*channels.Feishu)
	if !ok {
		return nil, 500, errors.New("registered channel is not a Feishu adapter")
	}
	return lk.HandleWebhook(body)
}
