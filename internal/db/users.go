package db

import (
	"context"
	"fmt"
)

// GlobalStats holds aggregated bot statistics for the admin /stats command.
type GlobalStats struct {
	UniqueUsers      int64
	ActiveToday      int64
	TotalDownloads   int64
	SuccessDownloads int64
	FailDownloads    int64
	CachedVideos     int64 // populated separately from Redis
}

// UpsertUser inserts a new user or updates their display name and last_seen timestamp.
// Empty firstName/username values are ignored to avoid overwriting existing data.
func (d *DB) UpsertUser(ctx context.Context, userID int64, firstName, username string) error {
	_, err := d.pool.ExecContext(ctx, `
		INSERT INTO users (id, first_name, username)
		VALUES ($1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET
			first_name = CASE WHEN EXCLUDED.first_name != '' THEN EXCLUDED.first_name ELSE users.first_name END,
			username   = CASE WHEN EXCLUDED.username   != '' THEN EXCLUDED.username   ELSE users.username   END,
			last_seen  = NOW()
	`, userID, firstName, username)
	return err
}

// UserStat holds a user's download activity summary.
type UserStat struct {
	UserID        int64
	FirstName     string
	Username      string
	DownloadCount int64
}

// GetTopUsers returns up to limit users ordered by successful download count (descending).
// Users with zero successful downloads are excluded.
func (d *DB) GetTopUsers(ctx context.Context, limit int) ([]UserStat, error) {
	rows, err := d.pool.QueryContext(ctx, `
		SELECT u.id, u.first_name, u.username,
		       COUNT(dl.id) FILTER (WHERE dl.success = true) AS download_count
		FROM users u
		LEFT JOIN downloads dl ON dl.user_id = u.id
		GROUP BY u.id, u.first_name, u.username
		HAVING COUNT(dl.id) FILTER (WHERE dl.success = true) > 0
		ORDER BY download_count DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("query top users: %w", err)
	}
	defer rows.Close()

	var stats []UserStat
	for rows.Next() {
		var s UserStat
		if err := rows.Scan(&s.UserID, &s.FirstName, &s.Username, &s.DownloadCount); err != nil {
			return nil, fmt.Errorf("scan user stat: %w", err)
		}
		stats = append(stats, s)
	}
	return stats, rows.Err()
}

// GetStats returns aggregated statistics from the database.
// CachedVideos is not populated here — the caller must fill it from Redis.
func (d *DB) GetStats(ctx context.Context) (GlobalStats, error) {
	var s GlobalStats

	if err := d.pool.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users`,
	).Scan(&s.UniqueUsers); err != nil {
		return s, fmt.Errorf("count users: %w", err)
	}

	if err := d.pool.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT user_id) FROM downloads WHERE created_at >= date_trunc('day', NOW())`,
	).Scan(&s.ActiveToday); err != nil {
		return s, fmt.Errorf("count active today: %w", err)
	}

	if err := d.pool.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE success = true),
			COUNT(*) FILTER (WHERE success = false)
		FROM downloads
	`).Scan(&s.TotalDownloads, &s.SuccessDownloads, &s.FailDownloads); err != nil {
		return s, fmt.Errorf("count downloads: %w", err)
	}

	return s, nil
}
