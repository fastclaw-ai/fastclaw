package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// userScopedLegacyEntries lists filenames/directories that used to live at the
// top of ~/.fastclaw but are user-scoped. On first run under the new layout,
// they move to ~/.fastclaw/users/local/.
var userScopedLegacyEntries = []string{
	"fastclaw.json",
	"credentials.json",
	"cron_jobs.json",
	"agents",
	"memory",
}

var (
	migrateOnce sync.Once
	migrateErr  error
)

// MigrateLegacyLayout moves pre-multiuser files from ~/.fastclaw/ into
// ~/.fastclaw/users/local/. Global resources (plugins, skills, fastclaw.pid)
// stay at the root. Safe to call repeatedly; runs at most once per process
// and is a no-op if nothing to migrate.
func MigrateLegacyLayout() error {
	migrateOnce.Do(func() {
		migrateErr = doMigrate()
	})
	return migrateErr
}

func doMigrate() error {
	homeDir, err := HomeDir()
	if err != nil {
		return err
	}

	// If the home dir itself does not exist, nothing to do.
	if _, err := os.Stat(homeDir); os.IsNotExist(err) {
		return nil
	}

	userDir, err := UserDir(DefaultUserID)
	if err != nil {
		return err
	}

	// Figure out which legacy entries actually need moving.
	var pending []string
	for _, name := range userScopedLegacyEntries {
		legacyPath := filepath.Join(homeDir, name)
		if _, err := os.Stat(legacyPath); err != nil {
			continue
		}
		newPath := filepath.Join(userDir, name)
		if _, err := os.Stat(newPath); err == nil {
			// Target already exists — assume a previous migration handled it.
			continue
		}
		pending = append(pending, name)
	}

	if len(pending) == 0 {
		return nil
	}

	if err := os.MkdirAll(userDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", userDir, err)
	}

	for _, name := range pending {
		src := filepath.Join(homeDir, name)
		dst := filepath.Join(userDir, name)
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("move %s → %s: %w", src, dst, err)
		}
		slog.Info("migrated legacy entry to user dir", "name", name, "user", DefaultUserID)
	}

	return nil
}
