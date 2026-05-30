package admin

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	sshhandler "github.com/mmrzaf/gitman/internal/ssh"
)

func CreateUser(database *db.DB, username, password string) error {
	if err := ValidateUsername(username); err != nil {
		return err
	}
	if err := IsPasswordStrong(password); err != nil {
		return err
	}

	ctx := contextBackground()
	_, err := database.CreateUser(ctx, username, password)
	if err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}

	fmt.Printf("User %q created successfully.\n", username)
	return nil
}

func ResetPassword(database *db.DB, username, password string) error {
	if err := IsPasswordStrong(password); err != nil {
		return err
	}
	ctx := contextBackground()
	if err := database.UpdateUserPassword(ctx, username, password); err != nil {
		return fmt.Errorf("failed to reset password: %w", err)
	}

	fmt.Printf("Password for %q reset successfully.\n", username)
	return nil
}

// DeleteUser moves repository data out of the active namespace before deleting
// the database record. Recreating the username can never adopt stale repos.
func DeleteUser(cfg *config.Config, database *db.DB, username string) error {
	if err := ValidateUsername(username); err != nil {
		return err
	}
	ctx := contextBackground()
	user, err := database.GetUserByUsername(ctx, username)
	if err != nil {
		return fmt.Errorf("look up user: %w", err)
	}
	if user == nil {
		return fmt.Errorf("user not found")
	}

	activeRepos := filepath.Join(cfg.ReposPath, username)
	quarantinedRepos, err := quarantineUserDirectory(cfg.ReposPath, activeRepos, username)
	if err != nil {
		return fmt.Errorf("quarantine user repositories: %w", err)
	}

	if err := database.DeleteUserByUsername(ctx, username); err != nil {
		if restoreErr := restoreQuarantinedDirectory(quarantinedRepos, activeRepos); restoreErr != nil {
			return fmt.Errorf("delete user: %v (repository restore failed: %v)", err, restoreErr)
		}
		return fmt.Errorf("delete user: %w", err)
	}

	cleanupPaths := []string{
		quarantinedRepos,
		filepath.Join(cfg.ArtifactsPath, "logs", username),
		filepath.Join(cfg.ArtifactsPath, "files", username),
		filepath.Join(cfg.CacheRoot, username),
	}
	var cleanupErr error
	for _, path := range cleanupPaths {
		if path == "" {
			continue
		}
		if err := os.RemoveAll(path); err != nil && cleanupErr == nil {
			cleanupErr = fmt.Errorf("cleanup failed for %s: %w", path, err)
		}
	}
	if err := sshhandler.SyncAuthorizedKeys(ctx, database, cfg); err != nil {
		if cleanupErr != nil {
			return fmt.Errorf("user deleted, but %v; authorized_keys sync also failed: %w", cleanupErr, err)
		}
		return fmt.Errorf("user deleted, but authorized_keys sync failed: %w", err)
	}
	if cleanupErr != nil {
		return fmt.Errorf("user deleted, but %w", cleanupErr)
	}

	fmt.Printf("User %q deleted.\n", username)
	return nil
}

func quarantineUserDirectory(reposRoot, activePath, username string) (string, error) {
	if _, err := os.Lstat(activePath); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", err
	}

	trashRoot := filepath.Join(reposRoot, ".trash", "users")
	if err := os.MkdirAll(trashRoot, 0o700); err != nil {
		return "", err
	}
	placeholder, err := os.MkdirTemp(trashRoot, username+"-")
	if err != nil {
		return "", err
	}
	if err := os.Remove(placeholder); err != nil {
		return "", err
	}
	if err := os.Rename(activePath, placeholder); err != nil {
		return "", err
	}
	return placeholder, nil
}

func restoreQuarantinedDirectory(quarantinePath, activePath string) error {
	if quarantinePath == "" {
		return nil
	}
	return os.Rename(quarantinePath, activePath)
}
