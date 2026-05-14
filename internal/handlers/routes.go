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
	r.Use(middleware.RedirectSlashes)
	r.Use(app.AuthMiddleware)
	r.Use(app.CSRFMiddleware)
	r.Use(securityHeaders)

	r.Handle("/static/*", http.StripPrefix("/static/",
		http.FileServer(app.StaticFS),
	))

	// Public pages
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		app.renderPage(w, r, "home.html", PageData{
			User: GetUser(r),
			Data: struct{ Page string }{Page: "home"},
		})
	})

	r.Get("/health", app.HandleHealth)

	r.Get("/login", app.HandleLoginGET)
	r.Post("/login", app.HandleLoginPOST)
	r.Get("/register", app.HandleRegisterGET)
	r.Post("/register", app.HandleRegisterPOST)
	r.Get("/logout", app.HandleLogout)

	// ── Authenticated user routes (keys, tokens, repos list) ────────────────
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

	// ── Git Smart HTTP routes (must be above web UI routes) ─────────────────
	r.Route("/{username}/{repo_name}.git", func(r chi.Router) {
		r.Use(app.GitHTTPAuthMiddleware)

		r.Get("/info/refs", app.HandleGitHTTP)
		r.Post("/git-upload-pack", app.HandleGitHTTP)
		r.Post("/git-receive-pack", app.HandleGitHTTP)
	})

	// ── Artifact download API (Bearer-token auth via global AuthMiddleware) ──
	r.Route("/api/repos/{username}/{repo_name}", func(r chi.Router) {
		r.Use(app.RequireAuth)
		r.Use(app.RepoAccessMiddleware)

		r.Get("/artifacts/latest/branch/{branch_name}/{filename}",
			app.HandleArtifactByBranch)
		r.Get("/artifacts/tag/{tag_name}/{filename}",
			app.HandleArtifactByTag)
		r.Get("/artifacts/commit/{commit_hash}/{filename}",
			app.HandleArtifactByCommit)
	})

	// ── Web interface for repositories ──────────────────────────────────────
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

		// Collaborators
		r.Get("/collaborators", app.HandleRepoCollaboratorsGET)
		r.Post("/collaborators/add", app.HandleRepoCollaboratorsAddPOST)
		r.Post("/collaborators/remove", app.HandleRepoCollaboratorsRemovePOST)

		// CI/CD
		r.Get("/ci", app.HandleCIGET)

		// Trigger: open to any authenticated user with repo access (auth already
		r.Post("/ci/trigger", app.HandleCITriggerPOST)
		r.Post("/ci/webhook", app.WebhookAuthMiddleware(http.HandlerFunc(app.HandleCITriggerWebhook)).ServeHTTP)
		// Individual run + live log polling
		r.Get("/ci/{run_id}", app.HandleCIRunGET)
		r.Get("/ci/{run_id}/log", app.HandleCIRunLogGET)

		// Secrets management
		r.Get("/ci/secrets", app.HandleCISecretsGET)
		r.Post("/ci/secrets", app.HandleCISecretsAddPOST)
		r.Post("/ci/secrets/{id}/delete", app.HandleCISecretsDeletePOST)

		// Hook install / uninstall
		r.Post("/ci/hook/install", app.HandleCIHookInstallPOST)
		r.Post("/ci/hook/uninstall", app.HandleCIHookUninstallPOST)
	})

	return r
}
