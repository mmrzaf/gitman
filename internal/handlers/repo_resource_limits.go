package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/mmrzaf/gitman/internal/apperr"
)

const (
	defaultRepoBrowseMaxConcurrent      = 16
	defaultRepoBrowseMaxConcurrentPerIP = 4
	defaultRepoBrowseTimeout            = 10 * time.Second
	defaultRepoStreamMaxConcurrent      = 8
	defaultRepoStreamMaxConcurrentPerIP = 2
	defaultRepoStreamTimeout            = 15 * time.Minute
)

func (app *App) repoBrowseConcurrencyLimiter() *requestConcurrencyLimiter {
	app.repoBrowseOnce.Do(func() {
		total := app.Config.RepoBrowseMaxConcurrent
		if total <= 0 {
			total = defaultRepoBrowseMaxConcurrent
		}
		perIP := app.Config.RepoBrowseMaxConcurrentPerIP
		if perIP <= 0 {
			perIP = defaultRepoBrowseMaxConcurrentPerIP
		}
		app.repoBrowseLimiter = newRequestConcurrencyLimiter(total, perIP)
	})
	return app.repoBrowseLimiter
}

func (app *App) repoStreamConcurrencyLimiter() *requestConcurrencyLimiter {
	app.repoStreamOnce.Do(func() {
		total := app.Config.RepoStreamMaxConcurrent
		if total <= 0 {
			total = defaultRepoStreamMaxConcurrent
		}
		perIP := app.Config.RepoStreamMaxConcurrentPerIP
		if perIP <= 0 {
			perIP = defaultRepoStreamMaxConcurrentPerIP
		}
		app.repoStreamLimiter = newRequestConcurrencyLimiter(total, perIP)
	})
	return app.repoStreamLimiter
}

// RepoBrowseLimits bounds CPU-heavy repository presentation work such as tree,
// ref, and commit inspection. It intentionally wraps only browser browse
// endpoints; long-lived raw/archive streams use the separate stream guard.
func (app *App) RepoBrowseLimits(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		release, ok := app.repoBrowseConcurrencyLimiter().tryAcquire(app.clientIP(r))
		if !ok {
			w.Header().Set("Retry-After", "1")
			app.respondForSurface(w, r, apperr.New(apperr.KindUnavailable, "Repository browser is busy; retry shortly"))
			return
		}
		defer release()

		timeout := app.Config.RepoBrowseTimeout
		if timeout <= 0 {
			timeout = defaultRepoBrowseTimeout
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// beginRepoStream establishes a bounded slot and response deadline for source
// archives and raw/download blob streams. The returned context is canceled at
// the same deadline so Git subprocesses are terminated even if the client keeps
// a connection slowly progressing.
func (app *App) beginRepoStream(w http.ResponseWriter, r *http.Request) (context.Context, func(), bool) {
	release, ok := app.repoStreamConcurrencyLimiter().tryAcquire(app.clientIP(r))
	if !ok {
		w.Header().Set("Retry-After", "2")
		app.respondForSurface(w, r, apperr.New(apperr.KindUnavailable, "Repository download capacity is busy; retry shortly"))
		return nil, nil, false
	}

	timeout := app.Config.RepoStreamTimeout
	if timeout <= 0 {
		timeout = defaultRepoStreamTimeout
	}
	deadline := time.Now().Add(timeout)
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		slog.Debug("could not set repository stream write deadline", "request_id", RequestID(r), "error", err)
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	cleanup := func() {
		cancel()
		_ = controller.SetWriteDeadline(time.Time{})
		release()
	}
	return ctx, cleanup, true
}
