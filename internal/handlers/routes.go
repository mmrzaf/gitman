package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func SetupRouter(app *App) *chi.Mux {
	r := chi.NewRouter()

	// Global middleware
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RedirectSlashes)
	r.Use(app.AuthMiddleware)
	r.Use(securityHeaders)

	// Static files
	r.Handle("/static/*", http.StripPrefix("/static/",
		http.FileServer(app.StaticFS),
	))

	// Public pages
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		app.renderPage(w, "home.html", PageData{
			User: GetUser(r),
			Data: struct{ Page string }{Page: "home"},
		})
	})

	r.Get("/login", app.HandleLoginGET)
	r.Post("/login", app.HandleLoginPOST)
	r.Get("/register", app.HandleRegisterGET)
	r.Post("/register", app.HandleRegisterPOST)
	r.Get("/logout", app.HandleLogout)

	// Authenticated user routes (keys, tokens, repos list)
	r.Group(func(r chi.Router) {
		r.Use(app.RequireAuth)

		r.Get("/keys", app.HandleKeysGET)
		r.Post("/keys", app.HandleKeysPOST)
		r.Post("/keys/{id}/delete", app.HandleKeyDeletePOST)

		r.Get("/tokens", app.HandleTokensGET)
		r.Post("/tokens", app.HandleTokensPOST)
		r.Post("/tokens/{id}/delete", app.HandleTokenDeletePOST)

		r.Get("/repos", app.HandleReposGET)
		r.Post("/repos", app.HandleReposPOST)
		r.Post("/repos/{id}/delete", app.HandleRepoDeletePOST)
	})

	// Git (smart HTTP) routes
	// Must remain ABOVE repo UI routes
	r.Route("/{username}/{repo_name}.git", func(r chi.Router) {
		r.Use(app.GitHTTPAuthMiddleware)

		r.Get("/info/refs", app.HandleGitHTTP)
		r.Post("/git-upload-pack", app.HandleGitHTTP)
		r.Post("/git-receive-pack", app.HandleGitHTTP)
	})

	// Web interface for repositories
	r.Route("/{username}/{repo_name}", func(r chi.Router) {
		r.Use(app.RepoAccessMiddleware)

		// Repo root tree
		r.Get("/", app.HandleRepoTreeGET)

		// Navigation & browsing
		r.Get("/tree/{ref}", app.HandleRepoTreeGET)
		r.Get("/tree/{ref}/*", app.HandleRepoTreeGET)

		r.Get("/blob/{ref}/*", app.HandleRepoBlobGET)

		// Commits
		r.Get("/commits/{ref}", app.HandleRepoCommitsGET)

		// Archive download
		r.Get("/archive/{filename}", app.HandleRepoArchiveGET)

		// Collaborators — fixes 404
		r.Get("/collaborators", app.HandleRepoCollaboratorsGET)
		r.Post("/collaborators/add", app.HandleRepoCollaboratorsAddPOST)
		r.Post("/collaborators/remove", app.HandleRepoCollaboratorsRemovePOST)
	})

	return r
}
