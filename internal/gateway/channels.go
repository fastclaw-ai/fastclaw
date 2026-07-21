package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/channels"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// storeLeaser adapts store.Store to channels.Leaser. The method names
// differ (Acquire/Renew/Release on the channels side, ...ChannelLease
// on the store side) so the store can grow other lease kinds without
// renaming. Lives here rather than in the store package to keep the
// store interface IM-agnostic.
type storeLeaser struct{ st store.Store }

func (s storeLeaser) Acquire(ctx context.Context, channel, accountID, holderID string, ttl time.Duration) (bool, error) {
	return s.st.AcquireChannelLease(ctx, channel, accountID, holderID, ttl)
}
func (s storeLeaser) Renew(ctx context.Context, channel, accountID, holderID string, ttl time.Duration) (bool, error) {
	return s.st.RenewChannelLease(ctx, channel, accountID, holderID, ttl)
}
func (s storeLeaser) Release(ctx context.Context, channel, accountID, holderID string) error {
	return s.st.ReleaseChannelLease(ctx, channel, accountID, holderID)
}

// registerChannelInstance starts a channel adapter for one kind="channel"
// row in configs. The row's credential_key is what processInbound
// reverse-looks up via Store.LookupChannelByCredential to find the owner —
// keep it stable (e.g. tail of bot token, app id).
//
// `hot` controls whether the bot adapter's polling goroutine is launched
// immediately. Boot-time registration uses Register (Manager.Start
// fans out everything in one go); dashboard mutations use
// RegisterAndStart so a freshly-saved bot starts receiving updates
// without restarting the process.
func registerChannelInstance(rec store.ConfigRecord, mb *bus.MessageBus, chanMgr *channels.Manager, st store.Store, hot bool) error {
	if !rec.Enabled {
		return nil
	}
	cc := decodeChannelConfig(rec)
	switch rec.Name {
	case "telegram":
		return registerTelegramChannels(cc, mb, chanMgr, hot)
	case "discord":
		return registerDiscordChannels(cc, mb, chanMgr, hot)
	case "slack":
		return registerSlackChannels(cc, mb, chanMgr, hot)
	case "line":
		return registerLINEChannels(cc, mb, chanMgr, hot)
	case "wechat":
		return registerWeChatChannels(rec, cc, mb, chanMgr, st, hot)
	case "feishu":
		return registerFeishuChannels(cc, mb, chanMgr, hot)
	case "wecom":
		return registerWeComChannels(cc, mb, chanMgr, hot)
	}
	return nil
}

// registerChannelFromRecord starts a channel adapter from a ChannelRecord.
// This is the new-table equivalent of registerChannelInstance.
func registerChannelFromRecord(rec store.ChannelRecord, mb *bus.MessageBus, chanMgr *channels.Manager, st store.Store, hot bool) error {
	if !rec.Enabled {
		return nil
	}
	cc := decodeChannelFromRecord(rec)
	switch rec.Type {
	case "telegram":
		return registerTelegramChannels(cc, mb, chanMgr, hot)
	case "discord":
		return registerDiscordChannels(cc, mb, chanMgr, hot)
	case "slack":
		return registerSlackChannels(cc, mb, chanMgr, hot)
	case "line":
		return registerLINEChannels(cc, mb, chanMgr, hot)
	case "wechat":
		cfgRec := channelRecordToConfigRecord(rec)
		return registerWeChatChannels(cfgRec, cc, mb, chanMgr, st, hot)
	case "feishu":
		return registerFeishuChannels(cc, mb, chanMgr, hot)
	case "wecom":
		return registerWeComChannels(cc, mb, chanMgr, hot)
	}
	return nil
}

// channelRecordToConfigRecord builds a ConfigRecord from a ChannelRecord
// for backward compatibility with registerWeChatChannels which needs
// the ConfigRecord shape for its on-expired callback.
func channelRecordToConfigRecord(ch store.ChannelRecord) store.ConfigRecord {
	return store.ConfigRecord{
		ID:            ch.ID,
		Kind:          store.KindChannel,
		UserID:        ch.UserID,
		AgentID:       ch.AgentID,
		Name:          ch.Type,
		Enabled:       ch.Enabled,
		CredentialKey: ch.AccountID,
		Data:          ch.Data,
		CreatedAt:     ch.CreatedAt,
		UpdatedAt:     ch.UpdatedAt,
	}
}

// decodeChannelFromRecord converts a ChannelRecord into a ChannelConfig.
// It reads from both the top-level fields and the Data JSON blob.
func decodeChannelFromRecord(rec store.ChannelRecord) config.ChannelConfig {
	cc := config.ChannelConfig{Enabled: rec.Enabled}
	// First, decode from the Data blob (preserves the Accounts map, etc.)
	if blob, err := json.Marshal(rec.Data); err == nil && len(blob) > 0 {
		_ = json.Unmarshal(blob, &cc)
	}
	cc.Enabled = rec.Enabled
	// Override BotToken from top-level field when present.
	if rec.BotToken != "" {
		cc.BotToken = rec.BotToken
	}
	return cc
}

// register adds an adapter to the manager via the appropriate path
// (boot-time Register vs hot RegisterAndStart). Keeps the per-channel
// case branches tidy.
func register(chanMgr *channels.Manager, ch channels.Channel, hot bool) {
	if hot {
		chanMgr.RegisterAndStart(ch)
		return
	}
	chanMgr.Register(ch)
}

// registerSingleton is the polling-channel variant: every replica
// running this binary will share the (channel, accountID) lease and
// only the leaseholder's Start runs. Use for Telegram long-poll,
// WeChat iLink long-poll, Discord/Slack WS, and Feishu long-conn —
// anything where a second concurrent client would deliver duplicate
// inbound. Webhook-only adapters (LINE, Feishu webhook mode) and
// in-process fanout (Web) stay on plain register.
func registerSingleton(chanMgr *channels.Manager, ch channels.Channel, hot bool) {
	if hot {
		chanMgr.RegisterSingletonAndStart(ch)
		return
	}
	chanMgr.RegisterSingleton(ch)
}

func decodeChannelConfig(rec store.ConfigRecord) config.ChannelConfig {
	cc := config.ChannelConfig{Enabled: rec.Enabled}
	if blob, err := json.Marshal(rec.Data); err == nil && len(blob) > 0 {
		_ = json.Unmarshal(blob, &cc)
	}
	cc.Enabled = rec.Enabled
	return cc
}

func registerTelegramChannels(chCfg config.ChannelConfig, mb *bus.MessageBus, chanMgr *channels.Manager, hot bool) error {
	if len(chCfg.Accounts) == 0 {
		if !chanMgr.ClaimTelegramToken(chCfg.BotToken) {
			slog.Warn("telegram token already registered in this process, skipping duplicate")
			return nil
		}
		tg, err := channels.NewTelegram(chCfg.BotToken, "", mb)
		if err != nil {
			return err
		}
		registerSingleton(chanMgr, tg, hot)
		return nil
	}
	for accountID, acct := range chCfg.Accounts {
		token := acct.BotToken
		if token == "" {
			token = chCfg.BotToken
		}
		if !chanMgr.ClaimTelegramToken(token) {
			slog.Warn("telegram token already registered in this process, skipping duplicate", "account", accountID)
			continue
		}
		tg, err := channels.NewTelegram(token, accountID, mb)
		if err != nil {
			return err
		}
		registerSingleton(chanMgr, tg, hot)
	}
	return nil
}

func registerDiscordChannels(chCfg config.ChannelConfig, mb *bus.MessageBus, chanMgr *channels.Manager, hot bool) error {
	if len(chCfg.Accounts) == 0 {
		dc, err := channels.NewDiscord(chCfg.BotToken, "", mb)
		if err != nil {
			return err
		}
		registerSingleton(chanMgr, dc, hot)
		return nil
	}
	for accountID, acct := range chCfg.Accounts {
		token := acct.BotToken
		if token == "" {
			token = chCfg.BotToken
		}
		dc, err := channels.NewDiscord(token, accountID, mb)
		if err != nil {
			return err
		}
		registerSingleton(chanMgr, dc, hot)
	}
	return nil
}

func registerSlackChannels(chCfg config.ChannelConfig, mb *bus.MessageBus, chanMgr *channels.Manager, hot bool) error {
	if len(chCfg.Accounts) == 0 {
		sl, err := channels.NewSlack(chCfg.BotToken, chCfg.AppToken, "", mb)
		if err != nil {
			return err
		}
		registerSingleton(chanMgr, sl, hot)
		return nil
	}
	for accountID, acct := range chCfg.Accounts {
		botToken := acct.BotToken
		if botToken == "" {
			botToken = chCfg.BotToken
		}
		sl, err := channels.NewSlack(botToken, chCfg.AppToken, accountID, mb)
		if err != nil {
			return err
		}
		registerSingleton(chanMgr, sl, hot)
	}
	return nil
}

func registerLINEChannels(chCfg config.ChannelConfig, mb *bus.MessageBus, chanMgr *channels.Manager, hot bool) error {
	// LINE row carries one or more (channel_access_token, channel_secret)
	// pairs keyed by bot userId. AccountConfig.BotToken is the channel
	// access token; AccountConfig.UserID is the channel secret (used
	// for inbound HMAC verification — see channels/line.go field-mapping
	// note).
	for accountID, acct := range chCfg.Accounts {
		token := acct.BotToken
		if token == "" {
			token = chCfg.BotToken
		}
		ln, err := channels.NewLINE(token, acct.UserID, accountID, mb)
		if err != nil {
			return err
		}
		register(chanMgr, ln, hot)
	}
	return nil
}

func registerFeishuChannels(chCfg config.ChannelConfig, mb *bus.MessageBus, chanMgr *channels.Manager, hot bool) error {
	// Feishu is multi-account: one row carries one or more (app_id,
	// app_secret, verification_token) triples keyed by app_id. No
	// legacy single-bot fallback — the per-account map is the only
	// shape produced by the connect handler.
	for accountID, acct := range chCfg.Accounts {
		secret := acct.BotToken
		if secret == "" {
			secret = chCfg.BotToken
		}
		// AccountConfig.UserID carries the verification token (see
		// channels/feishu.go for the field-mapping rationale).
		// AccountConfig.EncryptKey is set when the user enabled 加密
		// 策略 in the Feishu console; empty = plaintext webhook bodies.
		// AccountConfig.UseLongConn switches the adapter to outbound
		// WebSocket mode (no public URL needed); when true the
		// verificationToken/encryptKey fields are unused.
		lk, err := channels.NewFeishu(accountID, secret, acct.UserID, acct.EncryptKey, acct.UseLongConn, accountID, mb)
		if err != nil {
			return err
		}
		// Long-conn opens a Feishu WebSocket — two replicas would
		// both subscribe to im.message.receive_v1 and the bot owner
		// would see every reply twice. Webhook mode receives via an
		// HTTP route and is already idempotent at the gateway entry
		// (HandleWebhook is called once per HTTP POST), so it skips
		// the lease.
		if acct.UseLongConn {
			registerSingleton(chanMgr, lk, hot)
		} else {
			register(chanMgr, lk, hot)
		}
	}
	return nil
}

func registerWeComChannels(chCfg config.ChannelConfig, mb *bus.MessageBus, chanMgr *channels.Manager, hot bool) error {
	// WeCom Intelligent Bots use one authenticated WebSocket per Bot ID.
	// The account map key is therefore both the platform Bot ID and the
	// stable routing account ID persisted in the channels table.
	for botID, acct := range chCfg.Accounts {
		secret := acct.BotToken
		if secret == "" {
			secret = chCfg.BotToken
		}
		wc, err := channels.NewWeCom(channels.WeComOptions{
			BotID:     botID,
			Secret:    secret,
			AccountID: botID,
		}, mb)
		if err != nil {
			return err
		}
		registerSingleton(chanMgr, wc, hot)
	}
	return nil
}

func registerWeChatChannels(rec store.ConfigRecord, chCfg config.ChannelConfig, mb *bus.MessageBus, chanMgr *channels.Manager, st store.Store, hot bool) error {
	// WeChat is multi-account by design — every QR scan mints a new
	// (botToken, ilink_user_id, baseURL) triple keyed under a fresh
	// accountID. The legacy "no Accounts map → single bot from
	// top-level BotToken" shape doesn't apply (we never have a
	// usable top-level config; the per-account fields BaseURL +
	// UserID are required). So skip the empty-Accounts fallback.
	for accountID, acct := range chCfg.Accounts {
		token := acct.BotToken
		if token == "" {
			token = chCfg.BotToken
		}
		wc, err := channels.NewWeChat(token, acct.BaseURL, acct.UserID, accountID, mb)
		if err != nil {
			return err
		}
		// On confirmed token-expiry the adapter exits; clean up the
		// configs row so the next process restart doesn't re-register
		// a known-dead bot (which would log the same warning again on
		// boot). The user has to rescan the QR through the dashboard
		// — that flow re-creates the Accounts entry from scratch.
		if st != nil {
			rowID := rec.ID
			wc.SetOnExpired(func(deadAccount string) {
				if err := purgeWeChatAccount(st, rowID, deadAccount); err != nil {
					slog.Warn("wechat token-expired cleanup failed",
						"account", deadAccount, "error", err)
				}
				chanMgr.Unregister("wechat", deadAccount)
			})
		}
		registerSingleton(chanMgr, wc, hot)
	}
	return nil
}

// purgeWeChatAccount removes one account from the configs row's
// Accounts map. If the row is left empty after the removal the whole
// row gets deleted. Runs in the adapter's polling goroutine so the
// HTTP request ctx isn't available — use a fresh background ctx.
//
// Idempotent: ErrNotFound on the GetConfig lookup means the row is
// already gone (dashboard-side disconnect, or a sibling account's
// purge that emptied the row first) — that's success, not an error.
func purgeWeChatAccount(st store.Store, rowID, deadAccount string) error {
	ctx := context.Background()
	rec, err := st.GetConfig(ctx, rowID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if rec == nil {
		return nil
	}
	cc := config.ChannelConfig{Enabled: rec.Enabled}
	if blob, mErr := json.Marshal(rec.Data); mErr == nil && len(blob) > 0 {
		_ = json.Unmarshal(blob, &cc)
	}
	if _, ok := cc.Accounts[deadAccount]; !ok {
		return nil
	}
	delete(cc.Accounts, deadAccount)
	if len(cc.Accounts) == 0 {
		return st.DeleteConfig(ctx, rec.ID)
	}
	blob, mErr := json.Marshal(cc)
	if mErr != nil {
		return mErr
	}
	var data map[string]interface{}
	if mErr := json.Unmarshal(blob, &data); mErr != nil {
		return mErr
	}
	rec.Data = data
	return st.SaveConfig(ctx, rec)
}
