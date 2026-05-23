package db

import "context"

// RecordDownload stores a single download event.
// title may be empty if the download itself failed before metadata was obtained.
func (d *DB) RecordDownload(ctx context.Context, userID int64, url, title string, success bool) error {
	_, err := d.pool.ExecContext(ctx,
		`INSERT INTO downloads (user_id, url, title, success) VALUES ($1, $2, $3, $4)`,
		userID, url, title, success,
	)
	return err
}
