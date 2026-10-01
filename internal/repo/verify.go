package repo

import (
	"context"
	"fmt"
	"github.com/mmrzaf/gitman/internal/git"
)

// VerifySnapshot checks the database read model and retained history against
// the filesystem. It only reads; restoration must not hide an inconsistent pair
// by silently rebuilding the index.
func (s *Service) VerifySnapshot(ctx context.Context) error {
	all, err := s.List(ctx)
	if err != nil {
		return err
	}
	for _, r := range all {
		gr, err := s.Open(r)
		if err != nil {
			return err
		}
		if err := gr.CheckIntegrity(ctx); err != nil {
			return fmt.Errorf("%s: %w", r.Name, err)
		}
		branch, err := gr.DefaultBranch(ctx)
		if err != nil {
			return err
		}
		if branch != r.DefaultBranch {
			return fmt.Errorf("%s: default branch does not match snapshot", r.Name)
		}
		refs, err := gr.Refs(ctx)
		if err != nil {
			return err
		}
		indexed, err := s.ListRefs(ctx, r.ID)
		if err != nil {
			return err
		}
		actual := map[string]string{}
		for _, ref := range refs {
			actual[git.FullName(ref.Kind, ref.Name)] = ref.Commit
		}
		if len(actual) != len(indexed) {
			return fmt.Errorf("%s: indexed ref count does not match snapshot", r.Name)
		}
		for _, ref := range indexed {
			if actual[git.FullName(ref.Kind, ref.Name)] != ref.Commit {
				return fmt.Errorf("%s: indexed ref %s does not match snapshot", r.Name, ref.Name)
			}
		}
		hashes, err := s.retainedObjects(ctx, r.ID)
		if err != nil {
			return err
		}
		for _, h := range hashes {
			if git.IsZeroHash(h) {
				continue
			}
			if !git.IsHash(h) {
				return fmt.Errorf("%s: invalid retained object hash", r.Name)
			}
			if _, err := gr.ResolveCommit(ctx, h); err != nil {
				return fmt.Errorf("%s: retained object %s unavailable: %w", r.Name, h, err)
			}
		}
		if _, err := s.RunSecrets(ctx, r.ID); err != nil {
			return fmt.Errorf("%s secret recovery: %w", r.Name, err)
		}
	}
	var inconsistent bool
	err = s.db.Q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM repository_storage s LEFT JOIN (SELECT repo_id,sum(octet_length(content)) AS bytes FROM step_logs GROUP BY repo_id) l ON l.repo_id=s.repo_id WHERE s.log_bytes<>coalesce(l.bytes,0))`).Scan(&inconsistent)
	if err != nil {
		return err
	}
	if inconsistent {
		return fmt.Errorf("snapshot log-storage accounting does not match retained output")
	}
	return nil
}
