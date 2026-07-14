package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/handlers"
	gitmanssh "github.com/mmrzaf/gitman/internal/ssh"
)

func init() {
	register(Command{
		Name: "web",
		Run:  runWeb,
	})
}

func runWeb(cfg *config.Config, database *db.DB, args []string) error {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	port := fs.String("port", "", "")

	if err := fs.Parse(args); err != nil {
		return err
	}

	finalPort := cfg.Port
	if *port != "" {
		finalPort = *port
	}
	portNumber, err := strconv.Atoi(finalPort)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("web port must be a number between 1 and 65535")
	}

	if err := os.MkdirAll(cfg.ReposPath, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(cfg.ReposPath, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.ArtifactsPath, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(cfg.ArtifactsPath, 0o700); err != nil {
		return err
	}

	templates, err := handlers.LoadTemplates()
	if err != nil {
		return err
	}

	staticFS, err := handlers.NewStaticFS()
	if err != nil {
		return err
	}

	app := &handlers.App{
		Config:    cfg,
		DB:        database,
		Templates: templates,
		StaticFS:  staticFS,
	}
	if err := gitmanssh.SyncAuthorizedKeys(context.Background(), database, cfg); err != nil {
		return fmt.Errorf("synchronize authorized_keys: %w", err)
	}
	if err := database.DeleteExpiredSessions(context.Background()); err != nil {
		slog.Warn("failed to prune expired sessions at startup", "error", err)
	}
	pruneCtx, stopPrune := context.WithCancel(context.Background())
	defer stopPrune()
	go pruneExpiredSessions(pruneCtx, database)
	go app.RunCITriggerQueue(pruneCtx)

	router := handlers.SetupRouter(app)

	srv := &http.Server{
		Addr:              ":" + finalPort,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	errChan := make(chan error, 1)

	go func() {
		slog.Info("web server starting", "port", finalPort)
		errChan <- srv.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {

	case err := <-errChan:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}

	case sig := <-stop:
		slog.Info("shutdown", "signal", sig)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		return srv.Shutdown(ctx)
	}

	return nil
}

func pruneExpiredSessions(ctx context.Context, database *db.DB) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := database.DeleteExpiredSessions(context.Background()); err != nil {
				slog.Warn("failed to prune expired sessions", "error", err)
			}
		}
	}
}
