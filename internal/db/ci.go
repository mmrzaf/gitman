package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mmrzaf/gitman/internal/models"
)

const ciRunColumns = `id, repo_id, commit_hash, branch, tag, event, status, log_file,
	cancel_reason, status_reason, retry_of_run_id, attempt_id, created_at, started_at, heartbeat_at, completed_at`

var ErrCIRunLeaseInactive = errors.New("CI run lease is no longer active")

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCIRun(scanner rowScanner) (*models.CIRun, error) {
	var r models.CIRun
	var createdAt int64
	var startedAt, heartbeatAt, completedAt sql.NullInt64
	if err := scanner.Scan(
		&r.ID, &r.RepoID, &r.CommitHash, &r.Branch, &r.Tag,
		&r.Event, &r.Status, &r.LogFile, &r.CancelReason, &r.StatusReason, &r.RetryOfRunID, &r.AttemptID, &createdAt,
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
	return db.CreatePushCIRunWithTriggerKey(ctx, repoID, commitHash, branch, tag, "")
}

// CreatePushCIRunWithTriggerKey atomically deduplicates a durable trigger,
// cancels older pending pushes for the same ref, and creates the new run.
func (db *DB) CreatePushCIRunWithTriggerKey(ctx context.Context, repoID, commitHash, branch, tag, triggerKey string) (string, error) {
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
	triggerKey = strings.TrimSpace(triggerKey)
	if triggerKey != "" {
		var existingID string
		err := tx.QueryRowContext(ctx, "SELECT id FROM ci_runs WHERE trigger_key = ?", triggerKey).Scan(&existingID)
		if err == nil {
			return existingID, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}

	reason := fmt.Sprintf("Superseded by newer push %s", shortCommit(commitHash))
	if branch != "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE ci_runs
			SET status = 'cancelled', cancel_reason = ?, status_reason = ?, completed_at = ?, heartbeat_at = NULL
			WHERE repo_id = ? AND event = 'push' AND status = 'pending'
			  AND branch = ? AND tag = ''
		`, reason, reason, time.Now().Unix(), repoID, branch); err != nil {
			return "", err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE ci_runs
			SET status = 'cancelled', cancel_reason = ?, status_reason = ?, completed_at = ?, heartbeat_at = NULL
			WHERE repo_id = ? AND event = 'push' AND status = 'pending'
			  AND tag = ? AND branch = ''
		`, reason, reason, time.Now().Unix(), repoID, tag); err != nil {
			return "", err
		}
	}

	id := uuid.New().String()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ci_runs (id, repo_id, commit_hash, branch, tag, event, status, trigger_key)
		VALUES (?, ?, ?, ?, ?, 'push', 'pending', ?)
	`, id, repoID, commitHash, branch, tag, triggerKey); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// RetryCIRun creates a new pending run with the exact source revision of a
// completed run. The source run is never mutated.
func (db *DB) RetryCIRun(ctx context.Context, repoID, runID string) (string, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return "", err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			slog.Warn("failed to rollback transaction", "error", rollbackErr)
		}
	}()

	var commitHash, branch, tag, status string
	if err := tx.QueryRowContext(ctx, `
		SELECT commit_hash, branch, tag, status
		FROM ci_runs WHERE id = ? AND repo_id = ?
	`, runID, repoID).Scan(&commitHash, &branch, &tag, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", sql.ErrNoRows
		}
		return "", err
	}
	switch status {
	case "success", "failed", "skipped", "cancelled":
	default:
		return "", fmt.Errorf("CI run is not complete")
	}

	newID := uuid.New().String()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ci_runs (
			id, repo_id, commit_hash, branch, tag, event, status, retry_of_run_id
		) VALUES (?, ?, ?, ?, ?, 'retry', 'pending', ?)
	`, newID, repoID, commitHash, branch, tag, runID); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return newID, nil
}

// CancelCIRun transitions a pending or running run to cancelled. A running
// worker loses its active lease on the next heartbeat because status is no
// longer running.
func (db *DB) CancelCIRun(ctx context.Context, repoID, runID, reason string) (bool, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "Cancelled by user"
	}
	now := time.Now().Unix()
	res, err := db.ExecContext(ctx, `
		UPDATE ci_runs
		SET status = 'cancelled', cancel_reason = ?, status_reason = ?, completed_at = ?,
			heartbeat_at = CASE WHEN status = 'running' THEN ? ELSE NULL END
		WHERE id = ? AND repo_id = ? AND status IN ('pending', 'running')
	`, reason, reason, now, now, runID, repoID)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	return rows > 0, err
}

// AcknowledgeCancelledCIRun records that the worker which owned a cancelled
// attempt has stopped touching repository-scoped files. The attempt ID remains
// immutable so its logs and artifacts stay addressable; heartbeat_at is reused
// as the transient stopping marker after cancellation.
func (db *DB) AcknowledgeCancelledCIRun(ctx context.Context, runID, attemptID string) error {
	res, err := db.ExecContext(ctx, `
		UPDATE ci_runs SET heartbeat_at = NULL
		WHERE id = ? AND attempt_id = ? AND status = 'cancelled' AND heartbeat_at IS NOT NULL
	`, runID, attemptID)
	if err != nil {
		return err
	}
	return requireAffectedRow(res, ErrCIRunLeaseInactive)
}

// ReleaseStaleCancelledCIRuns prevents a worker crash after cancellation from
// blocking repository deletion forever. The normal path is the explicit
// acknowledgement above; this is only crash recovery after a full lease.
func (db *DB) ReleaseStaleCancelledCIRuns(ctx context.Context, staleBefore time.Time) (int64, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE ci_runs SET heartbeat_at = NULL
		WHERE status = 'cancelled' AND attempt_id <> '' AND heartbeat_at IS NOT NULL
		  AND completed_at IS NOT NULL AND completed_at < ?
	`, staleBefore.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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
		SET status = 'running', attempt_id = ?, log_file = '', cancel_reason = '', status_reason = '',
			started_at = ?, heartbeat_at = ?, completed_at = NULL
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
	return requireAffectedRow(res, ErrCIRunLeaseInactive)
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
		SET status = 'pending', attempt_id = '', log_file = '', cancel_reason = '', status_reason = '',
			started_at = NULL, heartbeat_at = NULL, completed_at = NULL
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
	return requireAffectedRow(res, ErrCIRunLeaseInactive)
}

// CompleteCIRun sets status and completed_at for one exact execution attempt.
func (db *DB) CompleteCIRun(ctx context.Context, runID, attemptID, status string) error {
	return db.CompleteCIRunWithReason(ctx, runID, attemptID, status, "")
}

// CompleteCIRunWithReason records one terminal outcome and a concise
// user-visible explanation for the exact execution attempt.
func (db *DB) CompleteCIRunWithReason(ctx context.Context, runID, attemptID, status, reason string) error {
	switch status {
	case "success", "failed", "skipped", "cancelled":
	default:
		return fmt.Errorf("invalid CI run status %q", status)
	}
	res, err := db.ExecContext(ctx, `
		UPDATE ci_runs SET status = ?, status_reason = ?, completed_at = ?, heartbeat_at = NULL
		WHERE id = ? AND attempt_id = ? AND status = 'running'
	`, status, strings.TrimSpace(reason), time.Now().Unix(), runID, attemptID)
	if err != nil {
		return err
	}
	return requireAffectedRow(res, ErrCIRunLeaseInactive)
}

func requireAffectedRow(res sql.Result, noRowsErr error) error {
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return noRowsErr
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

// HasActiveCIRuns reports whether a repository still has queued or executing
// CI work. Repository deletion uses this to avoid racing active workers and
// leaving logs, artifacts, or caches behind after the database row is gone.
func (db *DB) HasActiveCIRuns(ctx context.Context, repoID string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, `
		SELECT 1 FROM ci_runs
		WHERE repo_id = ?
		  AND (status IN ('pending', 'running') OR (status = 'cancelled' AND heartbeat_at IS NOT NULL))
		LIMIT 1
	`, repoID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
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
