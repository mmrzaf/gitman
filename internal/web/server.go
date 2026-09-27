package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"
)

// shutdownGrace is how long in-flight requests get to finish on
// shutdown. A clone still running when it expires is cut off; Git
// clients retry.
const shutdownGrace = 30 * time.Second

func (a *App) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.ping(ctx); err != nil {
		http.Error(w, "database unreachable", http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintln(w, "ok")
}

// Run serves until ctx is done, then shuts down gracefully.
func (a *App) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(a.cfg.Port)),
		Handler:           a.handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(a.log.Handler(), slog.LevelWarn),
		// No ReadTimeout or WriteTimeout: a clone or push streams for as
		// long as it takes. Handlers that stream set their own deadlines.
	}

	// Shutdown waits for requests to finish, and an event stream never
	// does on its own: end them all as shutdown begins.
	srv.RegisterOnShutdown(a.hub.close)

	go a.hub.run(ctx, a.listen, func(err error) {
		a.log.Warn("lost the connection live updates listen on; reconnecting", "error", err)
	})

	errs := make(chan error, 1)
	go func() {
		a.log.Info("listening", "addr", srv.Addr, "public_url", a.cfg.PublicURL)
		errs <- srv.ListenAndServe()
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	a.log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errs; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
