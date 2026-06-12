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

func (app *App) HandleReposGET(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	repos := app.getReposForUser(r, user.ID)

	app.renderPage(w, r, "repos.html", PageData{
		Title: "Repositories",
		User:  user,
		Data:  ReposPageData{Repos: repos},
	})
}

func (app *App) HandleReposPOST(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	isPrivate := r.FormValue("is_private") == "on"

	if !git.SafeNameRegex.MatchString(name) {
		app.renderPartial(w, r, "repos.html", "repos_panel", PageData{
			User:  user,
			Error: "Invalid repository name. Only letters, numbers, dashes, and underscores allowed.",
			Data:  ReposPageData{Repos: app.getReposForUser(r, user.ID)},
		})
		return
	}
	if len(description) > 500 {
		app.renderPartial(w, r, "repos.html", "repos_panel", PageData{
			User:  user,
			Error: "Description too long. Max 500 characters.",
			Data:  ReposPageData{Repos: app.getReposForUser(r, user.ID)},
		})
		return
	}
	repoPath, err := git.SecureRepoPath(app.Config.ReposPath, user.Username, name)
	if err != nil {
		app.renderPartial(w, r, "repos.html", "repos_panel", PageData{
			User:  user,
			Error: "Invalid path generated for repository.",
			Data:  ReposPageData{Repos: app.getReposForUser(r, user.ID)},
		})
		return
	}

	if err := git.InitBareRepo(r.Context(), repoPath, app.Config.GitReceiveMaxBytes); err != nil {
		app.renderPartial(w, r, "repos.html", "repos_panel", PageData{
			User:  user,
			Error: "Failed to initialize git repository on disk. Check for orphaned repository files.",
			Data:  ReposPageData{Repos: app.getReposForUser(r, user.ID)},
		})
		return
	}

	_, err = app.DB.CreateRepository(r.Context(), user.ID, name, description, isPrivate)
	if err != nil {
		if cleanupErr := git.DeleteRepo(repoPath); cleanupErr != nil {
			slog.Error("failed to remove unregistered repository", "path", repoPath, "error", cleanupErr)
		}
		app.renderPartial(w, r, "repos.html", "repos_panel", PageData{
			User:  user,
			Error: "Repository name already exists or database error occurred.",
			Data:  ReposPageData{Repos: app.getReposForUser(r, user.ID)},
		})
		return
	}

	app.renderPartial(w, r, "repos.html", "repos_panel", PageData{
		User:    user,
		Success: "Repository created successfully.",
		Data:    ReposPageData{Repos: app.getReposForUser(r, user.ID)},
	})
}

func (app *App) HandleRepoDeletePOST(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	repoID := chi.URLParam(r, "id")

	if repoID == "" {
		app.renderPartial(w, r, "repos.html", "repos_panel", PageData{
			User:  user,
			Error: "Invalid repository id.",
			Data:  ReposPageData{Repos: app.getReposForUser(r, user.ID)},
		})
		return
	}

	repo, err := app.DB.GetRepositoryByID(r.Context(), repoID)
	if err != nil || repo == nil || repo.OwnerID != user.ID {
		app.renderPartial(w, r, "repos.html", "repos_panel", PageData{
			User:  user,
			Error: "Repository not found or not accessible.",
			Data:  ReposPageData{Repos: app.getReposForUser(r, user.ID)},
		})
		return
	}

	repoPath, pathErr := git.SecureRepoPath(app.Config.ReposPath, user.Username, repo.Name)
	if pathErr != nil {
		app.renderPartial(w, r, "repos.html", "repos_panel", PageData{
			User:  user,
			Error: "Invalid repository path.",
			Data:  ReposPageData{Repos: app.getReposForUser(r, user.ID)},
		})
		return
	}

	quarantinePath, err := git.QuarantineRepo(repoPath)
	if err != nil {
		slog.Error("failed to quarantine repo", "path", repoPath, "error", err)
		app.renderPartial(w, r, "repos.html", "repos_panel", PageData{
			User:  user,
			Error: "Failed to quarantine repository files. Repository was not deleted.",
			Data:  ReposPageData{Repos: app.getReposForUser(r, user.ID)},
		})
		return
	}

	if err := app.DB.DeleteRepository(r.Context(), repoID, user.ID); err != nil {
		restoreErr := git.RestoreQuarantinedRepo(quarantinePath, repoPath)
		if restoreErr != nil {
			slog.Error("failed to delete repository record and restore quarantined files", "repoID", repoID, "quarantine", quarantinePath, "delete_error", err, "restore_error", restoreErr)
			app.renderPartial(w, r, "repos.html", "repos_panel", PageData{
				User:  user,
				Error: "Failed to delete repository record and restore repository files. Contact an operator; the repository remains quarantined.",
				Data:  ReposPageData{Repos: app.getReposForUser(r, user.ID)},
			})
			return
		}
		slog.Error("failed to delete repository from DB", "repoID", repoID, "error", err)
		app.renderPartial(w, r, "repos.html", "repos_panel", PageData{
			User:  user,
			Error: "Failed to delete repository record. Repository files were restored.",
			Data:  ReposPageData{Repos: app.getReposForUser(r, user.ID)},
		})
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

	app.renderPartial(w, r, "repos.html", "repos_panel", PageData{
		User:    user,
		Success: "Repository deleted.",
		Data:    ReposPageData{Repos: app.getReposForUser(r, user.ID)},
	})

}
