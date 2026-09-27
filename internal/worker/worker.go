// Package worker runs pipelines. A worker process claims queued runs
// from PostgreSQL — the only thing it shares with the web process —
// fetches each run's commit over Gitman's own Git HTTP, and runs every
// step in its own Docker container, recording statuses and output as it
// goes. Any number of workers can run; each runs one run at a time.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/id"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/repo"
)

// Timing of a worker's background duties.
const (
	heartbeatInterval = 10 * time.Second
	// cleanupInterval is how often leftover containers and workspaces of
	// ended runs are looked for.
	cleanupInterval = time.Minute
	// pollInterval is the safety net under LISTEN: a worker also looks
	// for queued runs this often, in case a notification was missed.
	pollInterval = 30 * time.Second
	// cancelPoll is how often a running run is checked for a request to
	// cancel it.
	cancelPoll = 2 * time.Second
	// dutyTimeout bounds one heartbeat, one cleanup pass, or one check
	// that Docker answers, so a wedged Docker daemon or a stalled
	// database call delays the next one instead of stopping it for good.
	dutyTimeout = 20 * time.Second
	// finishRetryDelay is the first wait before trying again to record a
	// run's end; it doubles up to maxFinishRetryDelay.
	finishRetryDelay    = time.Second
	maxFinishRetryDelay = 30 * time.Second
	// finishAttemptsWithSummary is how many times a run's end is recorded
	// with its summary before trying without it, in case a value in it is
	// what the database refuses.
	finishAttemptsWithSummary = 3
)

// Config is how a worker is set up.
type Config struct {
	// WorkspaceRoot is the directory run workspaces are created under.
	// It must be the same path on the Docker host, because step
	// containers mount workspaces by host path.
	WorkspaceRoot string
	// WebURL is where the web process serves Git HTTP.
	WebURL string
	// DefaultTimeout bounds a run whose pipeline sets no timeout.
	DefaultTimeout time.Duration
	Hostname       string
}

// Worker claims and runs queued runs.
type Worker struct {
	id     string
	cfg    Config
	db     *postgres.DB
	ci     *ci.Service
	repos  *repo.Service
	docker *Docker
	log    *slog.Logger
	active atomic.Int32

	// Intervals of the worker's background duties; the constants above,
	// shortened only by tests.
	heartbeatEvery, cleanupEvery, pollEvery time.Duration

	// healthySince is when this worker's heartbeats last started
	// succeeding without a failure in between, zero while they fail. It
	// is used only by the heartbeat goroutine.
	healthySince time.Time
}

// New returns a Worker with a fresh ID.
func New(cfg Config, db *postgres.DB, ciService *ci.Service, repos *repo.Service, docker *Docker, log *slog.Logger) *Worker {
	return &Worker{id: id.New(), cfg: cfg, db: db, ci: ciService, repos: repos, docker: docker, log: log,
		heartbeatEvery: heartbeatInterval, cleanupEvery: cleanupInterval, pollEvery: pollInterval}
}

// PrepareWorkspaceRoot creates the directory run workspaces live under,
// readable only by the worker's own user. A workspace's own directories
// are writable by everyone, since a step's image may run as any user;
// this keeps other users of the worker's host from reaching into them,
// or running anything a step left there.
func PrepareWorkspaceRoot(root string) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create workspace directory: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return fmt.Errorf("restrict workspace directory: %w", err)
	}
	return nil
}

// Run works until ctx ends. A run in progress when ctx ends is stopped
// and recorded as failed.
func (w *Worker) Run(ctx context.Context) error {
	if err := w.ci.RegisterWorker(ctx, w.id, w.cfg.Hostname); err != nil {
		return err
	}
	w.healthySince = time.Now()
	defer w.stop(ctx)
	w.log.Info("worker started", "worker", w.id, "host", w.cfg.Hostname)

	wake := make(chan struct{}, 1)
	signal := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	go w.db.Listen(ctx, []string{ci.NotifyChannel}, func(string, string) { signal() }, func(err error) {
		w.log.Warn("lost the connection that listens for queued runs; reconnecting", "error", err)
	})
	go w.heartbeat(ctx)
	go w.cleanup(ctx)

	poll := time.NewTicker(w.pollEvery)
	defer poll.Stop()
	for {
		w.drain(ctx)
		select {
		case <-ctx.Done():
			w.log.Info("worker stopping", "worker", w.id)
			return nil
		case <-wake:
		case <-poll.C:
		}
	}
}

// drain runs queued runs one after another until none is left. It
// claims nothing while Docker does not answer: a run claimed then could
// only fail, and another worker may be able to run it.
func (w *Worker) drain(ctx context.Context) {
	for ctx.Err() == nil {
		check, cancel := context.WithTimeout(ctx, dutyTimeout)
		err := w.docker.Available(check)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				w.log.Warn("Docker is not answering; claiming no runs until it does", "error", err)
			}
			return
		}
		claim, err := w.ci.ClaimNext(ctx, w.id)
		if err != nil {
			if ctx.Err() == nil {
				w.log.Error("could not claim a run", "error", err)
			}
			return
		}
		if claim == nil {
			return
		}
		w.handle(ctx, claim)
	}
}

func (w *Worker) handle(ctx context.Context, claim *ci.Claim) {
	w.active.Add(1)
	defer w.active.Add(-1)
	start := time.Now()
	log := w.log.With("repo", claim.RepoName, "run", claim.Number)
	log.Info("run started")

	outcome := w.execute(ctx, claim, log)
	if w.finish(ctx, claim.RunID, outcome, log) {
		log.Info("run finished", "status", outcome.Status, "duration", time.Since(start).Round(time.Second))
	}
}

// stopAttempts and stopRetryDelay bound how long Run's shutdown waits
// to record the worker stopped: unlike finish, this must not retry
// forever, since nothing else is keeping the process alive to retry on.
// A worker that never manages to record this is still caught by
// FailLostRuns once its heartbeat goes stale.
const (
	stopAttempts   = 4
	stopRetryDelay = 2 * time.Second
)

// stop records the worker stopping, retrying briefly against a database
// that is down right at shutdown rather than giving up on the first
// attempt. It takes ctx without its cancellation, since Run's context
// has already ended by the time this runs.
func (w *Worker) stop(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	op := func(c context.Context) error { return w.ci.StopWorker(c, w.id) }
	if err := retryBriefly(ctx, stopAttempts, stopRetryDelay, op); err != nil {
		w.log.Warn("could not record the worker stopping", "error", err)
	}
}

// retryBriefly calls op up to attempts times, pausing delay between
// tries, and returns the last error if none of them succeed.
func retryBriefly(ctx context.Context, attempts int, delay time.Duration, op func(context.Context) error) error {
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err = op(ctx); err == nil {
			return nil
		}
		if attempt < attempts {
			time.Sleep(delay)
		}
	}
	return err
}

// finish records how a run ended, trying again while the database cannot
// take it: a run whose end is never recorded would stay running for as
// long as this worker is alive. It gives up only when the worker stops,
// which marks the worker stopped so that the run is failed as lost.
func (w *Worker) finish(ctx context.Context, runID string, outcome ci.Outcome, log *slog.Logger) bool {
	record := context.WithoutCancel(ctx)
	delay := finishRetryDelay
	for attempt := 1; ; attempt++ {
		err := w.ci.Finish(record, runID, outcome)
		if err == nil {
			return true
		}
		log.Error("could not record the run's end", "attempt", attempt, "error", err)
		if attempt == finishAttemptsWithSummary && len(outcome.Summary) > 0 {
			outcome.Summary = nil
			if outcome.Reason == "" {
				outcome.Reason = "The run's summary could not be stored."
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
		}
		delay = min(2*delay, maxFinishRetryDelay)
	}
}

func (w *Worker) execute(ctx context.Context, claim *ci.Claim, log *slog.Logger) ci.Outcome {
	e := &execution{
		claim:          claim,
		journal:        w.ci,
		docker:         w.docker,
		repoURL:        fmt.Sprintf("%s/%s.git", w.cfg.WebURL, claim.RepoName),
		defaultTimeout: w.cfg.DefaultTimeout,
		cancelPoll:     cancelPoll,
		log:            log,
	}
	if claim.AllowSecrets {
		secrets, err := w.repos.RunSecrets(ctx, claim.RepoID)
		if err != nil {
			return e.internalFailure("Could not read the repository's secrets", err)
		}
		e.secrets = secrets
	}
	ws, err := createWorkspace(w.cfg.WorkspaceRoot, claim.RunID)
	if err != nil {
		return e.internalFailure("Could not prepare the run's workspace", err)
	}
	defer func() {
		if err := ws.remove(); err != nil {
			log.Warn("could not remove the run's workspace", "path", ws.root, "error", err)
		}
	}()
	e.ws = ws
	return e.run(ctx)
}

// heartbeat reports the worker alive, and fails runs of workers that are
// not, until ctx ends.
func (w *Worker) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(w.heartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.beat(ctx)
		}
	}
}

// beat sends one heartbeat and, once this worker's own heartbeats have
// been reaching the database for at least ci.WorkerLostAfter, fails the
// runs of workers whose heartbeats have not. Until then this worker
// cannot tell a dead worker from one that, like itself, was cut off: after a
// database outage every worker's last heartbeat is stale, and the first
// to reconnect must give the others a chance to report in before it
// judges any of them lost.
func (w *Worker) beat(ctx context.Context) {
	tick, cancel := context.WithTimeout(ctx, dutyTimeout)
	defer cancel()
	now := time.Now()
	if err := w.ci.Heartbeat(tick, w.id, int(w.active.Load())); err != nil {
		if ctx.Err() == nil {
			w.log.Warn("heartbeat failed", "error", err)
		}
		w.healthySince = time.Time{}
		return
	}
	if w.healthySince.IsZero() {
		w.healthySince = now
	}
	if now.Sub(w.healthySince) < ci.WorkerLostAfter {
		return
	}
	n, err := w.ci.FailLostRuns(tick, ci.WorkerLostAfter)
	switch {
	case err != nil && ctx.Err() == nil:
		w.log.Warn("could not check for runs of lost workers", "error", err)
	case n > 0:
		w.log.Warn("failed runs whose worker stopped responding", "runs", n)
	}
}

// cleanup removes what ended runs left behind, now and then every
// cleanupEvery until ctx ends. It runs apart from the heartbeat, since
// removing a large workspace can take longer than a worker may go
// without one.
func (w *Worker) cleanup(ctx context.Context) {
	ticker := time.NewTicker(w.cleanupEvery)
	defer ticker.Stop()
	for {
		w.removeLeftovers(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// removeLeftovers removes step containers and workspaces of runs that
// are no longer running: left behind by a worker that was killed, or by
// one whose run was failed as lost while it was cut off. A killed
// worker's run is still running until it is failed as lost, a minute
// later, so this runs periodically, not only when a worker starts.
func (w *Worker) removeLeftovers(ctx context.Context) {
	tick, cancel := context.WithTimeout(ctx, dutyTimeout)
	defer cancel()
	containers, workspaces, err := RemoveLeftovers(tick, w.docker, w.cfg.WorkspaceRoot, w.ci.RunningRunIDs)
	if err != nil && ctx.Err() == nil {
		w.log.Warn("could not remove leftovers of ended runs", "error", err)
	}
	if containers > 0 || workspaces > 0 {
		w.log.Info("removed leftovers of ended runs", "containers", containers, "workspaces", workspaces)
	}
}

// RemoveLeftovers removes step containers and workspaces under
// workspaceRoot that belong to a run runningRuns no longer reports as
// running: left behind by a worker that was killed, or a run that was
// failed as lost while its worker was cut off. A worker calls this
// periodically on its own behalf; "gitman admin worker cleanup" calls it
// directly, for a host whose only worker was killed and never restarted,
// so nothing runs it on that host's behalf.
//
// It lists step containers and workspaces first, and only then asks
// which runs are running. A run claimed in between has nothing listed
// yet, so nothing belonging to a live run is ever removed — which
// asking first and listing second could not promise.
func RemoveLeftovers(ctx context.Context, docker *Docker, workspaceRoot string,
	runningRuns func(context.Context) (map[string]bool, error)) (containers, workspaces int, err error) {
	listed, err := docker.runContainers(ctx)
	if err != nil {
		return 0, 0, err
	}
	dirs, err := workspaceRuns(workspaceRoot)
	if err != nil {
		return 0, 0, err
	}
	running, err := runningRuns(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("list running runs: %w", err)
	}
	var errs []error
	for containerID, runID := range listed {
		if running[runID] {
			continue
		}
		if err := docker.removeContainer(ctx, containerID); err != nil {
			errs = append(errs, err)
			continue
		}
		containers++
	}
	for _, runID := range dirs {
		if running[runID] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(workspaceRoot, runID)); err != nil {
			errs = append(errs, fmt.Errorf("remove workspace %s: %w", runID, err))
			continue
		}
		workspaces++
	}
	return containers, workspaces, errors.Join(errs...)
}
