package setup

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Re-connecting a bot (token rotation) must not silently reopen a
// restricted bot to everyone.
func TestSaveChannelRecordKeepsAllowlistOnReconnect(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewDBStore("sqlite", "file:"+filepath.Join(t.TempDir(), "fastclaw.db"))
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	s := NewServer(0)
	s.SetStore(st)

	connect := func(token string) {
		t.Helper()
		if err := s.saveChannelRecord(ctx, "u_owner", "agt_1", "telegram", "family_bot", true,
			config.ChannelConfig{BotToken: token}); err != nil {
			t.Fatalf("saveChannelRecord: %v", err)
		}
	}
	connect("old-token")
	ch, err := st.LookupChannel(ctx, "telegram", "family_bot")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	ch.AllowedUsers = []string{"111"}
	if err := st.SaveChannel(ctx, ch); err != nil {
		t.Fatalf("save allowlist: %v", err)
	}

	connect("new-token")
	ch, err = st.LookupChannel(ctx, "telegram", "family_bot")
	if err != nil {
		t.Fatalf("lookup after reconnect: %v", err)
	}
	if ch.BotToken != "new-token" {
		t.Fatalf("token not rotated: %q", ch.BotToken)
	}
	if !slices.Equal(ch.AllowedUsers, []string{"111"}) {
		t.Fatalf("allowlist after reconnect = %v, want [111]", ch.AllowedUsers)
	}
}

func TestNormalizeAllowedUsers(t *testing.T) {
	got := normalizeAllowedUsers([]string{" 111 ", "", "222", "111", "  "})
	if !slices.Equal(got, []string{"111", "222"}) {
		t.Fatalf("normalizeAllowedUsers = %v", got)
	}
}
