package repo

import (
	"context"
	"errors"
	"fmt"
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
		err := s.WithMutation(ctx, r.ID, func() error {
			hashes, err := s.retainedObjects(ctx, r.ID)
			if err != nil {
				return err
			}
			rows, err := s.db.Q.Query(ctx, `SELECT id FROM repository_operations WHERE repo_id=$1 ORDER BY id LIMIT 100001`, r.ID)
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
			if len(operations) > 100000 {
				return fmt.Errorf("repository %s exceeds its retained-operation budget", r.Name)
			}
			gr, err := s.Open(r)
			if err != nil {
				return err
			}
			return gr.ReconcilePins(ctx, hashes, operations)
		})
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *Service) retainedObjects(ctx context.Context, repoID string) ([]string, error) {
	rows, err := s.db.Q.Query(ctx, `SELECT hash FROM (SELECT commit_hash AS hash FROM runs WHERE repo_id=$1 UNION SELECT commit_hash FROM deployments WHERE repo_id=$1 UNION SELECT commit_hash FROM refs WHERE repo_id=$1 UNION SELECT u.old_commit FROM push_updates u JOIN pushes p ON p.id=u.push_id WHERE p.repo_id=$1 UNION SELECT u.new_commit FROM push_updates u JOIN pushes p ON p.id=u.push_id WHERE p.repo_id=$1) retained ORDER BY hash LIMIT 100001`, repoID)
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
	if len(hashes) > 100000 {
		return nil, fmt.Errorf("repository %s exceeds its 100,000 retained-object budget", repoID)
	}
	return hashes, nil
}
