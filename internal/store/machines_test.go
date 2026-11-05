package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// A machines table from before the settings columns gets them, with
// defaults, and migrating again is a no-op.
func TestMachineSettingsMigrationAndUpdate(t *testing.T) {
	ctx := context.Background()
	d, err := NewDBStore("sqlite", "file:"+filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.db.ExecContext(ctx, `CREATE TABLE machines (
		id TEXT PRIMARY KEY, user_id TEXT NOT NULL, agl_machine_id TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '', hostname TEXT NOT NULL DEFAULT '', os TEXT NOT NULL DEFAULT '',
		arch TEXT NOT NULL DEFAULT '', version TEXT NOT NULL DEFAULT '', agents TEXT NOT NULL DEFAULT '[]',
		token_hash TEXT NOT NULL DEFAULT '', last_seen_at BIGINT NOT NULL DEFAULT 0,
		created_at BIGINT NOT NULL DEFAULT 0, revoked_at BIGINT NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.ExecContext(ctx, `INSERT INTO machines (id, user_id, name) VALUES ('mch_old', 'u1', 'old')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := d.Migrate(ctx); err != nil {
			t.Fatalf("migrate #%d: %v", i+1, err)
		}
	}
	old, err := d.GetMachine(ctx, "mch_old")
	if err != nil {
		t.Fatal(err)
	}
	if !old.AllowAgents || old.DefaultAccess != "ask" || old.DefaultCwd != "" {
		t.Fatalf("existing row defaults: %+v", old)
	}

	name, allow, access, cwd := "Work Mac", false, "full", "~/src"
	if err := d.UpdateMachineSettings(ctx, "mch_old", MachineSettings{Name: &name, AllowAgents: &allow, DefaultAccess: &access, DefaultCwd: &cwd}); err != nil {
		t.Fatal(err)
	}
	got, _ := d.GetMachine(ctx, "mch_old")
	if got.Name != name || got.AllowAgents || got.DefaultAccess != access || got.DefaultCwd != cwd {
		t.Fatalf("after update: %+v", got)
	}
	// Partial: only allowAgents changes.
	allow = true
	if err := d.UpdateMachineSettings(ctx, "mch_old", MachineSettings{AllowAgents: &allow}); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetMachine(ctx, "mch_old"); !got.AllowAgents || got.Name != name || got.DefaultAccess != access {
		t.Fatalf("partial update: %+v", got)
	}

	if err := d.RevokeMachine(ctx, "mch_old", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateMachineSettings(ctx, "mch_old", MachineSettings{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked machine updated: %v", err)
	}
	if err := d.UpdateMachineSettings(ctx, "mch_none", MachineSettings{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown machine updated: %v", err)
	}

	// New rows default to allow / ask.
	if err := d.CreateMachine(ctx, &MachineRecord{ID: "mch_new", UserID: "u1", AllowAgents: true}); err != nil {
		t.Fatal(err)
	}
	if m, _ := d.GetMachine(ctx, "mch_new"); !m.AllowAgents || m.DefaultAccess != "ask" {
		t.Fatalf("new row: %+v", m)
	}
}
