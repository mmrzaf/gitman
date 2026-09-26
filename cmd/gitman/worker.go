package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
	"github.com/mmrzaf/gitman/internal/worker"
)

// defaultRunTimeout bounds a run whose pipeline sets no timeout.
const defaultRunTimeout = 30 * time.Minute

// runWorker runs a worker until it receives SIGINT or SIGTERM.
func runWorker(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("worker takes no arguments; it is configured through the environment")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("read hostname: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := postgres.Connect(ctx, cfg.DatabaseURL, postgres.Options{MaxConns: 4})
	if err != nil {
		return err
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		return err
	}
	if err := worker.PrepareWorkspaceRoot(cfg.WorkspacesPath()); err != nil {
		return err
	}

	ciService := ci.NewService(database)
	docker, err := newDocker(ctx, ciService, "docker")
	if err != nil {
		return err
	}
	store := git.NewStore(cfg.ReposPath())
	defer store.Close()
	w := worker.New(worker.Config{
		WorkspaceRoot:  cfg.WorkspacesPath(),
		WebURL:         cfg.WebURL,
		DefaultTimeout: defaultRunTimeout,
		Hostname:       hostname,
	}, database, ciService, reposvc.NewService(database, store, cfg.SecretKey), docker, log)
	return w.Run(ctx)
}

// newDocker returns the docker client a worker runs steps through, and
// "gitman admin worker cleanup" removes leftovers through, labelled with
// this instance's ID.
func newDocker(ctx context.Context, ciService *ci.Service, binary string) (*worker.Docker, error) {
	instance, err := ciService.InstanceID(ctx)
	if err != nil {
		return nil, err
	}
	return &worker.Docker{Binary: binary, Socket: "/var/run/docker.sock", Instance: instance}, nil
}
