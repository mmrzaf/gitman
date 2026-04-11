package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
)

type Command struct {
	Name string
	Run  func(*config.Config, *db.DB, []string) error
}

var commands = map[string]Command{}

func register(cmd Command) {
	commands[cmd.Name] = cmd
}

func Execute(args []string) error {
	cfg := config.LoadConfig()
	initLogger(cfg)

	if len(args) < 2 {
		return help(os.Stdout)
	}

	database, err := db.InitDB(cfg.DBPath)
	if err != nil {
		return err
	}
	defer database.Close()

	cmd, ok := commands[args[1]]
	if !ok {
		return fmt.Errorf("unknown command: %s", args[1])
	}

	return cmd.Run(cfg, database, args[2:])
}

func initLogger(cfg *config.Config) {
	level := config.ParseLogLevel(cfg.LogLevel)

	logger := slog.New(
		slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: level,
		}),
	)

	slog.SetDefault(logger)
}
