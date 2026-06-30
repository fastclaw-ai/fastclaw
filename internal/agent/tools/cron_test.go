package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func TestCreateCronJobPersistsMessageAccountID(t *testing.T) {
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	r := NewRegistry(t.TempDir(), t.TempDir())
	r.SetOwnerUserID("user-1")
	RegisterCronTools(r, db, "user-1", "agent-1")

	// Per-turn message context (channel/account/chatID) + chatter now
	// flow through TurnContext on ctx — the same path the agent loop
	// uses — instead of shared registry fields.
	ctx := WithTurnContext(context.Background(), &TurnContext{
		Channel:       "telegram",
		AccountID:     "dclaw_official_bot",
		ChatID:        "8169894742",
		ChatterUserID: "user-1",
	})

	args, err := json.Marshal(createCronJobArgs{
		Name:     "telegram reminder",
		Type:     "once",
		Schedule: time.Now().Add(time.Hour).Format(time.RFC3339),
		Message:  "提醒我",
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	if _, err := r.Execute(ctx, "create_cron_job", string(args)); err != nil {
		t.Fatalf("create cron job: %v", err)
	}

	jobs, err := db.ListCronJobsByAgent(context.Background(), "agent-1")
	if err != nil {
		t.Fatalf("list cron jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d cron jobs, want 1", len(jobs))
	}
	if got := jobs[0].AccountID; got != "dclaw_official_bot" {
		t.Fatalf("AccountID = %q, want dclaw_official_bot", got)
	}
	if got := jobs[0].Channel; got != "telegram" {
		t.Fatalf("Channel = %q, want telegram", got)
	}
	if got := jobs[0].ChatID; got != "8169894742" {
		t.Fatalf("ChatID = %q, want 8169894742", got)
	}
}
