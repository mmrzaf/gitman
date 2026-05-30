package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmrzaf/gitman"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
)

type contextKey string

const (
	userContextKey      contextKey = "user"
	repoContextKey      contextKey = "repo"
	repoPathContextKey  contextKey = "repoPath"
	repoOwnerContextKey contextKey = "repoOwner"
	csrfTokenKey        contextKey = "csrfToken"
)

var embeddedFiles = gitman.FS

type App struct {
	Config    *config.Config
	DB        *db.DB
	Templates map[string]*template.Template
	StaticFS  http.FileSystem
}

type PageData struct {
	Title     string
	User      *models.User
	Config    *config.Config
	Error     string
	Success   string
	Data      any
	CSRFToken string
}

func (app *App) requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if app == nil || app.Config == nil || !app.Config.TrustProxyHeaders {
		return false
	}
	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	forwarded := strings.ToLower(r.Header.Get("Forwarded"))
	return strings.Contains(forwarded, "proto=https")
}

func (app *App) secureCookie(r *http.Request) bool {
	if app != nil && app.Config != nil && app.Config.ForceSecureCookies {
		return true
	}
	return app.requestIsHTTPS(r)
}

func (app *App) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    "",
		MaxAge:   -1,
		Path:     "/",
		HttpOnly: true,
		Secure:   app.secureCookie(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func LoadTemplates() (map[string]*template.Template, error) {
	templates := make(map[string]*template.Template)

	pages, err := fs.Glob(embeddedFiles, "templates/pages/*.html")
	if err != nil {
		return nil, err
	}

	for _, page := range pages {
		name := filepath.Base(page)

		t, err := template.ParseFS(
			embeddedFiles,
			"templates/base.html",
			"templates/partials/*.html",
			page,
		)
		if err != nil {
			return nil, err
		}

		templates[name] = t
	}

	return templates, nil
}

func NewStaticFS() (http.FileSystem, error) {
	sub, err := fs.Sub(embeddedFiles, "static")
	if err != nil {
		return nil, err
	}
	return http.FS(sub), nil
}

func (app *App) renderTemplate(w http.ResponseWriter, tmplMapKey string, executeName string, data PageData) error {
	data.Config = app.Config

	t, ok := app.Templates[tmplMapKey]
	if !ok {
		return fs.ErrNotExist
	}

	return t.ExecuteTemplate(w, executeName, data)
}

func (app *App) renderPage(w http.ResponseWriter, r *http.Request, page string, data PageData) {
	if data.CSRFToken == "" {
		if token, ok := r.Context().Value(csrfTokenKey).(string); ok {
			data.CSRFToken = token
		}
	}
	if err := app.renderTemplate(w, page, "base.html", data); err != nil {
		slog.Error("failed to render page", "page", page, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (app *App) renderPartial(w http.ResponseWriter, r *http.Request, tmplMapKey string, partialName string, data PageData) {
	if data.CSRFToken == "" {
		if token, ok := r.Context().Value(csrfTokenKey).(string); ok {
			data.CSRFToken = token
		}
	}
	if err := app.renderTemplate(w, tmplMapKey, partialName, data); err != nil {
		slog.Error("failed to render partial", "template", tmplMapKey, "partial", partialName, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (app *App) renderError(w http.ResponseWriter, r *http.Request, data PageData, msg string, code int) {
	w.WriteHeader(code)

	errData := data
	errData.Title = "Error"
	errData.User = nil
	errData.Error = msg

	if err := app.renderTemplate(w, "error.html", "base.html", errData); err != nil {
		slog.Error("failed to render error page", "error", err, "status", code)
		http.Error(w, msg, code)
	}
}

// AuthMiddleware resolves the current user from either a session cookie OR a
// Bearer token in the Authorization header.  Both paths are tried in order;
// the first successful one wins and the user is stored in the request context.
// Unauthenticated requests pass through — protected routes use RequireAuth.
func (app *App) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie("session_token"); err == nil {
			user, err := app.DB.GetUserBySession(r.Context(), cookie.Value)
			if err == nil && user != nil {
				if extendErr := app.DB.ExtendSession(r.Context(), cookie.Value, 24*time.Hour); extendErr != nil {
					slog.Warn("failed to extend session", "error", extendErr)
				}
				ctx := context.WithValue(r.Context(), userContextKey, user)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			if err != nil {
				slog.Warn("GetUserBySession failed in AuthMiddleware", "error", err)
			}
		}

		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			token := strings.TrimPrefix(auth, "Bearer ")
			hash := sha256.Sum256([]byte(token))
			tokenHash := hex.EncodeToString(hash[:])
			user, err := app.DB.GetUserByTokenHash(r.Context(), tokenHash)
			if err == nil && user != nil {
				ctx := context.WithValue(r.Context(), userContextKey, user)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			app.clearSessionCookie(w, r)
			if err != nil {
				slog.Warn("GetUserByTokenHash failed in AuthMiddleware", "error", err)
			}
		}

		next.ServeHTTP(w, r)
	})
}

func (app *App) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value(userContextKey) == nil {
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func GetUser(r *http.Request) *models.User {
	if user, ok := r.Context().Value(userContextKey).(*models.User); ok {
		return user
	}
	return nil
}

func (app *App) WebhookAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret := r.Header.Get("X-Gitman-Webhook-Secret")
		if secret == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		repo, err := app.DB.GetRepositoryByWebhookSecret(r.Context(), secret)
		if err != nil || repo == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), repoContextKey, repo)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (app *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if app.requestIsHTTPS(r) || (app.Config != nil && app.Config.ForceSecureCookies) {
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
		}
		next.ServeHTTP(w, r)
	})
}

func GetRepo(r *http.Request) *models.Repository {
	if repo, ok := r.Context().Value(repoContextKey).(*models.Repository); ok {
		return repo
	}
	return nil
}

func GetRepoPath(r *http.Request) string {
	if path, ok := r.Context().Value(repoPathContextKey).(string); ok {
		return path
	}
	return ""
}

func GetRepoOwner(r *http.Request) *models.User {
	if owner, ok := r.Context().Value(repoOwnerContextKey).(*models.User); ok {
		return owner
	}
	return nil
}

func (app *App) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if err := app.DB.PingContext(r.Context()); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func generateCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

func (app *App) CSRFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			cookie, err := r.Cookie("csrf_token")
			var token string
			if err != nil || cookie.Value == "" {
				token, err = generateCSRFToken()
				if err != nil {
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
					return
				}
				http.SetCookie(w, &http.Cookie{
					Name:     "csrf_token",
					Value:    token,
					HttpOnly: true,
					Secure:   app.secureCookie(r),
					SameSite: http.SameSiteStrictMode,
					Path:     "/",
				})
			} else {
				token = cookie.Value
			}
			ctx := context.WithValue(r.Context(), csrfTokenKey, token)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie("csrf_token")
		if err != nil || cookie.Value == "" {
			http.Error(w, "CSRF token missing", http.StatusForbidden)
			return
		}

		formToken := r.FormValue("csrf_token")
		if formToken == "" {
			formToken = r.Header.Get("X-CSRF-Token")
		}
		if formToken == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(formToken)) != 1 {
			http.Error(w, "CSRF validation failed", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}
