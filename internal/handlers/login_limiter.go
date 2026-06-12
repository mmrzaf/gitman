package handlers

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	loginUsernameLimit  = 5
	loginIPLimit        = 20
	loginLimitWindow    = 15 * time.Minute
	loginLimiterMaxKeys = 4096
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
		{"u:" + normalizeLoginUsername(username), loginUsernameLimit},
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
	l.incrementLocked("u:"+normalizeLoginUsername(username), now)
	l.incrementLocked("ip:"+ip, now)
	if len(l.entries) > loginLimiterMaxKeys {
		l.dropOldestLocked()
	}
}

func (l *loginLimiter) recordSuccess(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, "u:"+normalizeLoginUsername(username))
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
	return strings.ToLower(strings.TrimSpace(username))
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

func firstForwardedFor(value string) string {
	for _, part := range strings.Split(value, ";") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(strings.ToLower(part), "for=") {
			continue
		}
		raw := strings.Trim(strings.TrimSpace(part[4:]), `"`)
		if strings.HasPrefix(raw, "[") {
			if host, _, err := net.SplitHostPort(raw); err == nil {
				raw = strings.Trim(host, "[]")
			}
		}
		return parseIPOnly(raw)
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
