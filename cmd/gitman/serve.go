package main

import (
	"fmt"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	sshhandler "github.com/mmrzaf/gitman/internal/ssh"
)

func init() {
	register(Command{
		Name: "serve",
		Run:  runServe,
	})
}

func runServe(cfg *config.Config, database *db.DB, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gitman serve <keyID>")
	}

	keyID := args[0]

	sshhandler.Serve(keyID, cfg, database)

	return nil
}
