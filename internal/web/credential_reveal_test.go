package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestRevealIsSessionBoundOneTimeAndExpires(t *testing.T) {
	now := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	store := newRevealStore(func() time.Time { return now })
	store.put("admin-session", revealPerson, "sara", "secret-password")
	store.put("admin-session", revealToken, "", "secret-token")
	if _, ok := store.take("other-session", revealPerson); ok {
		t.Fatal("other session read a reveal")
	}
	first, ok := store.take("admin-session", revealPerson)
	if !ok || first.username != "sara" || first.value != "secret-password" {
		t.Fatalf("reveal = %+v, %v", first, ok)
	}
	if _, ok := store.take("admin-session", revealPerson); ok {
		t.Fatal("refresh repeated a reveal")
	}
	if token, ok := store.take("admin-session", revealToken); !ok || token.value != "secret-token" {
		t.Fatal("person reveal discarded the token reveal")
	}
	store.put("admin-session", revealPerson, "sara", "another-secret")
	now = now.Add(revealTTL)
	if _, ok := store.take("admin-session", revealPerson); ok {
		t.Fatal("expired credential revealed")
	}
}

func TestRevealConcurrentRequestsConsumeOnce(t *testing.T) {
	store := newRevealStore(time.Now)
	store.put("session", revealToken, "", "only-once")
	var wg sync.WaitGroup
	results := make(chan bool, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, ok := store.take("session", revealToken); results <- ok }()
	}
	wg.Wait()
	close(results)
	shown := 0
	for ok := range results {
		if ok {
			shown++
		}
	}
	if shown != 1 {
		t.Fatalf("reveal consumed %d times", shown)
	}
}

func TestCrossSiteNavigationCannotConsumeReveal(t *testing.T) {
	app := &App{reveals: newRevealStore(time.Now)}
	app.reveals.put("session", revealPerson, "sara", "pass")
	req := httptest.NewRequest(http.MethodGet, "/people", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req = req.WithContext(context.WithValue(req.Context(), sessionIDKey{}, "session"))
	// Use the actual owner session: the origin check must be exercised,
	// not merely an absent-session lookup.
	rec := httptest.NewRecorder()
	if _, ok := app.consumeCredential(rec, req, revealPerson); ok {
		t.Fatal("cross-site consumed reveal")
	}
	valid := httptest.NewRequest(http.MethodGet, "/people", nil)
	valid = valid.WithContext(context.WithValue(valid.Context(), sessionIDKey{}, "session"))
	if item, ok := app.consumeCredential(httptest.NewRecorder(), valid, revealPerson); !ok || item.value != "pass" {
		t.Fatal("same-origin navigation could not consume the preserved reveal")
	}
}
