package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/worker"
)

// lostRunSweepInterval is how often the web process checks for runs
// whose worker has gone silent.
const lostRunSweepInterval = time.Minute

// runLostRunSweep is the web process's backstop for a run whose worker
// stopped and never came back: normally a restarted worker fails its
// dead predecessor's runs itself, on the same schedule, so this only
// matters when no worker ever restarts. It waits out worker.LostAfter
// from its own first reachable database, the same way a worker waits
// out its own heartbeat loop before judging another worker lost — right
// after an outage every worker's last heartbeat looks equally stale, and
// they need a chance to report back in first.
func runLostRunSweep(ctx context.Context, database *postgres.DB, runs *ci.Service, log *slog.Logger) {
	var healthySince time.Time
	ticker := time.NewTicker(lostRunSweepInterval)
	defer ticker.Stop()
	for {
		if err := database.Ping(ctx); err != nil {
			healthySince = time.Time{}
		} else {
			if healthySince.IsZero() {
				healthySince = time.Now()
			}
			if time.Since(healthySince) >= worker.LostAfter {
				if n, err := runs.FailLostRuns(ctx, worker.LostAfter); err != nil {
					if ctx.Err() == nil {
						log.Warn("could not check for runs of lost workers", "error", err)
					}
				} else if n > 0 {
					log.Warn("failed runs whose worker stopped responding", "runs", n)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
