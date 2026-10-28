package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/id"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/repo"
	"github.com/mmrzaf/gitman/internal/token"
)

// Service creates runs and reads runs and deployments.
type Service struct {
	db *postgres.DB
}

// NewService returns a Service backed by db.
func NewService(db *postgres.DB) *Service {
	return &Service{db: db}
}

// InProgressForRepos is InProgress, restricted to repoIDs — the
// instance-wide "running now" list for someone who cannot necessarily
// see every repository.
func (s *Service) InProgressForRepos(ctx context.Context, repoIDs []string, limit int) ([]Summary, error) {
	return selectInProgressForRepos(ctx, s.db.Q, repoIDs, limit)
}

// LatestHeadRunPerRef returns, for a repository, the most recent run on each
// of its refs, keyed by "<kind>/<name>".
func (s *Service) LatestHeadRunPerRef(ctx context.Context, repoID string) (map[string]Summary, error) {
	return selectLatestHeadRunPerRef(ctx, s.db.Q, repoID)
}

// RunsForRepo returns a page of a repository's runs, newest first. before,
// when nonzero, limits it to runs numbered lower than it, for paging
// backward through history.
func (s *Service) RunsForRepo(ctx context.Context, repoID string, before int64, limit int) ([]Summary, error) {
	return selectRunsForRepo(ctx, s.db.Q, repoID, before, limit)
}

// RunsOfRef returns the newest limit runs of one branch or tag, newest
// first.
func (s *Service) RunsOfRef(ctx context.Context, repoID string, kind git.Kind, name string, limit int) ([]Summary, error) {
	return selectRunsOfRef(ctx, s.db.Q, repoID, kind, name, limit)
}

// LatestDefaultHeadRuns returns the newest run of each of repoIDs' default
// branch, keyed by repository ID, for the ones that have one.
func (s *Service) LatestDefaultHeadRuns(ctx context.Context, repoIDs []string) (map[string]Summary, error) {
	return selectLatestDefaultHeadRuns(ctx, s.db.Q, repoIDs)
}

// LatestRunPerCommit returns the newest run of each of commits, keyed by
// commit, for the ones that have a run.
func (s *Service) LatestRunPerCommit(ctx context.Context, repoID string, commits []string) (map[string]Summary, error) {
	return selectLatestRunPerCommit(ctx, s.db.Q, repoID, commits)
}

// LiveForRepo is Live, scoped to one repository.
func (s *Service) LiveForRepo(ctx context.Context, repoID string) ([]Deployment, error) {
	return selectLiveDeployments(ctx, s.db.Q, repoID)
}

// LiveForRepos is Live, restricted to repoIDs.
func (s *Service) LiveForRepos(ctx context.Context, repoIDs []string) ([]Deployment, error) {
	return selectLiveDeploymentsForRepos(ctx, s.db.Q, repoIDs)
}

// LatestDeploymentPerRef returns, for a repository, the most recent
// deployment made by a run on each of its refs — what a branch or tag
// last shipped — keyed by "<kind>/<name>". A deployment whose run has
// since been pruned cannot be attributed to a ref and is left out;
// LiveForRepo, not this, is the source of truth for what is live.
func (s *Service) LatestDeploymentPerRef(ctx context.Context, repoID string) (map[string]Deployment, error) {
	return selectLatestDeploymentPerRef(ctx, s.db.Q, repoID)
}

// CreateTx records a new run inside the caller's transaction, cancels
// any older queued run for the same ref (a newer commit makes it moot),
// and publishes the run on NotifyChannel when the transaction commits.
//
// Everything decidable when a run is created is decided here: which
// target the ref resolves to, which steps apply, and whether the ref's
// rule allows what the pipeline asks for. A run that cannot proceed
// fails at creation with a reason that says why, instead of being
// handed to a worker to discover the same thing later.
func (s *Service) CreateTx(ctx context.Context, tx postgres.Tx, p CreateParams) (*Created, error) {
	pl := planRun(p)
	if pl.status == StatusQueued {
		var full bool
		if err := tx.QueryRow(ctx, `SELECT count(*)>=10000 FROM runs WHERE repo_id=$1 AND status IN ('queued','running')`, p.RepoID).Scan(&full); err != nil {
			return nil, err
		}
		if full {
			pl.status = StatusFailed
			pl.reason = "This repository reached its 10,000 pending-run budget. Clear queued work before starting another run."
		}
	}
	pl.reason = truncateReason(pl.reason)

	number, err := allocateRunNumber(ctx, tx, p.RepoID)
	if err != nil {
		return nil, err
	}

	run := &Created{ID: id.New(), Number: number, Status: pl.status, Reason: pl.reason, Target: pl.target, Version: pl.version}
	finished := pl.status != StatusQueued
	if err := insertRun(ctx, tx, run, p, number, finished, pl.timeout); err != nil {
		return nil, err
	}

	for i, step := range pl.steps {
		status := StepSkipped
		if pl.status == StatusQueued && step.run {
			status = StepPending
		}
		if err := insertStep(ctx, tx, run.ID, i, step.name, step.kind, status); err != nil {
			return nil, err
		}
	}

	superseded, err := supersedeQueued(ctx, tx, p.RepoID, p.RefKind, p.RefName, run.Number, fmt.Sprintf("Superseded by #%d.", run.Number))
	if err != nil {
		return nil, err
	}
	for _, runID := range superseded {
		if err := notifyRun(ctx, tx, runID); err != nil {
			return nil, err
		}
	}
	if err := notifyRun(ctx, tx, run.ID); err != nil {
		return nil, err
	}
	return run, nil
}

// CancelQueuedTx cancels every queued run of a ref inside the caller's
// transaction, giving reason, and publishes each on NotifyChannel: a
// deleted ref's queued runs would otherwise run, and ship, a ref that no
// longer exists.
func (s *Service) CancelQueuedTx(ctx context.Context, tx postgres.Tx, repoID string, refKind git.Kind, refName, reason string) error {
	cancelled, err := supersedeQueued(ctx, tx, repoID, refKind, refName, math.MaxInt64, reason)
	if err != nil {
		return err
	}
	for _, runID := range cancelled {
		if err := notifyRun(ctx, tx, runID); err != nil {
			return err
		}
	}
	return nil
}

// LoadPipeline reads the pipeline file at a commit. A missing or oversized
// file is not an error: it is a problem, returned as a reason the run
// that would have used it fails with. err is reserved for failing to read
// the repository at all.
func LoadPipeline(ctx context.Context, gitRepo *git.Repo, commit string) (data []byte, problem string, err error) {
	short := commit
	if len(short) > 7 {
		short = short[:7]
	}
	data, err = gitRepo.FileAt(ctx, commit, FileName, MaxFileBytes)
	var tooLarge *git.TooLargeError
	switch {
	case errors.Is(err, git.ErrNotFound):
		return nil, fmt.Sprintf("There is no %s at %s.", FileName, short), nil
	case errors.As(err, &tooLarge):
		return nil, fmt.Sprintf("%s at %s is %d bytes; the limit is %d.", FileName, short, tooLarge.Size, MaxFileBytes), nil
	case err != nil:
		return nil, "", fmt.Errorf("read %s: %w", FileName, err)
	}
	return data, "", nil
}

// ErrInvalidFetchToken is returned for a fetch token that does not
// belong to a running run.
var ErrInvalidFetchToken = errors.New("invalid fetch token")

// ErrRunFinished is returned when cancelling a run that already ended.
var ErrRunFinished = errors.New("the run has already finished")

// ErrRunEnded is returned when starting a step of a run that is no longer
// running, or that someone asked to cancel: nothing more of it may start.
var ErrRunEnded = errors.New("the run is no longer running")

// fetchTokenBytes is the randomness behind a run's fetch token.
const fetchTokenBytes = 32

// ClaimNext hands the oldest queued run to workerID, or returns nil when
// nothing is queued. The run gets a fresh fetch token, returned in the
// Claim and stored only as a hash.
func (s *Service) ClaimNext(ctx context.Context, workerID string, defaultTimeout time.Duration, images []string) (*Claim, error) {
	images = normalizeImages(images)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if defaultTimeout <= 0 || defaultTimeout > MaxTimeout {
		return nil, errors.New("invalid worker default timeout")
	}
	ctx, release, err := s.db.AdmitMutation(ctx)
	if err != nil {
		if apperr.KindOf(err) == apperr.KindUnavailable {
			return nil, nil
		}
		return nil, err
	}
	defer release()
	plain, err := token.New(fetchTokenBytes)
	if err != nil {
		return nil, err
	}
	var claim *Claim
	err = s.db.Tx(ctx, func(tx postgres.Tx) error {
		var err error
		if claim, err = claimNextRun(ctx, tx, workerID, token.Hash(plain), defaultTimeout, images); err != nil {
			return err
		}
		return notify(ctx, tx, NotifyChannel, claim.RunID)
	})
	if errors.Is(err, postgres.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	claim.FetchToken = plain
	return claim, nil
}

// AuthenticateFetch resolves a run's fetch token to the repository the
// run belongs to. The token works only while its run is running, and
// only for that repository; ErrInvalidFetchToken otherwise.
func (s *Service) AuthenticateFetch(ctx context.Context, fetchToken string) (repoID string, err error) {
	if fetchToken == "" {
		return "", ErrInvalidFetchToken
	}
	repoID, err = selectRunningRepoByFetchToken(ctx, s.db.Q, token.Hash(fetchToken))
	if errors.Is(err, postgres.ErrNotFound) {
		return "", ErrInvalidFetchToken
	}
	return repoID, err
}

// StepStarted marks a step running, or returns ErrRunEnded when its run
// has ended or is being cancelled.
func (s *Service) StepStarted(ctx context.Context, runID, stepID string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := setStepRunning(ctx, tx, stepID); err != nil {
			return err
		}
		return notify(ctx, tx, NotifyChannel, runID)
	})
}

// StepFinished records a step's final status and, when it ran a
// container, its exit code.
func (s *Service) StepFinished(ctx context.Context, runID, stepID string, status StepStatus, exitCode *int) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := finishRecordedStep(ctx, tx, runID, stepID, status, exitCode); err != nil {
			return err
		}
		return notify(ctx, tx, NotifyChannel, runID)
	})
}

// AppendLog stores the next chunk of a step's output. sequence numbers
// start at 0 and increase by one per chunk.
func (s *Service) AppendLog(ctx context.Context, runID, stepID string, sequence int, content string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := insertLogChunk(ctx, tx, stepID, sequence, content); err != nil {
			return err
		}
		return notify(ctx, tx, LogChannel, runID)
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "repository_log_budget" {
		_, _ = s.db.Q.Exec(ctx, `UPDATE steps SET log_recording_error='Repository log storage reached its 1 GiB budget. Some output could not be recorded.' WHERE id=$1`, stepID)
	}
	return err
}

// StopReason says why the worker executing a run should stop it, or is
// empty while the run should go on: someone cancelled it, it has already
// ended without that worker — failed as lost by another worker — or its
// repository was deleted.
func (s *Service) StopReason(ctx context.Context, runID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	status, cancelRequested, err := selectRunState(ctx, s.db.Q, runID)
	switch {
	case errors.Is(err, postgres.ErrNotFound):
		return "the run no longer exists", nil
	case err != nil:
		return "", err
	case cancelRequested:
		return "the run was cancelled", nil
	case status != StatusRunning:
		return "the run has already ended", nil
	}
	return "", nil
}

// Finish records how a running run ended: its status, reason, and summary,
// and settles steps that never finished, all in one transaction. Deployment
// receipts are recorded by the deploy-step ownership path. A run that is no
// longer running (the lost-worker reaper got to it first) is left as it is.
func (s *Service) Finish(ctx context.Context, runID string, o Outcome) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	runningBecomes, pendingBecomes := StepFailed, StepSkipped
	if o.Status == StatusCancelled {
		runningBecomes, pendingBecomes = StepCancelled, StepCancelled
	}
	err := s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := lockRunRepo(ctx, tx, runID); err != nil {
			return err
		}
		_, err := finishRunRow(ctx, tx, runID, o.Status, truncateReason(o.Reason))
		if err != nil {
			return err
		}
		if err := settleOpenSteps(ctx, tx, runID, runningBecomes, pendingBecomes); err != nil {
			return err
		}
		for key, value := range o.Summary {
			if err := insertSummary(ctx, tx, runID, key, value); err != nil {
				return err
			}
		}

		return notify(ctx, tx, NotifyChannel, runID)
	})
	if errors.Is(err, postgres.ErrNotFound) {
		return nil
	}
	return err
}

// Cancel stops run number of a repository on behalf of by (a person's
// name, shown in the run's reason). A queued run is cancelled at once; a
// running one is flagged, and its worker stops it and records the end.
// Cancelling a finished run returns ErrRunFinished.
func (s *Service) Cancel(ctx context.Context, repoID string, number int64, by string) error {
	reason := "Cancelled."
	if by != "" {
		reason = "Cancelled by " + by + "."
	}
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		runID, before, err := cancelRunRow(ctx, tx, repoID, number, reason)
		if err != nil {
			return err
		}
		if before != StatusQueued && before != StatusRunning {
			return ErrRunFinished
		}
		return notify(ctx, tx, NotifyChannel, runID)
	})
}

// RegisterWorker records a worker process starting.
func (s *Service) RegisterWorker(ctx context.Context, workerID, hostname string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return upsertWorker(ctx, s.db.Q, workerID, hostname)
}

// Heartbeat records that a worker is alive and how many runs it has.
func (s *Service) Heartbeat(ctx context.Context, workerID string, activeRuns int) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return touchWorker(ctx, s.db.Q, workerID, activeRuns)
}

// StopWorker records a worker process shutting down.
func (s *Service) StopWorker(ctx context.Context, workerID string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return markWorkerStopped(ctx, s.db.Q, workerID)
}

// WorkerLostAfter is how long a worker may go without a heartbeat before
// it counts as gone: no longer online, and its running runs failed. It is
// minutes, not seconds, so a Postgres restart or failover — which can
// itself take a minute or two — reads as a database blip to wait out,
// not a lost worker to fail runs for. "gitman admin worker cleanup" uses
// the same threshold, so an operator running it by hand fails a run
// exactly when a live worker would have.
const WorkerLostAfter = 5 * time.Minute

// WorkerReadyAfter is how old a worker's heartbeat or readiness check may
// be before it stops counting as ready for new work. It is shorter than
// WorkerLostAfter: not taking work is cheap to recover from, while
// declaring a worker lost fails its running runs.
const WorkerReadyAfter = time.Minute

// AnyWorkerReady reports whether any worker is running: one that has
// not stopped and has sent a heartbeat within WorkerLostAfter. Without
// one, a queued run waits until a worker starts.
func (s *Service) AnyWorkerReady(ctx context.Context) (bool, error) {
	return selectAnyWorkerReady(ctx, s.db.Q, WorkerReadyAfter)
}

// FailLostRuns fails every running run whose worker stopped, or has not
// sent a heartbeat in the last staleAfter, and returns how many it
// failed. Runs are never retried automatically: a run that shipped
// half-way must be looked at by a person.
func (s *Service) FailLostRuns(ctx context.Context, staleAfter time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var lost []string
	err := s.db.Tx(ctx, func(tx postgres.Tx) error {
		var err error
		if lost, err = selectLostRuns(ctx, tx, staleAfter); err != nil {
			return err
		}
		for _, runID := range lost {
			if _, err := finishRunRow(ctx, tx, runID, StatusFailed, "The worker running it stopped responding."); err != nil {
				return err
			}
			if err := settleOpenSteps(ctx, tx, runID, StepFailed, StepSkipped); err != nil {
				return err
			}
			if err := notify(ctx, tx, NotifyChannel, runID); err != nil {
				return err
			}
		}
		return nil
	})
	return len(lost), err
}

// RepoIDForRun returns the repository ID a run belongs to, for a caller
// that has only the run's ID, such as a notification's payload.
func (s *Service) RepoIDForRun(ctx context.Context, runID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return selectRepoIDForRun(ctx, s.db.Q, runID)
}

// RepoIDForStep returns the repository ID a step's run belongs to.
func (s *Service) RepoIDForStep(ctx context.Context, stepID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return selectRepoIDForStep(ctx, s.db.Q, stepID)
}

// InstanceID returns the ID naming this Gitman instance, which workers
// label their step containers with.
func (s *Service) InstanceID(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return selectInstanceID(ctx, s.db.Q)
}

// RunningRunIDs returns the IDs of every running run, for a worker to
// tell its own leftover containers from those of live runs.
func (s *Service) RunningRunIDs(ctx context.Context) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return selectRunningRunIDs(ctx, s.db.Q)
}

// Run returns run number of a repository, with its steps and summary.
func (s *Service) Run(ctx context.Context, repoID string, number int64) (*RunDetail, error) {
	return selectRunDetail(ctx, s.db.Q, repoID, number)
}

// LogChunks returns up to limit chunks of a step's output with a
// sequence number above after. Pass -1 to read from the start.
func (s *Service) LogChunks(ctx context.Context, stepID string, after, limit int) ([]LogChunk, error) {
	return selectLogChunks(ctx, s.db.Q, stepID, after, limit)
}

// StartParams describes a run a person starts by hand.
type StartParams struct {
	RepoID string
	// Git is the repository the commit is read from.
	Git    *git.Repo
	Commit string
	// RefKind and RefName name the ref the run is for.
	RefKind  git.Kind
	RefName  string
	PersonID string
	// Decision is what the repository's rules allow this person on this
	// ref; the caller has already checked that they may push to it.
	Decision repo.Decision
}

// Start creates a run a person asked for, exactly as a push would have
// created it, except that it runs whether or not the rule runs pipelines
// on push.
func (s *Service) Start(ctx context.Context, p StartParams) (*Created, error) {
	ctx, release, err := s.db.AdmitMutation(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	unlock, err := p.Git.MutationLock(ctx, p.RepoID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	var pending bool
	if err := s.db.Q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM repository_operations WHERE repo_id=$1 AND completed_at IS NULL)`, p.RepoID).Scan(&pending); err != nil {
		return nil, err
	}
	if pending {
		return nil, apperr.New(apperr.KindUnavailable, "This repository has an unfinished operation.")
	}
	if err := p.Git.PinCommit(ctx, p.Commit); err != nil {
		return nil, err
	}
	data, problem, err := LoadPipeline(ctx, p.Git, p.Commit)
	if err != nil {
		return nil, err
	}
	var run *Created
	err = s.db.Tx(ctx, func(tx postgres.Tx) error {
		var err error
		run, err = s.CreateTx(ctx, tx, CreateParams{
			RepoID: p.RepoID, Commit: p.Commit, RefKind: p.RefKind, RefName: p.RefName,
			Trigger: TriggerManual, PersonID: p.PersonID,
			Pipeline: data, PipelineProblem: problem, Decision: p.Decision,
		})
		return err
	})
	return run, err
}

// pruneBatch is how many runs one retention transaction deletes.
const pruneBatch = 500

// PruneRuns deletes runs that finished before `before`, with their steps,
// logs and summaries, and returns how many. The latest run of every ref
// is kept, so a ref's last result never disappears, and deployment
// records are never deleted.
func (s *Service) PruneRuns(ctx context.Context, before time.Time) (int64, error) {
	var total int64
	for {
		n, err := deleteOldRuns(ctx, s.db.Q, before, pruneBatch)
		total += n
		if err != nil || n < pruneBatch {
			return total, err
		}
	}
}

// PruneWorkers forgets workers that stopped, or were last heard from,
// before `before` and have no running run.
func (s *Service) PruneWorkers(ctx context.Context, before time.Time) (int64, error) {
	return deleteGoneWorkers(ctx, s.db.Q, before)
}

func (s *Service) LogTail(ctx context.Context, stepID string) (*LogTail, error) {
	return selectLogTail(ctx, s.db.Q, stepID)
}

// PlanContainer records a deterministic recovery handle before Docker creation.
func (s *Service) PlanContainer(ctx context.Context, runID, stepID, name string) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	tag, err := s.db.Q.Exec(ctx, `UPDATE steps SET container_name = $3 WHERE id = $2 AND run_id = $1 AND status = 'running' AND EXISTS (SELECT 1 FROM runs WHERE id = $1 AND status = 'running' AND NOT cancel_requested)`, runID, stepID, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrRunEnded
	}
	return nil
}

func (s *Service) ContainerCreated(ctx context.Context, runID, stepID, containerID string) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	tag, err := s.db.Q.Exec(ctx, `UPDATE steps SET container_id = $3 WHERE id = $2 AND run_id = $1 AND status = 'running' AND EXISTS (SELECT 1 FROM runs WHERE id = $1 AND status = 'running' AND NOT cancel_requested) AND (type = 'run' OR EXISTS(SELECT 1 FROM deployment_targets WHERE owner_run_id = $1 AND owner_step_id = $2 AND generation = steps.deployment_generation))`, runID, stepID, containerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrRunEnded
	}
	return nil
}

// RecoveryRun identifies a run interrupted on this worker. Recovery stops its
// containers and fails the run; it never re-executes pipeline scripts.
type RecoveryRun struct {
	ID         string
	Containers []string
	Foreign    bool
}

func (s *Service) RecoveryRuns(ctx context.Context, workerID, engineID string) ([]RecoveryRun, error) {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	rows, err := s.db.Q.Query(ctx, `SELECT r.id, ARRAY(SELECT container_name FROM steps WHERE run_id = r.id AND container_name IS NOT NULL AND container_removed_at IS NULL), r.worker_id <> $1 FROM runs r JOIN workers w ON w.id=r.worker_id WHERE (r.worker_id = $1 OR ($2<>'' AND w.engine_id=$2 AND (w.stopped_at IS NOT NULL OR w.heartbeat_at < now()-interval '1 minute'))) AND (r.status = 'running' OR EXISTS (SELECT 1 FROM steps WHERE run_id = r.id AND container_name IS NOT NULL AND container_removed_at IS NULL))`, workerID, engineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []RecoveryRun
	for rows.Next() {
		var run RecoveryRun
		if err := rows.Scan(&run.ID, &run.Containers, &run.Foreign); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// SetWorkerReadiness is independent of heartbeat. An alive worker may be unable
// to execute work because Docker, disk capacity or recovery is unavailable.
func (s *Service) SetWorkerReadiness(ctx context.Context, workerID string, ready bool, reason string, images []string) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	if images == nil {
		images = []string{}
	}
	_, err := s.db.Q.Exec(ctx, `UPDATE workers SET ready = $2, readiness_reason = $3, images = $4, readiness_at = now() WHERE id = $1`, workerID, ready, reason, normalizeImages(images))
	return err
}

type WorkerStatus struct {
	ID, Hostname, Reason string
	Ready                bool
	Heartbeat            time.Time
	Stopped              *time.Time
	Images               []string
	ActiveRuns           int
	EngineID             string
}

func (s *Service) Workers(ctx context.Context) ([]WorkerStatus, error) {
	rows, err := s.db.Q.Query(ctx, `SELECT id, hostname, ready AND stopped_at IS NULL AND heartbeat_at >= now() - make_interval(secs => $1) AND readiness_at >= now() - make_interval(secs => $1), CASE WHEN stopped_at IS NOT NULL THEN 'Stopped' WHEN heartbeat_at < now() - make_interval(secs => $1) THEN 'Heartbeat is stale' WHEN readiness_at IS NULL OR readiness_at < now() - make_interval(secs => $1) THEN 'Readiness has not been checked recently' ELSE readiness_reason END, heartbeat_at, stopped_at, images, active_runs, engine_id FROM workers ORDER BY started_at DESC LIMIT 100`, WorkerReadyAfter.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var workers []WorkerStatus
	for rows.Next() {
		var w WorkerStatus
		if err := rows.Scan(&w.ID, &w.Hostname, &w.Ready, &w.Reason, &w.Heartbeat, &w.Stopped, &w.Images, &w.ActiveRuns, &w.EngineID); err != nil {
			return nil, err
		}
		workers = append(workers, w)
	}
	return workers, rows.Err()
}

func (s *Service) AnyWorkerReadyForRun(ctx context.Context, runID string) (bool, error) {
	var ready bool
	err := s.db.Q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workers w JOIN runs r ON r.id = $1 WHERE w.ready AND w.stopped_at IS NULL AND w.heartbeat_at >= now() - make_interval(secs => $2) AND w.readiness_at >= now() - make_interval(secs => $2) AND r.required_images <@ w.images)`, runID, WorkerReadyAfter.Seconds()).Scan(&ready)
	return ready, err
}

// RecoverStepExit stores a retained daemon receipt without starting anything.
func (s *Service) RecoverStepExit(ctx context.Context, runID, name string, code int) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		var stepID string
		if err := tx.QueryRow(ctx, `SELECT id FROM steps WHERE run_id=$1 AND (container_name=$2 OR container_id=$2)`, runID, name).Scan(&stepID); err != nil {
			return postgres.NormalizeNotFound(err)
		}
		status := StepFailed
		if code == 0 {
			status = StepPassed
		}
		return finishExecutionReceipt(ctx, tx, runID, stepID, status, &code, true)
	})
}

func (s *Service) ContainerRemoved(ctx context.Context, runID, name string) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	_, err := s.db.Q.Exec(ctx, `UPDATE steps SET container_removed_at = now() WHERE run_id = $1 AND container_name = $2 AND container_removed_at IS NULL`, runID, name)
	return err
}

func (s *Service) AdmitMutation(ctx context.Context) (context.Context, func(), error) {
	return s.db.AdmitMutation(ctx)
}

func (s *Service) BindWorkerEngine(ctx context.Context, workerID, engineID, root string) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('gitman.engine.' || $1,0))`, engineID); err != nil {
			return err
		}
		var incompatible bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM workers
				WHERE engine_id=$1 AND workspace_root<>$2 AND id<>$4
				  AND stopped_at IS NULL AND heartbeat_at >= now() - make_interval(secs => $3)
			)
		`, engineID, root, WorkerLostAfter.Seconds(), workerID).Scan(&incompatible); err != nil {
			return err
		}
		if incompatible {
			return errors.New("workers sharing a Docker engine must share the same absolute workspace root")
		}
		_, err := tx.Exec(ctx, `UPDATE workers SET engine_id=$2,workspace_root=$3 WHERE id=$1`, workerID, engineID, root)
		return err
	})
}
func (s *Service) Maintenance(ctx context.Context) (bool, error) { return s.db.Maintenance(ctx) }

// ConfirmContainersRemoved is called only after cleanup removed every labelled
// container while holding the run's filesystem execution lock.
func (s *Service) ConfirmContainersRemoved(ctx context.Context, runID string) error {
	if err := s.ConfirmExecutionStopped(ctx, runID); err != nil {
		return err
	}
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	_, err := s.db.Q.Exec(ctx, `UPDATE steps SET container_removed_at=now() WHERE run_id=$1 AND container_name IS NOT NULL AND container_removed_at IS NULL`, runID)
	return err
}

// ImageFacts resolves the pipelines that can still need images and the latest
// deployments that can map to built tags. It is one snapshot: callers classify
// and plan from it without re-reading checkouts or the database.
func (s *Service) ImageFacts(ctx context.Context) (*ImageFacts, error) {
	pipelines, incomplete, err := selectRequiredPipelines(ctx, s.db.Q)
	if err != nil {
		return nil, err
	}
	facts := NewImageFacts()
	facts.Incomplete = incomplete
	repoIDs := map[string]bool{}
	for _, fact := range pipelines {
		cfg, err := Parse(fact.Pipeline)
		if err != nil {
			// Stored bytes were valid when the run was created; a row that
			// is not readable now may still require images. Treat the
			// snapshot as incomplete so nothing is pruned.
			facts.Incomplete = true
			continue
		}
		source := ImageSource{Repo: fact.RepoName, Ref: fact.Source}
		for _, ref := range append([]string{cfg.Image}, cfg.Requires...) {
			normalized := NormalizeImageReference(ref)
			required := facts.Required[normalized]
			if required.Ref == "" {
				required.Ref = ref
			}
			required.Sources = appendImageSource(required.Sources, source)
			facts.Required[normalized] = required
		}
		for repository, policy := range cfg.Images {
			built := facts.Built[repository]
			if policy.Keep > built.Keep {
				built.Keep = policy.Keep
			}
			built.Sources = appendImageSource(built.Sources, source)
			built.repoIDs = appendUnique(built.repoIDs, fact.RepoID)
			facts.Built[repository] = built
			repoIDs[fact.RepoID] = true
		}
	}
	if len(repoIDs) > 0 {
		ids := make([]string, 0, len(repoIDs))
		for repoID := range repoIDs {
			ids = append(ids, repoID)
		}
		deployments, err := selectLiveDeploymentsForRepos(ctx, s.db.Q, ids)
		if err != nil {
			return nil, err
		}
		versions := map[string]map[string]bool{}
		for _, deployment := range deployments {
			if versions[deployment.RepoID] == nil {
				versions[deployment.RepoID] = map[string]bool{}
			}
			versions[deployment.RepoID][deployment.Version] = true
		}
		for repository, built := range facts.Built {
			mapped := map[string]bool{}
			for _, repoID := range built.repoIDs {
				for version := range versions[repoID] {
					mapped[version] = true
				}
			}
			built.Deployments = sortedKeys(mapped)
			built.repoIDs = nil
			facts.Built[repository] = built
		}
	}
	return facts, nil
}

// PublishInventory replaces a host's image inventory in place, with the policy
// the worker is actually enforcing and the time the database received it.
func (s *Service) PublishInventory(ctx context.Context, inventory HostInventory) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	return upsertImageHost(ctx, s.db.Q, inventory)
}

// ImageHosts returns every known Docker host, its classified images, the plan
// for pruning its built images, missing required images on its ready workers,
// and its recent image operations.
func (s *Service) ImageHosts(ctx context.Context) ([]ImageHost, error) {
	facts, err := s.ImageFacts(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := selectImageHosts(ctx, s.db.Q)
	if err != nil {
		return nil, err
	}
	workers, err := s.Workers(ctx)
	if err != nil {
		return nil, err
	}
	operations, err := s.ImageOperations(ctx, 200)
	if err != nil {
		return nil, err
	}

	byEngine := map[string]*ImageHost{}
	for _, row := range rows {
		var images []InventoryImage
		if err := json.Unmarshal(row.Images, &images); err != nil {
			return nil, fmt.Errorf("decode image inventory for %s: %w", row.EngineID, err)
		}
		classified := Classify(images, facts)
		plan := PlanPrune(classified)
		applyPlan(classified, plan)
		host := &ImageHost{
			EngineID: row.EngineID, Hostname: row.Hostname, HasInventory: true, Stale: row.Stale,
			CollectedAt: row.CollectedAt, Automation: row.Automation, FactsIncomplete: facts.Incomplete, Images: classified, Plan: plan,
			Cache: ImageCacheState{
				Size: row.CacheSize, Reclaimable: row.CacheReclaimable,
				Policy:   ImageCachePolicy{KeepGiB: row.KeepGiB, OlderThanHours: row.OlderHours},
				Disabled: row.CacheDisabled, PrunedAt: row.PrunedAt, Reclaimed: row.Reclaimed, Error: row.CacheError,
				AboveCap: !row.CacheDisabled && row.KeepGiB > 0 && row.CacheSize > int64(row.KeepGiB)<<30,
			},
		}
		byEngine[row.EngineID] = host
	}
	for _, worker := range workers {
		if worker.EngineID == "" {
			continue
		}
		host := byEngine[worker.EngineID]
		if host == nil {
			host = &ImageHost{EngineID: worker.EngineID, Hostname: worker.Hostname, FactsIncomplete: facts.Incomplete}
			byEngine[worker.EngineID] = host
		}
		if host.Hostname == "" {
			host.Hostname = worker.Hostname
		}
		host.Workers = append(host.Workers, HostWorker{
			ID: worker.ID, Hostname: worker.Hostname, Ready: worker.Ready, images: worker.Images,
		})
	}
	for engineID, host := range byEngine {
		host.Missing = missingImages(facts, host.Workers)
		for _, operation := range operations {
			if operation.EngineID == engineID && len(host.Operations) < 20 {
				host.Operations = append(host.Operations, operation)
			}
		}
	}
	hosts := make([]ImageHost, 0, len(byEngine))
	for _, host := range byEngine {
		hosts = append(hosts, *host)
	}
	sort.Slice(hosts, func(i, j int) bool {
		if hosts[i].Hostname != hosts[j].Hostname {
			return hosts[i].Hostname < hosts[j].Hostname
		}
		return hosts[i].EngineID < hosts[j].EngineID
	})
	return hosts, nil
}

// ImageHost returns one Docker host by engine ID.
func (s *Service) ImageHost(ctx context.Context, engineID string) (*ImageHost, error) {
	hosts, err := s.ImageHosts(ctx)
	if err != nil {
		return nil, err
	}
	for i := range hosts {
		if hosts[i].EngineID == engineID {
			return &hosts[i], nil
		}
	}
	return nil, apperr.New(apperr.KindNotFound, "There is no image inventory for that Docker host.")
}

// QueueImagePrune plans a built-image prune from the current inventory and
// queues it for the host's workers. An empty plan returns no operation.
func (s *Service) QueueImagePrune(ctx context.Context, engineID, actorID string) (string, PrunePlan, error) {
	return s.queueImagePrune(ctx, engineID, actorID, nil)
}

// QueueImagePruneTargets queues only the targets the operator reviewed. The
// fresh plan is intersected with the requested ref+ID pairs, so an image that
// became prunable after the page rendered is never removed unseen.
func (s *Service) QueueImagePruneTargets(ctx context.Context, engineID, actorID string, requested []ImageTarget) (string, PrunePlan, error) {
	return s.queueImagePrune(ctx, engineID, actorID, requested)
}

func (s *Service) queueImagePrune(ctx context.Context, engineID, actorID string, requested []ImageTarget) (string, PrunePlan, error) {
	host, err := s.freshImageHost(ctx, engineID)
	if err != nil {
		return "", PrunePlan{}, err
	}
	if host.FactsIncomplete {
		return "", PrunePlan{}, apperr.New(apperr.KindConflict, "Image facts are incomplete; no built image is pruned. Try again after every stored pipeline can be read.")
	}
	plan := host.Plan
	if requested != nil {
		wanted := make(map[string]bool, len(requested))
		for _, target := range requested {
			wanted[NormalizeImageReference(target.Ref)+"\x00"+target.ID] = true
		}
		plan.Remove = nil
		plan.ReclaimableBytes = 0
		for _, image := range host.Plan.Remove {
			if !wanted[ImageKey(image)] {
				continue
			}
			plan.Remove = append(plan.Remove, image)
			plan.ReclaimableBytes += image.Size
		}
	}
	if len(plan.Remove) == 0 {
		return "", plan, nil
	}
	targets := make([]ImageTarget, 0, len(plan.Remove))
	for _, image := range plan.Remove {
		targets = append(targets, ImageTarget{Ref: image.Ref(), ID: image.ID})
	}
	id, err := s.queueImageOperation(ctx, engineID, actorID, ImageOperationPrune, imageOperationPayload{Targets: targets})
	return id, plan, err
}

// QueueImageRemove queues one explicitly named image. Required and in-use
// images are refused here, before anything is written.
func (s *Service) QueueImageRemove(ctx context.Context, engineID, ref, actorID string) (string, error) {
	host, err := s.freshImageHost(ctx, engineID)
	if err != nil {
		return "", err
	}
	if host.FactsIncomplete {
		return "", apperr.New(apperr.KindConflict, "Image facts are incomplete; no image is removed. Try again after every stored pipeline can be read.")
	}
	normalized := NormalizeImageReference(ref)
	var found *ClassifiedImage
	for i := range host.Images {
		image := &host.Images[i]
		if image.Ref() == ref || image.NormalizedRef() == normalized || image.ID == ref {
			found = image
			break
		}
	}
	if found == nil {
		return "", apperr.New(apperr.KindNotFound, "There is no image "+ref+" in that host's inventory.")
	}
	ok, reason := PlanRemoveOne(*found)
	if !ok {
		return "", apperr.New(apperr.KindConflict, "Refusing to remove "+found.Ref()+": "+reason+".")
	}
	return s.queueImageOperation(ctx, engineID, actorID, ImageOperationRemove,
		imageOperationPayload{Targets: []ImageTarget{{Ref: found.Ref(), ID: found.ID}}})
}

// QueueImageCachePrune queues a cache prune. A nil policy uses the policy the
// worker last reported; a non-nil one is a manual override.
func (s *Service) QueueImageCachePrune(ctx context.Context, engineID, actorID string, policy *ImageCachePolicy) (string, error) {
	host, err := s.freshImageHost(ctx, engineID)
	if err != nil {
		return "", err
	}
	applied := host.Cache.Policy
	if applied.KeepGiB == 0 {
		defaults := config.DefaultImageCache()
		applied = ImageCachePolicy{KeepGiB: defaults.KeepGiB, OlderThanHours: defaults.OlderThanHours()}
	}
	if policy != nil {
		applied = *policy
	}
	if applied.KeepGiB < 1 || applied.KeepGiB > config.MaxImageCacheKeepGiB {
		return "", apperr.New(apperr.KindInvalid, fmt.Sprintf("keep storage must be between 1 and %d GiB.", config.MaxImageCacheKeepGiB))
	}
	if applied.OlderThanHours < 1 || applied.OlderThanHours > config.MaxImageCacheMaxAgeHours {
		return "", apperr.New(apperr.KindInvalid, fmt.Sprintf("the maximum age must be between 1 and %d hours.", config.MaxImageCacheMaxAgeHours))
	}
	return s.queueImageOperation(ctx, engineID, actorID, ImageOperationCache, imageOperationPayload{Cache: applied})
}

func (s *Service) freshImageHost(ctx context.Context, engineID string) (*ImageHost, error) {
	host, err := s.ImageHost(ctx, engineID)
	if err != nil {
		return nil, err
	}
	if !host.HasInventory || host.Stale {
		return nil, apperr.New(apperr.KindConflict,
			fmt.Sprintf("The image inventory for %s is older than %s; try again after the worker refreshes it.", host.Hostname, ImageInventoryStaleAfter))
	}
	return host, nil
}

func (s *Service) queueImageOperation(ctx context.Context, engineID, actorID string, kind ImageOperationKind, payload imageOperationPayload) (string, error) {
	ctx, release, err := s.db.AdmitMutation(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	id := id.New()
	err = s.db.Tx(ctx, func(tx postgres.Tx) error {
		return insertImageOperation(ctx, tx, id, engineID, actorID, kind, payload)
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// ClaimImageOperation leases one operation for a Docker host, or returns nil
// when none is pending.
func (s *Service) ClaimImageOperation(ctx context.Context, engineID, workerID string) (*ImageOperation, error) {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	var operation *ImageOperation
	err := s.db.Tx(ctx, func(tx postgres.Tx) error {
		var err error
		operation, err = claimImageOperation(ctx, tx, engineID, workerID)
		return err
	})
	if errors.Is(err, postgres.ErrNotFound) {
		return nil, nil
	}
	return operation, err
}

// ImageOperations returns recent image operations, newest first.
func (s *Service) ImageOperations(ctx context.Context, limit int) ([]ImageOperation, error) {
	return selectImageOperations(ctx, s.db.Q, limit)
}

// ImageOperation returns one operation by ID.
func (s *Service) ImageOperation(ctx context.Context, id string) (*ImageOperation, error) {
	return selectImageOperation(ctx, s.db.Q, id)
}

// ReleaseImageOperation returns an unstarted operation to the queue, for a
// worker that could not reach Docker and should retry later.
func (s *Service) ReleaseImageOperation(ctx context.Context, id string) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	return releaseImageOperation(ctx, s.db.Q, id)
}

// FinishImageOperation records results and audits them in one transaction. A
// retry that lost its first commit is harmless: only the first update wins.
func (s *Service) FinishImageOperation(ctx context.Context, operation *ImageOperation, result ImageOperationResult) error {
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer stop()
	summary := result.Error
	if summary == "" && operationFailed(result) {
		summary = "One or more images could not be removed."
	}
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		finished, err := finishImageOperation(ctx, tx, operation.ID, result, summary)
		if err != nil {
			return err
		}
		if !finished {
			return nil
		}
		return activity.RecordAdmin(ctx, tx, "", operation.ActorID, imageOperationAction(operation.Kind),
			imageOperationDetail(operation, result))
	})
}

// RecordCachePrune stores the last cache-prune attempt and returns the error
// it replaced, so an unchanged recurring failure is not audited every pass.
func (s *Service) RecordCachePrune(ctx context.Context, hostID string, reclaimed int64, cacheError string) (string, error) {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	var previous string
	err := s.db.Tx(ctx, func(tx postgres.Tx) error {
		var err error
		previous, err = recordCachePrune(ctx, tx, hostID, reclaimed, cacheError)
		return err
	})
	return previous, err
}

// RecordImageAudit writes an image management event for an automatic action.
func (s *Service) RecordImageAudit(ctx context.Context, actorID, action, detail string) error {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		return activity.RecordAdmin(ctx, tx, "", actorID, action, detail)
	})
}

func appendImageSource(sources []ImageSource, source ImageSource) []ImageSource {
	for _, existing := range sources {
		if existing == source {
			return sources
		}
	}
	return append(sources, source)
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func missingImages(facts *ImageFacts, workers []HostWorker) []MissingImage {
	missing := map[string]*MissingImage{}
	for _, worker := range workers {
		if !worker.Ready {
			continue
		}
		advertised := make(map[string]bool, len(worker.images))
		for _, image := range worker.images {
			advertised[NormalizeImageReference(image)] = true
		}
		for normalized, required := range facts.Required {
			if advertised[normalized] {
				continue
			}
			entry := missing[normalized]
			if entry == nil {
				entry = &MissingImage{Ref: required.Ref, Sources: required.Sources}
				missing[normalized] = entry
			}
			entry.Workers = appendUnique(entry.Workers, worker.ID)
		}
	}
	result := make([]MissingImage, 0, len(missing))
	for _, entry := range missing {
		sort.Strings(entry.Workers)
		result = append(result, *entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Ref < result[j].Ref })
	return result
}

func operationFailed(result ImageOperationResult) bool {
	for _, image := range result.Results {
		if image.Status == "failed" {
			return true
		}
	}
	return false
}

func imageOperationAction(kind ImageOperationKind) string {
	switch kind {
	case ImageOperationRemove:
		return activity.ImageRemoved
	case ImageOperationPrune:
		return activity.ImagesPruned
	default:
		return activity.ImageCachePruned
	}
}

func imageOperationDetail(operation *ImageOperation, result ImageOperationResult) string {
	by := "the command line"
	if operation.ActorID != "" {
		by = "a person"
	}
	host := operation.EngineID
	if operation.Kind == ImageOperationCache {
		policy := operation.Cache
		return fmt.Sprintf("%s on %s: cache policy %d GiB, %d hours; reclaimed %d bytes",
			by, host, policy.KeepGiB, policy.OlderThanHours, result.ReclaimedBytes)
	}
	parts := make([]string, 0, len(result.Results))
	for i, image := range result.Results {
		if i == 20 {
			parts = append(parts, fmt.Sprintf("and %d more", len(result.Results)-i))
			break
		}
		part := image.Ref + " " + image.Status
		if image.Reason != "" && image.Status != "removed" {
			part += " (" + image.Reason + ")"
		}
		parts = append(parts, part)
	}
	return fmt.Sprintf("%s on %s: %s; reclaimed %d bytes", by, host, strings.Join(parts, ", "), result.ReclaimedBytes)
}
