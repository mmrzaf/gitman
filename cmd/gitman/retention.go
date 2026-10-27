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

// Each retention phase gets its own budget and admission, so one slow or
// failing phase cannot skip the rest or hold the backup fence for the
// others. Git pin reconciliation is the slowest on a busy repository.
const (
	pruneCredentialsTimeout = 2 * time.Minute
	pruneRunsTimeout        = 10 * time.Minute
	pruneGitTimeout         = 30 * time.Minute
)

// runRetention prunes expired sessions and tokens, stopped workers'
// records, finished runs and Git pins once at start and then every hour,
// until ctx ends.
func runRetention(ctx context.Context, people *auth.Service, runs *ci.Service, repos *repo.Service, policy config.Retention, log *slog.Logger) {
	prunePhase := func(name string, timeout time.Duration, fn func(context.Context) error) {
		work, stop := context.WithTimeout(ctx, timeout)
		defer stop()
		work, release, err := runs.AdmitMutation(work)
		if err != nil {
			if ctx.Err() == nil {
				log.Warn("retention phase skipped", "phase", name, "error", err)
			}
			return
		}
		defer release()
		if err := fn(work); err != nil {
			log.Error("retention requires attention", "phase", name, "error", err)
		}
	}

	prune := func() {
		prunePhase("credentials", pruneCredentialsTimeout, func(ctx context.Context) error {
			if n, err := people.PruneExpiredSessions(ctx); err != nil {
				log.Warn("could not prune expired sessions", "error", err)
			} else if n > 0 {
				log.Info("pruned expired sessions", "sessions", n)
			}
			if n, err := people.PruneExpiredTokens(ctx); err != nil {
				log.Warn("could not prune expired access tokens", "error", err)
			} else if n > 0 {
				log.Info("pruned expired access tokens", "tokens", n)
			}
			return nil
		})
		prunePhase("worker records", pruneCredentialsTimeout, func(ctx context.Context) error {
			n, err := runs.PruneWorkers(ctx, time.Now().Add(-workerRecordRetention))
			if err == nil && n > 0 {
				log.Info("pruned worker records", "workers", n)
			}
			return err
		})
		prunePhase("run history", pruneRunsTimeout, func(ctx context.Context) error {
			return runs.PruneRetention(ctx, time.Now(), policy)
		})
		prunePhase("Git pins", pruneGitTimeout, func(ctx context.Context) error {
			return repos.PruneGitPins(ctx)
		})
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
