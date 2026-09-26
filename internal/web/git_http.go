// git_http.go serves Git's smart HTTP protocol: clone and fetch through
// upload-pack, push through receive-pack.
//
// Every request authenticates with HTTP Basic auth: a Gitman username and
// an access token as the password. There is no anonymous access. A read
// token can clone and fetch; pushing needs a write token, and then each
// ref the push updates is checked against the repository's ref rules by
// the pre-receive hook.

package web

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/names"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/push"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

// Request body limits. A fetch sends only its negotiation — the commits
// it wants and has — which in a repository with many refs can still run
// to many megabytes; a push carries a pack, bounded by git.MaxPushBytes,
// plus its ref commands.
const (
	maxNegotiationBytes = 256 << 20
	maxPushBodyBytes    = git.MaxPushBytes + 64<<20
)

// registerGitRoutes adds the Git routes to mux. A repository may be
// addressed with or without the ".git" suffix, since Git clients accept
// both.
func (a *App) registerGitRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{repo}/info/refs", a.infoRefs)
	mux.HandleFunc("POST /{repo}/git-upload-pack", a.gitRPC(git.UploadPack))
	mux.HandleFunc("POST /{repo}/git-receive-pack", a.gitRPC(git.ReceivePack))
}

// requiredScope is the token scope a service needs.
func requiredScope(svc git.Service) auth.Scope {
	if svc == git.ReceivePack {
		return auth.ScopeWrite
	}
	return auth.ScopeRead
}

// gitRequest is one authenticated, resolved Git request.
type gitRequest struct {
	svc git.Service
	// person is who is cloning, fetching or pushing; nil when a worker is
	// fetching a run's commit.
	person *auth.Person
	repo   *reposvc.Repo
	git    *git.Repo
}

// actor names who made a Git request, for logs.
func (req *gitRequest) actor() string {
	if req.person == nil {
		return ci.FetchUsername
	}
	return req.person.Username
}

// resolveGit authenticates the request and looks up its repository. When it
// returns false it has already written the response. refuse reports an
// authorization failure after successful authentication: as a Git
// protocol error during ref advertisement, which Git shows the person
// verbatim, or as a plain status on the RPC requests that follow it.
func (a *App) resolveGit(w http.ResponseWriter, r *http.Request, svc git.Service, refuse func(message string, status int)) (*gitRequest, bool) {
	username, secret, ok := r.BasicAuth()
	if !ok || secret == "" {
		challenge(w, "Sign in with your Gitman username and an access token as the password.")
		return nil, false
	}

	// Who is asking: a person with an access token, or a worker with the
	// fetch token of a running run, which can only read, and only the
	// run's own repository.
	var person *auth.Person
	var runRepoID string
	if username == ci.FetchUsername {
		repoID, err := a.ci.AuthenticateFetch(r.Context(), secret)
		switch {
		case errors.Is(err, ci.ErrInvalidFetchToken):
			challenge(w, "Authentication failed: the run's fetch token is invalid or its run has finished.")
			return nil, false
		case err != nil:
			a.log.Error("git fetch-token auth failed", "error", err)
			http.Error(w, "Internal error.", http.StatusInternalServerError)
			return nil, false
		case svc != git.UploadPack:
			refuse("A run's fetch token can only fetch.", http.StatusForbidden)
			return nil, false
		}
		runRepoID = repoID
	} else {
		p, scope, err := a.people.Authenticate(r.Context(), secret)
		switch {
		case errors.Is(err, auth.ErrInvalidToken):
			challenge(w, "Authentication failed: the access token is invalid, expired, or belongs to a disabled person.")
			return nil, false
		case err != nil:
			a.log.Error("git auth failed", "error", err)
			http.Error(w, "Internal error.", http.StatusInternalServerError)
			return nil, false
		case !strings.EqualFold(username, p.Username):
			challenge(w, "Authentication failed: the access token belongs to a different person.")
			return nil, false
		case !scope.Satisfies(requiredScope(svc)):
			refuse("This access token can only read. Pushing needs a token with write access.", http.StatusForbidden)
			return nil, false
		}
		person = p
	}

	name := strings.TrimSuffix(r.PathValue("repo"), ".git")
	if names.ValidateRepository(name) != nil {
		refuse("Repository not found.", http.StatusNotFound)
		return nil, false
	}
	repoRecord, err := a.repos.GetByName(r.Context(), name)
	if errors.Is(err, postgres.ErrNotFound) || (err == nil && person == nil && repoRecord.ID != runRepoID) {
		refuse("Repository not found.", http.StatusNotFound)
		return nil, false
	}
	if err != nil {
		a.log.Error("git repository lookup failed", "repo", name, "error", err)
		http.Error(w, "Internal error.", http.StatusInternalServerError)
		return nil, false
	}
	gitRepo, err := a.repos.Open(repoRecord)
	if err != nil {
		a.log.Error("git repository open failed", "repo", name, "error", err)
		http.Error(w, "Internal error.", http.StatusInternalServerError)
		return nil, false
	}
	return &gitRequest{svc: svc, person: person, repo: repoRecord, git: gitRepo}, true
}

func (a *App) transportOptions(r *http.Request, req *gitRequest) (git.TransportOptions, bool) {
	protocol := r.Header.Get("Git-Protocol")
	if !git.ValidProtocolHeader(protocol) {
		return git.TransportOptions{}, false
	}
	opts := git.TransportOptions{Service: req.svc, Protocol: protocol}
	if req.svc == git.ReceivePack {
		opts.HooksPath = a.cfg.HooksPath()
		opts.Env = push.Env(a.cfg, req.repo.ID, req.person.ID, clientIP(r))
	}
	return opts, true
}

func (a *App) infoRefs(w http.ResponseWriter, r *http.Request) {
	svc, ok := git.ParseService(r.URL.Query().Get("service"))
	if !ok {
		http.Error(w, "Gitman only speaks Git's smart HTTP protocol; upgrade your Git client.", http.StatusForbidden)
		return
	}
	req, ok := a.resolveGit(w, r, svc, func(message string, _ int) { gitError(w, svc, message) })
	if !ok {
		return
	}
	opts, ok := a.transportOptions(r, req)
	if !ok {
		http.Error(w, "Invalid Git-Protocol header.", http.StatusBadRequest)
		return
	}

	noCache(w)
	w.Header().Set("Content-Type", "application/x-"+string(svc)+"-advertisement")
	if err := req.git.AdvertiseRefs(r.Context(), opts, w); err != nil {
		a.log.Warn("git advertisement failed", "repo", req.repo.Name, "service", svc, "error", err)
	}
}

func (a *App) gitRPC(svc git.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/x-"+string(svc)+"-request" {
			http.Error(w, "Unexpected content type.", http.StatusUnsupportedMediaType)
			return
		}
		req, ok := a.resolveGit(w, r, svc, func(message string, status int) { http.Error(w, message, status) })
		if !ok {
			return
		}
		opts, ok := a.transportOptions(r, req)
		if !ok {
			http.Error(w, "Invalid Git-Protocol header.", http.StatusBadRequest)
			return
		}

		limit := int64(maxNegotiationBytes)
		if svc == git.ReceivePack {
			limit = maxPushBodyBytes
		}
		var body io.Reader = http.MaxBytesReader(w, r.Body, limit)
		switch r.Header.Get("Content-Encoding") {
		case "":
		case "gzip":
			gz, err := gzip.NewReader(body)
			if err != nil {
				http.Error(w, "Invalid gzip request body.", http.StatusBadRequest)
				return
			}
			defer func() { _ = gz.Close() }()
			// The limit applies to the decompressed stream too, so a
			// small compressed body cannot expand without bound.
			body = io.LimitReader(gz, limit)
		default:
			http.Error(w, "Unsupported content encoding.", http.StatusUnsupportedMediaType)
			return
		}

		// A clone or push of a large repository legitimately outlasts
		// any server-wide deadline, so this request gets its own.
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(time.Hour))
		_ = rc.SetWriteDeadline(time.Now().Add(time.Hour))

		noCache(w)
		w.Header().Set("Content-Type", "application/x-"+string(svc)+"-result")
		start := time.Now()
		err := req.git.ServeRPC(r.Context(), opts, body, w)
		attrs := []any{"repo", req.repo.Name, "service", svc, "actor", req.actor(), "duration", time.Since(start).Round(time.Millisecond)}
		if err != nil {
			a.log.Warn("git request failed", append(attrs, "error", err)...)
			return
		}
		if svc == git.ReceivePack {
			a.log.Info("git push", attrs...)
		}
	}
}

// challenge asks the client for credentials. Git responds to a 401 with
// WWW-Authenticate by consulting its credential helper or prompting.
func challenge(w http.ResponseWriter, message string) {
	w.Header().Set("WWW-Authenticate", `Basic realm="Gitman", charset="UTF-8"`)
	http.Error(w, message, http.StatusUnauthorized)
}

// gitError reports an error Git shows to the person verbatim, as
// "remote error: <message>". A plain HTTP error status would reach them
// only as a bare status code.
func gitError(w http.ResponseWriter, svc git.Service, message string) {
	noCache(w)
	w.Header().Set("Content-Type", "application/x-"+string(svc)+"-advertisement")
	fmt.Fprint(w, git.PktLine("# service="+string(svc)+"\n")+"0000"+git.PktLine("ERR "+message+"\n"))
}

func noCache(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "Fri, 01 Jan 1980 00:00:00 GMT")
}
