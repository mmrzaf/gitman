package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mmrzaf/gitman/internal/git"
)

const (
	// retainedObjectBudget leaves ample room for the 365-day audit window
	// of a busy repository (~100k objects at 274 pushes a day) while
	// still bounding a single reconciliation's memory.
	retainedObjectBudget = 1_000_000
	// repoGitPruneTimeout gives one repository's reconciliation its own
	// budget, so a large repository cannot consume the whole phase.
	repoGitPruneTimeout = 10 * time.Minute
)

// PruneGitPins releases objects only after database retention commits. A pending
// repository operation blocks this sweep, and manual runs share its Git lock.
func (s *Service) PruneGitPins(ctx context.Context) error {
	repos, err := s.List(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, r := range repos {
		repoCtx, stop := context.WithTimeout(ctx, repoGitPruneTimeout)
		var gr *git.Repo
		err := s.WithMutation(repoCtx, r.ID, func() error {
			hashes, err := s.retainedObjects(repoCtx, r)
			if err != nil {
				return err
			}
			rows, err := s.db.Q.Query(repoCtx, `SELECT id FROM repository_operations WHERE repo_id=$1 ORDER BY id LIMIT $2`, r.ID, retainedObjectBudget+1)
			if err != nil {
				return err
			}
			var operations []string
			for rows.Next() {
				var v string
				if err := rows.Scan(&v); err != nil {
					rows.Close()
					return err
				}
				operations = append(operations, v)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if len(operations) > retainedObjectBudget {
				return fmt.Errorf("repository %s exceeds its retained-operation budget of %d", r.Name, retainedObjectBudget)
			}
			if gr, err = s.Open(r); err != nil {
				return err
			}
			return gr.ReconcilePins(repoCtx, hashes, operations)
		})
		stop()
		if err == nil && gr != nil {
			// GC runs without the mutation lock so a push never waits
			// behind a repack, and with its own budget in case the
			// reconciliation used most of the repository's time.
			gcCtx, stopGC := context.WithTimeout(ctx, repoGitPruneTimeout)
			err = gr.AutoGC(gcCtx)
			stopGC()
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *Service) retainedObjects(ctx context.Context, r *Repo) ([]string, error) {
	rows, err := s.db.Q.Query(ctx, `SELECT hash FROM (SELECT commit_hash AS hash FROM runs WHERE repo_id=$1 UNION SELECT commit_hash FROM deployments WHERE repo_id=$1 UNION SELECT commit_hash FROM refs WHERE repo_id=$1 UNION SELECT u.old_commit FROM push_updates u JOIN pushes p ON p.id=u.push_id WHERE p.repo_id=$1 UNION SELECT u.new_commit FROM push_updates u JOIN pushes p ON p.id=u.push_id WHERE p.repo_id=$1) retained ORDER BY hash LIMIT $2`, r.ID, retainedObjectBudget+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hashes []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		hashes = append(hashes, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(hashes) > retainedObjectBudget {
		return nil, fmt.Errorf("repository %s exceeds its retained-object budget of %d", r.Name, retainedObjectBudget)
	}
	return hashes, nil
}
