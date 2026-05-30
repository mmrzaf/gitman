package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mmrzaf/gitman/internal/admin"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
)

func init() {
	register(Command{Name: "admin", Run: runAdmin})
}

func runAdmin(cfg *config.Config, database *db.DB, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gitman admin <users|repos>")
	}
	switch args[0] {
	case "users":
		return runAdminUsers(cfg, database, args[1:])
	case "repos":
		return runAdminRepos(cfg, database, args[1:])
	default:
		return fmt.Errorf("unknown admin entity: %s", args[0])
	}
}

func runAdminUsers(cfg *config.Config, database *db.DB, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gitman admin users <create|reset-password|delete>")
	}

	switch args[0] {
	case "create":
		if len(args) != 2 {
			return fmt.Errorf("usage: printf 'password\\n' | gitman admin users create <username>")
		}
		password, err := readPasswordFromStdin()
		if err != nil {
			return err
		}
		return admin.CreateUser(database, args[1], password)
	case "reset-password":
		if len(args) != 2 {
			return fmt.Errorf("usage: printf 'password\\n' | gitman admin users reset-password <username>")
		}
		password, err := readPasswordFromStdin()
		if err != nil {
			return err
		}
		return admin.ResetPassword(database, args[1], password)
	case "delete":
		if len(args) != 2 {
			return fmt.Errorf("usage: gitman admin users delete <username>")
		}
		return admin.DeleteUser(cfg, database, args[1])
	default:
		return fmt.Errorf("unknown users action: %s", args[0])
	}
}

func readPasswordFromStdin() (string, error) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect stdin: %w", err)
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", fmt.Errorf("refusing to read an echoed password from an interactive terminal; pipe the password on stdin")
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
	if err != nil {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	if len(data) > 4096 {
		return "", fmt.Errorf("password input is too large")
	}
	password := strings.TrimRight(string(data), "\r\n")
	if password == "" {
		return "", fmt.Errorf("password is required on stdin")
	}
	return password, nil
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
	default:
		return fmt.Errorf("unknown repos action: %s", args[0])
	}
}
