// Package db manages the PostgreSQL connection and schema for the bot.
package db

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/lib/pq"
)

// DB wraps a PostgreSQL connection pool with high-level bot operations.
type DB struct {
	pool *sql.DB
}

// New opens a PostgreSQL connection, verifies it with a ping, and runs schema migrations.
func New(ctx context.Context, dsn string) (*DB, error) {
	pool, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	pool.SetMaxOpenConns(10)
	pool.SetMaxIdleConns(5)

	if err := pool.PingContext(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}

	d := &DB{pool: pool}
	if err := d.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

// Ping checks the database connectivity.
func (d *DB) Ping(ctx context.Context) error {
	return d.pool.PingContext(ctx)
}

// Close closes the underlying connection pool.
func (d *DB) Close() error {
	return d.pool.Close()
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
    id         BIGINT       PRIMARY KEY,
    first_name TEXT         NOT NULL DEFAULT '',
    username   TEXT         NOT NULL DEFAULT '',
    first_seen TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    last_seen  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS admins (
    user_id  BIGINT       PRIMARY KEY,
    added_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS downloads (
    id         BIGSERIAL    PRIMARY KEY,
    user_id    BIGINT       NOT NULL,
    url        TEXT         NOT NULL,
    title      TEXT         NOT NULL DEFAULT '',
    success    BOOLEAN      NOT NULL,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_downloads_user_id    ON downloads(user_id);
CREATE INDEX IF NOT EXISTS idx_downloads_created_at ON downloads(created_at);
CREATE INDEX IF NOT EXISTS idx_downloads_success    ON downloads(success);
`

func (d *DB) migrate(ctx context.Context) error {
	_, err := d.pool.ExecContext(ctx, schema)
	return err
}
