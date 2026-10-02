package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/id"
	"github.com/mmrzaf/gitman/internal/postgres"
)

// Operation is a durable intent. Payload must contain everything needed to finish
// without re-authorizing or scheduling a different execution on replay.
type Operation struct {
	ID, RepoID, Kind, Name, ActorID string
	Payload                         json.RawMessage
	CreatedAt                       time.Time
	Error                           string
}

type repositoryIntent struct{ Name, Description, Branch string }

const (
	recoveryLockTimeout    = 15 * time.Second
	recoveryAttemptTimeout = 5 * time.Minute
)

// WithMutation serializes filesystem changes and refuses further changes until an
// earlier intent has been reconciled. The filesystem lock also survives DB failure.
func (s *Service) WithMutation(ctx context.Context, repoID string, fn func() error) error {
	ctx, release, err := s.db.AdmitMutation(ctx)
	if err != nil {
		return err
	}
	defer release()
	unlock, err := s.git.MutationLock(ctx, repoID)
	if err != nil {
		return err
	}
	defer unlock()
	var pending bool
	if err := s.db.Q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM repository_operations WHERE repo_id=$1 AND completed_at IS NULL)`, repoID).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return apperr.New(apperr.KindUnavailable, "This repository has an unfinished operation. Wait for recovery or inspect its operation record.")
	}
	return fn()
}

// BeginOperation is called while WithMutation holds the repository lock.
func (s *Service) BeginOperation(ctx context.Context, repoID, kind, name, actorID string, payload any) (*Operation, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	op := &Operation{ID: id.New(), RepoID: repoID, Kind: kind, Name: name, ActorID: actorID, Payload: b}
	err = s.db.Tx(ctx, func(tx postgres.Tx) error {
		if kind == "delete" {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('gitman.execution.' || $1,0))`, repoID); err != nil {
				return err
			}
			var active bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE repo_id=$1 AND status='running') OR EXISTS(SELECT 1 FROM steps s JOIN runs r ON r.id=s.run_id WHERE r.repo_id=$1 AND s.container_name IS NOT NULL AND s.container_removed_at IS NULL) OR EXISTS(SELECT 1 FROM deployment_targets WHERE repo_id=$1 AND owner_run_id IS NOT NULL)`, repoID).Scan(&active); err != nil {
				return err
			}
			if active {
				return apperr.New(apperr.KindConflict, "Stop active executions and confirm their cleanup before deleting this repository.")
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO repository_operations(id,repo_id,kind,name,actor_id,payload) VALUES($1,$2,$3,$4,NULLIF($5,''),$6)`, op.ID, repoID, kind, name, actorID, b)
		return err
	})
	if err == nil {
		err = s.db.Q.QueryRow(ctx, `SELECT payload FROM repository_operations WHERE id=$1`, op.ID).Scan(&op.Payload)
	}
	return op, postgres.NormalizeWrite(err)
}

// CompleteOperationTx commits the receipt in the same transaction as domain rows.
func CompleteOperationTx(ctx context.Context, tx postgres.Tx, opID string) error {
	_, err := tx.Exec(ctx, `UPDATE repository_operations SET completed_at=now(),error='' WHERE id=$1`, opID)
	return err
}

// RejectOperation finishes an intent that provably did not change Git. Its
// reason remains recorded, while subsequent repository mutations can proceed.
func (s *Service) RejectOperation(ctx context.Context, opID string, reason error) error {
	_, err := s.db.Q.Exec(ctx, `UPDATE repository_operations SET completed_at=now(),error=$2 WHERE id=$1 AND completed_at IS NULL`, opID, reason.Error())
	return err
}

func (s *Service) PendingOperations(ctx context.Context) ([]Operation, error) {
	rows, err := s.db.Q.Query(ctx, `SELECT id,repo_id,kind,name,COALESCE(actor_id,''),payload,created_at,error FROM repository_operations WHERE completed_at IS NULL ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Operation
	for rows.Next() {
		var op Operation
		if err := rows.Scan(&op.ID, &op.RepoID, &op.Kind, &op.Name, &op.ActorID, &op.Payload, &op.CreatedAt, &op.Error); err != nil {
			return nil, err
		}
		result = append(result, op)
	}
	return result, rows.Err()
}

func (s *Service) applyRepositoryOperation(ctx context.Context, op Operation) error {
	var p repositoryIntent
	if err := json.Unmarshal(op.Payload, &p); err != nil {
		return err
	}
	switch op.Kind {
	case "create":
		if _, err := s.git.Open(op.RepoID); errors.Is(err, git.ErrNotFound) {
			if err := s.git.Create(ctx, op.RepoID, p.Branch); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		return s.db.Tx(ctx, func(tx postgres.Tx) error {
			if _, err := insertRepo(ctx, tx, op.RepoID, p.Name, p.Description, p.Branch, op.ActorID); err != nil {
				return err
			}
			if err := activity.Record(ctx, tx, op.RepoID, op.ActorID, activity.RepoCreated, p.Name); err != nil {
				return err
			}
			return CompleteOperationTx(ctx, tx, op.ID)
		})
	case "delete":
		if err := s.git.Delete(op.RepoID); err != nil && !errors.Is(err, git.ErrNotFound) {
			return err
		}
		return s.db.Tx(ctx, func(tx postgres.Tx) error {
			if err := lockRefIndex(ctx, tx, op.RepoID); err != nil {
				return err
			}
			if _, err := deleteRepoRow(ctx, tx, op.RepoID); err != nil && !errors.Is(err, postgres.ErrNotFound) {
				return err
			}
			if err := activity.Record(ctx, tx, "", op.ActorID, activity.RepoDeleted, p.Name); err != nil {
				return err
			}
			return CompleteOperationTx(ctx, tx, op.ID)
		})
	case "head":
		gr, err := s.git.Open(op.RepoID)
		if err != nil {
			return err
		}
		if err := gr.SetHead(ctx, p.Branch); err != nil {
			return err
		}
		return s.db.Tx(ctx, func(tx postgres.Tx) error {
			if err := lockRefIndex(ctx, tx, op.RepoID); err != nil {
				return err
			}
			if _, err := updateDefaultBranchRow(ctx, tx, op.RepoID, p.Branch); err != nil {
				return err
			}
			if err := activity.Record(ctx, tx, op.RepoID, op.ActorID, activity.RepoDefaultBranchChanged, p.Branch); err != nil {
				return err
			}
			return CompleteOperationTx(ctx, tx, op.ID)
		})
	}
	return fmt.Errorf("unexpected repository operation %q", op.Kind)
}

// RecoverOperations replays committed intent under the same filesystem locks as
// live mutations. Failure keeps intent visible and blocks further changes.
func (s *Service) RecoverOperations(ctx context.Context, recoverPush func(context.Context, Operation) error) error {
	ops, err := s.PendingOperations(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, op := range ops {
		lockCtx, stopLock := context.WithTimeout(ctx, recoveryLockTimeout)
		unlock, err := s.git.MutationLock(lockCtx, op.RepoID)
		stopLock()
		if err == nil {
			attempt, stopAttempt := context.WithTimeout(ctx, recoveryAttemptTimeout)
			// A live operation may have finished while recovery waited for its lock.
			var pending bool
			err = s.db.Q.QueryRow(attempt, `SELECT completed_at IS NULL FROM repository_operations WHERE id=$1`, op.ID).Scan(&pending)
			if err == nil && pending {
				if op.Kind == "push" {
					if recoverPush == nil {
						err = errors.New("push recovery is unavailable")
					} else {
						err = recoverPush(attempt, op)
					}
				} else {
					err = s.applyRepositoryOperation(attempt, op)
				}
			}
			stopAttempt()
			unlock()
		}
		if err != nil {
			note, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			_, _ = s.db.Q.Exec(note, `UPDATE repository_operations SET error=$2 WHERE id=$1 AND completed_at IS NULL`, op.ID, err.Error())
			stop()
			errs = append(errs, fmt.Errorf("operation %s: %w", op.ID, err))
		}
	}
	return errors.Join(errs...)
}

// QuarantineOrphans preserves repositories with neither a row nor pending intent.
func (s *Service) QuarantineOrphans(ctx context.Context) error {
	return s.git.QuarantineOrphans(ctx, func(repoID string) (bool, error) {
		var known bool
		err := s.db.Q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM repos WHERE id=$1) OR EXISTS(SELECT 1 FROM repository_operations WHERE repo_id=$1 AND completed_at IS NULL)`, repoID).Scan(&known)
		return known, err
	})
}

func (s *Service) AdmitMutation(ctx context.Context) (context.Context, func(), error) {
	return s.db.AdmitMutation(ctx)
}
func (s *Service) Maintenance(ctx context.Context) (bool, error) { return s.db.Maintenance(ctx) }

func (s *Service) CheckPushCapacity(incoming int64) error {
	if err := s.git.CheckPushCapacity(incoming); err != nil {
		return apperr.Wrap(apperr.KindUnavailable, "Repository disk reserve is low; pushes are paused until the disk reserve is available.", err)
	}
	return nil
}
