package gateway

import (
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/channels"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func TestRegisterWeComChannelFromRecord(t *testing.T) {
	mb := bus.New()
	mgr := channels.NewManager(mb)
	rec := store.ChannelRecord{
		Type:      "wecom",
		AccountID: "bot-1",
		Enabled:   true,
		BotToken:  "fixture-secret",
		Data: map[string]any{
			"accounts": map[string]any{
				"bot-1": map[string]any{"botToken": "fixture-secret"},
			},
		},
	}

	if err := registerChannelFromRecord(rec, mb, mgr, nil, false); err != nil {
		t.Fatalf("registerChannelFromRecord: %v", err)
	}
	got := mgr.Get("wecom", "bot-1")
	if got == nil {
		t.Fatal("wecom channel was not registered")
	}
	if _, ok := got.(*channels.WeCom); !ok {
		t.Fatalf("registered channel type = %T, want *channels.WeCom", got)
	}
	if got.AccountID() != "bot-1" {
		t.Fatalf("AccountID = %q, want bot-1", got.AccountID())
	}
}

func TestRegisterDisabledWeComChannelIsNoop(t *testing.T) {
	mb := bus.New()
	mgr := channels.NewManager(mb)
	rec := store.ChannelRecord{
		Type:      "wecom",
		AccountID: "bot-disabled",
		Enabled:   false,
		BotToken:  "fixture-secret",
		Data: map[string]any{
			"accounts": map[string]any{
				"bot-disabled": map[string]any{"botToken": "fixture-secret"},
			},
		},
	}

	if err := registerChannelFromRecord(rec, mb, mgr, nil, false); err != nil {
		t.Fatalf("registerChannelFromRecord: %v", err)
	}
	if got := mgr.Get("wecom", "bot-disabled"); got != nil {
		t.Fatalf("disabled channel registered as %T", got)
	}
}

func TestRegisterWeComChannelFromLegacyConfig(t *testing.T) {
	mb := bus.New()
	mgr := channels.NewManager(mb)
	cc := config.ChannelConfig{
		Enabled: true,
		Accounts: map[string]config.AccountConfig{
			"bot-legacy": {BotToken: "fixture-secret"},
		},
	}
	rec := store.ConfigRecord{Name: "wecom", Enabled: true, Data: channelConfigData(t, cc)}

	if err := registerChannelInstance(rec, mb, mgr, nil, false); err != nil {
		t.Fatalf("registerChannelInstance: %v", err)
	}
	if got := mgr.Get("wecom", "bot-legacy"); got == nil {
		t.Fatal("legacy wecom config was not registered")
	}
}

func channelConfigData(t *testing.T, cc config.ChannelConfig) map[string]any {
	t.Helper()
	return map[string]any{
		"accounts": map[string]any{
			"bot-legacy": map[string]any{"botToken": cc.Accounts["bot-legacy"].BotToken},
		},
	}
}
