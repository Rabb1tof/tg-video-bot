package db

import (
	"context"
	"fmt"
)

// IsAdmin returns true if userID exists in the admins table.
func (d *DB) IsAdmin(ctx context.Context, userID int64) bool {
	var count int
	err := d.pool.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM admins WHERE user_id = $1`, userID,
	).Scan(&count)
	return err == nil && count > 0
}

// AddAdmin inserts userID into the admins table. Does nothing if already present.
func (d *DB) AddAdmin(ctx context.Context, userID int64) error {
	_, err := d.pool.ExecContext(ctx,
		`INSERT INTO admins (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING`, userID,
	)
	return err
}

// RemoveAdmin removes userID from the admins table.
func (d *DB) RemoveAdmin(ctx context.Context, userID int64) error {
	_, err := d.pool.ExecContext(ctx,
		`DELETE FROM admins WHERE user_id = $1`, userID,
	)
	return err
}

// GetAdmins returns all admin user IDs from the database, ordered by insertion time.
func (d *DB) GetAdmins(ctx context.Context) ([]int64, error) {
	rows, err := d.pool.QueryContext(ctx, `SELECT user_id FROM admins ORDER BY added_at`)
	if err != nil {
		return nil, fmt.Errorf("query admins: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan admin: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
