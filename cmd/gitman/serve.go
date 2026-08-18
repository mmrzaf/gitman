package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	sshhandler "github.com/mmrzaf/gitman/internal/ssh"
)

func init() {
	register(Command{
		Name:     "serve",
		NeedsGit: true,
		Run:      runServe,
	})
}

func runServe(cfg *config.Config, database *db.DB, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gitman serve <keyID>")
	}

	keyID := args[0]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGHUP, syscall.SIGTERM)
	defer stop()

	return sshhandler.Serve(ctx, keyID, os.Getenv("SSH_ORIGINAL_COMMAND"), cfg, database, os.Stdin, os.Stdout, os.Stderr)
}
