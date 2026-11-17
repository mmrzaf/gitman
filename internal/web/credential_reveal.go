package web

import (
	"net/http"
	"sync"
	"time"
)

// A generated credential lives only briefly in this web process, bound
// to the signed-in browser session. It is never written to a cookie,
// redirect URL, log, or database as plaintext. The next GET consumes it
// once; refreshing that page cannot recreate a password or access token.
// A web-process restart may discard an unrevealed value, in which case
// the admin can reset the password or the person can mint a new token.
const (
	revealTTL    = 15 * time.Minute
	revealPerson = "person"
	revealToken  = "token"
)

type pendingReveal struct {
	kind, username, value string
	created               time.Time
}

type revealStore struct {
	mu        sync.Mutex
	bySession map[string]map[string]pendingReveal
	now       func() time.Time
}

func newRevealStore(now func() time.Time) *revealStore {
	return &revealStore{bySession: make(map[string]map[string]pendingReveal), now: now}
}

func (s *revealStore) prune(now time.Time) {
	for session, byKind := range s.bySession {
		for kind, item := range byKind {
			if !now.Before(item.created.Add(revealTTL)) {
				delete(byKind, kind)
			}
		}
		if len(byKind) == 0 {
			delete(s.bySession, session)
		}
	}
}

// put replaces a pending reveal of the same kind for this session.
// Expired pending reveals are discarded whenever the store is accessed.
func (s *revealStore) put(sessionID, kind, username, value string) {
	if sessionID == "" || value == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.prune(now)
	if s.bySession[sessionID] == nil {
		s.bySession[sessionID] = make(map[string]pendingReveal)
	}
	s.bySession[sessionID][kind] = pendingReveal{kind: kind, username: username, value: value, created: now}
}

func (s *revealStore) take(sessionID, kind string) (pendingReveal, bool) {
	if sessionID == "" {
		return pendingReveal{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(s.now())
	item, ok := s.bySession[sessionID][kind]
	if ok {
		delete(s.bySession[sessionID], kind)
		if len(s.bySession[sessionID]) == 0 {
			delete(s.bySession, sessionID)
		}
	}
	return item, ok
}

// showCredential records a one-time reveal and redirects with HTTP 303.
// The secret is not sent on the POST response, nor in the redirect URL.
func (a *App) showCredential(w http.ResponseWriter, r *http.Request, kind, username, secret, target string) {
	a.reveals.put(sessionIDFrom(r), kind, username, secret)
	noStore(w)
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// consumeCredential is only called by the destination page. Cross-site
// links must not be able to consume the pending reveal without showing it.
func (a *App) consumeCredential(w http.ResponseWriter, r *http.Request, kind string) (pendingReveal, bool) {
	if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
		return pendingReveal{}, false
	}
	item, ok := a.reveals.take(sessionIDFrom(r), kind)
	if ok {
		noStore(w)
	}
	return item, ok
}
