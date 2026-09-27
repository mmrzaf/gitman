package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/ci"
)

// retentionInterval is how often the web process prunes old data.
const retentionInterval = time.Hour

// workerRecordRetention is how long a stopped worker's record is kept.
const workerRecordRetention = 7 * 24 * time.Hour

// runRetention prunes expired sessions, stopped workers' records and —
// when retentionDays is not zero — finished runs older than that, once
// at start and then every hour, until ctx ends.
func runRetention(ctx context.Context, people *auth.Service, runs *ci.Service, retentionDays int, log *slog.Logger) {
	prune := func() {
		now := time.Now()
		if n, err := people.PruneExpiredSessions(ctx); err != nil {
			log.Warn("could not prune expired sessions", "error", err)
		} else if n > 0 {
			log.Info("pruned expired sessions", "sessions", n)
		}
		if n, err := runs.PruneWorkers(ctx, now.Add(-workerRecordRetention)); err != nil {
			log.Warn("could not prune worker records", "error", err)
		} else if n > 0 {
			log.Info("pruned worker records", "workers", n)
		}
		if retentionDays == 0 {
			return
		}
		if n, err := runs.PruneRuns(ctx, now.AddDate(0, 0, -retentionDays)); err != nil {
			log.Warn("could not prune old runs", "error", err)
		} else if n > 0 {
			log.Info("pruned old runs", "runs", n, "older_than_days", retentionDays)
		}
	}

	ticker := time.NewTicker(retentionInterval)
	defer ticker.Stop()
	for {
		prune()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
