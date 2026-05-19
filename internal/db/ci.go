package db

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/mmrzaf/gitman/internal/models"
)

// CreateCIRun inserts a new pending run and returns its UUID.
func (db *DB) CreateCIRun(ctx context.Context, repoID, commitHash, branch, tag, event string) (string, error) {
	id := uuid.New().String()
	_, err := db.ExecContext(ctx, `
		INSERT INTO ci_runs (id, repo_id, commit_hash, branch, tag, event, status)
		VALUES (?, ?, ?, ?, ?, ?, 'pending')
	`, id, repoID, commitHash, branch, tag, event)
	return id, err
}

// ClaimNextPendingRun atomically claims the oldest pending run for the worker
// by wrapping the SELECT + UPDATE in a transaction.
// Returns nil, nil when there are no pending runs.
func (db *DB) ClaimNextPendingRun(ctx context.Context) (*models.CIRun, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			slog.Warn("failed to rollback transaction", "error", rollbackErr)
		}
	}()

	res, err := tx.ExecContext(ctx, `
        UPDATE ci_runs
        SET status = 'running'
        WHERE id = (
            SELECT id FROM ci_runs
            WHERE status = 'pending'
            ORDER BY created_at ASC
            LIMIT 1
        )
    `)
	if err != nil {
		return nil, err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return nil, nil
	}

	var run models.CIRun
	var createdAt int64
	err = tx.QueryRowContext(ctx, `
        SELECT id, repo_id, commit_hash, branch, tag, event, status, log_file, created_at
        FROM ci_runs
        WHERE status = 'running'
        ORDER BY created_at DESC
        LIMIT 1
    `).Scan(&run.ID, &run.RepoID, &run.CommitHash, &run.Branch, &run.Tag,
		&run.Event, &run.Status, &run.LogFile, &createdAt)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	run.CreatedAt = unixToTime(createdAt)
	run.Status = "running"
	return &run, nil
}

// UpdateCIRunLogFile records the absolute path of the run's log file.
func (db *DB) UpdateCIRunLogFile(ctx context.Context, runID, logFile string) error {
	_, err := db.ExecContext(ctx,
		"UPDATE ci_runs SET log_file = ? WHERE id = ?", logFile, runID)
	return err
}

// CompleteCIRun sets status and completed_at for a finished run.
// status should be one of "success", "failed", or "skipped".
func (db *DB) CompleteCIRun(ctx context.Context, runID, status string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE ci_runs SET status = ?, completed_at = ? WHERE id = ?
	`, status, time.Now().Unix(), runID)
	return err
}

// GetCIRunsByRepo returns the most recent CI runs for a repository.
func (db *DB) GetCIRunsByRepo(ctx context.Context, repoID string, limit int) ([]models.CIRun, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, repo_id, commit_hash, branch, tag, event, status, log_file,
		       created_at, completed_at
		FROM ci_runs
		WHERE repo_id = ?
		ORDER BY created_at DESC
		LIMIT ?
	`, repoID, limit)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			slog.Warn("failed to close rows", "error", closeErr)
		}
	}()

	var runs []models.CIRun
	for rows.Next() {
		var r models.CIRun
		var createdAt int64
		var completedAt sql.NullInt64
		if err := rows.Scan(
			&r.ID, &r.RepoID, &r.CommitHash, &r.Branch, &r.Tag,
			&r.Event, &r.Status, &r.LogFile, &createdAt, &completedAt,
		); err != nil {
			return nil, err
		}
		r.CreatedAt = unixToTime(createdAt)
		r.CompletedAt = nullUnixToTime(completedAt)
		runs = append(runs, r)
	}
	return runs, nil
}

// GetCIRunByID fetches a single run by its UUID.
func (db *DB) GetCIRunByID(ctx context.Context, id string) (*models.CIRun, error) {
	var r models.CIRun
	var createdAt int64
	var completedAt sql.NullInt64
	err := db.QueryRowContext(ctx, `
		SELECT id, repo_id, commit_hash, branch, tag, event, status, log_file,
		       created_at, completed_at
		FROM ci_runs WHERE id = ?
	`, id).Scan(
		&r.ID, &r.RepoID, &r.CommitHash, &r.Branch, &r.Tag,
		&r.Event, &r.Status, &r.LogFile, &createdAt, &completedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.CreatedAt = unixToTime(createdAt)
	r.CompletedAt = nullUnixToTime(completedAt)
	return &r, nil
}

// GetSuccessfulCIRunsByRepo returns successful CI runs for a repository, newest first.
func (db *DB) GetSuccessfulCIRunsByRepo(ctx context.Context, repoID string, limit int) ([]models.CIRun, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, repo_id, commit_hash, branch, tag, event, status, log_file,
		       created_at, completed_at
		FROM ci_runs
		WHERE repo_id = ? AND status = 'success'
		ORDER BY created_at DESC
		LIMIT ?
	`, repoID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var runs []models.CIRun
	for rows.Next() {
		var r models.CIRun
		var createdAt int64
		var completedAt sql.NullInt64
		if err := rows.Scan(&r.ID, &r.RepoID, &r.CommitHash, &r.Branch, &r.Tag,
			&r.Event, &r.Status, &r.LogFile, &createdAt, &completedAt); err != nil {
			return nil, err
		}
		r.CreatedAt = unixToTime(createdAt)
		r.CompletedAt = nullUnixToTime(completedAt)
		runs = append(runs, r)
	}
	return runs, nil
}

// GetLatestSuccessfulRunForBranch returns the most recent successful run on a branch.
func (db *DB) GetLatestSuccessfulRunForBranch(ctx context.Context, repoID, branch string) (*models.CIRun, error) {
	return db.getSingleRun(ctx, `
		SELECT id, repo_id, commit_hash, branch, tag, event, status, log_file,
		       created_at, completed_at
		FROM ci_runs
		WHERE repo_id = ? AND branch = ? AND status = 'success'
		ORDER BY created_at DESC LIMIT 1
	`, repoID, branch)
}

// GetSuccessfulRunForTag returns the successful run associated with a git tag.
func (db *DB) GetSuccessfulRunForTag(ctx context.Context, repoID, tag string) (*models.CIRun, error) {
	return db.getSingleRun(ctx, `
		SELECT id, repo_id, commit_hash, branch, tag, event, status, log_file,
		       created_at, completed_at
		FROM ci_runs
		WHERE repo_id = ? AND tag = ? AND status = 'success'
		ORDER BY created_at DESC LIMIT 1
	`, repoID, tag)
}

// GetSuccessfulRunForCommit returns the successful run for a specific commit hash.
func (db *DB) GetSuccessfulRunForCommit(ctx context.Context, repoID, commitHash string) (*models.CIRun, error) {
	return db.getSingleRun(ctx, `
		SELECT id, repo_id, commit_hash, branch, tag, event, status, log_file,
		       created_at, completed_at
		FROM ci_runs
		WHERE repo_id = ? AND commit_hash = ? AND status = 'success'
		ORDER BY created_at DESC LIMIT 1
	`, repoID, commitHash)
}

func (db *DB) getSingleRun(ctx context.Context, query string, args ...any) (*models.CIRun, error) {
	var r models.CIRun
	var createdAt int64
	var completedAt sql.NullInt64
	err := db.QueryRowContext(ctx, query, args...).
		Scan(&r.ID, &r.RepoID, &r.CommitHash, &r.Branch, &r.Tag,
			&r.Event, &r.Status, &r.LogFile, &createdAt, &completedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.CreatedAt = unixToTime(createdAt)
	r.CompletedAt = nullUnixToTime(completedAt)
	return &r, nil
}

// AddRepoSecret inserts or replaces an encrypted secret for a repository.
// The caller is responsible for encrypting the value before calling this.
func (db *DB) AddRepoSecret(ctx context.Context, repoID, key, encryptedValue string) error {
	id := uuid.New().String()
	_, err := db.ExecContext(ctx, `
		INSERT INTO repo_secrets (id, repo_id, key, encrypted_value)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(repo_id, key)
		DO UPDATE SET encrypted_value = excluded.encrypted_value
	`, id, repoID, key, encryptedValue)
	return err
}

// GetRepoSecrets returns all secrets for a repository (encrypted values).
func (db *DB) GetRepoSecrets(ctx context.Context, repoID string) ([]models.RepoSecret, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, repo_id, key, encrypted_value, created_at
		FROM repo_secrets WHERE repo_id = ? ORDER BY key ASC
	`, repoID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			slog.Warn("failed to close rows", "error", closeErr)
		}
	}()

	var secrets []models.RepoSecret
	for rows.Next() {
		var s models.RepoSecret
		var createdAt int64
		if err := rows.Scan(&s.ID, &s.RepoID, &s.Key, &s.EncryptedValue, &createdAt); err != nil {
			return nil, err
		}
		s.CreatedAt = unixToTime(createdAt)
		secrets = append(secrets, s)
	}
	return secrets, nil
}

// DeleteRepoSecret removes a secret by ID, scoped to the given repo for safety.
func (db *DB) DeleteRepoSecret(ctx context.Context, id, repoID string) error {
	_, err := db.ExecContext(ctx,
		"DELETE FROM repo_secrets WHERE id = ? AND repo_id = ?", id, repoID)
	return err
}
