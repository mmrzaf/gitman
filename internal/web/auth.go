package web

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/netip"
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
	attempt, wait := a.limiter.allow(username, addr)
	if attempt == nil {
		a.log.Warn("sign-in refused by rate limit", "username", safeLogValue(username), "client", addr)
		minutes := int(math.Ceil(wait.Minutes()))
		return fail(http.StatusTooManyRequests,
			fmt.Sprintf("Too many failed attempts. Try again in %d minute%s.", minutes, plural(minutes)))
	}

	person, err := a.people.VerifyLogin(r.Context(), username, password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) || errors.Is(err, auth.ErrDisabled) {
			// The attempt was reserved before the password was checked,
			// so parallel guesses cannot all pass the limit while bcrypt
			// runs.
			a.log.Warn("failed sign-in", "username", safeLogValue(username), "client", addr)
			// A disabled person gets the same answer as a wrong password,
			// so the form never reveals which usernames exist.
			return fail(http.StatusUnauthorized, "Wrong username or password.")
		}
		return err
	}
	attempt.success()

	token, expires, err := a.people.CreateSession(r.Context(), person, sessionTTL, clientIP(r), r.UserAgent())
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

// Sessions expire after seven days; active use never postpones expiry.
const sessionTTL = 7 * 24 * time.Hour

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
type sessionIDKey struct{}

// withSession resolves the session cookie to a person. A cookie that no
// longer names a live session is cleared, so the browser stops sending it.
func (a *App) withSession(w http.ResponseWriter, r *http.Request) (*http.Request, error) {
	cookie, err := r.Cookie(a.sessionCookie())
	if err != nil || cookie.Value == "" {
		return r, nil
	}
	check, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	person, sessionID, err := a.people.SessionIdentity(check, cookie.Value)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidSession) {
			a.clearSessionCookie(w)
			return r, nil
		}
		return r, err
	}
	ctx := context.WithValue(r.Context(), personKey{}, person)
	ctx = context.WithValue(ctx, sessionTokenKey{}, cookie.Value)
	ctx = context.WithValue(ctx, sessionIDKey{}, sessionID)
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

func sessionIDFrom(r *http.Request) string {
	id, _ := r.Context().Value(sessionIDKey{}).(string)
	return id
}

// Login attempt limits. A person who mistypes gets five tries per
// fifteen minutes from one address; one address gets twenty tries across
// all usernames; and one username gets twenty tries across all addresses,
// so rotating addresses does not make guessing a single account free.
// All windows reset after fifteen minutes; a successful login refunds
// its address attempt and clears both per-username counts.
const (
	loginUsernameLimit = 5
	loginAddressLimit  = 20
	loginGlobalLimit   = 20
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

// loginReservation is an attempt already counted against every limit.
// Counting at reservation rather than after bcrypt closes the race where
// parallel guesses all pass the check before any failure is recorded.
type loginReservation struct {
	limiter  *loginLimiter
	username string
	address  string
}

func newLoginLimiter(now func() time.Time) *loginLimiter {
	return &loginLimiter{now: now, entries: make(map[string]loginAttempts)}
}

// clientKey collapses IPv6 clients to their /64, the smallest block a
// single subscriber is normally delegated: rotating addresses inside it
// must not reset the budget.
func clientKey(addr string) string {
	parsed, err := netip.ParseAddr(strings.TrimSpace(addr))
	if err != nil {
		return safeLogValue(addr)
	}
	parsed = parsed.Unmap().WithZone("")
	if parsed.Is6() {
		return netip.PrefixFrom(parsed, 64).Masked().String()
	}
	return parsed.String()
}

func normalizedUsername(username string) string {
	return safeLogValue(strings.ToLower(strings.TrimSpace(username)))
}

func usernameKey(username, addr string) string {
	return "u:" + normalizedUsername(username) + "@" + clientKey(addr)
}

func globalUsernameKey(username string) string {
	return "g:" + normalizedUsername(username)
}

func addressKey(addr string) string { return "a:" + clientKey(addr) }

// safeLogValue bounds and sanitises a name before it reaches a log line
// or a limiter key: a username is unauthenticated input, so it may carry
// control characters or be far longer than a real one.
func safeLogValue(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, value)
	if len(value) > 64 {
		value = value[:64]
	}
	return value
}

func (l *loginLimiter) keys(username, addr string) []string {
	return []string{usernameKey(username, addr), addressKey(addr), globalUsernameKey(username)}
}

// allow reserves one attempt against every limit, or reports how long
// until one may be made.
func (l *loginLimiter) allow(username, addr string) (*loginReservation, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)
	keys := l.keys(username, addr)
	limits := []int{loginUsernameLimit, loginAddressLimit, loginGlobalLimit}
	var wait time.Duration
	for i, key := range keys {
		e := l.entries[key]
		if e.count >= limits[i] {
			if remaining := loginWindow - now.Sub(e.first); remaining > wait {
				wait = remaining
			}
		}
	}
	if wait > 0 {
		return nil, wait
	}
	for _, key := range keys {
		e := l.entries[key]
		if e.count == 0 {
			e.first = now
		}
		e.count++
		e.last = now
		l.entries[key] = e
	}
	l.boundLocked()
	return &loginReservation{limiter: l, username: username, address: addr}, 0
}

// success refunds the address attempt and clears the per-username counts.
func (r *loginReservation) success() {
	l := r.limiter
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, usernameKey(r.username, r.address))
	delete(l.entries, globalUsernameKey(r.username))
	key := addressKey(r.address)
	if e, ok := l.entries[key]; ok {
		e.count--
		if e.count <= 0 {
			delete(l.entries, key)
		} else {
			l.entries[key] = e
		}
	}
}

// boundLocked keeps a flood of distinct usernames from growing memory
// without bound; dropping the least recently used entries only ever
// forgets attempts.
func (l *loginLimiter) boundLocked() {
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

func (l *loginLimiter) pruneLocked(now time.Time) {
	for key, e := range l.entries {
		if now.Sub(e.first) >= loginWindow {
			delete(l.entries, key)
		}
	}
}
