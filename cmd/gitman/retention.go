package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/repo"
)

// retentionInterval is how often the web process prunes old data.
const retentionInterval = time.Hour

// workerRecordRetention is how long a stopped worker's record is kept.
const workerRecordRetention = 7 * 24 * time.Hour

// runRetention prunes expired sessions, stopped workers' records and —
// when retentionDays is not zero — finished runs older than that, once
// at start and then every hour, until ctx ends.
func runRetention(ctx context.Context, people *auth.Service, runs *ci.Service, repos *repo.Service, policy config.Retention, log *slog.Logger) {
	prune := func() {
		work, stop := context.WithTimeout(ctx, 5*time.Minute)
		defer stop()
		work, release, err := runs.AdmitMutation(work)
		if err != nil {
			return
		}
		defer release()
		ctx := work
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
		if err := runs.PruneRetention(ctx, now, policy); err != nil {
			log.Error("retention requires attention", "error", err)
			return
		}
		if err := repos.PruneGitPins(ctx); err != nil {
			log.Error("Git retention requires attention", "error", err)
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
