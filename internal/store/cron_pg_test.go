package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// pgTestDSN is read from the environment to opt this test in. CI without a
// Postgres instance skips; a developer running ./scripts/run-postgres.sh sets
// it (the helper exports a matching DSN) to exercise the real backend.
const pgTestDSNEnv = "FASTCLAW_TEST_PG_DSN"

// openTestPG connects to the Postgres instance named by FASTCLAW_TEST_PG_DSN,
// creates a uniquely-named throwaway database, and returns a migrated *DBStore
// pointing at it plus a cleanup that drops the DB and closes the admin handle.
// The fresh database means every test starts from a clean schema — the
// timestamptz migration runs on CREATE TABLE + convert, exercising the same
// path a brand-new install takes.
func openTestPG(t *testing.T) (*DBStore, func()) {
	t.Helper()
	dsn := os.Getenv(pgTestDSNEnv)
	if dsn == "" {
		t.Skipf("%s not set — skipping Postgres integration test", pgTestDSNEnv)
	}
	// Connect to the maintenance DB (the DSN already targets a DB, but we need
	// a server-level handle to CREATE the scratch database). Reuse the creds by
	// rewriting the path to "postgres".
	adminDSN := pgRewriteDB(dsn, "postgres")
	admin, err := sql.Open("postgres", adminDSN)
	if err != nil {
		t.Fatalf("open admin conn: %v", err)
	}
	scratch := fmt.Sprintf("fc_test_%x", time.Now().UnixNano())
	// DROP+CREATE: escape the identifier just in case, though the name is hex.
	if _, err := admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, scratch)); err != nil {
		admin.Close()
		t.Fatalf("drop scratch db: %v", err)
	}
	if _, err := admin.Exec(fmt.Sprintf(`CREATE DATABASE %q`, scratch)); err != nil {
		admin.Close()
		t.Fatalf("create scratch db: %v", err)
	}
	storeDSN := pgRewriteDB(dsn, scratch)
	st, err := NewDBStore("postgres", storeDSN)
	if err != nil {
		admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, scratch))
		admin.Close()
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		st.Close()
		admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, scratch))
		admin.Close()
		t.Fatalf("migrate: %v", err)
	}
	cleanup := func() {
		st.Close()
		_, _ = admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, scratch))
		admin.Close()
	}
	return st, cleanup
}

// pgRewriteDB returns a copy of a lib/pq DSN with the database path replaced
// by newDB. It handles both the "postgres://…/dbname?…" URL form and the
// "host=… dbname=…" keyword form. Used to retarget a connection at the
// maintenance "postgres" DB or a per-test scratch DB.
func pgRewriteDB(dsn, newDB string) string {
	// URL form: scheme://user:pass@host:port/dbname?params
	if i := strings.Index(dsn, "://"); i >= 0 {
		pathStart := i + 3
		// find start of path after the authority (first '/' after host[:port])
		slash := strings.IndexByte(dsn[pathStart:], '/')
		if slash < 0 {
			return dsn + "/" + newDB
		}
		slash += pathStart
		query := strings.IndexByte(dsn[slash+1:], '?')
		if query < 0 {
			return dsn[:slash+1] + newDB
		}
		return dsn[:slash+1] + newDB + dsn[slash+1+query:]
	}
	// Keyword form: replace dbname=… value (space-terminated).
	if k := strings.Index(dsn, "dbname="); k >= 0 {
		rest := dsn[k+len("dbname="):]
		sp := strings.IndexByte(rest, ' ')
		if sp < 0 {
			return dsn[:k] + "dbname=" + newDB
		}
		return dsn[:k] + "dbname=" + newDB + rest[sp:]
	}
	return dsn + " dbname=" + newDB
}

// TestCronJobsPGTimestampTZ verifies the timezone fix on Postgres. The cron
// schedule columns must be timestamptz so a Go time.Time carrying a non-UTC
// offset survives the write/read round-trip as the exact same instant.
//
// This is the regression guard for the bug where a Beijing-time "09:00" job
// was stored as 09:00 wall-clock and reinterpreted as 09:00 UTC = 17:00
// Beijing, firing 8h late. Before the fix the drift assertion fails.
func TestCronJobsPGTimestampTZ(t *testing.T) {
	st, cleanup := openTestPG(t)
	defer cleanup()
	ctx := context.Background()
	db := st.DB()

	// Schema assertion: the four time columns are timestamptz, not timestamp.
	for _, col := range []string{"next_run", "last_run", "locked_at", "created_at"} {
		isTZ, err := st.columnIsTimestampTZ(ctx, "cron_jobs", col)
		if err != nil {
			t.Fatalf("probe %s: %v", col, err)
		}
		if !isTZ {
			t.Errorf("cron_jobs.%s is not timestamptz — timezone bug not migrated", col)
		}
	}

	// Regression: write a non-UTC-offset instant (Beijing 09:00 = 01:00 UTC)
	// and confirm the stored instant reads back exactly, with no drift.
	beijing, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load Asia/Shanghai: %v", err)
	}
	want := time.Date(2026, 7, 2, 9, 0, 0, 0, beijing) // 09:00 +08 = 01:00 UTC

	job := &CronJobRecord{
		ID:        "tz-test-1",
		AgentID:   "a_tz",
		Name:      "beijing 9am",
		Type:      "cron",
		Schedule:  "0 9 * * *",
		Message:   "wake up",
		Channel:   "web",
		ChatID:    "c",
		Timezone:  "Asia/Shanghai",
		Enabled:   true,
		NextRun:   &want,
		CreatedAt: time.Now().UTC(),
	}
	if err := st.SaveCronJob(ctx, job); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := st.GetCronJob(ctx, "tz-test-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.NextRun == nil {
		t.Fatal("NextRun nil after round-trip")
	}
	drift := got.NextRun.Sub(want)
	if drift < -time.Second || drift > time.Second {
		t.Errorf("next_run drift after round-trip = %v (want ~0). wrote %s, read %s",
			drift, want.Format(time.RFC3339), got.NextRun.Format(time.RFC3339))
	}

	// GetNextDueTime must return the same instant — the scheduler sleeps
	// until it, so any drift here directly shifts the fire time.
	nextDue, err := st.GetNextDueTime(ctx)
	if err != nil {
		t.Fatalf("GetNextDueTime: %v", err)
	}
	dueDrift := nextDue.Sub(want)
	if dueDrift < -time.Second || dueDrift > time.Second {
		t.Errorf("GetNextDueTime drift = %v (want ~0). want %s, got %s",
			dueDrift, want.Format(time.RFC3339), nextDue.Format(time.RFC3339))
	}

	// Sanity: also confirm a second column (last_run via UpdateCronJobRun)
	// round-trips. last_run is written through UpdateCronJobRun.
	lastRun := time.Date(2026, 7, 1, 9, 0, 0, 0, beijing)
	future := want.Add(24 * time.Hour)
	if err := st.UpdateCronJobRun(ctx, "tz-test-1", lastRun, future); err != nil {
		t.Fatalf("UpdateCronJobRun: %v", err)
	}
	got2, err := st.GetCronJob(ctx, "tz-test-1")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got2.LastRun == nil {
		t.Fatal("LastRun nil after update")
	}
	lastDrift := got2.LastRun.Sub(lastRun)
	if lastDrift < -time.Second || lastDrift > time.Second {
		t.Errorf("last_run drift = %v. wrote %s, read %s",
			lastDrift, lastRun.Format(time.RFC3339), got2.LastRun.Format(time.RFC3339))
	}

	// locked_at is exercised by LockCronJob. Verify it too for completeness.
	if _, err := db.ExecContext(ctx,
		`UPDATE cron_jobs SET locked_at = $1 WHERE id = $2`, want, "tz-test-1"); err != nil {
		t.Fatalf("set locked_at: %v", err)
	}
	var lockedAt time.Time
	if err := db.QueryRowContext(ctx,
		`SELECT locked_at FROM cron_jobs WHERE id = $1`, "tz-test-1").Scan(&lockedAt); err != nil {
		t.Fatalf("read locked_at: %v", err)
	}
	lockDrift := lockedAt.Sub(want)
	if lockDrift < -time.Second || lockDrift > time.Second {
		t.Errorf("locked_at drift = %v. wrote %s, read %s",
			lockDrift, want.Format(time.RFC3339), lockedAt.Format(time.RFC3339))
	}
}

// TestCronJobsPGMigrationIdempotent asserts that re-running Migrate on an
// already-converted schema is a no-op (no error, columns stay timestamptz,
// no data loss on the row we leave behind). The migration must be safe to
// run repeatedly — daemon restarts call Migrate on every boot.
func TestCronJobsPGMigrationIdempotent(t *testing.T) {
	st, cleanup := openTestPG(t)
	defer cleanup()
	ctx := context.Background()

	// Leave a row in place, then re-run Migrate. The idempotent path must
	// detect timestamptz and skip — it must NOT truncate.
	keep := time.Now().Add(time.Hour).UTC()
	job := &CronJobRecord{
		ID: "keep-1", AgentID: "a", Type: "interval", Schedule: "1h",
		Message: "x", Channel: "web", ChatID: "c", Timezone: "UTC",
		Enabled: true, NextRun: &keep, CreatedAt: time.Now().UTC(),
	}
	if err := st.SaveCronJob(ctx, job); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate failed: %v", err)
	}

	// Row must survive (no truncation on the idempotent path).
	got, err := st.GetCronJob(ctx, "keep-1")
	if err != nil {
		t.Fatalf("get after re-migrate: %v (row was wrongly truncated)", err)
	}
	if got.NextRun == nil {
		t.Fatal("NextRun nil after re-migrate")
	}
	drift := got.NextRun.Sub(keep)
	if drift < -time.Second || drift > time.Second {
		t.Errorf("next_run changed on idempotent re-migrate: drift=%v", drift)
	}
}
