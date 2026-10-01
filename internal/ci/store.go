package ci

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/id"
	"github.com/mmrzaf/gitman/internal/postgres"
)

func allocateRunNumber(ctx context.Context, tx postgres.Querier, repoID string) (int64, error) {
	var number int64
	if err := tx.QueryRow(ctx,
		`UPDATE repos SET run_counter = run_counter + 1 WHERE id = $1 RETURNING run_counter`,
		repoID).Scan(&number); err != nil {
		return 0, fmt.Errorf("allocate run number: %w", postgres.NormalizeNotFound(err))
	}
	return number, nil
}

func insertRun(ctx context.Context, tx postgres.Querier, run *Created, p CreateParams, number int64, finished bool, timeout time.Duration) error {
	images := []string{}
	if cfg, err := Parse(p.Pipeline); err == nil {
		images = append(images, cfg.Image)
		images = append(images, cfg.Requires...)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO runs (id, repo_id, number, commit_hash, ref_kind, ref_name, trigger, triggered_by, push_id,
		                  status, reason, target, version, allow_secrets, pipeline, finished_at, timeout_ns, required_images)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), NULLIF($9, ''), $10, $11, $12, $13, $14,
		        CASE WHEN $15 THEN NULL ELSE $16::bytea END, CASE WHEN $15 THEN now() END, $17, $18)
	`, run.ID, p.RepoID, number, p.Commit, string(p.RefKind), p.RefName, p.Trigger, p.PersonID, p.PushID,
		run.Status, run.Reason, run.Target, run.Version, p.Decision.AllowSecrets, finished, p.Pipeline, int64(timeout), normalizeImages(images))
	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}
	return nil
}

func insertStep(ctx context.Context, tx postgres.Querier, runID string, index int, name string, kind StepKind, status StepStatus) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO steps (id, run_id, index, name, status, type) VALUES ($1, $2, $3, $4, $5, $6)
	`, id.New(), runID, index, name, status, kind)
	if err != nil {
		return fmt.Errorf("insert step: %w", err)
	}
	return nil
}

func notifyRun(ctx context.Context, tx postgres.Querier, runID string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, NotifyChannel, runID); err != nil {
		return fmt.Errorf("notify run: %w", err)
	}
	return nil
}

// supersedeQueued cancels queued runs for the same ref that are older
// than beforeNumber — every one, given math.MaxInt64 — and returns their
// IDs so the caller can notify each. A run already picked up by a
// worker keeps running: stopping work in progress is a person's
// decision, not a side effect of a push.
func supersedeQueued(ctx context.Context, tx postgres.Querier, repoID string, refKind git.Kind, refName string, beforeNumber int64, reason string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		UPDATE runs SET status = 'cancelled', reason = $5, finished_at = now()
		WHERE repo_id = $1 AND ref_kind = $2 AND ref_name = $3 AND status = 'queued' AND number < $4
		RETURNING id
	`, repoID, string(refKind), refName, beforeNumber, reason)
	if err != nil {
		return nil, fmt.Errorf("supersede runs: %w", err)
	}
	var ids []string
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan superseded run: %w", err)
		}
		ids = append(ids, runID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("supersede runs: %w", err)
	}
	for _, runID := range ids {
		if _, err := tx.Exec(ctx, `UPDATE steps SET status = 'cancelled' WHERE run_id = $1 AND status = 'pending'`, runID); err != nil {
			return nil, fmt.Errorf("cancel superseded steps: %w", err)
		}
	}
	return ids, nil
}

const summaryColumns = `
	r.id, repos.name, r.number, r.ref_kind, r.ref_name, r.commit_hash, r.trigger, r.status, r.reason, r.target,
	COALESCE(p.username, ''), r.queued_at, r.started_at, r.finished_at, EXISTS(SELECT 1 FROM deployments WHERE run_id = r.id)
`

func scanSummary(row interface{ Scan(...any) error }) (Summary, error) {
	var s Summary
	err := row.Scan(&s.ID, &s.RepoName, &s.Number, &s.RefKind, &s.RefName, &s.Commit, &s.Trigger, &s.Status, &s.Reason, &s.Target,
		&s.Actor, &s.QueuedAt, &s.StartedAt, &s.FinishedAt, &s.Deployed)
	return s, err
}

func selectInProgressForRepos(ctx context.Context, q postgres.Querier, repoIDs []string, limit int) ([]Summary, error) {
	rows, err := q.Query(ctx, `
		SELECT `+summaryColumns+`
		FROM runs r
		JOIN repos ON repos.id = r.repo_id
		LEFT JOIN people p ON p.id = r.triggered_by
		WHERE r.status IN ('queued', 'running') AND r.repo_id = ANY($1)
		ORDER BY COALESCE(r.started_at, r.queued_at) DESC
		LIMIT $2
	`, repoIDs, limit)
	if err != nil {
		return nil, fmt.Errorf("list in-progress runs: %w", err)
	}
	defer rows.Close()
	var result []Summary
	for rows.Next() {
		s, err := scanSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

func selectRunsForRepo(ctx context.Context, q postgres.Querier, repoID string, before int64, limit int) ([]Summary, error) {
	rows, err := q.Query(ctx, `
		SELECT `+summaryColumns+`
		FROM runs r
		JOIN repos ON repos.id = r.repo_id
		LEFT JOIN people p ON p.id = r.triggered_by
		WHERE r.repo_id = $1 AND ($2 = 0 OR r.number < $2)
		ORDER BY r.number DESC
		LIMIT $3
	`, repoID, before, limit)
	if err != nil {
		return nil, fmt.Errorf("list runs for %s: %w", repoID, err)
	}
	defer rows.Close()
	var result []Summary
	for rows.Next() {
		s, err := scanSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// refKey identifies one branch or tag within a repository, for keying a
// map of per-ref results.
func refKey(kind git.Kind, name string) string { return string(kind) + "/" + name }

func selectLatestHeadRunPerRef(ctx context.Context, q postgres.Querier, repoID string) (map[string]Summary, error) {
	rows, err := q.Query(ctx, `
		SELECT DISTINCT ON (r.ref_kind, r.ref_name) `+summaryColumns+`
		FROM runs r
		JOIN repos ON repos.id = r.repo_id
		LEFT JOIN people p ON p.id = r.triggered_by
		WHERE r.repo_id = $1 AND EXISTS (SELECT 1 FROM refs head WHERE head.repo_id = r.repo_id AND head.kind = r.ref_kind AND head.name = r.ref_name AND head.commit_hash = r.commit_hash)
		ORDER BY r.ref_kind, r.ref_name, r.number DESC
	`, repoID)
	if err != nil {
		return nil, fmt.Errorf("list latest runs per ref: %w", err)
	}
	defer rows.Close()
	result := map[string]Summary{}
	for rows.Next() {
		s, err := scanSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		result[refKey(s.RefKind, s.RefName)] = s
	}
	return result, rows.Err()
}

// selectRunsOfRef returns the newest limit runs of one branch or tag,
// newest first.
func selectRunsOfRef(ctx context.Context, q postgres.Querier, repoID string, kind git.Kind, name string, limit int) ([]Summary, error) {
	rows, err := q.Query(ctx, `
		SELECT `+summaryColumns+`
		FROM runs r
		JOIN repos ON repos.id = r.repo_id
		LEFT JOIN people p ON p.id = r.triggered_by
		WHERE r.repo_id = $1 AND r.ref_kind = $2 AND r.ref_name = $3
		ORDER BY r.number DESC
		LIMIT $4
	`, repoID, string(kind), name, limit)
	if err != nil {
		return nil, fmt.Errorf("list runs of %s %s: %w", kind, name, err)
	}
	defer rows.Close()
	var result []Summary
	for rows.Next() {
		s, err := scanSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// selectLatestDefaultHeadRuns returns the newest run of each repository's
// default branch, keyed by repository ID.
func selectLatestDefaultHeadRuns(ctx context.Context, q postgres.Querier, repoIDs []string) (map[string]Summary, error) {
	rows, err := q.Query(ctx, `
		SELECT DISTINCT ON (r.repo_id) r.repo_id, `+summaryColumns+`
		FROM runs r
		JOIN repos ON repos.id = r.repo_id
		LEFT JOIN people p ON p.id = r.triggered_by
		WHERE r.repo_id = ANY($1) AND r.ref_kind = 'branch' AND r.ref_name = repos.default_branch AND EXISTS(SELECT 1 FROM refs head WHERE head.repo_id = r.repo_id AND head.kind = 'branch' AND head.name = repos.default_branch AND head.commit_hash = r.commit_hash)
		ORDER BY r.repo_id, r.number DESC
	`, repoIDs)
	if err != nil {
		return nil, fmt.Errorf("list latest default branch runs: %w", err)
	}
	defer rows.Close()
	result := map[string]Summary{}
	for rows.Next() {
		var repoID string
		var s Summary
		if err := rows.Scan(&repoID, &s.ID, &s.RepoName, &s.Number, &s.RefKind, &s.RefName, &s.Commit, &s.Trigger, &s.Status, &s.Reason, &s.Target,
			&s.Actor, &s.QueuedAt, &s.StartedAt, &s.FinishedAt, &s.Deployed); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		result[repoID] = s
	}
	return result, rows.Err()
}

// selectLatestRunPerCommit returns the newest run of each of commits,
// keyed by commit; a commit no run was made for is left out.
func selectLatestRunPerCommit(ctx context.Context, q postgres.Querier, repoID string, commits []string) (map[string]Summary, error) {
	rows, err := q.Query(ctx, `
		SELECT DISTINCT ON (r.commit_hash) `+summaryColumns+`
		FROM runs r
		JOIN repos ON repos.id = r.repo_id
		LEFT JOIN people p ON p.id = r.triggered_by
		WHERE r.repo_id = $1 AND r.commit_hash = ANY($2)
		ORDER BY r.commit_hash, r.number DESC
	`, repoID, commits)
	if err != nil {
		return nil, fmt.Errorf("list latest runs per commit: %w", err)
	}
	defer rows.Close()
	result := map[string]Summary{}
	for rows.Next() {
		s, err := scanSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		result[s.Commit] = s
	}
	return result, rows.Err()
}

// selectLiveDeployments returns the latest deployment for every
// repository and target, or, with repoID, for one repository's targets.
func selectLiveDeployments(ctx context.Context, q postgres.Querier, repoID *string) ([]Deployment, error) {
	rows, err := q.Query(ctx, `
		SELECT DISTINCT ON (d.repo_id, d.target)
		       d.repo_id, d.target, d.version, d.commit_hash,
		       COALESCE(r.number, 0), COALESCE(p.username, ''), d.created_at
		FROM deployments d
		LEFT JOIN runs r ON r.id = d.run_id
		LEFT JOIN people p ON p.id = d.person_id
		WHERE $1::text IS NULL OR d.repo_id = $1
		ORDER BY d.repo_id, d.target, d.created_at DESC
	`, repoID)
	if err != nil {
		return nil, fmt.Errorf("list live deployments: %w", err)
	}
	defer rows.Close()
	var result []Deployment
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(&d.RepoID, &d.Target, &d.Version, &d.Commit, &d.RunNumber, &d.Person, &d.At); err != nil {
			return nil, fmt.Errorf("scan deployment: %w", err)
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

// selectLiveDeploymentsForRepos is selectLiveDeployments, restricted to
// repoIDs — the instance-wide board for someone who cannot necessarily
// see every repository.
func selectLiveDeploymentsForRepos(ctx context.Context, q postgres.Querier, repoIDs []string) ([]Deployment, error) {
	rows, err := q.Query(ctx, `
		SELECT DISTINCT ON (d.repo_id, d.target)
		       d.repo_id, d.target, d.version, d.commit_hash,
		       COALESCE(r.number, 0), COALESCE(p.username, ''), d.created_at
		FROM deployments d
		LEFT JOIN runs r ON r.id = d.run_id
		LEFT JOIN people p ON p.id = d.person_id
		WHERE d.repo_id = ANY($1)
		ORDER BY d.repo_id, d.target, d.created_at DESC
	`, repoIDs)
	if err != nil {
		return nil, fmt.Errorf("list live deployments: %w", err)
	}
	defer rows.Close()
	var result []Deployment
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(&d.RepoID, &d.Target, &d.Version, &d.Commit, &d.RunNumber, &d.Person, &d.At); err != nil {
			return nil, fmt.Errorf("scan deployment: %w", err)
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

func selectLatestDeploymentPerRef(ctx context.Context, q postgres.Querier, repoID string) (map[string]Deployment, error) {
	rows, err := q.Query(ctx, `
		SELECT DISTINCT ON (r.ref_kind, r.ref_name)
		       r.ref_kind, r.ref_name, d.repo_id, d.target, d.version, d.commit_hash,
		       r.number, COALESCE(p.username, ''), d.created_at
		FROM deployments d
		JOIN runs r ON r.id = d.run_id
		LEFT JOIN people p ON p.id = d.person_id
		WHERE d.repo_id = $1 AND r.repo_id = $1 AND r.ref_kind <> ''
		ORDER BY r.ref_kind, r.ref_name, d.created_at DESC
	`, repoID)
	if err != nil {
		return nil, fmt.Errorf("list latest deployments per ref: %w", err)
	}
	defer rows.Close()
	result := map[string]Deployment{}
	for rows.Next() {
		var kind, name string
		var d Deployment
		if err := rows.Scan(&kind, &name, &d.RepoID, &d.Target, &d.Version, &d.Commit, &d.RunNumber, &d.Person, &d.At); err != nil {
			return nil, fmt.Errorf("scan deployment: %w", err)
		}
		result[refKey(git.Kind(kind), name)] = d
	}
	return result, rows.Err()
}

// claimNextRun marks the oldest queued run as running for workerID and
// returns it. SKIP LOCKED lets any number of workers claim concurrently
// without ever taking the same run. It returns postgres.ErrNotFound when
// nothing is queued.
func claimNextRun(ctx context.Context, tx postgres.Tx, workerID, fetchTokenHash string) (*Claim, error) {
	c := &Claim{}
	err := tx.QueryRow(ctx, `
		UPDATE runs SET status = 'running', started_at = now(), worker_id = $1, fetch_token_hash = $2
		FROM repos
		WHERE runs.id = (
			SELECT id FROM runs WHERE status = 'queued' ORDER BY queued_at, id
			FOR UPDATE SKIP LOCKED LIMIT 1
		) AND repos.id = runs.repo_id
		RETURNING runs.id, runs.repo_id, repos.name, runs.number, runs.commit_hash, runs.ref_kind,
		          runs.ref_name, runs.target, runs.version, runs.allow_secrets, runs.pipeline
	`, workerID, fetchTokenHash).Scan(&c.RunID, &c.RepoID, &c.RepoName, &c.Number, &c.Commit, &c.RefKind,
		&c.RefName, &c.Target, &c.Version, &c.AllowSecrets, &c.Pipeline)
	if err != nil {
		return nil, postgres.NormalizeNotFound(err)
	}
	rows, err := tx.Query(ctx, `SELECT id, index, name, status = 'skipped' FROM steps WHERE run_id = $1 ORDER BY index`, c.RunID)
	if err != nil {
		return nil, fmt.Errorf("list steps: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var st ClaimedStep
		if err := rows.Scan(&st.ID, &st.Index, &st.Name, &st.Skipped); err != nil {
			return nil, fmt.Errorf("scan step: %w", err)
		}
		c.Steps = append(c.Steps, st)
	}
	return c, rows.Err()
}

func selectRunningRepoByFetchToken(ctx context.Context, q postgres.Querier, tokenHash string) (string, error) {
	var repoID string
	err := q.QueryRow(ctx, `SELECT repo_id FROM runs WHERE fetch_token_hash = $1 AND status = 'running'`, tokenHash).Scan(&repoID)
	return repoID, postgres.NormalizeNotFound(err)
}

// setStepRunning and setStepFinished change a step only while its run is
// running: a run that has ended — failed as lost while its worker was
// cut off, say — keeps the step statuses it ended with. setStepRunning
// returns ErrRunEnded for such a run, so its worker starts nothing more.
func setStepRunning(ctx context.Context, q postgres.Querier, stepID string) error {
	tag, err := q.Exec(ctx, `
		UPDATE steps SET status = 'running', started_at = now()
		WHERE id = $1 AND status = 'pending'
		  AND EXISTS (SELECT 1 FROM runs WHERE runs.id = steps.run_id AND runs.status = 'running'
		              AND NOT runs.cancel_requested)
	`, stepID)
	if err != nil {
		return fmt.Errorf("start step: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRunEnded
	}
	return nil
}

func setStepFinished(ctx context.Context, q postgres.Querier, stepID string, status StepStatus, exitCode *int) error {
	if _, err := q.Exec(ctx, `
		UPDATE steps SET status = $2, exit_code = $3, finished_at = now()
		WHERE id = $1 AND status IN ('pending', 'running')
		  AND EXISTS (SELECT 1 FROM runs WHERE runs.id = steps.run_id AND runs.status = 'running')
	`, stepID, status, exitCode); err != nil {
		return fmt.Errorf("finish step: %w", err)
	}
	return nil
}

// settleOpenSteps gives every step of a run that never finished a final
// status: a running step becomes runningBecomes, a pending one
// pendingBecomes.
func settleOpenSteps(ctx context.Context, q postgres.Querier, runID string, runningBecomes, pendingBecomes StepStatus) error {
	if _, err := q.Exec(ctx, `
		UPDATE steps SET
			status = CASE status WHEN 'running' THEN $2 ELSE $3 END,
			finished_at = now()
		WHERE run_id = $1 AND status IN ('pending', 'running')
	`, runID, runningBecomes, pendingBecomes); err != nil {
		return fmt.Errorf("settle steps: %w", err)
	}
	return nil
}

// insertLogChunk stores one chunk. Storing the same chunk again is not an
// error: a worker that timed out waiting for the answer retries it
// without knowing whether the first attempt was stored. It silently does
// nothing once its run has ended — failed as lost while its worker kept
// writing, say — the same way setStepFinished leaves an ended run's
// steps alone.
func insertLogChunk(ctx context.Context, q postgres.Querier, stepID string, sequence int, content string) error {
	if _, err := q.Exec(ctx, `
		INSERT INTO step_logs (step_id, sequence, content, byte_len)
		SELECT $1, $2, $3, $4
		WHERE EXISTS (SELECT 1 FROM steps JOIN runs ON runs.id = steps.run_id
		              WHERE steps.id = $1 AND runs.status = 'running')
		ON CONFLICT (step_id, sequence) DO NOTHING
	`, stepID, sequence, content, len(content)); err != nil {
		return fmt.Errorf("append log: %w", err)
	}
	return nil
}

func notify(ctx context.Context, q postgres.Querier, channel, payload string) error {
	if _, err := q.Exec(ctx, `SELECT pg_notify($1, $2)`, channel, payload); err != nil {
		return fmt.Errorf("notify %s: %w", channel, err)
	}
	return nil
}

func selectRunState(ctx context.Context, q postgres.Querier, runID string) (Status, bool, error) {
	var status Status
	var cancelRequested bool
	err := q.QueryRow(ctx, `SELECT status, cancel_requested FROM runs WHERE id = $1`, runID).Scan(&status, &cancelRequested)
	return status, cancelRequested, postgres.NormalizeNotFound(err)
}

func selectRepoIDForRun(ctx context.Context, q postgres.Querier, runID string) (string, error) {
	var repoID string
	err := q.QueryRow(ctx, `SELECT repo_id FROM runs WHERE id = $1`, runID).Scan(&repoID)
	return repoID, postgres.NormalizeNotFound(err)
}

func selectRepoIDForStep(ctx context.Context, q postgres.Querier, stepID string) (string, error) {
	var repoID string
	err := q.QueryRow(ctx, `
		SELECT runs.repo_id FROM steps JOIN runs ON runs.id = steps.run_id WHERE steps.id = $1
	`, stepID).Scan(&repoID)
	return repoID, postgres.NormalizeNotFound(err)
}

// lockRunRepo takes the lock recording a deployment needs on a run's
// repository row before Finish locks the run itself. Deleting a
// repository locks that row first and its runs after; taking them in
// the other order here would let the two deadlock.
func lockRunRepo(ctx context.Context, tx postgres.Tx, runID string) error {
	_, err := tx.Exec(ctx, `
		SELECT 1 FROM repos WHERE id = (SELECT repo_id FROM runs WHERE id = $1) FOR KEY SHARE
	`, runID)
	if err != nil {
		return fmt.Errorf("lock run's repository: %w", err)
	}
	return nil
}

// finishedRun is what finishing a run needs to know about it to record a
// deployment.
type finishedRun struct {
	repoID, target, version, commit string
	triggeredBy                     *string
}

// finishRunRow records a running run's outcome and clears its fetch
// token. A cancelled run keeps the reason set when cancellation was
// requested. It returns postgres.ErrNotFound if the run is not running,
// which happens when it was already finished by the lost-worker reaper.
func finishRunRow(ctx context.Context, tx postgres.Tx, runID string, status Status, reason string) (*finishedRun, error) {
	f := &finishedRun{}
	err := tx.QueryRow(ctx, `
		UPDATE runs SET status = $2,
			reason = CASE WHEN $2 = 'cancelled' AND reason <> '' THEN reason ELSE $3 END,
			finished_at = now(), fetch_token_hash = NULL
		WHERE id = $1 AND status = 'running'
		RETURNING repo_id, target, version, commit_hash, triggered_by
	`, runID, status, reason).Scan(&f.repoID, &f.target, &f.version, &f.commit, &f.triggeredBy)
	if err != nil {
		return nil, postgres.NormalizeNotFound(err)
	}
	return f, nil
}

func insertSummary(ctx context.Context, tx postgres.Tx, runID, key, value string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO run_summary (run_id, key, value) VALUES ($1, $2, $3)
		ON CONFLICT (run_id, key) DO UPDATE SET value = EXCLUDED.value
	`, runID, key, value); err != nil {
		return fmt.Errorf("record summary: %w", err)
	}
	return nil
}

func insertDeployment(ctx context.Context, tx postgres.Tx, runID string, f *finishedRun) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO deployments (id, repo_id, target, version, commit_hash, run_id, person_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, id.New(), f.repoID, f.target, f.version, f.commit, runID, f.triggeredBy); err != nil {
		return fmt.Errorf("record deployment: %w", err)
	}
	return nil
}

// cancelRunRow cancels a run by repository and number: a queued run is
// cancelled outright, a running one is flagged for its worker to stop.
// It returns the run's ID and status as they were before the change, or
// postgres.ErrNotFound for no such run.
func cancelRunRow(ctx context.Context, tx postgres.Tx, repoID string, number int64, reason string) (runID string, before Status, err error) {
	err = tx.QueryRow(ctx, `SELECT id, status FROM runs WHERE repo_id = $1 AND number = $2 FOR UPDATE`, repoID, number).
		Scan(&runID, &before)
	if err != nil {
		return "", "", postgres.NormalizeNotFound(err)
	}
	switch before {
	case StatusQueued:
		if _, err := tx.Exec(ctx, `
			UPDATE runs SET status = 'cancelled', reason = $2, finished_at = now() WHERE id = $1
		`, runID, reason); err != nil {
			return "", "", fmt.Errorf("cancel run: %w", err)
		}
		if err := settleOpenSteps(ctx, tx, runID, StepCancelled, StepCancelled); err != nil {
			return "", "", err
		}
	case StatusRunning:
		if _, err := tx.Exec(ctx, `
			UPDATE runs SET cancel_requested = true, reason = $2 WHERE id = $1
		`, runID, reason); err != nil {
			return "", "", fmt.Errorf("request cancellation: %w", err)
		}
	}
	return runID, before, nil
}

func upsertWorker(ctx context.Context, q postgres.Querier, workerID, hostname string) error {
	if _, err := q.Exec(ctx, `
		INSERT INTO workers (id, hostname) VALUES ($1, $2)
		ON CONFLICT (id) DO UPDATE SET heartbeat_at = now(), stopped_at = NULL
	`, workerID, hostname); err != nil {
		return fmt.Errorf("register worker: %w", err)
	}
	return nil
}

func touchWorker(ctx context.Context, q postgres.Querier, workerID string, activeRuns int) error {
	if _, err := q.Exec(ctx, `
		UPDATE workers SET heartbeat_at = now(), active_runs = $2 WHERE id = $1
	`, workerID, activeRuns); err != nil {
		return fmt.Errorf("heartbeat: %w", err)
	}
	return nil
}

func markWorkerStopped(ctx context.Context, q postgres.Querier, workerID string) error {
	if _, err := q.Exec(ctx, `
		UPDATE workers SET stopped_at = now(), active_runs = 0 WHERE id = $1
	`, workerID); err != nil {
		return fmt.Errorf("stop worker: %w", err)
	}
	return nil
}

// selectAnyWorkerOnline reports whether a worker has not stopped and has
// heartbeated within staleAfter, judged by the database's clock, as
// selectLostRuns does.
func selectAnyWorkerOnline(ctx context.Context, q postgres.Querier, staleAfter time.Duration) (bool, error) {
	var online bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM workers
			WHERE stopped_at IS NULL AND heartbeat_at >= now() - make_interval(secs => $1)
		)
	`, staleAfter.Seconds()).Scan(&online); err != nil {
		return false, fmt.Errorf("check for online workers: %w", err)
	}
	return online, nil
}

// selectLostRuns locks and returns running runs whose worker has not
// heartbeated in the last staleAfter, or has stopped. The schema keeps a
// running run's worker in existence.
// The cutoff is computed by the database from its own clock: workers
// heartbeat with the database's now(), so comparing against a caller's
// local clock would make detection depend on clock skew between the
// database host and whichever worker happens to call this.
func selectLostRuns(ctx context.Context, tx postgres.Tx, staleAfter time.Duration) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT runs.id FROM runs
		JOIN workers ON workers.id = runs.worker_id
		WHERE runs.status = 'running'
		  AND (workers.stopped_at IS NOT NULL OR workers.heartbeat_at < now() - make_interval(secs => $1))
		FOR UPDATE OF runs SKIP LOCKED
	`, staleAfter.Seconds())
	if err != nil {
		return nil, fmt.Errorf("find lost runs: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			return nil, fmt.Errorf("scan lost run: %w", err)
		}
		ids = append(ids, runID)
	}
	return ids, rows.Err()
}

func selectRunningRunIDs(ctx context.Context, q postgres.Querier) (map[string]bool, error) {
	rows, err := q.Query(ctx, `SELECT id FROM runs WHERE status = 'running'`)
	if err != nil {
		return nil, fmt.Errorf("list running runs: %w", err)
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			return nil, fmt.Errorf("scan running run: %w", err)
		}
		ids[runID] = true
	}
	return ids, rows.Err()
}

func selectRunDetail(ctx context.Context, q postgres.Querier, repoID string, number int64) (*RunDetail, error) {
	d := &RunDetail{}
	err := q.QueryRow(ctx, `
		SELECT `+summaryColumns+`, r.repo_id, r.version, r.allow_secrets, r.cancel_requested
		FROM runs r
		JOIN repos ON repos.id = r.repo_id
		LEFT JOIN people p ON p.id = r.triggered_by
		WHERE r.repo_id = $1 AND r.number = $2
	`, repoID, number).Scan(&d.ID, &d.RepoName, &d.Number, &d.RefKind, &d.RefName, &d.Commit, &d.Trigger, &d.Status, &d.Reason, &d.Target,
		&d.Actor, &d.QueuedAt, &d.StartedAt, &d.FinishedAt, &d.Deployed,
		&d.RepoID, &d.Version, &d.AllowSecrets, &d.CancelRequested)
	if err != nil {
		return nil, postgres.NormalizeNotFound(err)
	}

	rows, err := q.Query(ctx, `
		SELECT s.id, s.index, s.name, s.type, s.status, s.exit_code, s.started_at, s.finished_at,
		       s.log_bytes,s.logs_expired_at,s.log_recording_error
		FROM steps s WHERE s.run_id = $1 ORDER BY s.index
	`, d.ID)
	if err != nil {
		return nil, fmt.Errorf("list steps: %w", err)
	}
	for rows.Next() {
		var st StepDetail
		if err := rows.Scan(&st.ID, &st.Index, &st.Name, &st.Type, &st.Status, &st.ExitCode, &st.StartedAt, &st.FinishedAt, &st.LogBytes, &st.LogsExpiredAt, &st.LogRecordingError); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan step: %w", err)
		}
		d.Steps = append(d.Steps, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list steps: %w", err)
	}

	rows, err = q.Query(ctx, `SELECT key, value FROM run_summary WHERE run_id = $1 ORDER BY key`, d.ID)
	if err != nil {
		return nil, fmt.Errorf("read summary: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e SummaryEntry
		if err := rows.Scan(&e.Key, &e.Value); err != nil {
			return nil, fmt.Errorf("scan summary: %w", err)
		}
		d.Results = append(d.Results, e)
	}
	return d, rows.Err()
}

func selectLogChunks(ctx context.Context, q postgres.Querier, stepID string, after, limit int) ([]LogChunk, error) {
	rows, err := q.Query(ctx, `
		SELECT sequence, content FROM step_logs
		WHERE step_id = $1 AND sequence > $2 ORDER BY sequence LIMIT $3
	`, stepID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("read log: %w", err)
	}
	defer rows.Close()
	var chunks []LogChunk
	for rows.Next() {
		var c LogChunk
		if err := rows.Scan(&c.Sequence, &c.Content); err != nil {
			return nil, fmt.Errorf("scan log: %w", err)
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

// deleteOldRuns deletes up to limit runs that finished before `before`,
// never the latest finished run of any ref. Steps, logs and summaries go
// with them through foreign keys; deployments stay, their run reference
// cleared.
func deleteOldRuns(ctx context.Context, q postgres.Querier, before time.Time, limit int) (int64, error) {
	tag, err := q.Exec(ctx, `
		DELETE FROM runs WHERE id IN (
			SELECT r.id FROM runs r
			WHERE r.finished_at < $1
			  AND NOT EXISTS(SELECT 1 FROM steps s WHERE s.run_id=r.id AND s.container_name IS NOT NULL AND s.container_removed_at IS NULL)
			  AND NOT EXISTS(SELECT 1 FROM deployment_targets WHERE owner_run_id=r.id)
			  AND r.id NOT IN (
				SELECT DISTINCT ON (head.repo_id,head.kind,head.name) latest.id FROM refs head JOIN runs latest ON latest.repo_id=head.repo_id AND latest.ref_kind=head.kind AND latest.ref_name=head.name AND latest.commit_hash=head.commit_hash
				WHERE latest.finished_at IS NOT NULL ORDER BY head.repo_id,head.kind,head.name,latest.number DESC
			  )
			LIMIT $2
		)
	`, before, limit)
	if err != nil {
		return 0, fmt.Errorf("delete old runs: %w", err)
	}
	return tag.RowsAffected(), nil
}

func deleteGoneWorkers(ctx context.Context, q postgres.Querier, before time.Time) (int64, error) {
	tag, err := q.Exec(ctx, `
		DELETE FROM workers
		WHERE COALESCE(stopped_at, heartbeat_at) < $1
		  AND NOT EXISTS (SELECT 1 FROM runs WHERE runs.worker_id = workers.id AND runs.status = 'running')
	`, before)
	if err != nil {
		return 0, fmt.Errorf("delete gone workers: %w", err)
	}
	return tag.RowsAffected(), nil
}

func selectInstanceID(ctx context.Context, q postgres.Querier) (string, error) {
	var instanceID string
	err := q.QueryRow(ctx, `SELECT id FROM instance`).Scan(&instanceID)
	if err != nil {
		return "", fmt.Errorf("read instance ID: %w", postgres.NormalizeNotFound(err))
	}
	return instanceID, nil
}

// selectLogTail reads at most 256 recent chunks, retaining only chunks within
// the byte/line window. Content transferred is bounded by the window plus
// one chunk; line accounting comes from metadata rather than old content.
func selectLogTail(ctx context.Context, q postgres.Querier, stepID string) (*LogTail, error) {
	tail := &LogTail{After: -1, First: 1}
	var totalBytes, totalLines int64
	rows, err := q.Query(ctx, `WITH recent AS (
 SELECT sequence, content, byte_len, line_count FROM step_logs WHERE step_id = $1 ORDER BY sequence DESC LIMIT 256
 ), counted AS (
 SELECT *, COALESCE(SUM(byte_len) OVER (ORDER BY sequence DESC ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING),0) AS prior_bytes,
 COALESCE(SUM(line_count) OVER (ORDER BY sequence DESC ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING),0) AS prior_lines FROM recent
 ) SELECT COALESCE(c.sequence,-1), COALESCE(c.content,''), s.log_bytes, s.log_lines FROM steps s LEFT JOIN counted c ON c.prior_bytes < 1048576 AND c.prior_lines < 20000 WHERE s.id = $1 ORDER BY c.sequence`, stepID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var text strings.Builder
	for rows.Next() {
		var content string
		if err := rows.Scan(&tail.After, &content, &totalBytes, &totalLines); err != nil {
			return nil, err
		}
		text.WriteString(content)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	tail.Text = text.String()
	tail.First = int(totalLines) - strings.Count(tail.Text, "\n") + 1
	cut := max(0, len(tail.Text)-(1<<20))
	if cut > 0 {
		for cut < len(tail.Text) && !utf8.RuneStart(tail.Text[cut]) {
			cut++
		}
		if n := strings.IndexByte(tail.Text[cut:], '\n'); n >= 0 {
			cut += n + 1
		}
	}
	lines := strings.Count(tail.Text[cut:], "\n")
	if !strings.HasSuffix(tail.Text, "\n") && len(tail.Text) > cut {
		lines++
	}
	for lines > 20000 {
		n := strings.IndexByte(tail.Text[cut:], '\n')
		if n < 0 {
			break
		}
		cut += n + 1
		lines--
	}
	tail.First += strings.Count(tail.Text[:cut], "\n")
	tail.Text = tail.Text[cut:]
	tail.Cut = int64(len(tail.Text)) < totalBytes
	return tail, nil
}
