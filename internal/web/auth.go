package web

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mmrzaf/gitman/internal/auth"
)

type loginPage struct {
	Form *form
	Next string
	Host string
}

// safeNext keeps a return-to address on this site: a path, never a URL
// that could send a person somewhere else after signing in.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || strings.HasPrefix(u.Path, "/login") {
		return "/"
	}
	return u.RequestURI()
}

func (a *App) loginPageData(f *form, next string) loginPage {
	host := a.origin
	if u, err := url.Parse(a.origin); err == nil {
		host = u.Host
	}
	return loginPage{Form: f, Next: safeNext(next), Host: host}
}

func (a *App) loginForm(w http.ResponseWriter, r *http.Request) error {
	next := r.URL.Query().Get("next")
	if personFrom(r) != nil {
		http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
		return nil
	}
	a.render(w, r, http.StatusOK, "login", "Sign in", a.loginPageData(newForm(nil), next))
	return nil
}

func (a *App) login(w http.ResponseWriter, r *http.Request) error {
	if err := parseForm(w, r); err != nil {
		return err
	}
	f := newForm(r.PostForm)
	next := f.Get("next")
	username := f.Get("username")
	password := r.PostForm.Get("password")
	addr := clientIP(r)

	fail := func(status int, message string) error {
		f.Error = message
		a.render(w, r, status, "login", "Sign in", a.loginPageData(f, next))
		return nil
	}

	if username == "" || password == "" {
		if username == "" {
			f.Fail("username", "Enter your username.")
		}
		if password == "" {
			f.Fail("password", "Enter your password.")
		}
		a.render(w, r, http.StatusUnprocessableEntity, "login", "Sign in", a.loginPageData(f, next))
		return nil
	}
	if ok, wait := a.limiter.allow(username, addr); !ok {
		a.log.Warn("sign-in refused by rate limit", "username", username, "client", addr)
		minutes := int(math.Ceil(wait.Minutes()))
		return fail(http.StatusTooManyRequests,
			fmt.Sprintf("Too many failed attempts. Try again in %d minute%s.", minutes, plural(minutes)))
	}

	person, err := a.people.VerifyLogin(r.Context(), username, password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) || errors.Is(err, auth.ErrDisabled) {
			a.limiter.failure(username, addr)
			a.log.Warn("failed sign-in", "username", username, "client", addr)
			// A disabled person gets the same answer as a wrong password,
			// so the form never reveals which usernames exist.
			return fail(http.StatusUnauthorized, "Wrong username or password.")
		}
		return err
	}
	a.limiter.success(username, addr)

	token, expires, err := a.people.CreateSession(r.Context(), person.ID, sessionTTL)
	if err != nil {
		return err
	}
	a.setSessionCookie(w, token, expires)
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
	return nil
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) error {
	if token := sessionTokenFrom(r); token != "" {
		if err := a.people.DeleteSession(r.Context(), token); err != nil {
			return err
		}
	}
	a.clearSessionCookie(w)
	a.redirect(w, r, "/login", flashInfo, "Signed out.")
	return nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Sessions last a month and are renewed whenever a person uses Gitman in
// their last week, so someone who opens it daily never has to sign in
// again, and a forgotten browser stops working on its own.
const (
	sessionTTL          = 30 * 24 * time.Hour
	sessionExtendWithin = 7 * 24 * time.Hour
)

// cookieName is the name of one of Gitman's cookies. Over HTTPS the
// __Host- prefix makes browsers refuse the cookie unless it is Secure,
// host-only and scoped to "/", so no sibling subdomain can plant or
// shadow it.
func (a *App) cookieName(name string) string {
	if a.secure {
		return "__Host-" + name
	}
	return name
}

func (a *App) sessionCookie() string { return a.cookieName("gitman_session") }

func (a *App) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.sessionCookie(),
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *App) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.sessionCookie(),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

type personKey struct{}
type sessionTokenKey struct{}

// withSession resolves the session cookie to a person. A cookie that no
// longer names a live session is cleared, so the browser stops sending it.
func (a *App) withSession(w http.ResponseWriter, r *http.Request) (*http.Request, error) {
	cookie, err := r.Cookie(a.sessionCookie())
	if err != nil || cookie.Value == "" {
		return r, nil
	}
	person, err := a.people.SessionPerson(r.Context(), cookie.Value)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidSession) {
			a.clearSessionCookie(w)
			return r, nil
		}
		return r, err
	}
	extended, err := a.people.ExtendSession(r.Context(), cookie.Value, sessionTTL, sessionExtendWithin)
	if err != nil {
		return r, err
	}
	if extended {
		a.setSessionCookie(w, cookie.Value, a.now().Add(sessionTTL))
	}
	ctx := context.WithValue(r.Context(), personKey{}, person)
	ctx = context.WithValue(ctx, sessionTokenKey{}, cookie.Value)
	return r.WithContext(ctx), nil
}

// personFrom returns the signed-in person, or nil.
func personFrom(r *http.Request) *auth.Person {
	p, _ := r.Context().Value(personKey{}).(*auth.Person)
	return p
}

func sessionTokenFrom(r *http.Request) string {
	token, _ := r.Context().Value(sessionTokenKey{}).(string)
	return token
}

// Login attempt limits. A person who mistypes gets five tries per
// fifteen minutes from one address; one address gets twenty tries across
// all usernames. Both reset after the window; a successful login clears
// the per-username count.
const (
	loginUsernameLimit = 5
	loginAddressLimit  = 20
	loginWindow        = 15 * time.Minute
	loginLimiterKeys   = 4096
)

type loginLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]loginAttempts
}

type loginAttempts struct {
	count int
	first time.Time
	last  time.Time
}

func newLoginLimiter(now func() time.Time) *loginLimiter {
	return &loginLimiter{now: now, entries: make(map[string]loginAttempts)}
}

func usernameKey(username, addr string) string {
	username = strings.ToLower(strings.TrimSpace(username))
	if len(username) > 64 {
		username = username[:64]
	}
	return "u:" + username + "@" + addr
}

// allow reports whether another attempt may be made, and if not, how long
// until one may.
func (l *loginLimiter) allow(username, addr string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)
	var wait time.Duration
	for key, limit := range map[string]int{usernameKey(username, addr): loginUsernameLimit, "a:" + addr: loginAddressLimit} {
		e := l.entries[key]
		if e.count >= limit {
			if remaining := loginWindow - now.Sub(e.first); remaining > wait {
				wait = remaining
			}
		}
	}
	return wait == 0, wait
}

func (l *loginLimiter) failure(username, addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)
	for _, key := range []string{usernameKey(username, addr), "a:" + addr} {
		e := l.entries[key]
		if e.count == 0 {
			e.first = now
		}
		e.count++
		e.last = now
		l.entries[key] = e
	}
	// A flood of distinct usernames must not grow memory without bound;
	// dropping the least recently used entries only ever forgets attempts.
	for len(l.entries) > loginLimiterKeys {
		var oldestKey string
		var oldest time.Time
		for key, e := range l.entries {
			if oldestKey == "" || e.last.Before(oldest) {
				oldestKey, oldest = key, e.last
			}
		}
		delete(l.entries, oldestKey)
	}
}

func (l *loginLimiter) success(username, addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, usernameKey(username, addr))
}

func (l *loginLimiter) pruneLocked(now time.Time) {
	for key, e := range l.entries {
		if now.Sub(e.first) >= loginWindow {
			delete(l.entries, key)
		}
	}
}
