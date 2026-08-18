package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	citrigger "github.com/mmrzaf/gitman/internal/ci/trigger"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

var (
	ErrActiveCI       = errors.New("repository has active CI runs")
	ErrQueuedTriggers = errors.New("repository has undelivered CI push events")
	ErrCleanup        = errors.New("repository deleted with cleanup errors")
)

// Manager owns operations that must keep repository database and filesystem
// state consistent. HTTP and admin commands translate the returned errors for
// their own surfaces; they do not reimplement lifecycle sequencing.
type Manager struct {
	DB                 *db.DB
	ReposPath          string
	ArtifactsPath      string
	CacheRoot          string
	GitReceiveMaxBytes int64
	Triggers           *citrigger.Manager
}

func (m *Manager) triggerManager() *citrigger.Manager {
	if m.Triggers != nil {
		return m.Triggers
	}
	return &citrigger.Manager{DB: m.DB, ReposPath: m.ReposPath}
}

func (m *Manager) Create(ctx context.Context, owner *models.User, name, description string, private bool) (id string, retErr error) {
	lock, err := LockNamespace(ctx, m.ReposPath, owner.Username)
	if err != nil {
		return "", fmt.Errorf("lock repository namespace: %w", err)
	}
	defer func() {
		if err := lock.Release(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("release repository namespace lock: %w", err))
		}
	}()

	repoPath, err := git.SecureRepoPath(m.ReposPath, owner.Username, name)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	if err := git.InitBareRepo(ctx, repoPath, m.GitReceiveMaxBytes); err != nil {
		return "", fmt.Errorf("initialize bare repository: %w", err)
	}
	cleanupRepo := func() error {
		if err := git.DeleteRepo(repoPath); err != nil {
			return fmt.Errorf("remove unregistered repository: %w", err)
		}
		return nil
	}

	if err := m.triggerManager().EnsureHook(ctx, owner.Username, name); err != nil {
		return "", errors.Join(fmt.Errorf("install CI trigger hook: %w", err), cleanupRepo())
	}

	id, err = m.DB.CreateRepository(ctx, owner.ID, name, description, private)
	if err != nil {
		return "", errors.Join(err, cleanupRepo())
	}
	return id, nil
}

func (m *Manager) Delete(ctx context.Context, owner *models.User, repoID string) (retErr error) {
	lock, err := LockNamespace(ctx, m.ReposPath, owner.Username)
	if err != nil {
		return fmt.Errorf("lock repository namespace: %w", err)
	}
	defer func() {
		if err := lock.Release(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("release repository namespace lock: %w", err))
		}
	}()

	repo, err := m.DB.GetRepositoryByID(ctx, repoID)
	if err != nil {
		return err
	}
	if repo.OwnerID != owner.ID {
		return db.ErrNotFound
	}
	repoPath, err := git.SecureRepoPath(m.ReposPath, owner.Username, repo.Name)
	if err != nil {
		return fmt.Errorf("resolve repository path: %w", err)
	}

	triggers := m.triggerManager()
	if err := triggers.DrainRepository(ctx, owner, repo); err != nil {
		return fmt.Errorf("drain CI push events: %w", err)
	}
	active, err := m.DB.HasActiveCIRuns(ctx, repo.ID)
	if err != nil {
		return fmt.Errorf("check active CI runs: %w", err)
	}
	if active {
		return ErrActiveCI
	}
	queued, err := citrigger.HasQueuedEvents(filepath.Join(repoPath, "hooks", citrigger.QueueDirName))
	if err != nil {
		return fmt.Errorf("check queued CI events: %w", err)
	}
	if queued {
		return ErrQueuedTriggers
	}

	quarantinePath, err := git.QuarantineRepo(repoPath)
	if err != nil {
		return fmt.Errorf("quarantine repository: %w", err)
	}

	deleted, deleteErr := m.DB.DeleteRepository(ctx, repo.ID, owner.ID)
	if deleteErr != nil || !deleted {
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		state, recoveryErr := m.reconcileQuarantine(recoveryCtx, repo.ID, repoPath, quarantinePath)
		cancel()
		if recoveryErr != nil {
			return errors.Join(deleteErr, fmt.Errorf("reconcile repository deletion: %w", recoveryErr))
		}
		if state == repositoryGone {
			return nil
		}
		if deleteErr != nil {
			return deleteErr
		}
		return ErrActiveCI
	}

	if quarantinePath != "" {
		if err := git.DeleteRepo(quarantinePath); err != nil {
			return errors.Join(ErrCleanup, fmt.Errorf("remove quarantined repository: %w", err))
		}
	}

	var cleanupPaths []string
	if m.ArtifactsPath != "" {
		cleanupPaths = append(cleanupPaths,
			filepath.Join(m.ArtifactsPath, "logs", owner.Username, repo.Name),
			filepath.Join(m.ArtifactsPath, "files", owner.Username, repo.Name),
		)
	}
	if m.CacheRoot != "" {
		cleanupPaths = append(cleanupPaths, filepath.Join(m.CacheRoot, owner.Username, repo.Name))
	}

	var cleanupErrs []error
	for _, path := range cleanupPaths {
		if err := os.RemoveAll(path); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	if len(cleanupErrs) > 0 {
		return errors.Join(append([]error{ErrCleanup}, cleanupErrs...)...)
	}
	return nil
}

type quarantineState uint8

const (
	repositoryPresent quarantineState = iota + 1
	repositoryGone
)

func (m *Manager) reconcileQuarantine(ctx context.Context, repoID, repoPath, quarantinePath string) (quarantineState, error) {
	_, err := m.DB.GetRepositoryByID(ctx, repoID)
	switch {
	case err == nil:
		if err := git.RestoreQuarantinedRepo(quarantinePath, repoPath); err != nil {
			return repositoryPresent, err
		}
		return repositoryPresent, nil
	case errors.Is(err, db.ErrNotFound):
		if quarantinePath != "" {
			if err := git.DeleteRepo(quarantinePath); err != nil {
				return repositoryGone, err
			}
		}
		return repositoryGone, nil
	default:
		// An uncertain DB state must not resurrect storage. Leave the quarantine
		// in place for operator recovery rather than guessing.
		return 0, err
	}
}
