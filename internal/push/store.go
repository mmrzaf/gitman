package push

import (
	"context"
	"fmt"

	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/id"
	"github.com/mmrzaf/gitman/internal/postgres"
)

func insertPush(ctx context.Context, tx postgres.Tx, pushID, repoID, personID, sourceIP string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO pushes (id, repo_id, person_id, source_ip) VALUES ($1, $2, $3, $4)
	`, pushID, repoID, personID, sourceIP); err != nil {
		return fmt.Errorf("record push: %w", err)
	}
	return nil
}

func insertPushUpdate(ctx context.Context, tx postgres.Tx, pushID string, kind git.Kind, name, oldCommit, newCommit string,
	isCreate, isDelete, isForce bool, commits int, capped bool) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO push_updates (id, push_id, kind, name, old_commit, new_commit, is_create, is_delete, is_force,
		                          commit_count, commit_count_capped)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, id.New(), pushID, kind, name, oldCommit, newCommit, isCreate, isDelete, isForce, commits, capped); err != nil {
		return fmt.Errorf("record update of %s %s: %w", kind, name, err)
	}
	return nil
}

func insertRefusal(ctx context.Context, tx postgres.Tx, repoID, personID, sourceIP string, refusals []refusal) error {
	refusalID := id.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO push_refusals (id, repo_id, person_id, source_ip) VALUES ($1, $2, $3, $4)
	`, refusalID, repoID, personID, sourceIP); err != nil {
		return fmt.Errorf("record refused push: %w", err)
	}
	for i, r := range refusals {
		if _, err := tx.Exec(ctx, `
			INSERT INTO push_refusal_refs (refusal_id, position, ref, reason) VALUES ($1, $2, $3, $4)
		`, refusalID, i, r.ref, r.reason); err != nil {
			return fmt.Errorf("record why %s was refused: %w", r.ref, err)
		}
	}
	return nil
}
