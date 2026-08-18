package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func SetupRouter(app *App) *chi.Mux {
	r := chi.NewRouter()

	// Global middlewares (apply to every request)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RedirectSlashes)
	r.Use(app.securityHeaders) // safe for all requests

	// Static files do not require session resolution.
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(app.StaticFS)))

	// Health probes intentionally bypass session and CSRF work. Liveness must
	// remain independent of the database; readiness performs the dependency
	// checks explicitly in its handler.
	r.Get("/health", app.HandleHealth)
	r.Get("/healthz", app.HandleLiveness)
	r.Get("/readyz", app.HandleReadiness)

	// ── Git Smart HTTP routes (NO CSRF) ─────────────────────────────
	r.Route("/{username}/{repo_name}.git", func(r chi.Router) {
		r.Use(app.GitHTTPAuthMiddleware)
		r.Get("/info/refs", app.HandleGitHTTP)
		r.Post("/git-upload-pack", app.HandleGitHTTP)
		r.Post("/git-receive-pack", app.HandleGitHTTP)
	})

	// ── Artifact download API (requires auth, no CSRF) ──────────────
	r.Route("/api/repos/{username}/{repo_name}", func(r chi.Router) {
		r.Use(app.AuthMiddleware)
		r.Use(app.RequireAPIAuth)
		r.Use(app.RepoAccessMiddleware)
		r.Use(app.RequireRepoMember)
		r.Get("/artifacts/latest/branch/*", app.HandleArtifactByBranch)
		r.Get("/artifacts/tag/*", app.HandleArtifactByTag)
		r.Get("/artifacts/commit/{commit_hash}/*", app.HandleArtifactByCommit)
		r.Get("/artifacts/run/{run_id}/*", app.HandleArtifactByRunID)
	})

	// ── Web UI and internal API (with Auth + CSRF) ──────────────────
	r.Group(func(r chi.Router) {
		r.Use(app.AuthMiddleware)
		r.Use(limitRequestBody(maxUIRequestBodyBytes))
		r.Use(app.CSRFMiddleware)

		// Public pages (login, register, home)
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			app.renderPage(w, r, "home.html", PageData{
				User: GetUser(r),
				Data: struct{ Page string }{Page: "home"},
			})
		})
		r.Get("/login", app.HandleLoginGET)
		r.Post("/login", app.HandleLoginPOST)
		if app.Config != nil && app.Config.AllowRegister {
			r.Get("/register", app.HandleRegisterGET)
			r.Post("/register", app.HandleRegisterPOST)
		}
		r.Post("/logout", app.HandleLogout)

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

		// Web interface for repositories (all require auth + CSRF)
		r.Route("/{username}/{repo_name}", func(r chi.Router) {
			r.Use(app.RepoAccessMiddleware)

			r.Get("/", app.HandleRepoTreeGET)
			r.Get("/tree", app.HandleRepoTreeGET)
			r.Get("/blob", app.HandleRepoBlobGET)
			r.Get("/raw", app.HandleRepoBlobRawGET)
			r.Get("/download", app.HandleRepoBlobDownloadGET)
			r.Get("/files/search", app.HandleRepoFileSearchGET)
			r.Get("/commits", app.HandleRepoCommitsGET)
			r.Get("/commit/{commit_hash}", app.HandleRepoCommitGET)
			r.Get("/archive/{format}", app.HandleRepoArchiveGET)

			// Legacy path-based routes remain during the beta transition.
			r.Get("/tree/{ref}", app.HandleRepoTreeGET)
			r.Get("/tree/{ref}/*", app.HandleRepoTreeGET)
			r.Get("/blob/{ref}/*", app.HandleRepoBlobGET)
			r.Get("/commits/{ref}", app.HandleRepoCommitsGET)
			r.Get("/archive/*", app.HandleRepoArchiveGET)

			r.Get("/settings", app.HandleRepoSettingsGET)
			r.Post("/settings", app.HandleRepoSettingsPOST)

			// Collaborators
			r.Get("/collaborators", app.HandleRepoCollaboratorsGET)
			r.Post("/collaborators/add", app.HandleRepoCollaboratorsAddPOST)
			r.Post("/collaborators/remove", app.HandleRepoCollaboratorsRemovePOST)

			// CI output and artifacts may contain sensitive build data. Public source
			// browsing does not imply public CI visibility.
			r.Group(func(r chi.Router) {
				r.Use(app.RequireRepoMember)
				r.Get("/ci", app.HandleCIGET)
				r.Post("/ci/trigger", app.HandleCITriggerPOST)
				r.Post("/ci/rules", app.HandleCISettingsRulePOST)
				r.Post("/ci/rules/delete", app.HandleCISettingsRuleDeletePOST)
				r.Get("/ci/{run_id}", app.HandleCIRunGET)
				r.Post("/ci/{run_id}/cancel", app.HandleCIRunCancelPOST)
				r.Post("/ci/{run_id}/retry", app.HandleCIRunRetryPOST)
				r.Get("/ci/{run_id}/log", app.HandleCIRunLogGET)
				r.Get("/ci/{run_id}/logs/download", app.HandleCIRunLogsDownloadGET)
				r.Get("/ci/{run_id}/artifacts/preview/*", app.HandleCIRunArtifactPreviewGET)

				// Secrets
				r.Get("/ci/secrets", app.HandleCISecretsGET)
				r.Post("/ci/secrets", app.HandleCISecretsAddPOST)
				r.Post("/ci/secrets/{id}/delete", app.HandleCISecretsDeletePOST)

				// Hook install / uninstall
				r.Post("/ci/hook/install", app.HandleCIHookInstallPOST)
				r.Post("/ci/hook/uninstall", app.HandleCIHookUninstallPOST)
			})
		})
	})

	// ── Webhook endpoint (must be outside CSRF, uses its own auth) ──
	r.Post("/repos/{username}/{repo_name}/ci/webhook", app.WebhookAuthMiddleware(http.HandlerFunc(app.HandleCITriggerWebhook)).ServeHTTP)

	return r
}
