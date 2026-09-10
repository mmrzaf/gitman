package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mmrzaf/gitman/internal/admin"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
)

func init() {
	register(Command{Name: "admin", Run: runAdmin})
}

func runAdmin(cfg *config.Config, database *db.DB, args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(args) == 0 {
		return fmt.Errorf("usage: gitman admin <users|repos|status|audit>")
	}
	switch args[0] {
	case "users":
		return runAdminUsers(ctx, cfg, database, args[1:])
	case "repos":
		return runAdminRepos(ctx, cfg, database, args[1:])
	case "status":
		return runAdminStatus(ctx, cfg, database, args[1:])
	case "audit":
		return runAdminAudit(ctx, database, args[1:])
	default:
		return fmt.Errorf("unknown admin entity: %s", args[0])
	}
}

func runAdminUsers(ctx context.Context, cfg *config.Config, database *db.DB, args []string) error {
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
		if err := admin.CreateUser(ctx, cfg, database, args[1], password); err != nil {
			return err
		}
		_, err = fmt.Fprintf(os.Stdout, "User %q created successfully.\n", args[1])
		return err
	case "reset-password":
		if len(args) != 2 {
			return fmt.Errorf("usage: printf 'password\\n' | gitman admin users reset-password <username>")
		}
		password, err := readPasswordFromStdin()
		if err != nil {
			return err
		}
		if err := admin.ResetPassword(ctx, database, args[1], password); err != nil {
			return err
		}
		_, err = fmt.Fprintf(os.Stdout, "Password for %q reset successfully.\n", args[1])
		return err
	case "delete":
		if len(args) != 2 {
			return fmt.Errorf("usage: gitman admin users delete <username>")
		}
		if err := admin.DeleteUser(ctx, cfg, database, args[1]); err != nil {
			return err
		}
		_, err := fmt.Fprintf(os.Stdout, "User %q deleted.\n", args[1])
		return err
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

func runAdminRepos(ctx context.Context, cfg *config.Config, database *db.DB, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gitman admin repos <backup|backup-all|configure-all>")
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
		return admin.BackupAll(ctx, database, cfg, args[1])
	case "configure-all":
		if len(args) != 1 {
			return fmt.Errorf("usage: gitman admin repos configure-all")
		}
		return admin.ConfigureAllRepos(ctx, database, cfg)
	default:
		return fmt.Errorf("unknown repos action: %s", args[0])
	}
}

func runAdminStatus(ctx context.Context, cfg *config.Config, database *db.DB, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: gitman admin status")
	}
	status := admin.CollectStatus(ctx, cfg, database, time.Now())
	var werr error
	emit := func(format string, a ...any) {
		if werr == nil {
			_, werr = fmt.Fprintf(os.Stdout, format, a...)
		}
	}
	emit("Gitman: %s\n", version)
	if status.DatabaseReady {
		emit("Database: ok (schema %d)\n", status.SchemaVersion)
	} else {
		emit("Database: not ready (%s)\n", status.DatabaseError)
	}
	printAdminPathStatus(emit, "Repositories", status.Repositories)
	printAdminPathStatus(emit, "Artifacts", status.Artifacts)
	if status.WorkerError != "" {
		emit("CI workers: unavailable (%s)\n", status.WorkerError)
	} else {
		emit("CI workers: %d healthy / %d recent, %d active jobs\n", status.Workers.Healthy, status.Workers.Recent, status.Workers.ActiveJobs)
	}
	if status.QueueError != "" {
		emit("CI queue: unavailable (%s)\n", status.QueueError)
	} else if status.Queue.OldestPendingAt != nil {
		emit("CI queue: %d pending (oldest %s)\n", status.Queue.Pending, status.Queue.OldestPendingAt.UTC().Format(time.RFC3339))
	} else {
		emit("CI queue: 0 pending\n")
	}
	if len(status.Warnings) == 0 {
		emit("Configuration warnings: none\n")
	} else {
		emit("Configuration warnings:\n")
		for _, warning := range status.Warnings {
			emit("  - %s\n", warning)
		}
	}
	if werr != nil {
		return werr
	}
	if !status.CoreReady() {
		return fmt.Errorf("core readiness checks failed")
	}
	if !status.CIReady() {
		if status.WorkerError != "" {
			return fmt.Errorf("CI worker status query failed")
		}
		if status.QueueError != "" {
			return fmt.Errorf("CI queue status query failed")
		}
		return fmt.Errorf("CI queue has pending work but no healthy worker")
	}
	return nil
}

func printAdminPathStatus(emit func(string, ...any), label string, status admin.PathStatus) {
	if status.Ready {
		emit("%s: ok (%s)\n", label, status.Path)
		return
	}
	emit("%s: not ready (%s: %s)\n", label, status.Path, status.Error)
}

func runAdminAudit(ctx context.Context, database *db.DB, args []string) error {
	fs := flag.NewFlagSet("admin audit", flag.ContinueOnError)
	limit := fs.Int("limit", 100, "maximum number of events (1-500)")
	jsonLines := fs.Bool("json", false, "emit newline-delimited JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *limit < 1 || *limit > 500 {
		return fmt.Errorf("usage: gitman admin audit [--limit 1-500] [--json]")
	}
	return admin.WriteAuditEvents(ctx, database, os.Stdout, *limit, *jsonLines)
}
