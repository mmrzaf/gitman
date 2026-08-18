package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/mmrzaf/gitman/internal/apperr"
)

func SetupRouter(app *App) *chi.Mux {
	r := chi.NewRouter()

	// Global middlewares (apply to every request). Request IDs and surface-aware
	// recovery are Gitman-owned so browser, API, and Git clients get appropriate
	// failure responses.
	r.Use(app.requestIDMiddleware)
	r.Use(app.accessLogMiddleware)
	r.Use(app.recoverer)
	r.Use(middleware.RedirectSlashes)
	r.Use(app.securityHeaders) // safe for all requests
	r.NotFound(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app.respondForSurface(w, r, apperr.New(apperr.KindNotFound, "Not found"))
	}))
	r.MethodNotAllowed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app.respondForSurface(w, r, apperr.New(apperr.KindMethodNotAllowed, "Method not allowed"))
	}))

	// Static files do not require session resolution.
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(app.StaticFS)))

	// Health probes intentionally bypass session and CSRF work. Liveness must
	// remain independent of the database; readiness performs the dependency
	// checks explicitly in its handler.
	r.With(responseSurfaceMiddleware(surfaceAPI), app.recoverer).Get("/health", app.HandleHealth)
	r.With(responseSurfaceMiddleware(surfaceAPI), app.recoverer).Get("/healthz", app.HandleLiveness)
	r.With(responseSurfaceMiddleware(surfaceAPI), app.recoverer).Get("/readyz", app.HandleReadiness)

	// ── Git Smart HTTP routes (NO CSRF) ─────────────────────────────
	r.Route("/{username}/{repo_name}.git", func(r chi.Router) {
		r.Use(responseSurfaceMiddleware(surfaceGitHTTP))
		r.Use(app.recoverer)
		r.With(app.GitHTTPInfoRefsAuthMiddleware).Get("/info/refs", app.HandleGitHTTP)
		r.With(app.GitHTTPAuthMiddleware(gitHTTPRead)).Post("/git-upload-pack", app.HandleGitHTTP)
		r.With(app.GitHTTPAuthMiddleware(gitHTTPWrite)).Post("/git-receive-pack", app.HandleGitHTTP)
	})

	// ── Artifact download API (requires auth, no CSRF) ──────────────
	r.Route("/api/repos/{username}/{repo_name}", func(r chi.Router) {
		r.Use(responseSurfaceMiddleware(surfaceAPI))
		r.Use(app.recoverer)
		r.Use(app.AuthMiddleware)
		r.Use(app.RequireAPIAuth)
		r.Use(app.RepoAccessMiddleware)
		r.Use(app.RequireRepoMember)
		r.Get("/artifacts/latest/branch/*", app.HandleArtifactByBranch)
		r.Get("/artifacts/tag/*", app.HandleArtifactByTag)
		r.Get("/artifacts/commit/{commit_hash}/*", app.HandleArtifactByCommit)
		r.Get("/artifacts/run/{run_id}/*", app.HandleArtifactByRunID)
	})

	// ── Repository routes ─────────────────────────────────────────
	// One repository mount contains three explicit response stacks. Machine
	// endpoints establish their response language before authentication and
	// repository resolution; browser endpoints establish the Web surface before
	// the same dependencies. Keeping one mount avoids dispatch depending on the
	// router's behavior for duplicate wildcard mounts.
	r.Route("/{username}/{repo_name}", func(r chi.Router) {
		// Machine-readable source lookup.
		r.Group(func(r chi.Router) {
			r.Use(responseSurfaceMiddleware(surfaceAPI))
			r.Use(app.recoverer)
			r.Use(app.AuthMiddleware)
			r.Use(app.RepoAccessMiddleware)
			r.Get("/files/search", app.HandleRepoFileSearchGET)
		})

		// Raw/download/log surfaces intentionally return plain text or file data,
		// including when authentication or repository resolution fails.
		r.Group(func(r chi.Router) {
			r.Use(responseSurfaceMiddleware(surfacePlain))
			r.Use(app.recoverer)
			r.Use(app.AuthMiddleware)
			r.Use(app.RepoAccessMiddleware)
			r.Get("/raw", app.HandleRepoBlobRawGET)
			r.Get("/download", app.HandleRepoBlobDownloadGET)
			r.Group(func(r chi.Router) {
				r.Use(app.RequireRepoMember)
				r.Get("/ci/{run_id}/log", app.HandleCIRunLogGET)
				r.Get("/ci/{run_id}/logs/download", app.HandleCIRunLogsDownloadGET)
				r.Get("/ci/{run_id}/artifacts/preview/*", app.HandleCIRunArtifactPreviewGET)
			})
		})

		// Browser repository UI.
		r.Group(func(r chi.Router) {
			r.Use(responseSurfaceMiddleware(surfaceWeb))
			r.Use(app.recoverer)
			r.Use(app.AuthMiddleware)
			r.Use(app.limitRequestBody(maxUIRequestBodyBytes))
			r.Use(app.CSRFMiddleware)
			r.Use(app.RepoAccessMiddleware)

			r.Get("/", app.HandleRepoTreeGET)
			r.Get("/tree", app.HandleRepoTreeGET)
			r.Get("/blob", app.HandleRepoBlobGET)
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

	// ── Web UI (with Auth + CSRF) ──────────────────────────────────
	r.Group(func(r chi.Router) {
		r.Use(responseSurfaceMiddleware(surfaceWeb))
		r.Use(app.recoverer)
		r.Use(app.AuthMiddleware)
		r.Use(app.limitRequestBody(maxUIRequestBodyBytes))
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

	})

	// ── Webhook endpoint (must be outside CSRF, uses its own auth) ──
	r.With(responseSurfaceMiddleware(surfaceAPI), app.recoverer).Post("/repos/{username}/{repo_name}/ci/webhook", app.WebhookAuthMiddleware(http.HandlerFunc(app.HandleCITriggerWebhook)).ServeHTTP)

	return r
}
