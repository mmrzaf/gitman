package ci

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/postgres"
)

// ErrWorkerAlive refuses to release a worker that is still heartbeating:
// a lost worker command must never race a live one for its runs.
var ErrWorkerAlive = errors.New("worker is still heartbeating")

// ReleaseLostWorker is the deliberate operator escape hatch for a worker
// that is gone for good — a destroyed host, a reinstalled machine — and
// will never recover its own runs. The operator asserts that its
// containers are stopped; only then does Gitman fail its running runs,
// release their deployment targets, mark their containers removed, and
// stop the worker record. It is never automatic.
func (s *Service) ReleaseLostWorker(ctx context.Context, workerID, onlyRunID string) (int, error) {
	ctx, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	released := 0
	var hostname string
	err := s.db.Tx(ctx, func(tx postgres.Tx) error {
		var alive bool
		err := tx.QueryRow(ctx, `
			SELECT hostname, stopped_at IS NULL AND heartbeat_at >= now() - make_interval(secs => $2)
			FROM workers WHERE id = $1 FOR UPDATE
		`, workerID, WorkerLostAfter.Seconds()).Scan(&hostname, &alive)
		if err != nil {
			return postgres.NormalizeNotFound(err)
		}
		if alive {
			return ErrWorkerAlive
		}

		query := `
			SELECT id, status FROM runs
			WHERE worker_id = $1
			  AND (status = 'running' OR EXISTS (
				SELECT 1 FROM steps WHERE steps.run_id = runs.id
				  AND container_name IS NOT NULL AND container_removed_at IS NULL))
			FOR UPDATE`
		args := []any{workerID}
		if onlyRunID != "" {
			query = `
				SELECT id, status FROM runs
				WHERE id = $2 AND worker_id = $1
				  AND (status = 'running' OR EXISTS (
					SELECT 1 FROM steps WHERE steps.run_id = runs.id
					  AND container_name IS NOT NULL AND container_removed_at IS NULL))
				FOR UPDATE`
			args = append(args, onlyRunID)
		}
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("list runs of lost worker: %w", err)
		}
		type runState struct {
			id     string
			status Status
		}
		var runs []runState
		for rows.Next() {
			var run runState
			if err := rows.Scan(&run.id, &run.status); err != nil {
				rows.Close()
				return err
			}
			runs = append(runs, run)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if onlyRunID != "" && len(runs) == 0 {
			return postgres.ErrNotFound
		}

		for _, run := range runs {
			if run.status == StatusRunning {
				if _, err := finishRunRow(ctx, tx, run.id, StatusFailed,
					"The worker running it was confirmed gone by an operator."); err != nil {
					return err
				}
				if err := settleOpenSteps(ctx, tx, run.id, StepFailed, StepSkipped); err != nil {
					return err
				}
				if err := confirmExecutionStoppedTx(ctx, tx, run.id); err != nil {
					return err
				}
				if err := notify(ctx, tx, NotifyChannel, run.id); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `
				UPDATE steps SET container_removed_at = now()
				WHERE run_id = $1 AND container_name IS NOT NULL AND container_removed_at IS NULL
			`, run.id); err != nil {
				return err
			}
			released++
		}
		// An owner can outlive a run that already finished (a crash after
		// its receipt). The operator has confirmed this worker's host is
		// gone, so none of its targets may stay owned.
		ownerQuery := `UPDATE deployment_targets SET owner_run_id = NULL, owner_step_id = NULL
			WHERE owner_run_id IN (SELECT id FROM runs WHERE worker_id = $1)`
		ownerArgs := []any{workerID}
		if onlyRunID != "" {
			ownerQuery = `UPDATE deployment_targets SET owner_run_id = NULL, owner_step_id = NULL
				WHERE owner_run_id = $2 AND $2 IN (SELECT id FROM runs WHERE worker_id = $1)`
			ownerArgs = append(ownerArgs, onlyRunID)
		}
		if _, err := tx.Exec(ctx, ownerQuery, ownerArgs...); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE workers SET stopped_at = COALESCE(stopped_at, now()), ready = false,
				readiness_reason = 'Stopped: confirmed gone by an operator', active_runs = 0
			WHERE id = $1
		`, workerID); err != nil {
			return err
		}
		return activity.RecordAdmin(ctx, tx, "", "", activity.WorkerReleased,
			fmt.Sprintf("%s (%s), %d run(s)", hostname, workerID, released))
	})
	return released, err
}
