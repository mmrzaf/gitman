package main

import (
	"context"
	"fmt"

	"github.com/mmrzaf/gitman/internal/admin"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
)

func init() {
	register(Command{
		Name: "admin",
		Run:  runAdmin,
	})
}

func runAdmin(cfg *config.Config, database *db.DB, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gitman admin <users|repos>")
	}

	switch args[0] {
	case "users":
		return runAdminUsers(database, args[1:])
	case "repos":
		return runAdminRepos(cfg, database, args[1:])
	}

	return fmt.Errorf("unknown admin entity: %s", args[0])
}

func runAdminUsers(database *db.DB, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gitman admin users <create|reset-password|delete>")
	}

	switch args[0] {

	case "create":
		if len(args) != 3 {
			return fmt.Errorf("usage: gitman admin users create <username> <password>")
		}

		return admin.CreateUser(database, args[1], args[2])

	case "reset-password":
		if len(args) != 3 {
			return fmt.Errorf("usage: gitman admin users reset-password <username> <password>")
		}

		return admin.ResetPassword(database, args[1], args[2])

	case "delete":
		if len(args) != 2 {
			return fmt.Errorf("usage: gitman admin users delete <username>")
		}

		return admin.DeleteUser(database, args[1])
	}

	return fmt.Errorf("unknown users action: %s", args[0])
}

func runAdminRepos(cfg *config.Config, database *db.DB, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gitman admin repos <backup|backup-all>")
	}

	switch args[0] {
	case "backup":
		if len(args) != 2 {
			return fmt.Errorf("usage: gitman admin repos backup <destination>")
		}
		return admin.BackupRepos(cfg.ReposPath, args[1])
	case "backup-all":
		if len(args) != 2 {
			return fmt.Errorf("usage: gitman admin repos backup-all <destination>")
		}
		return admin.BackupAll(context.Background(), database, cfg, args[1])
	}

	return fmt.Errorf("unknown repos action: %s", args[0])
}
