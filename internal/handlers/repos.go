package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
	"github.com/mmrzaf/gitman/internal/repository"
	"github.com/mmrzaf/gitman/internal/validate"
)

type ReposPageData struct {
	Repos []models.Repository
}

type repositoryQuarantineState uint8

const (
	repositoryStillRegistered repositoryQuarantineState = iota + 1
	repositoryAlreadyDeleted
)

// reconcileRepositoryQuarantine determines whether quarantined repository
// storage should be restored after a guarded database delete failed or lost a
// race. It never guesses: an uncertain database state leaves the quarantine in
// place for operator recovery rather than risking resurrection of orphaned
// active storage.
func (app *App) reconcileRepositoryQuarantine(ctx context.Context, repoID, repoPath, quarantinePath string) (repositoryQuarantineState, error) {
	_, err := app.DB.GetRepositoryByID(ctx, repoID)
	switch {
	case err == nil:
		if restoreErr := git.RestoreQuarantinedRepo(quarantinePath, repoPath); restoreErr != nil {
			return repositoryStillRegistered, fmt.Errorf("restore quarantined repository: %w", restoreErr)
		}
		return repositoryStillRegistered, nil
	case errors.Is(err, db.ErrNotFound):
		if quarantinePath != "" {
			if cleanupErr := git.DeleteRepo(quarantinePath); cleanupErr != nil {
				return repositoryAlreadyDeleted, fmt.Errorf("remove quarantined repository: %w", cleanupErr)
			}
		}
		return repositoryAlreadyDeleted, nil
	default:
		return 0, err
	}
}

func releaseNamespaceLock(r *http.Request, lock *repository.NamespaceLock) {
	if err := lock.Release(); err != nil {
		slog.Error("release repository namespace lock", "request_id", RequestID(r), "error", err)
	}
}

func (app *App) renderReposPage(w http.ResponseWriter, r *http.Request, user *models.User, errStr, successStr string) {
	repos, err := app.DB.GetUserRepositories(r.Context(), user.ID)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
		return
	}
	app.renderPage(w, r, "repos.html", PageData{
		Title:   "Repositories",
		User:    user,
		Error:   errStr,
		Success: successStr,
		Data:    ReposPageData{Repos: repos},
	})
}

func (app *App) HandleReposGET(w http.ResponseWriter, r *http.Request) {
	app.renderReposPage(w, r, GetUser(r), "", "")
}

func (app *App) HandleReposPOST(w http.ResponseWriter, r *http.Request) {
	if !app.parseWebForm(w, r) {
		return
	}
	user := GetUser(r)
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	isPrivate := r.FormValue("is_private") == "on"

	if err := validate.RepositoryName(name); err != nil {
		app.renderReposPage(w, r, user, err.Error()+".", "")
		return
	}
	if len(description) > 500 {
		app.renderReposPage(w, r, user, "Description too long. Max 500 characters.", "")
		return
	}
	lock, err := repository.LockNamespace(r.Context(), app.Config.ReposPath, user.Username)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository namespace is temporarily unavailable", err))
		return
	}
	defer releaseNamespaceLock(r, lock)

	repoPath, err := git.SecureRepoPath(app.Config.ReposPath, user.Username, name)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindInternal, "Gitman could not resolve the repository path", err))
		return
	}

	if err := git.InitBareRepo(r.Context(), repoPath, app.Config.GitReceiveMaxBytes); err != nil {
		if errors.Is(err, git.ErrRepoPathExists) {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindConflict, "Repository storage already exists for this name", err))
		} else {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository storage is temporarily unavailable", err))
		}
		return
	}

	_, err = app.DB.CreateRepository(r.Context(), user.ID, name, description, isPrivate)
	if err != nil {
		if cleanupErr := git.DeleteRepo(repoPath); cleanupErr != nil {
			slog.Error("failed to remove unregistered repository", "request_id", RequestID(r), "path", repoPath, "error", cleanupErr)
		}
		if errors.Is(err, db.ErrAlreadyExists) {
			app.renderReposPage(w, r, user, "Repository name already exists.", "")
		} else {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository storage is temporarily unavailable", err))
		}
		return
	}

	app.renderReposPage(w, r, user, "", "Repository created successfully.")
}

func (app *App) HandleRepoDeletePOST(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	repoID := chi.URLParam(r, "id")

	if repoID == "" {
		app.renderReposPage(w, r, user, "Invalid repository id.", "")
		return
	}
	lock, err := repository.LockNamespace(r.Context(), app.Config.ReposPath, user.Username)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository namespace is temporarily unavailable", err))
		return
	}
	defer releaseNamespaceLock(r, lock)

	repo, err := app.DB.GetRepositoryByID(r.Context(), repoID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.renderReposPage(w, r, user, "Repository not found or not accessible.", "")
		} else {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
		}
		return
	}
	if repo.OwnerID != user.ID {
		app.renderReposPage(w, r, user, "Repository not found or not accessible.", "")
		return
	}

	repoPath, pathErr := git.SecureRepoPath(app.Config.ReposPath, user.Username, repo.Name)
	if pathErr != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindInternal, "Gitman could not resolve the repository path", pathErr))
		return
	}

	// Pull durable filesystem triggers into SQLite before deciding whether the
	// repository is idle. If an older event is temporarily undeliverable, keep
	// the repository rather than deleting the only durable copy of the push.
	app.drainRepoCITriggerQueue(r.Context(), user, repo)
	hasActiveRuns, err := app.DB.HasActiveCIRuns(r.Context(), repo.ID)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository activity is temporarily unavailable", err))
		return
	}
	if hasActiveRuns {
		app.renderReposPage(w, r, user, "Cancel or wait for queued and running CI jobs before deleting this repository.", "")
		return
	}
	hasQueuedTriggers, err := hasQueuedCITriggerFiles(filepath.Join(repoPath, "hooks", ciHookQueueDirName))
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository CI state is temporarily unavailable", err))
		return
	}
	if hasQueuedTriggers {
		app.renderReposPage(w, r, user, "Wait for queued push events to enter CI before deleting this repository.", "")
		return
	}

	quarantinePath, err := git.QuarantineRepo(repoPath)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository storage is temporarily unavailable", err))
		return
	}

	deleted, err := app.DB.DeleteRepository(r.Context(), repoID, user.ID)
	if err != nil {
		// Recovery must outlive a cancelled client request long enough to restore
		// consistency. Another delete may also have committed after this statement
		// failed, so re-check the immutable repository ID before restoring storage.
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		state, recoveryErr := app.reconcileRepositoryQuarantine(recoveryCtx, repoID, repoPath, quarantinePath)
		cancel()
		switch {
		case recoveryErr != nil:
			slog.Error("repository deletion recovery failed",
				"request_id", RequestID(r), "repo", repoID, "quarantine", quarantinePath,
				"delete_error", err, "recovery_error", recoveryErr)
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository deletion state is temporarily unavailable", errors.Join(err, recoveryErr)))
		case state == repositoryAlreadyDeleted:
			app.renderReposPage(w, r, user, "", "Repository deleted.")
		default:
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository storage is temporarily unavailable", err))
		}
		return
	}
	if !deleted {
		// A zero-row delete can mean active CI raced the guarded DELETE, but it
		// can also mean another concurrent delete already removed the database
		// row. Only restore quarantined storage when the row is positively known
		// to still exist; otherwise we could resurrect an orphaned bare repo.
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		state, stateErr := app.reconcileRepositoryQuarantine(recoveryCtx, repoID, repoPath, quarantinePath)
		cancel()
		switch {
		case stateErr == nil && state == repositoryStillRegistered:
			app.renderReposPage(w, r, user, "Repository activity changed while deletion was being prepared. Try again after active CI has stopped.", "")
		case stateErr == nil && state == repositoryAlreadyDeleted:
			app.renderReposPage(w, r, user, "", "Repository deleted.")
		default:
			// The DB state is uncertain. Preserve the quarantine and do not guess
			// whether restoring it would create an orphaned active repository.
			slog.Error("repository deletion state could not be reconciled",
				"request_id", RequestID(r), "repo", repoID, "quarantine", quarantinePath, "error", stateErr)
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository deletion state is temporarily unavailable", stateErr))
		}
		return
	}

	if quarantinePath != "" {
		if err := git.DeleteRepo(quarantinePath); err != nil {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindInternal, "Repository was deleted, but Gitman could not finish removing repository data", err))
			return
		}
	}

	cleanupPaths := []string{
		filepath.Join(app.Config.ArtifactsPath, "logs", user.Username, repo.Name),
		filepath.Join(app.Config.ArtifactsPath, "files", user.Username, repo.Name),
		filepath.Join(app.Config.CacheRoot, user.Username, repo.Name),
	}
	for _, path := range cleanupPaths {
		if path == "" {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			slog.Warn("repository cleanup failed", "path", path, "error", err)
		}
	}

	app.renderReposPage(w, r, user, "", "Repository deleted.")
}

type RepoSettingsPageData struct {
	Owner      *models.User
	Repository *models.Repository
}

func (app *App) renderRepoSettings(w http.ResponseWriter, r *http.Request, errMessage, successMessage string) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	app.renderPage(w, r, "repo_settings.html", PageData{
		Title:   repo.Name + " - Settings",
		User:    GetUser(r),
		Error:   errMessage,
		Success: successMessage,
		RepoNav: app.repoNavData(r, ""),
		Data: RepoSettingsPageData{
			Owner:      owner,
			Repository: repo,
		},
	})
}

func (app *App) HandleRepoSettingsGET(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	repo := GetRepo(r)
	if user == nil || repo == nil || user.ID != repo.OwnerID {
		app.respondWebError(w, r, apperr.New(apperr.KindForbidden, "Forbidden"))
		return
	}
	app.renderRepoSettings(w, r, "", "")
}

func (app *App) HandleRepoSettingsPOST(w http.ResponseWriter, r *http.Request) {
	if !app.parseWebForm(w, r) {
		return
	}
	user := GetUser(r)
	repo := GetRepo(r)
	if user == nil || repo == nil || user.ID != repo.OwnerID {
		app.respondWebError(w, r, apperr.New(apperr.KindForbidden, "Forbidden"))
		return
	}
	description := strings.TrimSpace(r.FormValue("description"))
	if len(description) > 500 {
		app.renderRepoSettings(w, r, "Description is limited to 500 characters.", "")
		return
	}
	isPrivate := r.FormValue("is_private") == "on"
	if err := app.DB.UpdateRepositorySettings(r.Context(), repo.ID, user.ID, description, isPrivate); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindNotFound, "Repository not found", err))
		} else {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository settings are temporarily unavailable", err))
		}
		return
	}
	repo.Description = description
	repo.IsPrivate = isPrivate
	app.renderRepoSettings(w, r, "", "Repository settings saved.")
}
