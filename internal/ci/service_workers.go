package ci

import (
	"context"
	"errors"
	"time"

	"github.com/mmrzaf/gitman/internal/postgres"
)

// RegisterWorker records a worker process starting.
func (s *Service) RegisterWorker(ctx context.Context, workerID, hostname string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return upsertWorker(ctx, s.db.Q, workerID, hostname)
}

// Heartbeat records that a worker is alive and how many runs it has.
func (s *Service) Heartbeat(ctx context.Context, workerID string, activeRuns int) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return touchWorker(ctx, s.db.Q, workerID, activeRuns)
}

// StopWorker records a worker process shutting down.
func (s *Service) StopWorker(ctx context.Context, workerID string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return markWorkerStopped(ctx, s.db.Q, workerID)
}

// WorkerLostAfter is how long a worker may go without a heartbeat before
// it counts as gone: no longer online, and its running runs failed. It is
// minutes, not seconds, so a Postgres restart or failover — which can
// itself take a minute or two — reads as a database blip to wait out,
// not a lost worker to fail runs for. "gitman admin worker cleanup" uses
// the same threshold, so an operator running it by hand fails a run
// exactly when a live worker would have.
const WorkerLostAfter = 5 * time.Minute

// WorkerReadyAfter is how old a worker's heartbeat or readiness check may
// be before it stops counting as ready for new work. It is shorter than
// WorkerLostAfter: not taking work is cheap to recover from, while
// declaring a worker lost fails its running runs.
const WorkerReadyAfter = time.Minute

// FailLostRuns fails every running run whose worker stopped, or has not
// sent a heartbeat in the last staleAfter, and returns how many it
// failed. Runs are never retried automatically: a run that shipped
// half-way must be looked at by a person.
func (s *Service) FailLostRuns(ctx context.Context, staleAfter time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var lost []string
	err := s.db.Tx(ctx, func(tx postgres.Tx) error {
		var err error
		if lost, err = selectLostRuns(ctx, tx, staleAfter); err != nil {
			return err
		}
		for _, runID := range lost {
			if _, err := finishRunRow(ctx, tx, runID, StatusFailed, "The worker running it stopped responding."); err != nil {
				return err
			}
			if err := settleOpenSteps(ctx, tx, runID, StepFailed, StepSkipped); err != nil {
				return err
			}
			if err := notify(ctx, tx, NotifyChannel, runID); err != nil {
				return err
			}
		}
		return nil
	})
	return len(lost), err
}

// RunningRunIDs returns the IDs of every running run, for a worker to
// tell its own leftover containers from those of live runs.
func (s *Service) RunningRunIDs(ctx context.Context) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return selectRunningRunIDs(ctx, s.db.Q)
}

// PruneWorkers forgets workers that stopped, or were last heard from,
// before `before` and have no running run.
func (s *Service) PruneWorkers(ctx context.Context, before time.Time) (int64, error) {
	return deleteGoneWorkers(ctx, s.db.Q, before)
}

// PlanContainer records a deterministic recovery handle before Docker creation.
func (s *Service) PlanContainer(ctx context.Context, runID, stepID, name string) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	tag, err := s.db.Q.Exec(ctx, `UPDATE steps SET container_name = $3 WHERE id = $2 AND run_id = $1 AND status = 'running' AND EXISTS (SELECT 1 FROM runs WHERE id = $1 AND status = 'running' AND NOT cancel_requested)`, runID, stepID, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrRunEnded
	}
	return nil
}

func (s *Service) ContainerCreated(ctx context.Context, runID, stepID, containerID string) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	tag, err := s.db.Q.Exec(ctx, `UPDATE steps SET container_id = $3 WHERE id = $2 AND run_id = $1 AND status = 'running' AND EXISTS (SELECT 1 FROM runs WHERE id = $1 AND status = 'running' AND NOT cancel_requested) AND (type = 'run' OR EXISTS(SELECT 1 FROM deployment_targets WHERE owner_run_id = $1 AND owner_step_id = $2 AND generation = steps.deployment_generation))`, runID, stepID, containerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrRunEnded
	}
	return nil
}

// RecoveryRun identifies a run interrupted on this worker. Recovery stops its
// containers and fails the run; it never re-executes pipeline scripts.
type RecoveryRun struct {
	ID         string
	Containers []string
	Foreign    bool
}

func (s *Service) RecoveryRuns(ctx context.Context, workerID, engineID string) ([]RecoveryRun, error) {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	rows, err := s.db.Q.Query(ctx, `SELECT r.id, ARRAY(SELECT container_name FROM steps WHERE run_id = r.id AND container_name IS NOT NULL AND container_removed_at IS NULL), r.worker_id <> $1 FROM runs r JOIN workers w ON w.id=r.worker_id WHERE (r.worker_id = $1 OR ($2<>'' AND w.engine_id=$2 AND (w.stopped_at IS NOT NULL OR w.heartbeat_at < now()-interval '1 minute'))) AND (r.status = 'running' OR EXISTS (SELECT 1 FROM steps WHERE run_id = r.id AND container_name IS NOT NULL AND container_removed_at IS NULL))`, workerID, engineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []RecoveryRun
	for rows.Next() {
		var run RecoveryRun
		if err := rows.Scan(&run.ID, &run.Containers, &run.Foreign); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// SetWorkerReadiness is independent of heartbeat. An alive worker may be unable
// to execute work because Docker, disk capacity or recovery is unavailable.
func (s *Service) SetWorkerReadiness(ctx context.Context, workerID string, ready bool, reason string, images []string) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	if images == nil {
		images = []string{}
	}
	_, err := s.db.Q.Exec(ctx, `UPDATE workers SET ready = $2, readiness_reason = $3, images = $4, readiness_at = now() WHERE id = $1`, workerID, ready, reason, normalizeImages(images))
	return err
}

// WorkerStatus is a worker as the Workers page shows it. Ready and Online are
// judged by the database clock: Online means the worker has not stopped and
// has heartbeated within WorkerReadyAfter, Ready additionally that its last
// readiness check passed and is recent.
type WorkerStatus struct {
	ID, Hostname, Reason string
	Ready, Online        bool
	Heartbeat            time.Time
	Stopped              *time.Time
	Images               []string
	ActiveRuns           int
	EngineID             string
}

func (s *Service) Workers(ctx context.Context) ([]WorkerStatus, error) {
	return selectWorkers(ctx, s.db.Q)
}

func selectWorkers(ctx context.Context, q postgres.Querier) ([]WorkerStatus, error) {
	rows, err := q.Query(ctx, `
		SELECT id, hostname,
		       ready AND stopped_at IS NULL AND heartbeat_at >= now() - make_interval(secs => $1) AND readiness_at >= now() - make_interval(secs => $1),
		       stopped_at IS NULL AND heartbeat_at >= now() - make_interval(secs => $1),
		       CASE WHEN stopped_at IS NOT NULL THEN 'Stopped'
		            WHEN heartbeat_at < now() - make_interval(secs => $1) THEN 'Heartbeat is stale'
		            WHEN readiness_at IS NULL OR readiness_at < now() - make_interval(secs => $1) THEN 'Readiness has not been checked recently'
		            ELSE readiness_reason END,
		       heartbeat_at, stopped_at, images, active_runs, engine_id
		FROM workers ORDER BY started_at DESC LIMIT 100`, WorkerReadyAfter.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var workers []WorkerStatus
	for rows.Next() {
		var w WorkerStatus
		if err := rows.Scan(&w.ID, &w.Hostname, &w.Ready, &w.Online, &w.Reason, &w.Heartbeat, &w.Stopped, &w.Images, &w.ActiveRuns, &w.EngineID); err != nil {
			return nil, err
		}
		workers = append(workers, w)
	}
	return workers, rows.Err()
}

// RecoverStepExit stores a retained daemon receipt without starting anything.
func (s *Service) RecoverStepExit(ctx context.Context, runID, name string, code int) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		var stepID string
		if err := tx.QueryRow(ctx, `SELECT id FROM steps WHERE run_id=$1 AND (container_name=$2 OR container_id=$2)`, runID, name).Scan(&stepID); err != nil {
			return postgres.NormalizeNotFound(err)
		}
		status := StepFailed
		if code == 0 {
			status = StepPassed
		}
		return finishExecutionReceipt(ctx, tx, runID, stepID, status, &code, true)
	})
}

func (s *Service) ContainerRemoved(ctx context.Context, runID, name string) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	_, err := s.db.Q.Exec(ctx, `UPDATE steps SET container_removed_at = now() WHERE run_id = $1 AND container_name = $2 AND container_removed_at IS NULL`, runID, name)
	return err
}

func (s *Service) BindWorkerEngine(ctx context.Context, workerID, engineID, root string) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('gitman.engine.' || $1,0))`, engineID); err != nil {
			return err
		}
		var incompatible bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM workers
				WHERE engine_id=$1 AND workspace_root<>$2 AND id<>$4
				  AND stopped_at IS NULL AND heartbeat_at >= now() - make_interval(secs => $3)
			)
		`, engineID, root, WorkerLostAfter.Seconds(), workerID).Scan(&incompatible); err != nil {
			return err
		}
		if incompatible {
			return errors.New("workers sharing a Docker engine must share the same absolute workspace root")
		}
		_, err := tx.Exec(ctx, `UPDATE workers SET engine_id=$2,workspace_root=$3 WHERE id=$1`, workerID, engineID, root)
		return err
	})
}

// ConfirmContainersRemoved is called only after cleanup removed every labelled
// container while holding the run's filesystem execution lock.
func (s *Service) ConfirmContainersRemoved(ctx context.Context, runID string) error {
	if err := s.ConfirmExecutionStopped(ctx, runID); err != nil {
		return err
	}
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	_, err := s.db.Q.Exec(ctx, `UPDATE steps SET container_removed_at=now() WHERE run_id=$1 AND container_name IS NOT NULL AND container_removed_at IS NULL`, runID)
	return err
}
