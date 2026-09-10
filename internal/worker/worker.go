package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
)

const (
	pollInterval  = 3 * time.Second
	shutdownGrace = 60 * time.Second
)

var (
	errDiskLimitExceeded           = errors.New("disk limit exceeded")
	errDockerSocketWorkerDisabled  = errors.New("docker socket access disabled by worker")
	errDockerSocketRefNotTrusted   = errors.New("docker socket access not trusted for ref")
	errDockerSocketUnavailable     = errors.New("docker socket unavailable")
	errDockerImageUnavailable      = errors.New("docker image unavailable")
	errDockerUnavailable           = errors.New("docker runner unavailable")
	errDockerHostPathMisconfigured = errors.New("docker host path mapping misconfigured")
	errCISecretsRefNotTrusted      = errors.New("CI secrets not trusted for ref")
	errCISecretStoreUnavailable    = errors.New("CI secret store unavailable")
	errArtifactPublication         = errors.New("artifact publication failed")
	errWorkerStorageUnavailable    = errors.New("worker storage unavailable")
)

func Run(cfg *config.Config, database *db.DB) error {
	if err := validateWorkerConfig(cfg); err != nil {
		return err
	}
	for _, warning := range cfg.ProductionWarnings() {
		slog.Warn("production configuration warning", "warning", warning)
	}
	slog.Info("starting gitman worker",
		"artifacts", cfg.ArtifactsPath,
		"cache", cfg.CacheRoot,
		"workers", cfg.WorkerConcurrency,
		"timeout", cfg.CIJobTimeout,
		"network", cfg.CINetwork,
	)

	if err := prepareDirectories(cfg.ArtifactsPath, cfg.CacheRoot, cfg.CIWorkspaceRoot); err != nil {
		return err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("detect worker hostname: %w", err)
	}
	workerID := uuid.New().String()
	state := newWorkerRuntimeState(workerID)
	registerCtx, cancelRegister := context.WithTimeout(context.Background(), 5*time.Second)
	err = database.RegisterCIWorker(registerCtx, models.CIWorker{
		ID:            workerID,
		Hostname:      hostname,
		PID:           os.Getpid(),
		Concurrency:   cfg.WorkerConcurrency,
		Healthy:       false,
		StatusMessage: "starting",
		StartedAt:     time.Now(),
		HeartbeatAt:   time.Now(),
	})
	cancelRegister()
	if err != nil {
		return fmt.Errorf("register CI worker: %w", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if stopErr := database.StopCIWorker(ctx, workerID); stopErr != nil && !errors.Is(stopErr, db.ErrNotFound) {
			slog.Warn("failed to mark CI worker stopped", "worker_id", workerID, "error", stopErr)
		}
	}()

	runtimeCtx, stopRuntime := context.WithCancel(context.Background())
	defer stopRuntime()
	refreshWorkerHealth(runtimeCtx, cfg, database, state)
	go monitorWorkerHealth(runtimeCtx, cfg, database, state)

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	pollCtx, stopPolling := context.WithCancel(context.Background())
	defer stopPolling()
	jobRootCtx, cancelJobs := context.WithCancel(context.Background())
	defer cancelJobs()
	go reapStaleRuns(pollCtx, cfg, database, state)

	var wg sync.WaitGroup

	for i := 0; i < cfg.WorkerConcurrency; i++ {
		wg.Add(1)
		go worker(pollCtx, jobRootCtx, cfg, database, state, &wg)
	}

	slog.Info("worker pool started", "worker_id", workerID)
	sig := <-signals
	slog.Info("shutdown signal received; draining active CI jobs", "signal", sig, "grace", shutdownGrace)
	state.beginDrain()
	persistWorkerHeartbeat(database, state)
	stopPolling()

	drainDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(drainDone)
	}()

	drainTimer := time.NewTimer(shutdownGrace)
	defer drainTimer.Stop()
	select {
	case <-drainDone:
		slog.Info("all workers finished gracefully")
		return nil
	case sig := <-signals:
		slog.Warn("second shutdown signal received; cancelling active CI jobs", "signal", sig)
	case <-drainTimer.C:
		slog.Warn("shutdown grace period expired; cancelling active CI jobs")
	}

	cancelJobs()
	forceTimer := time.NewTimer(15 * time.Second)
	defer forceTimer.Stop()
	select {
	case <-drainDone:
		slog.Info("active CI jobs stopped")
	case <-forceTimer.C:
		slog.Warn("active CI jobs did not stop before process shutdown")
	}
	return nil
}

func reapStaleRuns(ctx context.Context, cfg *config.Config, database *db.DB, state *workerRuntimeState) {
	interval := cfg.CILeaseTimeout / 2
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !state.readyForClaims() {
				continue
			}
			requeued, err := reconcileAndRequeue(ctx, cfg, database)
			if err != nil {
				slog.Warn("failed to reconcile stale CI runs", "error", err)
				continue
			}
			if requeued > 0 {
				slog.Warn("requeued stale CI runs", "count", requeued)
			}
		}
	}
}

func worker(pollCtx, jobParentCtx context.Context, cfg *config.Config, database *db.DB, state *workerRuntimeState, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		if pollCtx.Err() != nil {
			return
		}
		if !state.readyForClaims() {
			if !waitForPoll(pollCtx) {
				return
			}
			continue
		}
		processed, err := processNext(pollCtx, jobParentCtx, cfg, database, state)
		if err != nil {
			slog.Error("job processing error", "error", err)
			if !waitForPoll(pollCtx) {
				return
			}
			continue
		}
		if processed {
			// Drain queued work immediately. Polling is only for the idle case.
			continue
		}
		if !waitForPoll(pollCtx) {
			return
		}
	}
}

func waitForPoll(ctx context.Context) bool {
	timer := time.NewTimer(pollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func processNext(claimCtx, jobParentCtx context.Context, cfg *config.Config, database *db.DB, state *workerRuntimeState) (bool, error) {
	run, err := database.ClaimNextPendingRun(claimCtx)
	if err != nil {
		return false, fmt.Errorf("claim run: %w", err)
	}
	if run == nil {
		return false, nil
	}
	// Admission can become unhealthy while ClaimNextPendingRun is in flight.
	// Never start a newly leased attempt after that transition; release this
	// exact lease immediately so another healthy worker can pick it up later.
	if !state.readyForClaims() {
		requeueCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := database.RequeueCIRunAttempt(requeueCtx, run.ID, run.AttemptID, "Waiting for worker admission")
		cancel()
		if err != nil && !errors.Is(err, db.ErrCIRunLeaseInactive) {
			return true, fmt.Errorf("release CI run claimed during admission pause: %w", err)
		}
		return true, nil
	}
	state.activeJobs.Add(1)
	defer state.activeJobs.Add(-1)

	jobCtx, cancelJob := context.WithCancel(jobParentCtx)
	if cfg.CIJobTimeout > 0 {
		jobCtx, cancelJob = context.WithTimeout(jobParentCtx, cfg.CIJobTimeout)
	}
	defer cancelJob()

	// Keep heartbeats alive until this attempt actually returns. Job cancellation
	// can take time to stop Docker and clean up; dropping the lease early would
	// make the same run eligible for recovery while the old attempt still owns files.
	heartbeatCtx, stopHeartbeat := context.WithCancel(context.Background())
	defer stopHeartbeat()
	leaseLost := make(chan error, 1)
	go heartbeatRun(heartbeatCtx, database, run.ID, run.AttemptID, cfg.CIHeartbeatInterval, cfg.CILeaseTimeout, cancelJob, leaseLost)

	repo, owner, err := resolveRepo(jobCtx, database, run.RepoID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			slog.Warn("CI repository metadata disappeared", "run_id", run.ID, "attempt_id", run.AttemptID, "error", err)
			if completeErr := completeCIRunReliably(database, run.ID, run.AttemptID, models.CIStatusFailed, "Repository is no longer available"); completeErr != nil && !errors.Is(completeErr, db.ErrCIRunLeaseInactive) {
				return true, fmt.Errorf("record missing repository failure: %w", completeErr)
			}
			return true, nil
		}
		// A persistence outage is not evidence that the repository disappeared.
		// Leave the claimed run for lease recovery/requeue instead of recording a
		// false terminal result.
		return true, fmt.Errorf("load CI repository metadata: %w", err)
	}

	j := &job{
		cfg:         cfg,
		database:    database,
		run:         run,
		repo:        repo,
		owner:       owner,
		workerState: state,
	}

	if err := j.execute(jobCtx); err != nil {
		if isRetriableWorkerInfrastructureFailure(err) {
			state.setHealth(false, err.Error())
			persistWorkerHeartbeat(database, state)
			requeueCtx, cancelRequeue := context.WithTimeout(context.Background(), 5*time.Second)
			requeueErr := database.RequeueCIRunAttempt(requeueCtx, run.ID, run.AttemptID, "Waiting for worker infrastructure")
			cancelRequeue()
			if requeueErr == nil {
				if j.logFile != nil {
					if removeErr := os.Remove(j.logFile.Name()); removeErr != nil && !os.IsNotExist(removeErr) {
						slog.Warn("failed to remove transient CI attempt log", "run_id", run.ID, "attempt_id", run.AttemptID, "error", removeErr)
					}
				}
				slog.Warn("requeued CI run after worker infrastructure failure", "run_id", run.ID, "attempt_id", run.AttemptID, "error", err)
				return true, nil
			}
			if !errors.Is(requeueErr, db.ErrCIRunLeaseInactive) {
				return true, fmt.Errorf("requeue CI run after worker infrastructure failure: %w", requeueErr)
			}
		}
		readCtx, cancelRead := context.WithTimeout(context.Background(), 5*time.Second)
		current, readErr := database.GetCIRunByID(readCtx, run.ID)
		cancelRead()
		if readErr == nil && current != nil && current.Status != models.CIStatusRunning {
			if current.Status == models.CIStatusCancelled {
				if ackErr := acknowledgeCancelledRun(database, run.ID, run.AttemptID); ackErr != nil && !errors.Is(ackErr, db.ErrCIRunLeaseInactive) {
					return true, fmt.Errorf("acknowledge cancelled CI run: %w", ackErr)
				}
				slog.Info("CI run stopped after cancellation", "run_id", run.ID, "attempt_id", run.AttemptID)
				return true, nil
			}
			return true, fmt.Errorf("CI run finished but worker returned an error: %w", err)
		}
		select {
		case leaseErr := <-leaseLost:
			slog.Warn("CI run stopped after lease loss", "run_id", run.ID, "attempt_id", run.AttemptID, "error", leaseErr)
			return true, nil
		default:
			if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
				slog.Error("CI run timed out", "run_id", run.ID, "attempt_id", run.AttemptID, "timeout", cfg.CIJobTimeout)
			} else {
				slog.Error("CI run fatal error", "run_id", run.ID, "attempt_id", run.AttemptID, "error", err)
			}
		}
		if completeErr := completeCIRunReliably(database, run.ID, run.AttemptID, models.CIStatusFailed, "Worker failed before recording a final result"); completeErr != nil && !errors.Is(completeErr, db.ErrCIRunLeaseInactive) {
			return true, fmt.Errorf("record fallback CI failure: %w", completeErr)
		}
	}
	return true, nil
}

func acknowledgeCancelledRun(database *db.DB, runID, attemptID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return database.AcknowledgeCancelledCIRun(ctx, runID, attemptID)
}

func completeCIRunReliably(database *db.DB, runID, attemptID string, status models.CIStatus, reason string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		lastErr = database.CompleteCIRunWithReason(ctx, runID, attemptID, status, reason)
		cancel()
		if lastErr == nil || errors.Is(lastErr, db.ErrCIRunLeaseInactive) {
			return lastErr
		}
		time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
	}
	return lastErr
}

func heartbeatRun(ctx context.Context, database *db.DB, runID, attemptID string, interval, leaseTimeout time.Duration, cancelJob context.CancelFunc, leaseLost chan<- error) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if leaseTimeout <= 0 {
		leaseTimeout = 2 * time.Minute
	}
	lastSuccess := time.Now()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			heartbeatCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := database.HeartbeatCIRun(heartbeatCtx, runID, attemptID)
			cancel()
			if err == nil {
				lastSuccess = time.Now()
				continue
			}
			if errors.Is(err, db.ErrCIRunLeaseInactive) || time.Since(lastSuccess) >= leaseTimeout {
				slog.Warn("CI lease lost; cancelling attempt", "run_id", runID, "attempt_id", attemptID, "error", err)
				select {
				case leaseLost <- err:
				default:
				}
				cancelJob()
				return
			}
			slog.Warn("temporary CI heartbeat failure; keeping attempt active", "run_id", runID, "attempt_id", attemptID, "error", err, "lease_remaining", leaseTimeout-time.Since(lastSuccess))
		}
	}
}
