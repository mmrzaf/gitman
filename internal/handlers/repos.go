package handlers

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

type ReposPageData struct {
	Repos []models.Repository
}

func (app *App) getReposForUser(r *http.Request, userID string) []models.Repository {
	repos, err := app.DB.GetUserRepositories(r.Context(), userID)
	if err != nil {
		return []models.Repository{}
	}
	return repos
}

func (app *App) renderReposPage(w http.ResponseWriter, r *http.Request, user *models.User, errStr, successStr string) {
	app.renderPage(w, r, "repos.html", PageData{
		Title:   "Repositories",
		User:    user,
		Error:   errStr,
		Success: successStr,
		Data:    ReposPageData{Repos: app.getReposForUser(r, user.ID)},
	})
}

func (app *App) HandleReposGET(w http.ResponseWriter, r *http.Request) {
	app.renderReposPage(w, r, GetUser(r), "", "")
}

func (app *App) HandleReposPOST(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	isPrivate := r.FormValue("is_private") == "on"

	if !git.SafeNameRegex.MatchString(name) {
		app.renderReposPage(w, r, user, "Invalid repository name. Only letters, numbers, dashes, and underscores allowed.", "")
		return
	}
	if len(description) > 500 {
		app.renderReposPage(w, r, user, "Description too long. Max 500 characters.", "")
		return
	}
	repoPath, err := git.SecureRepoPath(app.Config.ReposPath, user.Username, name)
	if err != nil {
		app.renderReposPage(w, r, user, "Invalid path generated for repository.", "")
		return
	}

	if err := git.InitBareRepo(r.Context(), repoPath, app.Config.GitReceiveMaxBytes); err != nil {
		app.renderReposPage(w, r, user, "Failed to initialize git repository on disk. Check for orphaned repository files.", "")
		return
	}

	_, err = app.DB.CreateRepository(r.Context(), user.ID, name, description, isPrivate)
	if err != nil {
		if cleanupErr := git.DeleteRepo(repoPath); cleanupErr != nil {
			slog.Error("failed to remove unregistered repository", "path", repoPath, "error", cleanupErr)
		}
		app.renderReposPage(w, r, user, "Repository name already exists or database error occurred.", "")
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

	repo, err := app.DB.GetRepositoryByID(r.Context(), repoID)
	if err != nil || repo == nil || repo.OwnerID != user.ID {
		app.renderReposPage(w, r, user, "Repository not found or not accessible.", "")
		return
	}

	repoPath, pathErr := git.SecureRepoPath(app.Config.ReposPath, user.Username, repo.Name)
	if pathErr != nil {
		app.renderReposPage(w, r, user, "Invalid repository path.", "")
		return
	}

	quarantinePath, err := git.QuarantineRepo(repoPath)
	if err != nil {
		slog.Error("failed to quarantine repo", "path", repoPath, "error", err)
		app.renderReposPage(w, r, user, "Failed to quarantine repository files. Repository was not deleted.", "")
		return
	}

	if err := app.DB.DeleteRepository(r.Context(), repoID, user.ID); err != nil {
		restoreErr := git.RestoreQuarantinedRepo(quarantinePath, repoPath)
		if restoreErr != nil {
			slog.Error("failed to delete repository record and restore quarantined files", "repoID", repoID, "quarantine", quarantinePath, "delete_error", err, "restore_error", restoreErr)
			app.renderReposPage(w, r, user, "Failed to delete repository record and restore repository files. Contact an operator; the repository remains quarantined.", "")
			return
		}
		slog.Error("failed to delete repository from DB", "repoID", repoID, "error", err)
		app.renderReposPage(w, r, user, "Failed to delete repository record. Repository files were restored.", "")
		return
	}

	cleanupPaths := []string{
		quarantinePath,
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
