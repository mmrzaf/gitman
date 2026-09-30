package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/mmrzaf/gitman/internal/apperr"
)

// register adds every route to mux: health checks, the Git smart HTTP
// transport, and the web interface's pages.
func (a *App) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /readyz", a.ready)

	a.registerGitRoutes(mux)

	// /assets/static/{file} is three segments on purpose: a two-segment
	// /assets/{file} would collide with any future /{repo}/<word> page
	// route (Go's router treats "GET /{repo}/settings" and
	// "GET /assets/{file}" as genuinely ambiguous, since each has a
	// wildcard and a literal in opposite positions of the same shape).
	// Three segments can never collide with a two-segment repo route.
	mux.Handle("GET /assets/static/{file}", http.HandlerFunc(a.serveAsset))
	mux.HandleFunc("GET /favicon.ico", a.serveFavicon)

	mux.Handle("GET /login", a.page(anyone, a.loginForm))
	mux.Handle("POST /login", a.page(anyone, a.login))
	// Anyone may sign out: a browser whose session has already ended is
	// told it is signed out, not asked to sign in first.
	mux.Handle("POST /logout", a.page(anyone, a.logout))
	mux.Handle("GET /{$}", a.page(member, a.home))
	mux.Handle("POST /repos", a.page(member, a.repoCreate))

	mux.Handle("GET /jump", a.page(member, a.jump))
	mux.Handle("GET /me", a.page(member, a.meView))
	mux.Handle("POST /me/password", a.page(member, a.mePasswordChange))
	mux.Handle("POST /me/tokens", a.page(member, a.meCreateToken))
	mux.Handle("POST /me/tokens/{id}/delete", a.page(member, a.meDeleteToken))

	mux.Handle("GET /people", a.page(admin, a.peopleView))
	mux.Handle("POST /people", a.page(admin, a.peopleAdd))
	mux.Handle("POST /people/{username}/disable", a.page(admin, a.peopleDisable))
	mux.Handle("POST /people/{username}/enable", a.page(admin, a.peopleEnable))
	mux.Handle("POST /people/{username}/role", a.page(admin, a.peopleRole))
	mux.Handle("POST /people/{username}/reset-password", a.page(admin, a.peopleResetPassword))

	mux.Handle("GET /{repo}", a.page(member, a.repository))
	mux.Handle("GET /events", a.page(member, a.events))

	mux.Handle("POST /{repo}/refs/delete", a.page(member, a.refDelete))
	mux.Handle("GET /{repo}/commit/{sha}", a.page(member, a.commitView))
	mux.Handle("GET /{repo}/commits", a.page(member, a.commits))
	mux.Handle("GET /{repo}/activity", a.page(member, a.activityView))
	mux.Handle("GET /{repo}/archive/{ref...}", a.page(member, a.archive))
	mux.Handle("GET /{repo}/runs", a.page(member, a.runs))
	mux.Handle("POST /{repo}/runs", a.page(member, a.runRef))
	mux.Handle("GET /{repo}/runs/{n}", a.page(member, a.runView))
	mux.Handle("GET /{repo}/runs/{n}/log", a.page(member, a.runLog))
	mux.Handle("POST /{repo}/runs/{n}/cancel", a.page(member, a.runCancel))
	mux.Handle("POST /{repo}/runs/{n}/again", a.page(member, a.runAgain))
	mux.Handle("GET /{repo}/compare", a.page(member, a.compareRedirect))
	mux.Handle("GET /{repo}/compare/{crange...}", a.page(member, a.compareRedirect))
	mux.Handle("GET /{repo}/tree-paths", a.page(member, a.treePaths))
	// Settings routes are registered at member, not admin, level: the
	// admin requirement is checked inside each handler, after resolving
	// the repository, so a restricted repository a non-admin cannot read
	// stays a 404 rather than announcing itself with a 403. A member who
	// can read the repository but isn't an admin still gets the same 403
	// as before.
	mux.Handle("GET /{repo}/settings", a.page(member, a.repoSettings))
	mux.Handle("POST /{repo}/settings/description", a.page(member, a.repoSettingsDescription))
	mux.Handle("POST /{repo}/settings/default-branch", a.page(member, a.repoSettingsDefaultBranch))
	mux.Handle("POST /{repo}/settings/rules", a.page(member, a.repoSettingsRuleSet))
	mux.Handle("POST /{repo}/settings/rules/delete", a.page(member, a.repoSettingsRuleDelete))
	mux.Handle("POST /{repo}/settings/secrets", a.page(member, a.repoSettingsSecretSet))
	mux.Handle("POST /{repo}/settings/secrets/delete", a.page(member, a.repoSettingsSecretDelete))
	mux.Handle("POST /{repo}/settings/access", a.page(member, a.repoSettingsAccess))
	mux.Handle("POST /{repo}/settings/delete", a.page(member, a.repoSettingsDelete))

	// Every other path is a page that does not exist, rendered inside the
	// frame so the person keeps their navigation.
	mux.Handle("/", a.page(member, func(w http.ResponseWriter, r *http.Request) error {
		return apperr.New(apperr.KindNotFound, "There is nothing at this address.")
	}))
}

// filesAtRef reports whether a request addresses files at a ref:
// /waiotech@develop/server/app.py. Its first path segment carries "@",
// which a repository name never contains, so the rule is unambiguous.
func filesAtRef(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	return strings.Contains(first, "@")
}

// routeFilesAtRef sends requests for files at a ref to the file browser
// before the router sees them. Matched by the router instead, a file
// path would collide with every page under a repository: a directory
// named "runs" or "settings" would open that page, not the directory.
func (a *App) routeFilesAtRef(next http.Handler) http.Handler {
	files := a.page(member, func(w http.ResponseWriter, r *http.Request) error {
		first, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		name, refAndPath, _ := splitRepoRef(first)
		if rest = strings.Trim(rest, "/"); rest != "" {
			refAndPath += "/" + rest
		}
		return a.files(w, r, name, refAndPath)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filesAtRef(r) {
			files.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
