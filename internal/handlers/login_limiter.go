package handlers

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	loginUsernameIPLimit = 5
	loginIPLimit         = 20
	loginLimitWindow     = 15 * time.Minute
	loginLimiterMaxKeys  = 4096
)

type loginLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]loginLimitEntry
}

type loginLimitEntry struct {
	Count int
	First time.Time
	Last  time.Time
}

func newLoginLimiter(now func() time.Time) *loginLimiter {
	if now == nil {
		now = time.Now
	}
	return &loginLimiter{now: now, entries: make(map[string]loginLimitEntry)}
}

func (l *loginLimiter) allow(username, ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)
	blocked, retry := false, time.Duration(0)
	for _, scope := range []struct {
		key   string
		limit int
	}{
		{loginUsernameIPKey(username, ip), loginUsernameIPLimit},
		{"ip:" + ip, loginIPLimit},
	} {
		entry := l.entries[scope.key]
		if entry.Count >= scope.limit && now.Sub(entry.First) < loginLimitWindow {
			blocked = true
			if wait := loginLimitWindow - now.Sub(entry.First); wait > retry {
				retry = wait
			}
		}
	}
	return !blocked, retry
}

func (l *loginLimiter) recordFailure(username, ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)
	l.incrementLocked(loginUsernameIPKey(username, ip), now)
	l.incrementLocked("ip:"+ip, now)
	if len(l.entries) > loginLimiterMaxKeys {
		l.dropOldestLocked()
	}
}

func (l *loginLimiter) recordSuccess(username, ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, loginUsernameIPKey(username, ip))
}

func loginUsernameIPKey(username, ip string) string {
	return "uip:" + normalizeLoginUsername(username) + ":" + ip
}

func (l *loginLimiter) incrementLocked(key string, now time.Time) {
	entry := l.entries[key]
	if entry.Count == 0 || now.Sub(entry.First) >= loginLimitWindow {
		entry = loginLimitEntry{First: now}
	}
	entry.Count++
	entry.Last = now
	l.entries[key] = entry
}

func (l *loginLimiter) pruneLocked(now time.Time) {
	for key, entry := range l.entries {
		if now.Sub(entry.First) >= loginLimitWindow {
			delete(l.entries, key)
		}
	}
}

func (l *loginLimiter) dropOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for key, entry := range l.entries {
		if oldestKey == "" || entry.Last.Before(oldest) {
			oldestKey = key
			oldest = entry.Last
		}
	}
	if oldestKey != "" {
		delete(l.entries, oldestKey)
	}
}

func normalizeLoginUsername(username string) string {
	username = strings.ToLower(strings.TrimSpace(username))
	// Registered usernames are at most 32 ASCII characters. Bound untrusted
	// login input before using it as an in-memory limiter key so an oversized
	// credential cannot turn the limiter into an oversized allocation store.
	if len(username) > 64 {
		username = username[:64]
	}
	return username
}

func (app *App) clientIP(r *http.Request) string {
	if app != nil && app.Config != nil && app.Config.TrustProxyHeaders {
		if ip := firstForwardedFor(r.Header.Get("Forwarded")); ip != "" {
			return ip
		}
		if ip := firstForwardedIP(r.Header.Get("X-Forwarded-For")); ip != "" {
			return ip
		}
		if ip := parseIPOnly(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	if ip := parseIPOnly(r.RemoteAddr); ip != "" {
		return ip
	}
	return "unknown"
}

func forwardedParam(value, name string) string {
	first, _, _ := strings.Cut(value, ",")
	for _, part := range strings.Split(first, ";") {
		key, raw, ok := strings.Cut(part, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), name) {
			continue
		}
		return strings.Trim(strings.TrimSpace(raw), `"`)
	}
	return ""
}

func firstForwardedFor(value string) string {
	raw := forwardedParam(value, "for")
	if raw == "" {
		return ""
	}
	if ip := parseIPOnly(raw); ip != "" {
		return ip
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		return parseIPOnly(strings.Trim(host, "[]"))
	}
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		return parseIPOnly(strings.Trim(raw, "[]"))
	}
	return ""
}

func firstForwardedIP(value string) string {
	first, _, _ := strings.Cut(value, ",")
	return parseIPOnly(strings.TrimSpace(first))
}

func parseIPOnly(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.Trim(value, "[]")
	ip := net.ParseIP(value)
	if ip == nil {
		return ""
	}
	return ip.String()
}
