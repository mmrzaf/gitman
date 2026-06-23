package handlers

import (
	"net/http"
	"strings"
)

func (app *App) renderRepoCollaboratorsPage(w http.ResponseWriter, r *http.Request, errStr, successStr string) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	currentUser := GetUser(r)
	collaborators, err := app.DB.GetCollaborators(r.Context(), repo.ID)
	if err != nil {
		app.renderError(w, r, PageData{User: currentUser}, "Failed to fetch collaborators", http.StatusInternalServerError)
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
		app.renderError(w, r, PageData{User: currentUser}, "Forbidden", http.StatusForbidden)
		return
	}

	app.renderRepoCollaboratorsPage(w, r, "", "")
}

// HandleRepoCollaboratorsAddPOST handles the form submission to add a user to a repo.
func (app *App) HandleRepoCollaboratorsAddPOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	currentUser := GetUser(r)

	if currentUser == nil {
		app.renderError(w, r, PageData{}, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if currentUser.ID != repo.OwnerID {
		app.renderRepoCollaboratorsPage(w, r, "Only the repository owner can manage collaborators.", "")
		return
	}
	if err := r.ParseForm(); err != nil {
		app.renderRepoCollaboratorsPage(w, r, "Invalid form data.", "")
		return
	}

	targetUsername := strings.TrimSpace(r.FormValue("username"))
	accessLevel := r.FormValue("access_level")
	if accessLevel != "read" && accessLevel != "write" {
		app.renderRepoCollaboratorsPage(w, r, "Invalid access level.", "")
		return
	}

	targetUser, err := app.DB.GetUserByUsername(r.Context(), targetUsername)
	if err != nil {
		app.renderRepoCollaboratorsPage(w, r, "Failed to query user.", "")
		return
	}
	if targetUser == nil {
		app.renderRepoCollaboratorsPage(w, r, "User not found.", "")
		return
	}
	if targetUser.ID == repo.OwnerID {
		app.renderRepoCollaboratorsPage(w, r, "Owner cannot be added as a collaborator.", "")
		return
	}

	if err := app.DB.AddCollaborator(r.Context(), repo.ID, targetUser.ID, accessLevel); err != nil {
		app.renderRepoCollaboratorsPage(w, r, "Failed to add collaborator (may already exist).", "")
		return
	}

	app.renderRepoCollaboratorsPage(w, r, "", "Collaborator added successfully.")
}

// HandleRepoCollaboratorsRemovePOST handles the removal of a collaborator.
func (app *App) HandleRepoCollaboratorsRemovePOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	currentUser := GetUser(r)

	if currentUser == nil {
		app.renderError(w, r, PageData{}, "Unauthorized", http.StatusUnauthorized)
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
		app.renderRepoCollaboratorsPage(w, r, "Failed to remove collaborator.", "")
		return
	}

	app.renderRepoCollaboratorsPage(w, r, "", "Collaborator removed successfully.")
}
