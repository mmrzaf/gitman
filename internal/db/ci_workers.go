package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/models"
)

func (db *DB) RegisterCIWorker(ctx context.Context, worker models.CIWorker) error {
	if worker.ID == "" || worker.Hostname == "" || worker.Concurrency <= 0 || worker.ActiveJobs < 0 {
		return fmt.Errorf("invalid CI worker registration")
	}
	now := time.Now()
	if worker.StartedAt.IsZero() {
		worker.StartedAt = now
	}
	if worker.HeartbeatAt.IsZero() {
		worker.HeartbeatAt = now
	}
	worker.StatusMessage = boundedCIWorkerStatus(worker.StatusMessage)

	tx, err := db.sql.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ci_workers (
			id, hostname, pid, concurrency, healthy, status_message, active_jobs,
			started_at, heartbeat_at, stopped_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
	`, worker.ID, worker.Hostname, worker.PID, worker.Concurrency, worker.Healthy,
		worker.StatusMessage, worker.ActiveJobs, worker.StartedAt.Unix(), worker.HeartbeatAt.Unix()); err != nil {
		return err
	}
	// Worker IDs are per-process. Bound stale process history so repeated
	// restarts cannot grow this operational table forever.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM ci_workers
		WHERE id <> ? AND heartbeat_at < ?
	`, worker.ID, now.Add(-30*24*time.Hour).Unix()); err != nil {
		return err
	}
	// Also cap recent history by count. A process crash loop creates a fresh UUID
	// per restart and must not make health queries grow without bound inside the
	// 30-day retention window. The newly inserted worker has a current heartbeat
	// and is explicitly excluded from deletion even if the host clock moved.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM ci_workers
		WHERE id <> ? AND id NOT IN (
			SELECT id FROM ci_workers
			WHERE id <> ?
			ORDER BY heartbeat_at DESC, id ASC
			LIMIT 999
		)
	`, worker.ID, worker.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) HeartbeatCIWorker(ctx context.Context, workerID string, healthy bool, statusMessage string, activeJobs int) error {
	if workerID == "" || activeJobs < 0 {
		return fmt.Errorf("invalid CI worker heartbeat")
	}
	statusMessage = boundedCIWorkerStatus(statusMessage)
	res, err := db.sql.ExecContext(ctx, `
		UPDATE ci_workers
		SET healthy = ?, status_message = ?, active_jobs = ?, heartbeat_at = ?, stopped_at = NULL
		WHERE id = ?
	`, healthy, statusMessage, activeJobs, time.Now().Unix(), workerID)
	if err != nil {
		return err
	}
	return requireAffectedRow(res, ErrNotFound)
}

func (db *DB) StopCIWorker(ctx context.Context, workerID string) error {
	if workerID == "" {
		return fmt.Errorf("invalid CI worker id")
	}
	now := time.Now().Unix()
	res, err := db.sql.ExecContext(ctx, `
		UPDATE ci_workers
		SET healthy = 0, status_message = 'stopped', active_jobs = 0,
			heartbeat_at = ?, stopped_at = ?
		WHERE id = ?
	`, now, now, workerID)
	if err != nil {
		return err
	}
	return requireAffectedRow(res, ErrNotFound)
}

func (db *DB) ListCIWorkers(ctx context.Context) (workers []models.CIWorker, err error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, hostname, pid, concurrency, healthy, status_message, active_jobs,
			started_at, heartbeat_at, stopped_at
		FROM ci_workers
		ORDER BY heartbeat_at DESC, id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	for rows.Next() {
		var worker models.CIWorker
		var startedAt, heartbeatAt int64
		var stoppedAt sql.NullInt64
		if scanErr := rows.Scan(
			&worker.ID, &worker.Hostname, &worker.PID, &worker.Concurrency,
			&worker.Healthy, &worker.StatusMessage, &worker.ActiveJobs,
			&startedAt, &heartbeatAt, &stoppedAt,
		); scanErr != nil {
			return nil, scanErr
		}
		worker.StartedAt = time.Unix(startedAt, 0)
		worker.HeartbeatAt = time.Unix(heartbeatAt, 0)
		worker.StoppedAt = nullUnixToTime(stoppedAt)
		workers = append(workers, worker)
	}
	return workers, rows.Err()
}

func boundedCIWorkerStatus(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}
