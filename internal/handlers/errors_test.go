package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mmrzaf/gitman/internal/apperr"
)

func TestHTTPStatusForError(t *testing.T) {
	tests := []struct {
		kind apperr.Kind
		want int
	}{
		{apperr.KindInvalid, http.StatusBadRequest},
		{apperr.KindUnauthenticated, http.StatusUnauthorized},
		{apperr.KindForbidden, http.StatusForbidden},
		{apperr.KindNotFound, http.StatusNotFound},
		{apperr.KindConflict, http.StatusConflict},
		{apperr.KindTooLarge, http.StatusRequestEntityTooLarge},
		{apperr.KindUnsupported, http.StatusUnsupportedMediaType},
		{apperr.KindMethodNotAllowed, http.StatusMethodNotAllowed},
		{apperr.KindUnavailable, http.StatusServiceUnavailable},
		{apperr.KindInternal, http.StatusInternalServerError},
	}
	for _, tt := range tests {
		if got := httpStatusForError(apperr.New(tt.kind, "test")); got != tt.want {
			t.Fatalf("kind %s status = %d, want %d", tt.kind.String(), got, tt.want)
		}
	}
}

func TestRequestIDMiddlewareSetsHeaderAndContext(t *testing.T) {
	app := &App{}
	var contextID string
	h := app.requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contextID = RequestID(r)
		w.WriteHeader(http.StatusNoContent)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	headerID := w.Header().Get(requestIDHeader)
	if headerID == "" || contextID == "" || headerID != contextID {
		t.Fatalf("header ID=%q context ID=%q", headerID, contextID)
	}
}

func TestGitHTTPUnauthorizedResponseUsesBasicChallenge(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest(http.MethodGet, "/alice/project.git/info/refs", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDContextKey, "req-test"))
	w := httptest.NewRecorder()
	app.writeGitHTTPError(w, req, apperr.New(apperr.KindUnauthenticated, "authentication required"))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Header().Get("WWW-Authenticate"); got != `Basic realm="Gitman Repository"` {
		t.Fatalf("WWW-Authenticate = %q", got)
	}
	if got := w.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestAPIErrorIncludesRequestID(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDContextKey, "req-json"))
	w := httptest.NewRecorder()
	app.writeAPIError(w, req, apperr.New(apperr.KindUnavailable, "temporarily unavailable"))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", w.Code)
	}
	var payload map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["request_id"] != "req-json" || payload["error"] != "temporarily unavailable" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestResponseSurfaceMiddlewareOverridesPathGuessing(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest(http.MethodGet, "/looks-like-web", nil)
	w := httptest.NewRecorder()
	h := responseSurfaceMiddleware(surfacePlain)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app.writeForSurface(w, r, apperr.New(apperr.KindUnavailable, "temporarily unavailable"))
	}))
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestRequestSurfaceFallbackForUnmatchedMachineRoutes(t *testing.T) {
	tests := []struct {
		path string
		want responseSurface
	}{
		{"/alice/project.git/info/refs", surfaceGitHTTP},
		{"/api/missing", surfaceAPI},
		{"/readyz", surfaceAPI},
		{"/ci-healthz", surfaceAPI},
		{"/alice/project/files/search", surfaceAPI},
		{"/alice/project/raw", surfacePlain},
		{"/alice/project/download", surfacePlain},
		{"/alice/project/ci/run-id/log", surfacePlain},
		{"/alice/project/ci/run-id/logs/download", surfacePlain},
		{"/alice/project/ci/run-id/artifacts/preview/report.txt", surfacePlain},
		{"/alice/project", surfaceWeb},
		{"/alice/raw", surfaceWeb},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		if got := requestSurface(req); got != tt.want {
			t.Fatalf("requestSurface(%q) = %d, want %d", tt.path, got, tt.want)
		}
	}
}

func TestRecovererUsesExplicitInnerSurface(t *testing.T) {
	app := &App{}
	panicHandler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})

	// Route-specific recovery sits inside the response-surface middleware so
	// the recovered request carries the declared client language.
	h := responseSurfaceMiddleware(surfacePlain)(app.recoverer(panicHandler))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/looks-like-web", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want text/plain", got)
	}
}

func TestMachineSurfaceIsEstablishedBeforeAuthentication(t *testing.T) {
	app := &App{}
	terminal := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request reached terminal handler")
	})

	for _, tt := range []struct {
		name        string
		surface     responseSurface
		path        string
		contentType string
	}{
		{name: "api", surface: surfaceAPI, path: "/alice/project/files/search", contentType: "application/json"},
		{name: "plain", surface: surfacePlain, path: "/alice/project/raw", contentType: "text/plain; charset=utf-8"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// A bearer token forces AuthMiddleware to consult app.DB. Keeping DB nil
			// deliberately panics at that boundary; the inner recoverer must still
			// answer in the endpoint's declared language.
			h := responseSurfaceMiddleware(tt.surface)(app.recoverer(app.AuthMiddleware(terminal)))
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("Authorization", "Bearer test-token")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", w.Code)
			}
			if got := w.Header().Get("Content-Type"); got != tt.contentType {
				t.Fatalf("Content-Type = %q, want %q", got, tt.contentType)
			}
		})
	}
}

func TestQuietSuccessfulAccessLogOnlySuppressesPollNoise(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodGet, "/static/css/gitman.css", true},
		{http.MethodGet, "/health", true},
		{http.MethodGet, "/owner/repo/ci/run-id/log", true},
		{http.MethodGet, "/owner/repo/ci", false},
		{http.MethodGet, "/owner/ci/tree", false},
		{http.MethodPost, "/owner/repo/ci/run-id/log", false},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(tt.method, tt.path, nil)
		if got := quietSuccessfulAccessLog(r); got != tt.want {
			t.Fatalf("quietSuccessfulAccessLog(%s %s) = %v, want %v", tt.method, tt.path, got, tt.want)
		}
	}
}
