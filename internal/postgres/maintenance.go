package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mmrzaf/gitman/internal/apperr"
)

type admissionKey struct{}
type admission struct{ db *DB }

// AdmitMutation holds shared admission until its caller finishes. Maintenance
// takes exclusive admission before persisting the pause, draining admitted work.
func (d *DB) AdmitMutation(ctx context.Context) (context.Context, func(), error) {
	if held, ok := ctx.Value(admissionKey{}).(admission); ok && held.db == d {
		return ctx, func() {}, nil
	}
	conn, err := d.admissionPool.Acquire(ctx)
	if err != nil {
		return ctx, nil, err
	}
	var key int64
	if err := conn.QueryRow(ctx, `SELECT hashtextextended('gitman.mutations.' || id,0) FROM instance`).Scan(&key); err != nil {
		conn.Release()
		return ctx, nil, err
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock_shared($1)`, key); err != nil {
		conn.Release()
		return ctx, nil, err
	}
	release := func() { unlockConnection(conn, true, key) }
	var paused bool
	if err := conn.QueryRow(ctx, `SELECT maintenance FROM instance`).Scan(&paused); err != nil {
		release()
		return ctx, nil, err
	}
	if paused {
		release()
		return ctx, nil, apperr.New(apperr.KindUnavailable, "Gitman is paused for maintenance. Try again when maintenance finishes.")
	}
	return context.WithValue(ctx, admissionKey{}, admission{db: d}), release, nil
}

func unlockConnection(conn *pgxpool.Conn, shared bool, key int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	query := `SELECT pg_advisory_unlock($1)`
	if shared {
		query = `SELECT pg_advisory_unlock_shared($1)`
	}
	if _, err := conn.Exec(ctx, query, key); err != nil {
		_ = conn.Conn().Close(ctx)
	}
	conn.Release()
}

// SetMaintenance is durable across CLI/web restarts. Resume is explicit, so an
// interrupted backup does not silently reopen mutations.
func (d *DB) SetMaintenance(ctx context.Context, paused bool) error {
	conn, err := d.admissionPool.Acquire(ctx)
	if err != nil {
		return err
	}
	var key int64
	if err := conn.QueryRow(ctx, `SELECT hashtextextended('gitman.mutations.' || id,0) FROM instance`).Scan(&key); err != nil {
		conn.Release()
		return err
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, key); err != nil {
		conn.Release()
		return err
	}
	defer unlockConnection(conn, false, key)
	_, err = conn.Exec(ctx, `UPDATE instance SET maintenance=$1`, paused)
	return err
}

func (d *DB) Maintenance(ctx context.Context) (bool, error) {
	var paused bool
	err := d.Q.QueryRow(ctx, `SELECT maintenance FROM instance`).Scan(&paused)
	return paused, err
}

// BackupReady requires admitted operations, executions and cleanup to drain.
func (d *DB) BackupReady(ctx context.Context) error {
	var paused, busy bool
	err := d.Q.QueryRow(ctx, `SELECT maintenance, EXISTS(SELECT 1 FROM repository_operations WHERE completed_at IS NULL) OR EXISTS(SELECT 1 FROM runs WHERE status='running') OR EXISTS(SELECT 1 FROM steps WHERE container_name IS NOT NULL AND container_removed_at IS NULL) OR EXISTS(SELECT 1 FROM deployment_targets WHERE owner_run_id IS NOT NULL) FROM instance`).Scan(&paused, &busy)
	if err != nil {
		return err
	}
	if !paused {
		return errors.New("enable maintenance before backing up")
	}
	if busy {
		return errors.New("wait for repository operations and execution cleanup to drain before backing up")
	}
	return nil
}

// SnapshotLease prevents resume until a complete backup has been written.
func (d *DB) SnapshotLease(ctx context.Context) (func(), error) {
	conn, err := d.admissionPool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var key int64
	if err := conn.QueryRow(ctx, `SELECT hashtextextended('gitman.mutations.' || id,0) FROM instance`).Scan(&key); err != nil {
		conn.Release()
		return nil, err
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, key); err != nil {
		conn.Release()
		return nil, err
	}
	release := func() { unlockConnection(conn, false, key) }
	if err := d.BackupReady(ctx); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
