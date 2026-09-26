package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/push"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
	"github.com/mmrzaf/gitman/internal/web"
)

// dbAcquireTimeout bounds how long the web process waits for a
// connection from an exhausted pool before answering 503 rather than
// leaving the request to hang: long enough that a brief burst of
// concurrent requests is never the cause, short enough that a person
// waiting on a page is not left staring at it for long either.
const dbAcquireTimeout = 5 * time.Second

// runWeb runs the web process until it receives SIGINT or SIGTERM.
func runWeb(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("web takes no arguments; it is configured through the environment")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := cfg.NewLogger(os.Stderr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := postgres.Connect(ctx, cfg.DatabaseURL, postgres.Options{
		MaxConns: int32(cfg.DatabaseMaxConns), AcquireTimeout: dbAcquireTimeout,
	})
	if err != nil {
		return err
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		return err
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate the gitman binary: %w", err)
	}
	if executable, err = filepath.EvalSymlinks(executable); err != nil {
		return fmt.Errorf("locate the gitman binary: %w", err)
	}
	if err := push.Install(cfg.HooksPath(), executable); err != nil {
		return err
	}

	store := git.NewStore(cfg.ReposPath())
	defer store.Close()
	if err := store.Sweep(); err != nil {
		log.Warn("could not remove leftovers of interrupted repository operations", "error", err)
	}

	people := auth.NewService(database)
	runs := ci.NewService(database)
	app, err := web.New(cfg, web.Services{
		People:   people,
		Repos:    reposvc.NewService(database, store, cfg.SecretKey),
		CI:       runs,
		Activity: activity.NewService(database),
		Ping:     database.Ping,
		Listen:   database.Listen,
	}, log)
	if err != nil {
		return err
	}
	go runRetention(ctx, people, runs, cfg.RetentionDays, log)
	go runLostRunSweep(ctx, database, runs, log)
	return app.Run(ctx)
}
