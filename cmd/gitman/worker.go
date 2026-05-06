package main

import (
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/worker"
)

func init() {
	register(Command{
		Name: "worker",
		Run:  runWorker,
	})
}

func runWorker(cfg *config.Config, database *db.DB, args []string) error {
	return worker.Run(cfg, database)
}
