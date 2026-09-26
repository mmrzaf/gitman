package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/push"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

// errHookFailed makes the process exit non-zero after the hook has
// already written its own explanation for the pusher.
var errHookFailed = errors.New("hook failed")

// runHook runs a Git hook. Git starts it, through the scripts the web
// process installs, with the push's context in the environment; what it
// writes to standard error, Git shows the pusher.
func runHook(args []string) error {
	if len(args) != 1 || (args[0] != push.PreReceive && args[0] != push.PostReceive) {
		return fmt.Errorf("usage: gitman hook %s|%s", push.PreReceive, push.PostReceive)
	}
	name := args[0]
	if err := push.Explain(os.Stderr, name, serveHook(name, os.Stdin, os.Stderr)); err != nil {
		return errHookFailed
	}
	return nil
}

func serveHook(name string, stdin io.Reader, out io.Writer) error {
	hctx, err := push.ContextFromEnv()
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), push.Timeout(name))
	defer cancel()

	// Git runs a bare repository's hooks inside the repository directory.
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := push.CheckDir(cwd, cfg.ReposPath(), hctx.RepoID); err != nil {
		return err
	}

	database, err := postgres.Connect(ctx, cfg.DatabaseURL, postgres.Options{MaxConns: 2})
	if err != nil {
		return err
	}
	defer database.Close()
	gitRepo := git.OpenHookRepo(cwd, hctx.GitEnv)
	defer gitRepo.Close()
	store := git.NewStore(cfg.ReposPath())
	defer store.Close()

	h := &push.Hook{
		DB:        database,
		People:    auth.NewService(database),
		Repos:     reposvc.NewService(database, store, ""),
		CI:        ci.NewService(database),
		Git:       gitRepo,
		Ctx:       hctx,
		PublicURL: cfg.PublicURL,
		Out:       out,
	}
	return h.Serve(ctx, name, stdin)
}
