package handlers

import (
	"net/http"
)

// HandleRepoCollaboratorsGET renders the repository's collaborators view.
func (app *App) HandleRepoCollaboratorsGET(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	ctx := r.Context()

	data := RepoPageData{
		Owner:      owner,
		Repository: repo,
	}

	currentUser := GetUser(r)
	if currentUser == nil || currentUser.ID != repo.OwnerID {
		app.renderError(w, PageData{User: currentUser}, "Forbidden", http.StatusForbidden)
		return
	}

	collaborators, err := app.DB.GetCollaborators(ctx, repo.ID)
	if err != nil {
		app.renderError(w, PageData{User: currentUser}, "Failed to fetch collaborators", http.StatusInternalServerError)
		return
	}
	data.Collaborators = collaborators

	app.renderPage(w, "repo_collaborators.html", PageData{
		Title: repo.Name + " - Collaborators",
		User:  currentUser,
		Data:  data,
	})
}

// HandleRepoCollaboratorsAddPOST handles the form submission to add a user to a repo.
func (app *App) HandleRepoCollaboratorsAddPOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	currentUser := GetUser(r)

	data := RepoPageData{
		Owner:      owner,
		Repository: repo,
	}

	// Helper function to re-render the partial for HTMX
	renderPanel := func(errStr, successStr string) {
		cols, _ := app.DB.GetCollaborators(r.Context(), repo.ID)
		data.Collaborators = cols
		app.renderPartial(w, "repo_collaborators.html", "collaborators_panel", PageData{
			User:    currentUser,
			Error:   errStr,
			Success: successStr,
			Data:    data,
		})
	}

	if currentUser.ID != repo.OwnerID {
		renderPanel("Only the repository owner can manage collaborators.", "")
		return
	}

	if err := r.ParseForm(); err != nil {
		renderPanel("Invalid form data.", "")
		return
	}

	targetUsername := r.FormValue("username")
	accessLevel := r.FormValue("access_level")

	if accessLevel != "read" && accessLevel != "write" {
		renderPanel("Invalid access level.", "")
		return
	}

	targetUser, err := app.DB.GetUserByUsername(r.Context(), targetUsername)
	if err != nil {
		renderPanel("Failed to query user.", "")
		return
	}
	if targetUser == nil {
		renderPanel("User not found.", "")
		return
	}

	if targetUser.ID == repo.OwnerID {
		renderPanel("Owner cannot be added as a collaborator.", "")
		return
	}

	err = app.DB.AddCollaborator(r.Context(), repo.ID, targetUser.ID, accessLevel)
	if err != nil {
		renderPanel("Failed to add collaborator (may already exist).", "")
		return
	}

	renderPanel("", "Collaborator added successfully.")
}

// HandleRepoCollaboratorsRemovePOST handles the removal of a collaborator.
func (app *App) HandleRepoCollaboratorsRemovePOST(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	currentUser := GetUser(r)

	data := RepoPageData{
		Owner:      owner,
		Repository: repo,
	}

	// Helper function to re-render the partial for HTMX
	renderPanel := func(errStr, successStr string) {
		cols, _ := app.DB.GetCollaborators(r.Context(), repo.ID)
		data.Collaborators = cols
		app.renderPartial(w, "repo_collaborators.html", "collaborators_panel", PageData{
			User:    currentUser,
			Error:   errStr,
			Success: successStr,
			Data:    data,
		})
	}

	if currentUser.ID != repo.OwnerID {
		renderPanel("Forbidden", "")
		return
	}

	targetUserID := r.URL.Query().Get("user_id")
	if targetUserID == "" {
		renderPanel("Invalid user ID.", "")
		return
	}

	err := app.DB.RemoveCollaborator(r.Context(), repo.ID, targetUserID)
	if err != nil {
		renderPanel("Failed to remove collaborator.", "")
		return
	}

	renderPanel("", "Collaborator removed successfully.")
}
