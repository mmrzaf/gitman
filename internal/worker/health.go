package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
)

const workerHealthInterval = 10 * time.Second

type workerRuntimeState struct {
	id string

	mu       sync.RWMutex
	healthy  bool
	message  string
	draining bool

	activeJobs atomic.Int64
}

func newWorkerRuntimeState(id string) *workerRuntimeState {
	return &workerRuntimeState{id: id, message: "starting"}
}

func (s *workerRuntimeState) setHealth(healthy bool, message string) (changed bool) {
	message = strings.TrimSpace(message)
	if len(message) > 500 {
		message = message[:500]
	}
	s.mu.Lock()
	changed = s.healthy != healthy || s.message != message
	s.healthy = healthy
	s.message = message
	s.mu.Unlock()
	return changed
}

func (s *workerRuntimeState) beginDrain() {
	s.mu.Lock()
	s.draining = true
	s.mu.Unlock()
}

func (s *workerRuntimeState) readyForClaims() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.healthy && !s.draining
}

func (s *workerRuntimeState) isDraining() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.draining
}

func (s *workerRuntimeState) snapshot() (healthy bool, message string, activeJobs int) {
	s.mu.RLock()
	healthy = s.healthy
	message = s.message
	draining := s.draining
	s.mu.RUnlock()
	if draining {
		healthy = false
		message = "draining active CI jobs"
	}
	active := s.activeJobs.Load()
	if active < 0 {
		active = 0
	}
	return healthy, message, int(active)
}

func probeWorkerAdmission(ctx context.Context, cfg *config.Config) error {
	if err := checkFilesystemHeadroom(
		[]string{cfg.ArtifactsPath, cfg.CacheRoot, cfg.CIWorkspaceRoot},
		cfg.CIStorageMinFreeBytes,
		cfg.CIStorageMinFreeInodes,
	); err != nil {
		return err
	}
	return probeDockerDaemon(ctx)
}

func refreshWorkerHealth(ctx context.Context, cfg *config.Config, database *db.DB, state *workerRuntimeState) {
	wasReady := state.readyForClaims()
	probeCtx, cancelProbe := context.WithTimeout(ctx, 5*time.Second)
	err := database.PingContext(probeCtx)
	cancelProbe()
	if err != nil {
		err = fmt.Errorf("database health probe: %w", err)
	} else {
		err = probeWorkerAdmission(ctx, cfg)
	}
	if err == nil && !wasReady && !state.isDraining() {
		reconcileCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, reconcileErr := reconcileAndRequeue(reconcileCtx, cfg, database)
		cancel()
		if reconcileErr != nil {
			err = fmt.Errorf("reconcile CI state before accepting work: %w", reconcileErr)
		}
	}
	if err != nil {
		if state.setHealth(false, err.Error()) {
			slog.Warn("CI worker admission unavailable; pausing new claims", "worker_id", state.id, "error", err)
		}
	} else if state.setHealth(true, "ready") {
		slog.Info("CI worker admission ready", "worker_id", state.id)
	}
	persistWorkerHeartbeat(database, state)
}

func persistWorkerHeartbeat(database *db.DB, state *workerRuntimeState) {
	healthy, message, activeJobs := state.snapshot()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := database.HeartbeatCIWorker(ctx, state.id, healthy, message, activeJobs); err != nil {
		slog.Warn("failed to persist CI worker heartbeat", "worker_id", state.id, "error", err)
	}
}

func monitorWorkerHealth(ctx context.Context, cfg *config.Config, database *db.DB, state *workerRuntimeState) {
	ticker := time.NewTicker(workerHealthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshWorkerHealth(ctx, cfg, database, state)
		}
	}
}
