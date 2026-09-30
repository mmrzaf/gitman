package ci

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

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

// InProgress returns queued and running runs across every repository,
// most recently active first.
func (s *Service) InProgress(ctx context.Context, limit int) ([]Summary, error) {
	return selectInProgress(ctx, s.db.Q, limit)
}

// InProgressForRepos is InProgress, restricted to repoIDs — the
// instance-wide "running now" list for someone who cannot necessarily
// see every repository.
func (s *Service) InProgressForRepos(ctx context.Context, repoIDs []string, limit int) ([]Summary, error) {
	return selectInProgressForRepos(ctx, s.db.Q, repoIDs, limit)
}

// LatestRunPerRef returns, for a repository, the most recent run on each
// of its refs, keyed by "<kind>/<name>".
func (s *Service) LatestRunPerRef(ctx context.Context, repoID string) (map[string]Summary, error) {
	return selectLatestRunPerRef(ctx, s.db.Q, repoID)
}

// RunsForRepo returns a page of a repository's runs, newest first. before,
// when nonzero, limits it to runs numbered lower than it, for paging
// backward through history.
func (s *Service) RunsForRepo(ctx context.Context, repoID string, before int64, limit int) ([]Summary, error) {
	return selectRunsForRepo(ctx, s.db.Q, repoID, before, limit)
}

// LatestRunPerCommit returns the newest run of each of commits, keyed by
// commit, for the ones that have a run.
func (s *Service) LatestRunPerCommit(ctx context.Context, repoID string, commits []string) (map[string]Summary, error) {
	return selectLatestRunPerCommit(ctx, s.db.Q, repoID, commits)
}

// Live returns, for every repository and target, the latest deployment:
// what is live there now, ordered by repository, then target.
func (s *Service) Live(ctx context.Context) ([]Deployment, error) {
	return selectLiveDeployments(ctx, s.db.Q, nil)
}

// LiveForRepo is Live, scoped to one repository.
func (s *Service) LiveForRepo(ctx context.Context, repoID string) ([]Deployment, error) {
	return selectLiveDeployments(ctx, s.db.Q, &repoID)
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
	pl.reason = truncateReason(pl.reason)

	number, err := allocateRunNumber(ctx, tx, p.RepoID)
	if err != nil {
		return nil, err
	}

	run := &Created{ID: id.New(), Number: number, Status: pl.status, Reason: pl.reason, Target: pl.target, Version: pl.version}
	finished := pl.status != StatusQueued
	if err := insertRun(ctx, tx, run, p, number, finished); err != nil {
		return nil, err
	}

	for i, step := range pl.steps {
		status := StepSkipped
		if step.run {
			status = StepPending
		}
		if err := insertStep(ctx, tx, run.ID, i, step.name, status); err != nil {
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
func (s *Service) ClaimNext(ctx context.Context, workerID string) (*Claim, error) {
	plain, err := token.New(fetchTokenBytes)
	if err != nil {
		return nil, err
	}
	var claim *Claim
	err = s.db.Tx(ctx, func(tx postgres.Tx) error {
		var err error
		if claim, err = claimNextRun(ctx, tx, workerID, token.Hash(plain)); err != nil {
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
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := setStepFinished(ctx, tx, stepID, status, exitCode); err != nil {
			return err
		}
		return notify(ctx, tx, NotifyChannel, runID)
	})
}

// AppendLog stores the next chunk of a step's output. sequence numbers
// start at 0 and increase by one per chunk.
func (s *Service) AppendLog(ctx context.Context, runID, stepID string, sequence int, content string) error {
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := insertLogChunk(ctx, tx, stepID, sequence, content); err != nil {
			return err
		}
		return notify(ctx, tx, LogChannel, runID)
	})
}

// StopReason says why the worker executing a run should stop it, or is
// empty while the run should go on: someone cancelled it, it has already
// ended without that worker — failed as lost by another worker — or its
// repository was deleted.
func (s *Service) StopReason(ctx context.Context, runID string) (string, error) {
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

// Finish records how a running run ended: its status and reason, the
// summary it wrote, and — for a passing run with a target — the
// deployment it made, all in one transaction. Steps that never finished
// are settled to match. A run that is no longer running (the lost-worker
// reaper got to it first) is left as it is.
func (s *Service) Finish(ctx context.Context, runID string, o Outcome) error {
	runningBecomes, pendingBecomes := StepFailed, StepSkipped
	if o.Status == StatusCancelled {
		runningBecomes, pendingBecomes = StepCancelled, StepCancelled
	}
	err := s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := lockRunRepo(ctx, tx, runID); err != nil {
			return err
		}
		f, err := finishRunRow(ctx, tx, runID, o.Status, truncateReason(o.Reason))
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
		if o.Status == StatusPassed && f.target != "" {
			if err := insertDeployment(ctx, tx, runID, f); err != nil {
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
	return upsertWorker(ctx, s.db.Q, workerID, hostname)
}

// Heartbeat records that a worker is alive and how many runs it has.
func (s *Service) Heartbeat(ctx context.Context, workerID string, activeRuns int) error {
	return touchWorker(ctx, s.db.Q, workerID, activeRuns)
}

// StopWorker records a worker process shutting down.
func (s *Service) StopWorker(ctx context.Context, workerID string) error {
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

// AnyWorkerOnline reports whether any worker is running: one that has
// not stopped and has sent a heartbeat within WorkerLostAfter. Without
// one, a queued run waits until a worker starts.
func (s *Service) AnyWorkerOnline(ctx context.Context) (bool, error) {
	return selectAnyWorkerOnline(ctx, s.db.Q, WorkerLostAfter)
}

// FailLostRuns fails every running run whose worker stopped, or has not
// sent a heartbeat in the last staleAfter, and returns how many it
// failed. Runs are never retried automatically: a run that shipped
// half-way must be looked at by a person.
func (s *Service) FailLostRuns(ctx context.Context, staleAfter time.Duration) (int, error) {
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
	return selectRepoIDForRun(ctx, s.db.Q, runID)
}

// RepoIDForStep returns the repository ID a step's run belongs to.
func (s *Service) RepoIDForStep(ctx context.Context, stepID string) (string, error) {
	return selectRepoIDForStep(ctx, s.db.Q, stepID)
}

// InstanceID returns the ID naming this Gitman instance, which workers
// label their step containers with.
func (s *Service) InstanceID(ctx context.Context) (string, error) {
	return selectInstanceID(ctx, s.db.Q)
}

// RunningRunIDs returns the IDs of every running run, for a worker to
// tell its own leftover containers from those of live runs.
func (s *Service) RunningRunIDs(ctx context.Context) (map[string]bool, error) {
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
