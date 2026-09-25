package gateway

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func TestAdmitInboundAppliesChannelAllowlist(t *testing.T) {
	db, err := store.NewDBStore("sqlite", "file:"+t.TempDir()+"/allowlist.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, ch := range []*store.ChannelRecord{
		{UserID: "u_owner", AgentID: "agt_1", Type: "telegram", AccountID: "open_bot", Enabled: true},
		{UserID: "u_owner", AgentID: "agt_1", Type: "telegram", AccountID: "family_bot", Enabled: true,
			AllowedUsers: []string{"111", "222"}},
	} {
		if err := db.SaveChannel(ctx, ch); err != nil {
			t.Fatalf("save channel: %v", err)
		}
	}
	g := &Gateway{store: db}

	cases := []struct {
		name    string
		account string
		sender  string
		want    bool
	}{
		{"open bot admits anyone", "open_bot", "999", true},
		{"allowlisted sender admitted", "family_bot", "222", true},
		{"stranger dropped", "family_bot", "999", false},
		{"empty sender dropped", "family_bot", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, ok := g.admitInbound(ctx, bus.InboundMessage{
				Channel: "telegram", AccountID: tc.account, UserID: tc.sender, ChatID: tc.sender,
			})
			if ok != tc.want {
				t.Fatalf("admitInbound ok = %v, want %v", ok, tc.want)
			}
			if ok && info.ownerID != "u_owner" {
				t.Fatalf("ownerID = %q, want u_owner", info.ownerID)
			}
		})
	}
}
