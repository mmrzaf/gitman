// Package postgres is Gitman's only persistence layer. Every part of Gitman that
// touches state that must survive a restart goes through it.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB is Gitman's PostgreSQL connection pool. Q is Pool itself, bounded by
// AcquireTimeout when the caller who opened this DB set one: every
// package that reads or writes outside a transaction goes through Q, not
// Pool directly, so that bound applies everywhere without each call site
// repeating it.
type DB struct {
	Pool           *pgxpool.Pool
	Q              Querier
	acquireTimeout time.Duration
}

// Options tunes a connection pool for the process that owns it. The web
// process uses the pgx default; a worker sets a small fixed cap, since
// it runs one run at a time; a Git hook, which runs once per push and
// exits, needs only a couple of connections.
type Options struct {
	// MaxConns caps the pool size. Zero keeps the pgx default.
	MaxConns int32
	// AcquireTimeout bounds how long a call waits to get a connection
	// from an exhausted pool before failing with ErrUnavailable. Zero
	// waits as long as the caller's own context allows, as before.
	AcquireTimeout time.Duration
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
	d := &DB{Pool: pool, acquireTimeout: opts.AcquireTimeout}
	d.Q = boundedQuerier{db: d}
	return d, nil
}

// probeAcquire briefly acquires and releases a connection, bounded by
// acquireTimeout, before a caller goes on to run its real query or
// transaction on ctx unbounded. It only detects sustained exhaustion: a
// connection that frees up a moment later is not what it is for. Doing
// nothing when acquireTimeout is unset keeps every caller that never
// opted into it exactly as unbounded as before.
func (d *DB) probeAcquire(ctx context.Context) error {
	if d.acquireTimeout <= 0 {
		return nil
	}
	pctx, cancel := context.WithTimeout(ctx, d.acquireTimeout)
	defer cancel()
	conn, err := d.Pool.Acquire(pctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return ErrUnavailable
		}
		return err
	}
	conn.Release()
	return nil
}

// boundedQuerier is DB.Q: Pool's three read/write methods, each preceded
// by probeAcquire so a caller waiting on an exhausted pool fails fast
// instead of blocking for as long as its own context allows.
type boundedQuerier struct {
	db *DB
}

func (b boundedQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if err := b.db.probeAcquire(ctx); err != nil {
		return pgconn.CommandTag{}, err
	}
	return b.db.Pool.Exec(ctx, sql, args...)
}

func (b boundedQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if err := b.db.probeAcquire(ctx); err != nil {
		return nil, err
	}
	return b.db.Pool.Query(ctx, sql, args...)
}

func (b boundedQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if err := b.db.probeAcquire(ctx); err != nil {
		return erroredRow{err}
	}
	return b.db.Pool.QueryRow(ctx, sql, args...)
}

// erroredRow is a pgx.Row that reports probeAcquire's failure as a
// QueryRow caller expects to see one: from Scan, not from QueryRow
// itself, which never returns an error.
type erroredRow struct{ err error }

func (r erroredRow) Scan(dest ...any) error { return r.err }

// Close releases the connection pool.
func (d *DB) Close() {
	d.Pool.Close()
}

// Ping reports whether the database is reachable.
func (d *DB) Ping(ctx context.Context) error {
	return d.Pool.Ping(ctx)
}
