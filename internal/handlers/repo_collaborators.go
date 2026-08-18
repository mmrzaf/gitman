package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
)

func (app *App) renderRepoCollaboratorsPage(w http.ResponseWriter, r *http.Request, errStr, successStr string) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	currentUser := GetUser(r)
	collaborators, err := app.DB.GetCollaborators(r.Context(), repo.ID)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Collaborator data is temporarily unavailable", err))
		return
	}

	app.renderPage(w, r, "repo_collaborators.html", PageData{
		Title:   repo.Name + " - Collaborators",
		User:    currentUser,
		Error:   errStr,
		Success: successStr,
		Data: RepoPageData{
			Owner:         owner,
			Repository:    repo,
			Collaborators: collaborators,
		},
	})
}

// HandleRepoCollaboratorsGET renders the repository's collaborators view.
func (app *App) HandleRepoCollaboratorsGET(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	currentUser := GetUser(r)
	if currentUser == nil || currentUser.ID != repo.OwnerID {
		app.respondWebError(w, r, apperr.New(apperr.KindForbidden, "Forbidden"))
		return
	}

	app.renderRepoCollaboratorsPage(w, r, "", "")
}

// HandleRepoCollaboratorsAddPOST handles the form submission to add a user to a repo.
func (app *App) HandleRepoCollaboratorsAddPOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	currentUser := GetUser(r)

	if currentUser == nil {
		app.respondWebError(w, r, apperr.New(apperr.KindUnauthenticated, "Authentication required"))
		return
	}
	if currentUser.ID != repo.OwnerID {
		app.renderRepoCollaboratorsPage(w, r, "Only the repository owner can manage collaborators.", "")
		return
	}
	if !app.parseWebForm(w, r) {
		return
	}

	targetUsername := strings.TrimSpace(r.FormValue("username"))
	accessLevel := models.AccessLevel(r.FormValue("access_level"))
	if !accessLevel.Valid() {
		app.renderRepoCollaboratorsPage(w, r, "Invalid access level.", "")
		return
	}

	targetUser, err := app.DB.GetUserByUsername(r.Context(), targetUsername)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.renderRepoCollaboratorsPage(w, r, "User not found.", "")
		} else {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "User data is temporarily unavailable", err))
		}
		return
	}
	if targetUser.ID == repo.OwnerID {
		app.renderRepoCollaboratorsPage(w, r, "Owner cannot be added as a collaborator.", "")
		return
	}

	if err := app.DB.AddCollaborator(r.Context(), repo.ID, targetUser.ID, accessLevel); err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Collaborator data is temporarily unavailable", err))
		return
	}

	app.renderRepoCollaboratorsPage(w, r, "", "Collaborator added successfully.")
}

// HandleRepoCollaboratorsRemovePOST handles the removal of a collaborator.
func (app *App) HandleRepoCollaboratorsRemovePOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	currentUser := GetUser(r)

	if currentUser == nil {
		app.respondWebError(w, r, apperr.New(apperr.KindUnauthenticated, "Authentication required"))
		return
	}
	if currentUser.ID != repo.OwnerID {
		app.renderRepoCollaboratorsPage(w, r, "Forbidden.", "")
		return
	}

	targetUserID := r.URL.Query().Get("user_id")
	if targetUserID == "" {
		app.renderRepoCollaboratorsPage(w, r, "Invalid user ID.", "")
		return
	}

	if err := app.DB.RemoveCollaborator(r.Context(), repo.ID, targetUserID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.renderRepoCollaboratorsPage(w, r, "Collaborator not found.", "")
		} else {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Collaborator data is temporarily unavailable", err))
		}
		return
	}

	app.renderRepoCollaboratorsPage(w, r, "", "Collaborator removed successfully.")
}
