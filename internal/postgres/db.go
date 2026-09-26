// Package postgres is Gitman's only persistence layer. Every part of Gitman that
// touches state that must survive a restart goes through it.
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB is Gitman's PostgreSQL connection pool.
type DB struct {
	Pool *pgxpool.Pool
}

// Options tunes a connection pool for the process that owns it. The web
// process uses the pgx default; a worker sets a small fixed cap, since
// it runs one run at a time; a Git hook, which runs once per push and
// exits, needs only a couple of connections.
type Options struct {
	// MaxConns caps the pool size. Zero keeps the pgx default.
	MaxConns int32
}

// Connect opens a connection pool and verifies the database is reachable.
// It does not apply migrations: only long-running processes do that, via
// Migrate, so a hook invoked on every push never touches the schema.
func Connect(ctx context.Context, databaseURL string, opts Options) (*DB, error) {
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	if opts.MaxConns > 0 {
		poolConfig.MaxConns = opts.MaxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &DB{Pool: pool}, nil
}

// Close releases the connection pool.
func (d *DB) Close() {
	d.Pool.Close()
}

// Ping reports whether the database is reachable.
func (d *DB) Ping(ctx context.Context) error {
	return d.Pool.Ping(ctx)
}
