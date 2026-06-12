package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/mmrzaf/gitman/internal/models"
)

const ciRunColumns = `id, repo_id, commit_hash, branch, tag, event, status, log_file,
	cancel_reason, attempt_id, created_at, started_at, heartbeat_at, completed_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCIRun(scanner rowScanner) (*models.CIRun, error) {
	var r models.CIRun
	var createdAt int64
	var startedAt, heartbeatAt, completedAt sql.NullInt64
	if err := scanner.Scan(
		&r.ID, &r.RepoID, &r.CommitHash, &r.Branch, &r.Tag,
		&r.Event, &r.Status, &r.LogFile, &r.CancelReason, &r.AttemptID, &createdAt,
		&startedAt, &heartbeatAt, &completedAt,
	); err != nil {
		return nil, err
	}
	r.CreatedAt = unixToTime(createdAt)
	r.StartedAt = nullUnixToTime(startedAt)
	r.HeartbeatAt = nullUnixToTime(heartbeatAt)
	r.CompletedAt = nullUnixToTime(completedAt)
	return &r, nil
}

// CreateCIRun inserts a new pending run and returns its UUID.
func (db *DB) CreateCIRun(ctx context.Context, repoID, commitHash, branch, tag, event string) (string, error) {
	id := uuid.New().String()
	_, err := db.ExecContext(ctx, `
		INSERT INTO ci_runs (id, repo_id, commit_hash, branch, tag, event, status)
		VALUES (?, ?, ?, ?, ?, ?, 'pending')
	`, id, repoID, commitHash, branch, tag, event)
	return id, err
}

// CreatePushCIRun cancels older pending push runs for the same exact ref and
// inserts the new pending run atomically.
func (db *DB) CreatePushCIRun(ctx context.Context, repoID, commitHash, branch, tag string) (string, error) {
	if branch == "" && tag == "" {
		return "", fmt.Errorf("push run requires branch or tag")
	}
	if branch != "" && tag != "" {
		return "", fmt.Errorf("push run cannot target both branch and tag")
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return "", err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			slog.Warn("failed to rollback transaction", "error", rollbackErr)
		}
	}()

	reason := fmt.Sprintf("Superseded by newer push %s", shortCommit(commitHash))
	if branch != "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE ci_runs
			SET status = 'cancelled', cancel_reason = ?, completed_at = ?, heartbeat_at = NULL
			WHERE repo_id = ? AND event = 'push' AND status = 'pending'
			  AND branch = ? AND tag = ''
		`, reason, time.Now().Unix(), repoID, branch); err != nil {
			return "", err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE ci_runs
			SET status = 'cancelled', cancel_reason = ?, completed_at = ?, heartbeat_at = NULL
			WHERE repo_id = ? AND event = 'push' AND status = 'pending'
			  AND tag = ? AND branch = ''
		`, reason, time.Now().Unix(), repoID, tag); err != nil {
			return "", err
		}
	}

	id := uuid.New().String()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ci_runs (id, repo_id, commit_hash, branch, tag, event, status)
		VALUES (?, ?, ?, ?, ?, 'push', 'pending')
	`, id, repoID, commitHash, branch, tag); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

func shortCommit(commit string) string {
	if len(commit) <= 12 {
		return commit
	}
	return commit[:12]
}

// ClaimNextPendingRun atomically leases one pending run. A fresh attempt ID is
// generated for every claim so a stale worker cannot mutate a replacement run.
func (db *DB) ClaimNextPendingRun(ctx context.Context) (*models.CIRun, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			slog.Warn("failed to rollback transaction", "error", rollbackErr)
		}
	}()

	now := time.Now().Unix()
	attemptID := uuid.New().String()
	run, err := scanCIRun(tx.QueryRowContext(ctx, `
		UPDATE ci_runs
		SET status = 'running', attempt_id = ?, log_file = '', started_at = ?, heartbeat_at = ?, completed_at = NULL
		WHERE id = (
			SELECT id FROM ci_runs
			WHERE status = 'pending'
			ORDER BY created_at ASC
			LIMIT 1
		)
		RETURNING `+ciRunColumns, attemptID, now, now))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return run, nil
}

// HeartbeatCIRun refreshes the lease for one exact execution attempt.
func (db *DB) HeartbeatCIRun(ctx context.Context, runID, attemptID string) error {
	res, err := db.ExecContext(ctx,
		"UPDATE ci_runs SET heartbeat_at = ? WHERE id = ? AND attempt_id = ? AND status = 'running'",
		time.Now().Unix(), runID, attemptID,
	)
	if err != nil {
		return err
	}
	return requireAffectedRow(res, "CI run lease is no longer active")
}

// IsCIRunAttemptActive reports whether an attempt still owns a running lease.
func (db *DB) IsCIRunAttemptActive(ctx context.Context, runID, attemptID string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, `
		SELECT 1 FROM ci_runs
		WHERE id = ? AND attempt_id = ? AND status = 'running'
	`, runID, attemptID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// IsCIRunAttemptFresh reports whether an attempt owns a lease with a recent
// heartbeat. It is used when reconciling containers after worker crashes.
func (db *DB) IsCIRunAttemptFresh(ctx context.Context, runID, attemptID string, staleBefore time.Time) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, `
		SELECT 1 FROM ci_runs
		WHERE id = ? AND attempt_id = ? AND status = 'running'
		  AND heartbeat_at IS NOT NULL AND heartbeat_at >= ?
	`, runID, attemptID, staleBefore.Unix()).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// RequeueStaleCIRuns releases runs whose worker disappeared. The old attempt
// ID is cleared so stale workers lose authority immediately.
func (db *DB) RequeueStaleCIRuns(ctx context.Context, staleBefore time.Time) (int64, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE ci_runs
		SET status = 'pending', attempt_id = '', log_file = '', started_at = NULL, heartbeat_at = NULL
		WHERE status = 'running' AND (heartbeat_at IS NULL OR heartbeat_at < ?)
	`, staleBefore.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UpdateCIRunLogFile records the absolute log path for one active attempt.
func (db *DB) UpdateCIRunLogFile(ctx context.Context, runID, attemptID, logFile string) error {
	res, err := db.ExecContext(ctx, `
		UPDATE ci_runs SET log_file = ?
		WHERE id = ? AND attempt_id = ? AND status = 'running'
	`, logFile, runID, attemptID)
	if err != nil {
		return err
	}
	return requireAffectedRow(res, "CI run lease is no longer active")
}

// CompleteCIRun sets status and completed_at for one exact execution attempt.
func (db *DB) CompleteCIRun(ctx context.Context, runID, attemptID, status string) error {
	switch status {
	case "success", "failed", "skipped", "cancelled":
	default:
		return fmt.Errorf("invalid CI run status %q", status)
	}
	res, err := db.ExecContext(ctx, `
		UPDATE ci_runs SET status = ?, completed_at = ?, heartbeat_at = NULL
		WHERE id = ? AND attempt_id = ? AND status = 'running'
	`, status, time.Now().Unix(), runID, attemptID)
	if err != nil {
		return err
	}
	return requireAffectedRow(res, "CI run lease is no longer active")
}

func requireAffectedRow(res sql.Result, message string) error {
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New(message)
	}
	return nil
}

// GetCIRunsByRepo returns the most recent CI runs for a repository.
func (db *DB) GetCIRunsByRepo(ctx context.Context, repoID string, limit int) (runs []models.CIRun, err error) {
	rows, err := db.QueryContext(ctx, `SELECT `+ciRunColumns+`
		FROM ci_runs WHERE repo_id = ? ORDER BY created_at DESC LIMIT ?`, repoID, limit)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	for rows.Next() {
		r, err := scanCIRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *r)
	}
	return runs, rows.Err()
}

// GetCIRunByID fetches a single run by its UUID.
func (db *DB) GetCIRunByID(ctx context.Context, id string) (*models.CIRun, error) {
	r, err := scanCIRun(db.QueryRowContext(ctx,
		`SELECT `+ciRunColumns+` FROM ci_runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// GetSuccessfulCIRunsByRepo returns successful CI runs for a repository, newest first.
func (db *DB) GetSuccessfulCIRunsByRepo(ctx context.Context, repoID string, limit int) (runs []models.CIRun, err error) {
	rows, err := db.QueryContext(ctx, `SELECT `+ciRunColumns+`
		FROM ci_runs WHERE repo_id = ? AND status = 'success'
		ORDER BY created_at DESC LIMIT ?`, repoID, limit)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	for rows.Next() {
		r, err := scanCIRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *r)
	}
	return runs, rows.Err()
}

func (db *DB) GetLatestSuccessfulRunForBranch(ctx context.Context, repoID, branch string) (*models.CIRun, error) {
	return db.getSingleRun(ctx, `SELECT `+ciRunColumns+`
		FROM ci_runs WHERE repo_id = ? AND branch = ? AND status = 'success'
		ORDER BY created_at DESC LIMIT 1`, repoID, branch)
}

func (db *DB) GetSuccessfulRunForTag(ctx context.Context, repoID, tag string) (*models.CIRun, error) {
	return db.getSingleRun(ctx, `SELECT `+ciRunColumns+`
		FROM ci_runs WHERE repo_id = ? AND tag = ? AND status = 'success'
		ORDER BY created_at DESC LIMIT 1`, repoID, tag)
}

func (db *DB) GetSuccessfulRunForCommit(ctx context.Context, repoID, commitHash string) (*models.CIRun, error) {
	return db.getSingleRun(ctx, `SELECT `+ciRunColumns+`
		FROM ci_runs WHERE repo_id = ? AND commit_hash = ? AND status = 'success'
		ORDER BY created_at DESC LIMIT 1`, repoID, commitHash)
}

func (db *DB) getSingleRun(ctx context.Context, query string, args ...any) (*models.CIRun, error) {
	r, err := scanCIRun(db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// AddRepoSecret inserts or replaces an encrypted secret for a repository.
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

func (db *DB) GetRepoSecrets(ctx context.Context, repoID string) (secrets []models.RepoSecret, err error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, repo_id, key, encrypted_value, created_at
		FROM repo_secrets WHERE repo_id = ? ORDER BY key ASC
	`, repoID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	for rows.Next() {
		var s models.RepoSecret
		var createdAt int64
		if err := rows.Scan(&s.ID, &s.RepoID, &s.Key, &s.EncryptedValue, &createdAt); err != nil {
			return nil, err
		}
		s.CreatedAt = unixToTime(createdAt)
		secrets = append(secrets, s)
	}
	return secrets, rows.Err()
}

func (db *DB) DeleteRepoSecret(ctx context.Context, id, repoID string) error {
	_, err := db.ExecContext(ctx,
		"DELETE FROM repo_secrets WHERE id = ? AND repo_id = ?", id, repoID)
	return err
}
