package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
	"github.com/mmrzaf/gitman/internal/repository"
	"github.com/mmrzaf/gitman/internal/validate"
)

type ReposPageData struct {
	PageData
	Repos []models.Repository
}

func (app *App) repositoryManager() *repository.Manager {
	return &repository.Manager{
		DB:                 app.DB,
		ReposPath:          app.Config.ReposPath,
		ArtifactsPath:      app.Config.ArtifactsPath,
		CacheRoot:          app.Config.CacheRoot,
		GitReceiveMaxBytes: app.Config.GitReceiveMaxBytes,
	}
}

func (app *App) renderReposPage(w http.ResponseWriter, r *http.Request, user *models.User, errStr, successStr string) {
	repos, err := app.DB.GetUserRepositories(r.Context(), user.ID)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
		return
	}
	app.renderPage(w, r, "repos.html", &ReposPageData{
		PageData: PageData{Title: "Repositories", User: user, Error: errStr, Success: successStr},
		Repos:    repos,
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
	repoID, err := app.repositoryManager().Create(r.Context(), user, name, description, isPrivate)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrAlreadyExists), errors.Is(err, git.ErrRepoPathExists):
			app.renderReposPage(w, r, user, "Repository name already exists.", "")
		default:
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository storage is temporarily unavailable", err))
		}
		return
	}
	app.recordAuditEvent(r, user, models.AuditActionRepositoryCreated, "repository", repoID, map[string]string{
		"name":       name,
		"is_private": strconv.FormatBool(isPrivate),
	})
	app.renderReposPage(w, r, user, "", "Repository created successfully.")
}

func (app *App) HandleRepoDeletePOST(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	repoID := chi.URLParam(r, "id")
	if repoID == "" {
		app.renderReposPage(w, r, user, "Invalid repository id.", "")
		return
	}

	err := app.repositoryManager().Delete(r.Context(), user, repoID)
	switch {
	case err == nil:
		app.recordAuditEvent(r, user, models.AuditActionRepositoryDeleted, "repository", repoID, nil)
		app.renderReposPage(w, r, user, "", "Repository deleted.")
	case errors.Is(err, db.ErrNotFound):
		app.renderReposPage(w, r, user, "Repository not found or not accessible.", "")
	case errors.Is(err, repository.ErrActiveCI):
		app.renderReposPage(w, r, user, "Cancel or wait for queued and running CI jobs before deleting this repository.", "")
	case errors.Is(err, repository.ErrQueuedTriggers):
		app.renderReposPage(w, r, user, "Wait for queued push events to enter CI before deleting this repository.", "")
	case errors.Is(err, repository.ErrCleanup):
		app.respondWebError(w, r, apperr.Wrap(apperr.KindInternal, "Repository was deleted, but Gitman could not finish removing repository data", err))
	default:
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository deletion is temporarily unavailable", err))
	}
}

type RepoSettingsPageData struct {
	PageData
	Owner      *models.User
	Repository *models.Repository
}

func (app *App) renderRepoSettings(w http.ResponseWriter, r *http.Request, errMessage, successMessage string) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	app.renderPage(w, r, "repo_settings.html", &RepoSettingsPageData{
		PageData: PageData{Title: repo.Name + " - Settings", User: GetUser(r), Error: errMessage, Success: successMessage, RepoNav: app.repoNavData(r, "")},
		Owner:    owner, Repository: repo,
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
	app.recordAuditEvent(r, user, models.AuditActionRepositoryUpdated, "repository", repo.ID, map[string]string{
		"is_private": strconv.FormatBool(isPrivate),
	})
	app.renderRepoSettings(w, r, "", "Repository settings saved.")
}
