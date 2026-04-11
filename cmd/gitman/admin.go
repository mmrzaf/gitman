package main

import (
	"context"
	"fmt"
	"regexp"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
)

var usernameRegex = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9_-]*[a-zA-Z0-9])?$`)

func runAdmin(cfg *config.Config, database *db.DB, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gitman admin <entity> <action> [args...]\nEntities: users, repos")
	}

	entity := args[0]
	switch entity {
	case "users":
		return runAdminUsers(database, args[1:])
	case "repos":
		return runAdminRepos(cfg, database, args[1:])
	default:
		return fmt.Errorf("unknown admin entity: %s\nAvailable entities: users, repos", entity)
	}
}

func runAdminUsers(database *db.DB, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gitman admin users <create|reset-password|delete>")
	}

	action := args[0]
	ctx := context.Background()

	switch action {
	case "create":
		if len(args) != 3 {
			return fmt.Errorf("usage: gitman admin users create <username> <password>")
		}
		username, password := args[1], args[2]

		if err := validateUser(username, password); err != nil {
			return err
		}

		user, err := database.CreateUser(ctx, username, password)
		if err != nil {
			return fmt.Errorf("failed to create user: %w", err)
		}
		fmt.Printf("Successfully created user '%s' with ID: %s\n", user.Username, user.ID)
		return nil

	case "reset-password":
		if len(args) != 3 {
			return fmt.Errorf("usage: gitman admin users reset-password <username> <new_password>")
		}
		username, password := args[1], args[2]

		if len(password) < 8 {
			return fmt.Errorf("password must be at least 8 characters")
		}

		err := database.UpdateUserPassword(ctx, username, password)
		if err != nil {
			return fmt.Errorf("failed to reset password: %w", err)
		}
		fmt.Printf("Successfully reset password for user '%s'\n", username)
		return nil

	case "delete":
		if len(args) != 2 {
			return fmt.Errorf("usage: gitman admin users delete <username>")
		}
		username := args[1]

		err := database.DeleteUserByUsername(ctx, username)
		if err != nil {
			return fmt.Errorf("failed to delete user: %w", err)
		}
		fmt.Printf("Successfully deleted user '%s'\n", username)
		return nil

	default:
		return fmt.Errorf("unknown users action: %s\nAvailable actions: create, reset-password, delete", action)
	}
}

func runAdminRepos(cfg *config.Config, database *db.DB, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gitman admin repos <backup|reindex>")
	}

	action := args[0]
	switch action {
	case "backup":
		if len(args) != 2 {
			return fmt.Errorf("usage: gitman admin repos backup <destination_dir>")
		}
		// TODO: Implement repo backup logic (e.g. copying cfg.ReposPath to destination)
		dest := args[1]
		fmt.Printf("Backup of repos from '%s' to '%s' is not yet implemented.\n", cfg.ReposPath, dest)
		return nil
	default:
		return fmt.Errorf("unknown repos action: %s", action)
	}
}

// validateUser ensures CLI user creation follows the same rules as the Web UI
func validateUser(username, password string) error {
	if len(username) < 3 || len(username) > 32 {
		return fmt.Errorf("username must be between 3 and 32 characters")
	}
	if !usernameRegex.MatchString(username) {
		return fmt.Errorf("username may only contain letters, numbers, dashes and underscores")
	}
	if len(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	return nil
}
