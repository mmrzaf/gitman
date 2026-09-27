package web

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/apperr"
)

// contentSecurityPolicy allows the page's own scripts, styles and images
// and nothing else: no inline script, no third-party origin, no framing.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func setSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
}

// sameOrigin reports whether a request may change state. Safe methods
// always may. For the rest, browsers say where a request came from — in
// Sec-Fetch-Site, or failing that in Origin — and a request from any
// other site is refused. That, with SameSite=Lax session cookies, is the
// whole cross-site request forgery defense: there are no hidden form
// tokens to get wrong. A request carrying neither header does not come
// from a browser, and without a browser there is no forgery to prevent.
func (a *App) sameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
	default:
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return parsed.Scheme+"://"+parsed.Host == a.origin
}

// maxFormBytes bounds every form submission. Gitman's forms carry names,
// descriptions and settings, never files.
const maxFormBytes = 64 << 10

// parseForm reads a url-encoded form with a size limit.
func parseForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	err := r.ParseForm()
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		return apperr.Wrap(apperr.KindTooLarge, "The form is larger than Gitman accepts.", err)
	case err != nil:
		return apperr.Wrap(apperr.KindInvalid, "The form could not be read.", err)
	}
	return nil
}

// Client address resolution. Behind a reverse proxy the connection's
// peer is the proxy, so the client is taken from X-Forwarded-For — but
// only from a peer that is one of the instance's configured trusted
// proxies, since any client can send that header.

func isTrustedProxy(trusted []netip.Prefix, addr netip.Addr) bool {
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// resolveClientIP returns the client address of r. Walking
// X-Forwarded-For from the right, it skips trusted proxies and returns
// the first address that is not one: everything to the left of that was
// written by the client and cannot be believed.
func (a *App) resolveClientIP(r *http.Request) string {
	peer, ok := peerAddr(r)
	if !ok {
		return r.RemoteAddr
	}
	if !isTrustedProxy(a.cfg.TrustedProxies, peer) {
		return peer.String()
	}
	var hops []string
	for _, header := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(header, ",")...)
	}
	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		client = addr.Unmap()
		if !isTrustedProxy(a.cfg.TrustedProxies, client) {
			break
		}
	}
	return client.String()
}

func peerAddr(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

type clientIPKey struct{}

// clientIPMiddleware resolves each request's client address once and
// stores it for clientIP to read back.
func (a *App) clientIPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), clientIPKey{}, a.resolveClientIP(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// clientIP returns the client address stored by clientIPMiddleware, or
// the connection's peer address when the request did not pass through
// it.
func clientIP(r *http.Request) string {
	if ip, ok := r.Context().Value(clientIPKey{}).(string); ok {
		return ip
	}
	if addr, ok := peerAddr(r); ok {
		return addr.String()
	}
	return r.RemoteAddr
}

// statusRecorder captures the status code a handler wrote, and passes
// Unwrap through so streaming and ResponseController keep working
// behind it.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += int64(n)
	return n, err
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// logRequests logs one line per request, at a level that reflects the
// response status, with the resolved client address.
func (a *App) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		level := slog.LevelDebug
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400 && status != http.StatusUnauthorized:
			level = slog.LevelInfo
		}
		a.log.Log(r.Context(), level, "request",
			"method", r.Method, "path", r.URL.Path, "status", status, "client", clientIP(r),
			"bytes", rec.bytes, "duration", time.Since(start).Round(time.Millisecond))
	})
}

// recoverPanics turns a panic in a handler into a logged error and a
// generic 500, instead of taking the process down.
func (a *App) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil || v == http.ErrAbortHandler {
				if v != nil {
					panic(v)
				}
				return
			}
			a.log.Error("panic serving request", "method", r.Method, "path", r.URL.Path,
				"panic", v, "stack", strings.TrimSpace(string(debug.Stack())))
			http.Error(w, "Internal error.", http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}
