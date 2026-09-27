// Package web is Gitman's user interface: server-rendered HTML pages,
// forms that redirect after every successful POST, and a few small
// scripts that only ever enhance a page that already works without them.
package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/postgres"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

// App is Gitman's web process: the HTTP server carrying the Git smart
// HTTP transport, health checks and the web interface. It reaches data
// only through the domain services, never the database directly.
type App struct {
	cfg      *config.Config
	people   *auth.Service
	repos    *reposvc.Service
	ci       *ci.Service
	activity *activity.Service
	ping     func(context.Context) error
	listen   ListenFunc
	hub      *hub
	log      *slog.Logger
	views    *views
	assets   *assets
	limiter  *loginLimiter
	handler  http.Handler
	// gitSlots bounds how many Git HTTP requests run at once.
	gitSlots chan struct{}
	// origin is the scheme and host of the public URL, which every
	// state-changing request must come from.
	origin string
	secure bool
	now    func() time.Time
	// pageReadTimeout and pageWriteTimeout bound reading one page
	// request and writing its response, so a client cannot hold a
	// connection open by sending or reading slowly. The server itself has
	// no such timeouts, because Git transfers run for as long as they take.
	pageReadTimeout  time.Duration
	pageWriteTimeout time.Duration
}

// Default page deadlines.
const (
	pageReadTimeout  = 30 * time.Second
	pageWriteTimeout = 2 * time.Minute
)

// Services are what the web process works with.
type Services struct {
	People   *auth.Service
	Repos    *reposvc.Service
	CI       *ci.Service
	Activity *activity.Service
	// Ping reports whether the database is reachable, for /readyz.
	Ping func(context.Context) error
	// Listen delivers database notifications, for live pages.
	Listen ListenFunc
}

// ListenFunc delivers PostgreSQL notifications on channels until ctx
// ends, reporting each lost connection to failed before it reconnects;
// postgres.DB.Listen is the implementation.
type ListenFunc func(ctx context.Context, channels []string, notify func(channel, payload string), failed func(error))

// New builds the App and its full route table. It fails if the web
// interface's templates or assets are malformed, so a broken template
// stops the process at startup instead of on the page that uses it.
func New(cfg *config.Config, services Services, log *slog.Logger) (*App, error) {
	public, err := url.Parse(cfg.PublicURL)
	if err != nil {
		return nil, fmt.Errorf("parse public URL: %w", err)
	}
	a := &App{
		cfg:      cfg,
		people:   services.People,
		repos:    services.Repos,
		ci:       services.CI,
		activity: services.Activity,
		ping:     services.Ping,
		listen:   services.Listen,
		hub:      newHub(),
		log:      log,
		limiter:  newLoginLimiter(time.Now),
		gitSlots: make(chan struct{}, gitConcurrencyLimit),
		origin:   public.Scheme + "://" + public.Host,
		secure:   public.Scheme == "https",
		now:      time.Now,

		pageReadTimeout:  pageReadTimeout,
		pageWriteTimeout: pageWriteTimeout,
	}
	if a.assets, err = loadAssets(); err != nil {
		return nil, err
	}
	if a.views, err = loadViews(a.assets); err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	a.register(mux)
	a.handler = a.recoverPanics(a.clientIPMiddleware(a.logRequests(a.routeFilesAtRef(mux))))
	return a, nil
}

// access is who may open a page.
type access int

const (
	// anyone may open the page, signed in or not: only the login page.
	anyone access = iota
	// member pages need any signed-in person.
	member
	// admin pages need a signed-in admin.
	admin
)

// handler is a page handler. Returning an error renders the matching
// error page; handlers only write a response themselves on success.
type handler func(w http.ResponseWriter, r *http.Request) error

// page wraps a handler with what every page needs: deadlines, security
// headers, the cross-origin check for state-changing requests, the
// signed-in person, the pending flash message, and the access check.
func (a *App) page(level access, h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(a.pageReadTimeout))
		_ = rc.SetWriteDeadline(time.Now().Add(a.pageWriteTimeout))
		setSecurityHeaders(w.Header())

		if !a.sameOrigin(r) {
			a.renderError(w, r, apperr.New(apperr.KindForbidden,
				"This request came from another site, so it was not carried out."))
			return
		}

		r, err := a.withSession(w, r)
		if err != nil {
			a.renderError(w, r, err)
			return
		}
		r = a.withFlash(w, r)

		person := personFrom(r)
		switch {
		case level >= member && person == nil:
			a.requireSignIn(w, r)
			return
		case level == admin && !person.IsAdmin:
			message := "Only admins can open this page."
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				message = "Only admins can do that."
			}
			a.renderError(w, r, apperr.New(apperr.KindForbidden, message))
			return
		}

		if err := h(w, r); err != nil {
			a.renderError(w, r, err)
		}
	})
}

// requireSignIn sends a signed-out visitor to the login page. A page
// they were opening is returned to after signing in; an action they were
// submitting is not replayed, since the form's contents cannot survive
// the detour.
func (a *App) requireSignIn(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	a.redirect(w, r, "/login", flashInfo, "Sign in to continue.")
}

// statusFor maps an error kind to its HTTP status.
func statusFor(kind apperr.Kind) int {
	switch kind {
	case apperr.KindInvalid:
		return http.StatusUnprocessableEntity
	case apperr.KindForbidden:
		return http.StatusForbidden
	case apperr.KindNotFound:
		return http.StatusNotFound
	case apperr.KindConflict:
		return http.StatusConflict
	case apperr.KindTooLarge:
		return http.StatusRequestEntityTooLarge
	case apperr.KindUnavailable:
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

var errorTitles = map[int]string{
	http.StatusNotFound:            "Not found",
	http.StatusForbidden:           "Not allowed",
	http.StatusServiceUnavailable:  "Too busy",
	http.StatusInternalServerError: "Something went wrong",
}

// renderError shows an error page. Only errors built with apperr carry a
// message meant for people; anything else is logged in full and shown as
// a generic failure, so internal details never reach the page.
func (a *App) renderError(w http.ResponseWriter, r *http.Request, err error) {
	status := statusFor(apperr.KindOf(err))
	message := apperr.PublicMessage(err)
	if errors.Is(err, postgres.ErrNotFound) {
		status = http.StatusNotFound
	}
	if errors.Is(err, postgres.ErrUnavailable) {
		status = http.StatusServiceUnavailable
	}
	// One check for 503 regardless of how it was reached — apperr.New
	// with KindUnavailable directly, or postgres.ErrUnavailable above —
	// so neither path can drift from the other's Retry-After and message.
	if status == http.StatusServiceUnavailable {
		message = "Gitman is too busy right now. Try again in a moment."
		w.Header().Set("Retry-After", "5")
	}
	if status == http.StatusInternalServerError {
		a.log.Error("page failed", "method", r.Method, "path", r.URL.Path, "error", err)
		message = "The server could not finish this request. It has been logged."
	}
	title := errorTitles[status]
	if title == "" {
		title = http.StatusText(status)
	}
	a.render(w, r, status, "error", title, errorPage{Status: status, Message: message})
}

type errorPage struct {
	Status  int
	Message string
}
