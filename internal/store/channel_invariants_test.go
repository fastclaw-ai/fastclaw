package store

import (
	"context"
	"testing"
)

func TestSaveChannelForcesWeComSharedIdentityOff(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	ctx := context.Background()
	ch := &ChannelRecord{
		Type:           "wecom",
		AccountID:      "bot-shared",
		SharedIdentity: true,
	}
	if err := db.SaveChannel(ctx, ch); err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}

	got, err := db.LookupChannel(ctx, "wecom", "bot-shared")
	if err != nil {
		t.Fatalf("LookupChannel: %v", err)
	}
	if got.SharedIdentity {
		t.Fatal("WeCom persisted sharedIdentity=true")
	}
}

func TestSaveChannelPreservesSharedIdentityForOtherChannels(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	ctx := context.Background()
	ch := &ChannelRecord{
		Type:           "telegram",
		AccountID:      "bot-shared",
		SharedIdentity: true,
	}
	if err := db.SaveChannel(ctx, ch); err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}

	got, err := db.LookupChannel(ctx, "telegram", "bot-shared")
	if err != nil {
		t.Fatalf("LookupChannel: %v", err)
	}
	if !got.SharedIdentity {
		t.Fatal("non-WeCom channel lost sharedIdentity=true")
	}
}
