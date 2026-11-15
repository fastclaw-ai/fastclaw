package store

import (
	"context"
	"fmt"
)

// GetScheduler returns the account's scheduler ("" when none).
func (d *DBStore) GetScheduler(ctx context.Context, accountID string) (url, key string, err error) {
	err = d.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT scheduler_url, scheduler_key FROM users WHERE id = %s`, d.ph(1)), accountID).
		Scan(&url, &key)
	if err != nil {
		return "", "", scanErr(err)
	}
	return url, key, nil
}

// SetScheduler sets (or, with an empty url, removes) the account's
// scheduler. The key is stored as given, like the billing hook secret.
func (d *DBStore) SetScheduler(ctx context.Context, accountID, url, key string) error {
	if url == "" {
		key = ""
	}
	res, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE users SET scheduler_url = %s, scheduler_key = %s WHERE id = %s`, d.ph(1), d.ph(2), d.ph(3)),
		url, key, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
