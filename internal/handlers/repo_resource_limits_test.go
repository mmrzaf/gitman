package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
)

func TestRepoBrowseLimitsRejectsWhenFull(t *testing.T) {
	app := &App{Config: &config.Config{RepoBrowseMaxConcurrent: 1, RepoBrowseMaxConcurrentPerIP: 1, RepoBrowseTimeout: time.Second}}
	release, ok := app.repoBrowseConcurrencyLimiter().tryAcquire("192.0.2.10")
	if !ok {
		t.Fatal("expected initial browse slot")
	}
	defer release()

	h := app.RepoBrowseLimits(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("busy browse request reached handler")
	}))
	req := httptest.NewRequest(http.MethodGet, "/alice/repo/tree", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
}

func TestRepoBrowseLimitsAppliesDeadline(t *testing.T) {
	app := &App{Config: &config.Config{RepoBrowseMaxConcurrent: 1, RepoBrowseMaxConcurrentPerIP: 1, RepoBrowseTimeout: 20 * time.Millisecond}}
	var ctxErr error
	h := app.RepoBrowseLimits(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		ctxErr = r.Context().Err()
	}))
	req := httptest.NewRequest(http.MethodGet, "/alice/repo/tree", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !errors.Is(ctxErr, context.DeadlineExceeded) {
		t.Fatalf("context error=%v, want deadline exceeded", ctxErr)
	}
}

func TestBeginRepoStreamRejectsWhenFull(t *testing.T) {
	app := &App{Config: &config.Config{RepoStreamMaxConcurrent: 1, RepoStreamMaxConcurrentPerIP: 1, RepoStreamTimeout: time.Second}}
	release, ok := app.repoStreamConcurrencyLimiter().tryAcquire("192.0.2.20")
	if !ok {
		t.Fatal("expected initial stream slot")
	}
	defer release()

	req := httptest.NewRequest(http.MethodGet, "/alice/repo/archive/zip", nil)
	req.RemoteAddr = "192.0.2.20:1234"
	w := httptest.NewRecorder()
	ctx, cleanup, ok := app.beginRepoStream(w, req)
	if ok || ctx != nil || cleanup != nil {
		t.Fatal("busy stream unexpectedly acquired")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", w.Code)
	}
}

func TestBeginRepoStreamAppliesDeadline(t *testing.T) {
	app := &App{Config: &config.Config{RepoStreamMaxConcurrent: 1, RepoStreamMaxConcurrentPerIP: 1, RepoStreamTimeout: 20 * time.Millisecond}}
	req := httptest.NewRequest(http.MethodGet, "/alice/repo/archive/zip", nil)
	req.RemoteAddr = "192.0.2.20:1234"
	w := httptest.NewRecorder()
	ctx, cleanup, ok := app.beginRepoStream(w, req)
	if !ok {
		t.Fatal("stream slot was unexpectedly rejected")
	}
	defer cleanup()
	<-ctx.Done()
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("context error=%v, want deadline exceeded", ctx.Err())
	}
}
