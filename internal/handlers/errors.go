package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/git"
)

const requestIDHeader = "X-Request-ID"

var requestIDFallback atomic.Uint64

type responseSurface uint8

const (
	surfaceWeb responseSurface = iota
	surfaceAPI
	surfaceGitHTTP
	surfacePlain
)

func newRequestID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	// Entropy failure must not destroy request correlation. The timestamp and
	// monotonic process-local counter are not security material; they only keep
	// fallback IDs distinct enough for operator logs.
	return fmt.Sprintf("fallback-%x-%x", time.Now().UnixNano(), requestIDFallback.Add(1))
}

func RequestID(r *http.Request) string {
	if r == nil {
		return ""
	}
	if id, ok := r.Context().Value(requestIDContextKey).(string); ok {
		return id
	}
	return ""
}

func responseStarted(w http.ResponseWriter) bool {
	wrapped, ok := w.(middleware.WrapResponseWriter)
	return ok && (wrapped.Status() != 0 || wrapped.BytesWritten() != 0)
}

func (app *App) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set(requestIDHeader, id)
		ctx := context.WithValue(r.Context(), requestIDContextKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func responseSurfaceMiddleware(surface responseSurface) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), responseSurfaceKey, surface)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func (app *App) accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		started := time.Now()
		next.ServeHTTP(wrapped, r)
		status := wrapped.Status()
		if status == 0 {
			status = http.StatusOK
		}
		slog.Info("http request",
			"request_id", RequestID(r),
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"bytes", wrapped.BytesWritten(),
			"duration", time.Since(started),
		)
	})
}

func webErrorCopy(status int) (string, string) {
	switch status {
	case http.StatusBadRequest:
		return "Bad request", "Check the request and try again."
	case http.StatusUnauthorized:
		return "Authentication required", "Sign in and try the request again."
	case http.StatusForbidden:
		return "Access denied", "Your account does not have permission to do that."
	case http.StatusNotFound:
		return "Not found", "The resource may have moved, been deleted, or may not be visible to you."
	case http.StatusConflict:
		return "Conflict", "Gitman could not apply the change because the current state has changed."
	case http.StatusRequestEntityTooLarge:
		return "Request too large", "Reduce the request size and try again."
	case http.StatusUnsupportedMediaType:
		return "Unsupported request", "Use a supported request format and try again."
	case http.StatusServiceUnavailable:
		return "Temporarily unavailable", "Try again in a moment. If this continues, check the Gitman server logs."
	case http.StatusMethodNotAllowed:
		return "Method not allowed", "This endpoint does not support that operation."
	default:
		return "Gitman could not complete this request", "Try again. If this continues, use the request ID when checking the server logs."
	}
}

func httpStatusForError(err error) int {
	switch apperr.KindOf(err) {
	case apperr.KindInvalid:
		return http.StatusBadRequest
	case apperr.KindUnauthenticated:
		return http.StatusUnauthorized
	case apperr.KindForbidden:
		return http.StatusForbidden
	case apperr.KindNotFound:
		return http.StatusNotFound
	case apperr.KindConflict:
		return http.StatusConflict
	case apperr.KindTooLarge:
		return http.StatusRequestEntityTooLarge
	case apperr.KindUnsupported:
		return http.StatusUnsupportedMediaType
	case apperr.KindMethodNotAllowed:
		return http.StatusMethodNotAllowed
	case apperr.KindUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func (app *App) logRequestError(r *http.Request, err error, status int) {
	attrs := []any{
		"request_id", RequestID(r),
		"method", r.Method,
		"path", r.URL.Path,
		"status", status,
		"kind", apperr.KindOf(err).String(),
		"error", err,
	}
	if status >= 500 {
		slog.Error("request failed", attrs...)
	} else {
		slog.Debug("request rejected", attrs...)
	}
}

func (app *App) writeWebError(w http.ResponseWriter, r *http.Request, err error) {
	status := httpStatusForError(err)
	app.renderError(w, r, &PageData{User: GetUser(r)}, apperr.PublicMessage(err), status)
}

func (app *App) respondWebError(w http.ResponseWriter, r *http.Request, err error) {
	status := httpStatusForError(err)
	app.logRequestError(r, err, status)
	app.writeWebError(w, r, err)
}

func (app *App) writeAPIError(w http.ResponseWriter, r *http.Request, err error) {
	status := httpStatusForError(err)
	noStore(w)
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":      apperr.PublicMessage(err),
		"request_id": RequestID(r),
	})
}

func (app *App) respondAPIError(w http.ResponseWriter, r *http.Request, err error) {
	status := httpStatusForError(err)
	app.logRequestError(r, err, status)
	app.writeAPIError(w, r, err)
}

func (app *App) writePlainError(w http.ResponseWriter, r *http.Request, err error) {
	status := httpStatusForError(err)
	noStore(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	http.Error(w, apperr.PublicMessage(err), status)
}

func (app *App) respondPlainError(w http.ResponseWriter, r *http.Request, err error) {
	status := httpStatusForError(err)
	app.logRequestError(r, err, status)
	app.writePlainError(w, r, err)
}

func (app *App) writeGitHTTPError(w http.ResponseWriter, r *http.Request, err error) {
	status := httpStatusForError(err)
	noStore(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Basic realm="Gitman Repository"`)
	}
	http.Error(w, apperr.PublicMessage(err), status)
}

func (app *App) respondGitHTTPError(w http.ResponseWriter, r *http.Request, err error) {
	status := httpStatusForError(err)
	app.logRequestError(r, err, status)
	app.writeGitHTTPError(w, r, err)
}

func isGitHTTPRequest(r *http.Request) bool {
	return strings.Contains(r.URL.Path, ".git/") || strings.HasSuffix(r.URL.Path, ".git")
}

// repositoryMachineSurface recognizes only Gitman's small set of repository
// endpoints whose client language is not HTML. It is a fallback for router-level
// errors such as 405 where route-specific middleware is not guaranteed to run.
func repositoryMachineSurface(path string) (responseSurface, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 {
		return surfaceWeb, false
	}
	suffix := parts[2:]
	if len(suffix) == 2 && suffix[0] == "files" && suffix[1] == "search" {
		return surfaceAPI, true
	}
	if len(suffix) == 1 && (suffix[0] == "raw" || suffix[0] == "download") {
		return surfacePlain, true
	}
	if len(suffix) >= 3 && suffix[0] == "ci" && suffix[1] != "" {
		if len(suffix) == 3 && suffix[2] == "log" {
			return surfacePlain, true
		}
		if len(suffix) == 4 && suffix[2] == "logs" && suffix[3] == "download" {
			return surfacePlain, true
		}
		if len(suffix) >= 4 && suffix[2] == "artifacts" && suffix[3] == "preview" {
			return surfacePlain, true
		}
	}
	return surfaceWeb, false
}

func requestSurface(r *http.Request) responseSurface {
	if r != nil {
		if surface, ok := r.Context().Value(responseSurfaceKey).(responseSurface); ok {
			return surface
		}
		// Unmatched routes never enter route-specific middleware. Preserve the
		// client language for their 404/recovery response as a narrow fallback.
		if isGitHTTPRequest(r) {
			return surfaceGitHTTP
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/health" || r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			return surfaceAPI
		}
		if surface, ok := repositoryMachineSurface(r.URL.Path); ok {
			return surface
		}
	}
	return surfaceWeb
}

func (app *App) writeForSurface(w http.ResponseWriter, r *http.Request, err error) {
	switch requestSurface(r) {
	case surfaceGitHTTP:
		app.writeGitHTTPError(w, r, err)
	case surfaceAPI:
		app.writeAPIError(w, r, err)
	case surfacePlain:
		app.writePlainError(w, r, err)
	default:
		app.writeWebError(w, r, err)
	}
}

func (app *App) respondForSurface(w http.ResponseWriter, r *http.Request, err error) {
	status := httpStatusForError(err)
	app.logRequestError(r, err, status)
	app.writeForSurface(w, r, err)
}

func repositoryGitError(err error, notFoundMessage string) error {
	switch {
	case errors.Is(err, git.ErrInvalidRef), errors.Is(err, git.ErrInvalidCommit):
		return apperr.Wrap(apperr.KindInvalid, "Invalid reference", err)
	case errors.Is(err, git.ErrRefNotFound):
		return apperr.Wrap(apperr.KindNotFound, "Revision not found", err)
	case errors.Is(err, git.ErrPathNotFound):
		return apperr.Wrap(apperr.KindNotFound, notFoundMessage, err)
	case errors.Is(err, git.ErrRepoEmpty):
		return apperr.Wrap(apperr.KindNotFound, "Repository is empty", err)
	default:
		return apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err)
	}
}

func formParseError(err error) error {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return apperr.Wrap(apperr.KindTooLarge, "Request body too large", err)
	}
	return apperr.Wrap(apperr.KindInvalid, "Invalid form data", err)
}

func (app *App) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err := apperr.New(apperr.KindInternal, "Gitman could not complete this request")
				slog.Error("request panic",
					"request_id", RequestID(r),
					"method", r.Method,
					"path", r.URL.Path,
					"panic", fmt.Sprint(recovered),
					"stack", string(debug.Stack()),
				)
				if !responseStarted(w) {
					app.writeForSurface(w, r, err)
				}
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// parseWebForm parses a bounded UI form without letting net/http's FormValue
// helper hide malformed or oversized request bodies as missing fields.
func (app *App) parseWebForm(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		app.respondWebError(w, r, formParseError(err))
		return false
	}
	return true
}
