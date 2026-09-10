package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
	"github.com/mmrzaf/gitman/internal/repository"
	sshhandler "github.com/mmrzaf/gitman/internal/ssh"
	"github.com/mmrzaf/gitman/internal/validate"
)

func recordAdminAuditEvent(ctx context.Context, database *db.DB, action, targetType, targetID string, metadata map[string]string) {
	if database == nil {
		return
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := database.RecordAuditEvent(auditCtx, models.AuditEvent{
		ActorUsername: "admin-cli",
		Action:        action,
		TargetType:    targetType,
		TargetID:      targetID,
		Metadata:      metadata,
	}); err != nil {
		slog.Error("record admin audit event", "action", action, "target_type", targetType, "target_id", targetID, "error", err)
	}
}

func CreateUser(ctx context.Context, cfg *config.Config, database *db.DB, username, password string) (retErr error) {
	if err := validate.Username(username); err != nil {
		return err
	}
	if err := validate.Password(password); err != nil {
		return err
	}
	if cfg == nil {
		return fmt.Errorf("configuration is required")
	}

	lock, err := repository.LockNamespace(ctx, cfg.ReposPath, username)
	if err != nil {
		return fmt.Errorf("lock user namespace: %w", err)
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("release user namespace lock: %w", releaseErr))
		}
	}()

	user, err := database.CreateUser(ctx, username, password)
	if err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}
	recordAdminAuditEvent(ctx, database, models.AuditActionUserCreated, "user", user.ID, map[string]string{"username": user.Username})

	return nil
}

func ResetPassword(ctx context.Context, database *db.DB, username, password string) error {
	if err := validate.Password(password); err != nil {
		return err
	}
	user, err := database.GetUserByUsername(ctx, username)
	if err != nil {
		return fmt.Errorf("look up user before password reset: %w", err)
	}
	if err := database.UpdateUserPassword(ctx, username, password); err != nil {
		return fmt.Errorf("failed to reset password: %w", err)
	}
	recordAdminAuditEvent(ctx, database, models.AuditActionPasswordReset, "user", user.ID, map[string]string{"username": user.Username})

	return nil
}

// DeleteUser moves repository data out of the active namespace before deleting
// the database record. Recreating the username can never adopt stale repos.
func DeleteUser(ctx context.Context, cfg *config.Config, database *db.DB, username string) (retErr error) {
	if err := validate.Username(username); err != nil {
		return err
	}
	if cfg == nil {
		return fmt.Errorf("configuration is required")
	}
	lock, err := repository.LockNamespace(ctx, cfg.ReposPath, username)
	if err != nil {
		return fmt.Errorf("lock user namespace: %w", err)
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("release user namespace lock: %w", releaseErr))
		}
	}()

	user, err := database.GetUserByUsername(ctx, username)
	if err != nil {
		return fmt.Errorf("look up user: %w", err)
	}

	activeRepos := filepath.Join(cfg.ReposPath, username)
	quarantinedRepos, err := quarantineUserDirectory(cfg.ReposPath, activeRepos, username)
	if err != nil {
		return fmt.Errorf("quarantine user repositories: %w", err)
	}

	if err := database.DeleteUserByID(ctx, user.ID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			// Another deletion already removed this exact user. Never restore the
			// old quarantined directory: the username may already belong to a new
			// account, and restoring would resurrect orphaned repository storage.
			if quarantinedRepos != "" {
				if cleanupErr := os.RemoveAll(quarantinedRepos); cleanupErr != nil {
					return fmt.Errorf("user was already deleted, but quarantined repositories could not be removed: %w", cleanupErr)
				}
			}
			if syncErr := sshhandler.SyncAuthorizedKeys(ctx, database, cfg); syncErr != nil {
				return fmt.Errorf("user was already deleted, but authorized_keys sync failed: %w", syncErr)
			}
			return nil
		}
		primary := fmt.Errorf("delete user: %w", err)

		// A failed DELETE does not prove that this user still exists: another
		// Gitman process may have deleted the same immutable user ID immediately
		// afterward. Recovery gets a short independent context so cancellation of
		// the CLI request cannot force us to guess whether restoring old repository
		// storage would resurrect an orphaned username namespace.
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, stateErr := database.GetUserByID(recoveryCtx, user.ID)
		cancel()
		switch {
		case stateErr == nil:
			if restoreErr := restoreQuarantinedDirectory(quarantinedRepos, activeRepos); restoreErr != nil {
				return errors.Join(primary, fmt.Errorf("restore quarantined repositories: %w", restoreErr))
			}
		case errors.Is(stateErr, db.ErrNotFound):
			if quarantinedRepos != "" {
				if cleanupErr := os.RemoveAll(quarantinedRepos); cleanupErr != nil {
					return errors.Join(primary, fmt.Errorf("user was concurrently deleted, but quarantined repositories could not be removed: %w", cleanupErr))
				}
			}
			return nil
		default:
			return errors.Join(primary, fmt.Errorf("determine user deletion state: %w", stateErr))
		}
		return primary
	}
	recordAdminAuditEvent(ctx, database, models.AuditActionUserDeleted, "user", user.ID, map[string]string{"username": user.Username})

	cleanupPaths := make([]string, 0, 4)
	if quarantinedRepos != "" {
		cleanupPaths = append(cleanupPaths, quarantinedRepos)
	}
	if cfg.ArtifactsPath != "" {
		cleanupPaths = append(cleanupPaths,
			filepath.Join(cfg.ArtifactsPath, "logs", username),
			filepath.Join(cfg.ArtifactsPath, "files", username),
		)
	}
	if cfg.CacheRoot != "" {
		cleanupPaths = append(cleanupPaths, filepath.Join(cfg.CacheRoot, username))
	}
	var cleanupErrs []error
	for _, path := range cleanupPaths {
		if err := os.RemoveAll(path); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("cleanup failed for %s: %w", path, err))
		}
	}
	cleanupErr := errors.Join(cleanupErrs...)
	if err := sshhandler.SyncAuthorizedKeys(ctx, database, cfg); err != nil {
		syncErr := fmt.Errorf("user deleted, but authorized_keys sync failed: %w", err)
		if cleanupErr != nil {
			return errors.Join(cleanupErr, syncErr)
		}
		return syncErr
	}
	if cleanupErr != nil {
		return fmt.Errorf("user deleted, but %w", cleanupErr)
	}

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
