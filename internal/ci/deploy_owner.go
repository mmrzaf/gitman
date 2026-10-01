package ci

import (
	"context"
	"errors"
	"time"

	"github.com/mmrzaf/gitman/internal/postgres"
)

var ErrDeploymentBusy = errors.New("another execution owns this deployment target")
var ErrDeploymentSuperseded = errors.New("a newer run has already deployed this target")

var ErrDeploymentOwnership = errors.New("deployment ownership is no longer valid")

// BeginDeployment acquires a durable target owner. The owner has no expiry:
// a stopped heartbeat is not proof that a deployment script has terminated.
// All transactions use repository, run, step, target lock order.
func (s *Service) BeginDeployment(ctx context.Context, runID, stepID string) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := lockRunRepo(ctx, tx, runID); err != nil {
			return err
		}
		var repoID, target string
		var number int64
		var trigger Trigger
		if err := tx.QueryRow(ctx, `SELECT repo_id, target, number, trigger FROM runs WHERE id = $1 AND status = 'running' AND NOT cancel_requested AND target <> '' AND deadline_at > now() FOR UPDATE`, runID).Scan(&repoID, &target, &number, &trigger); err != nil {
			return ErrRunEnded
		}
		var kind StepKind
		if err := tx.QueryRow(ctx, `SELECT type FROM steps WHERE id = $2 AND run_id = $1 AND status = 'running' FOR UPDATE`, runID, stepID).Scan(&kind); err != nil {
			return err
		}
		if kind != StepDeploy {
			return ErrDeploymentOwnership
		}
		if _, err := tx.Exec(ctx, `INSERT INTO deployment_targets (repo_id, target) VALUES ($1, $2) ON CONFLICT DO NOTHING`, repoID, target); err != nil {
			return err
		}
		var owner *string
		var generation, latestNumber int64
		if err := tx.QueryRow(ctx, `SELECT owner_step_id, generation, latest_run_number FROM deployment_targets WHERE repo_id = $1 AND target = $2 FOR UPDATE`, repoID, target).Scan(&owner, &generation, &latestNumber); err != nil {
			return err
		}
		if trigger == TriggerPush && number < latestNumber {
			return ErrDeploymentSuperseded
		}
		if owner != nil && *owner != stepID {
			return ErrDeploymentBusy
		}
		if owner == nil {
			generation++
			if _, err := tx.Exec(ctx, `UPDATE deployment_targets SET owner_run_id = $3, owner_step_id = $4, generation = $5 WHERE repo_id = $1 AND target = $2`, repoID, target, runID, stepID, generation); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE steps SET deployment_generation = $2 WHERE id = $1`, stepID, generation)
		return err
	})
}

// finishRecordedStep commits the step receipt, any explicit deployment, and
// target release together. Retrying the same step cannot create two deployments.
func finishRecordedStep(ctx context.Context, tx postgres.Tx, runID, stepID string, status StepStatus, code *int) error {
	return finishExecutionReceipt(ctx, tx, runID, stepID, status, code, false)
}

func finishExecutionReceipt(ctx context.Context, tx postgres.Tx, runID, stepID string, status StepStatus, code *int, recovery bool) error {
	if err := lockRunRepo(ctx, tx, runID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, runID); err != nil {
		return err
	}
	var kind StepKind
	var before StepStatus
	var generation *int64
	var recordedCode *int
	if err := tx.QueryRow(ctx, `SELECT type, status, deployment_generation, exit_code FROM steps WHERE id = $2 AND run_id = $1 FOR UPDATE`, runID, stepID).Scan(&kind, &before, &generation, &recordedCode); err != nil {
		return postgres.NormalizeNotFound(err)
	}
	if before != StepPending && before != StepRunning && (!recovery || recordedCode != nil) {
		return nil
	}
	if kind == StepDeploy && status == StepPassed {
		if generation == nil || code == nil || *code != 0 {
			return ErrDeploymentOwnership
		}
		f := &finishedRun{}
		if err := tx.QueryRow(ctx, `SELECT repo_id, target, version, commit_hash, triggered_by FROM runs WHERE id = $1 AND ($2 OR status = 'running')`, runID, recovery).Scan(&f.repoID, &f.target, &f.version, &f.commit, &f.triggeredBy); err != nil {
			return ErrRunEnded
		}
		var owned int
		if err := tx.QueryRow(ctx, `SELECT 1 FROM deployment_targets WHERE repo_id = $1 AND target = $2 AND owner_run_id = $3 AND owner_step_id = $4 AND generation = $5 FOR UPDATE`, f.repoID, f.target, runID, stepID, *generation).Scan(&owned); err != nil {
			return ErrDeploymentOwnership
		}
		if err := insertDeployment(ctx, tx, runID, stepID, f); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE deployment_targets SET latest_run_number=GREATEST(latest_run_number,(SELECT number FROM runs WHERE id=$3)) WHERE repo_id=$1 AND target=$2`, f.repoID, f.target, runID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE steps SET status=$2,exit_code=$3,finished_at=COALESCE(finished_at,now()) WHERE id=$1`, stepID, status, code); err != nil {
		return err
	}
	if kind == StepDeploy && generation != nil {
		_, err := tx.Exec(ctx, `UPDATE deployment_targets SET owner_run_id = NULL, owner_step_id = NULL WHERE owner_run_id = $1 AND owner_step_id = $2 AND generation = $3`, runID, stepID, *generation)
		return err
	}
	return nil
}

// ConfirmExecutionStopped is called only after daemon termination is confirmed.
// Recovery releases uncertain target owners without claiming that a deploy
// script succeeded. Merely failing a lost run never releases its target owner.
func (s *Service) ConfirmExecutionStopped(ctx context.Context, runID string) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := lockRunRepo(ctx, tx, runID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, runID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE steps SET status = 'failed', finished_at = now() WHERE run_id = $1 AND type = 'deploy' AND status = 'running'`, runID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE deployment_targets SET owner_run_id = NULL, owner_step_id = NULL WHERE owner_run_id = $1`, runID)
		return err
	})
}
