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
	admissionPool  *pgxpool.Pool
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
// It does not apply migrations itself: a caller runs them via Migrate
// when it wants them run. A Git hook, invoked on every push, deliberately
// never calls Migrate, so a push never touches the schema.
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
	// Admission locks must never consume connections needed by domain work.
	admissionConfig := poolConfig.Copy()
	admissionConfig.MaxConns = 4
	admissionConfig.MinConns = 0
	admissionPool, err := pgxpool.NewWithConfig(ctx, admissionConfig)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("open admission pool: %w", err)
	}
	d := &DB{Pool: pool, admissionPool: admissionPool, acquireTimeout: opts.AcquireTimeout}
	d.Q = boundedQuerier{db: d}
	return d, nil
}

// acquireConn gets one connection from the pool, bounded by acquireTimeout
// when set. The bound applies only to the wait for a free connection: the
// timeout is not carried into the connection's later use, so a caller
// that then runs a genuinely slow (but healthy) query is never cut off by
// it. Cancelling the bounding context right after Acquire returns is safe
// — a *pgxpool.Conn does not hold onto the context it was acquired with.
func (d *DB) acquireConn(ctx context.Context) (*pgxpool.Conn, error) {
	if d.acquireTimeout <= 0 {
		return d.Pool.Acquire(ctx)
	}
	bctx, cancel := context.WithTimeout(ctx, d.acquireTimeout)
	defer cancel()
	conn, err := d.Pool.Acquire(bctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, ErrUnavailable
		}
		return nil, err
	}
	return conn, nil
}

// boundedQuerier is DB.Q: each call acquires its own connection through
// acquireConn, bounded by acquireTimeout, and runs on it directly —
// rather than acquiring-and-releasing a probe connection and then asking
// Pool for a separate one, which would leave a second, unbounded wait for
// the real connection once another waiter takes the one the probe just
// freed.
type boundedQuerier struct {
	db *DB
}

func (b boundedQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	conn, err := b.db.acquireConn(ctx)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	defer conn.Release()
	return conn.Exec(ctx, sql, args...)
}

// Query returns a releasingRows that holds its connection until the
// caller closes it, exactly as if it were still Pool's own.
func (b boundedQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	conn, err := b.db.acquireConn(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		conn.Release()
		return nil, err
	}
	return &releasingRows{Rows: rows, conn: conn}, nil
}

// QueryRow returns a releasingRow that holds its connection until the
// caller scans it, exactly as if it were still Pool's own.
func (b boundedQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	conn, err := b.db.acquireConn(ctx)
	if err != nil {
		return erroredRow{err}
	}
	return &releasingRow{row: conn.QueryRow(ctx, sql, args...), conn: conn}
}

// releasingRows wraps the Rows of a connection acquired just for this
// query, releasing that connection back to the pool exactly when the
// caller is done with them — on Close, the same as Pool.Query's own
// rows already release theirs internally.
type releasingRows struct {
	pgx.Rows
	conn     *pgxpool.Conn
	released bool
}

func (r *releasingRows) Close() {
	r.Rows.Close()
	if !r.released {
		r.released = true
		r.conn.Release()
	}
}

// releasingRow is releasingRows' equivalent for QueryRow, whose one row
// is consumed by a single Scan instead of a Close.
type releasingRow struct {
	row  pgx.Row
	conn *pgxpool.Conn
}

func (r *releasingRow) Scan(dest ...any) error {
	defer r.conn.Release()
	return r.row.Scan(dest...)
}

// erroredRow is a pgx.Row that reports acquireConn's failure as a
// QueryRow caller expects to see one: from Scan, not from QueryRow
// itself, which never returns an error.
type erroredRow struct{ err error }

func (r erroredRow) Scan(dest ...any) error { return r.err }

// Close releases the connection pool.
func (d *DB) Close() {
	d.admissionPool.Close()
	d.Pool.Close()
}

// Ping reports whether the database is reachable.
func (d *DB) Ping(ctx context.Context) error {
	return d.Pool.Ping(ctx)
}
