package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
)

func TestHealthAndReadiness(t *testing.T) {
	database := pgtest.Open(t)
	store := git.NewStore(t.TempDir())
	defer store.Close()
	cfg := &config.Config{Retention: config.DefaultRetention(), DataDir: t.TempDir(), PublicURL: "http://localhost:8080", Port: 8080}
	app, err := New(cfg, testServices(database, store, cfg.SecretKey), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.handler)
	defer srv.Close()

	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s = %d", path, resp.StatusCode)
		}
	}
}

func TestRecoverPanics(t *testing.T) {
	a := &App{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := a.recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}
