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
