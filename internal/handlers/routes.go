package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func SetupRouter(app *App) *chi.Mux {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(app.AuthMiddleware)
	r.Use(securityHeaders)

	// Serve embedded static files
	r.Handle("/static/*", http.StripPrefix("/static/",
		http.FileServer(app.StaticFS),
	))

	// Public routes
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

	// Protected routes
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
	r.Route("/{username}/{repo_name}.git", func(r chi.Router) {
		r.Use(app.GitHTTPAuthMiddleware)

		r.Get("/info/refs", app.HandleGitHTTP)
		r.Post("/git-upload-pack", app.HandleGitHTTP)
		r.Post("/git-receive-pack", app.HandleGitHTTP)
	})
	r.Route("/{username}/{repo_name}", func(r chi.Router) {
		r.Use(app.RepoAccessMiddleware)

		r.Get("/", app.HandleRepoTreeGET)                 // Root of default branch
		r.Get("/tree/{ref}", app.HandleRepoTreeGET)       // Root of a specific branch/commit
		r.Get("/tree/{ref}/*", app.HandleRepoTreeGET)     // Subdirectories
		r.Get("/blob/{ref}/*", app.HandleRepoBlobGET)     // View file
		r.Get("/commits/{ref}", app.HandleRepoCommitsGET) // View commit history
	})

	return r
}
