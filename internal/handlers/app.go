package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mmrzaf/gitman"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
)

type contextKey string

const (
	userContextKey       contextKey = "user"
	repoContextKey       contextKey = "repo"
	repoPathContextKey   contextKey = "repoPath"
	repoOwnerContextKey  contextKey = "repoOwner"
	repoMemberContextKey contextKey = "repoMember"
	repoWriteContextKey  contextKey = "repoWrite"
	csrfTokenKey         contextKey = "csrfToken"
	requestIDContextKey  contextKey = "requestID"
	responseSurfaceKey   contextKey = "responseSurface"
)

var embeddedFiles = gitman.FS

type App struct {
	Config       *config.Config
	DB           *db.DB
	Templates    map[string]*template.Template
	StaticFS     http.FileSystem
	LoginLimiter *loginLimiter
	loginMu      sync.Mutex
}

func (app *App) loginLimiter() *loginLimiter {
	app.loginMu.Lock()
	defer app.loginMu.Unlock()
	if app.LoginLimiter == nil {
		app.LoginLimiter = newLoginLimiter(time.Now)
	}
	return app.LoginLimiter
}

type RepoNavData struct {
	Owner           *models.User
	Repository      *models.Repository
	CurrentRef      string
	Active          string
	IsOwner         bool
	CanViewCI       bool
	SettingsSection string
}

type PageData struct {
	Title      string
	User       *models.User
	Config     *config.Config
	Error      string
	Success    string
	CSRFToken  string
	RepoNav    *RepoNavData
	RequestID  string
	StatusCode int
	ErrorTitle string
	ErrorHint  string
}

// pageModel is the deliberately small template boundary. Every page embeds
// PageData and exposes its concrete fields directly to templates; there is no
// untyped payload bag at the rendering boundary.
type pageModel interface {
	basePage() *PageData
}

func (p *PageData) basePage() *PageData { return p }

func (app *App) requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if app == nil || app.Config == nil || !app.Config.TrustProxyHeaders {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		return true
	}
	return strings.EqualFold(forwardedParam(r.Header.Get("Forwarded"), "proto"), "https")
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

func shortString(s string, limit int) string {
	if limit < 0 || len(s) <= limit {
		return s
	}
	return s[:limit]
}

func escapePath(s string) string {
	return strings.ReplaceAll(url.PathEscape(s), "%2F", "/")
}

func humanBytes(size int64) string {
	if size < 0 {
		return ""
	}
	const unit = int64(1024)
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := unit, 0
	for n := size / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}

func repoNavActive(requestPath string) string {
	parts := strings.Split(strings.Trim(requestPath, "/"), "/")
	if len(parts) < 3 {
		return "files"
	}
	switch parts[2] {
	case "settings":
		return "settings"
	case "ci":
		return "ci"
	case "commits", "commit":
		return "commits"
	default:
		return "files"
	}
}

func repoSettingsSection(requestPath string) string {
	parts := strings.Split(strings.Trim(requestPath, "/"), "/")
	if len(parts) < 3 || parts[2] != "settings" {
		return ""
	}
	if len(parts) == 3 {
		return "general"
	}
	switch parts[3] {
	case "access":
		return "access"
	case "ci":
		return "ci"
	default:
		return "general"
	}
}

var templateFuncs = template.FuncMap{
	"short":      shortString,
	"pathEscape": escapePath,
	"humanSize":  humanBytes,
	"sub1": func(value int) int {
		return value - 1
	},
	"joinPath": func(base, name string) string {
		base = strings.Trim(base, "/")
		name = strings.Trim(name, "/")
		if base == "" {
			return name
		}
		if name == "" {
			return base
		}
		return base + "/" + name
	},
	"statusLabel": StatusLabel,
	"runDuration": func(run any) string {
		switch v := run.(type) {
		case *models.CIRun:
			return FormatDuration(v)
		case models.CIRun:
			return FormatDuration(&v)
		default:
			return ""
		}
	},
	"queueDuration": func(run any) string {
		switch v := run.(type) {
		case *models.CIRun:
			return FormatQueueDuration(v)
		case models.CIRun:
			return FormatQueueDuration(&v)
		default:
			return ""
		}
	},
	"canCancelRun": func(status models.CIStatus) bool {
		return status == models.CIStatusPending || status == models.CIStatusRunning
	},
	"canRetryRun": func(status models.CIStatus) bool {
		return status.Terminal()
	},
}

func LoadTemplates() (map[string]*template.Template, error) {
	templates := make(map[string]*template.Template)

	pages, err := fs.Glob(embeddedFiles, "templates/pages/*.html")
	if err != nil {
		return nil, err
	}

	for _, page := range pages {
		name := filepath.Base(page)

		t, err := template.New("base.html").Funcs(templateFuncs).ParseFS(
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

func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

const maxUIRequestBodyBytes int64 = 1 << 20

func (app *App) limitRequestBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			if r.ContentLength > maxBytes {
				app.respondWebError(w, r, apperr.New(apperr.KindTooLarge, "Request body too large"))
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

func (app *App) repoNavData(r *http.Request, currentRef string) *RepoNavData {
	repo := GetRepo(r)
	owner := GetRepoOwner(r)
	if repo == nil || owner == nil {
		return nil
	}
	if currentRef == "" {
		currentRef = requestRef(r)
	}
	nav := &RepoNavData{
		Owner:           owner,
		Repository:      repo,
		CurrentRef:      currentRef,
		Active:          repoNavActive(r.URL.Path),
		SettingsSection: repoSettingsSection(r.URL.Path),
	}
	user := GetUser(r)
	if user == nil {
		return nav
	}
	if user.ID == repo.OwnerID {
		nav.IsOwner = true
	}
	if member, ok := r.Context().Value(repoMemberContextKey).(bool); ok {
		nav.CanViewCI = member
	} else {
		nav.CanViewCI = nav.IsOwner
	}
	return nav
}

func (app *App) preparePageData(r *http.Request, data pageModel) {
	base := data.basePage()
	if base.RequestID == "" {
		base.RequestID = RequestID(r)
	}
	if base.CSRFToken == "" {
		if token, ok := r.Context().Value(csrfTokenKey).(string); ok {
			base.CSRFToken = token
		}
	}
	if base.RepoNav == nil {
		base.RepoNav = app.repoNavData(r, "")
	}
}

func (app *App) renderTemplateStatus(w http.ResponseWriter, tmplMapKey string, executeName string, data pageModel, status int) error {
	data.basePage().Config = app.Config

	t, ok := app.Templates[tmplMapKey]
	if !ok {
		return fs.ErrNotExist
	}

	// Render before committing the response status. A template execution error
	// can then still be translated into Gitman's normal error surface instead
	// of leaving a half-started 200/4xx response.
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, executeName, data); err != nil {
		return err
	}

	noStore(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if status != 0 && status != http.StatusOK {
		w.WriteHeader(status)
	}
	_, err := w.Write(buf.Bytes())
	return err
}

func (app *App) renderTemplate(w http.ResponseWriter, tmplMapKey string, executeName string, data pageModel) error {
	return app.renderTemplateStatus(w, tmplMapKey, executeName, data, http.StatusOK)
}

func (app *App) renderPageStatus(w http.ResponseWriter, r *http.Request, page string, data pageModel, status int) {
	app.preparePageData(r, data)
	if err := app.renderTemplateStatus(w, page, "base.html", data, status); err != nil {
		appErr := apperr.Wrap(apperr.KindInternal, "Gitman could not render this page", err)
		if responseStarted(w) {
			app.logRequestError(r, appErr, http.StatusInternalServerError)
			return
		}
		app.respondWebError(w, r, appErr)
	}
}

func (app *App) renderPage(w http.ResponseWriter, r *http.Request, page string, data pageModel) {
	app.renderPageStatus(w, r, page, data, http.StatusOK)
}

func (app *App) renderError(w http.ResponseWriter, r *http.Request, data *PageData, msg string, code int) {
	errData := *data
	errData.StatusCode = code
	errData.ErrorTitle, errData.ErrorHint = webErrorCopy(code)
	errData.Title = errData.ErrorTitle
	errData.Error = msg
	app.preparePageData(r, &errData)

	if err := app.renderTemplateStatus(w, "error.html", "base.html", &errData, code); err != nil {
		slog.Error("failed to render error page", "request_id", RequestID(r), "error", err, "status", code)
		if responseStarted(w) {
			return
		}
		noStore(w)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(code)
		_, _ = fmt.Fprintln(w, msg)
	}
}

func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	return token, true
}

// AuthMiddleware resolves the current user from either a session cookie OR a
// Bearer token in the Authorization header.  Both paths are tried in order;
// the first successful one wins and the user is stored in the request context.
// Unauthenticated requests pass through — protected routes use RequireAuth.
func (app *App) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookie, cookieErr := r.Cookie("session_token"); cookieErr == nil {
			user, err := app.DB.GetUserBySession(r.Context(), cookie.Value)
			switch {
			case err == nil:
				extended, extendErr := app.DB.ExtendSessionIfExpiring(r.Context(), cookie.Value, sessionDuration, 12*time.Hour)
				if extendErr != nil {
					slog.Warn("failed to extend session", "request_id", RequestID(r), "error", extendErr)
				} else if extended {
					app.setSessionCookie(w, r, cookie.Value, time.Now().Add(sessionDuration))
				}
				ctx := context.WithValue(r.Context(), userContextKey, user)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			case errors.Is(err, db.ErrNotFound):
				app.clearSessionCookie(w, r)
			default:
				app.respondForSurface(w, r, apperr.Wrap(apperr.KindUnavailable, "Gitman is temporarily unavailable", err))
				return
			}
		}

		if token, ok := bearerToken(r.Header.Get("Authorization")); ok {
			hash := sha256.Sum256([]byte(token))
			tokenHash := hex.EncodeToString(hash[:])
			user, err := app.DB.GetUserByTokenHash(r.Context(), tokenHash)
			switch {
			case err == nil:
				ctx := context.WithValue(r.Context(), userContextKey, user)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			case errors.Is(err, db.ErrNotFound):
				// Invalid bearer credentials are handled by the protected surface.
			default:
				app.respondForSurface(w, r, apperr.Wrap(apperr.KindUnavailable, "Gitman is temporarily unavailable", err))
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func (app *App) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value(userContextKey) == nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (app *App) RequireAPIAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value(userContextKey) == nil {
			app.respondAPIError(w, r, apperr.New(apperr.KindUnauthenticated, "authentication required"))
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

func (app *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if app.requestIsHTTPS(r) || (app.Config != nil && app.Config.ForceSecureCookies) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
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

func (app *App) HandleLiveness(w http.ResponseWriter, _ *http.Request) {
	noStore(w)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (app *App) HandleReadiness(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if err := app.DB.PingContext(r.Context()); err != nil {
		slog.Warn("readiness database check failed", "request_id", RequestID(r), "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "not_ready", "component": "database"})
		return
	}
	for name, configuredPath := range map[string]string{
		"repositories": app.Config.ReposPath,
		"artifacts":    app.Config.ArtifactsPath,
	} {
		info, err := os.Stat(configuredPath)
		if err != nil || !info.IsDir() {
			slog.Warn("readiness storage check failed", "request_id", RequestID(r), "component", name, "path", configuredPath, "error", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "not_ready", "component": name})
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (app *App) HandleHealth(w http.ResponseWriter, r *http.Request) {
	app.HandleReadiness(w, r)
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
					app.respondWebError(w, r, apperr.Wrap(apperr.KindInternal, "Gitman could not create a CSRF token", err))
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
			app.respondForSurface(w, r, apperr.New(apperr.KindForbidden, "CSRF token missing"))
			return
		}

		formToken := r.Header.Get("X-CSRF-Token")
		if formToken == "" {
			contentType := r.Header.Get("Content-Type")
			if strings.HasPrefix(contentType, "application/x-www-form-urlencoded") || strings.HasPrefix(contentType, "multipart/form-data") {
				if err := r.ParseForm(); err != nil {
					app.respondForSurface(w, r, formParseError(err))
					return
				}
				formToken = r.PostForm.Get("csrf_token")
			}
		}
		if formToken == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(formToken)) != 1 {
			app.respondForSurface(w, r, apperr.New(apperr.KindForbidden, "CSRF validation failed"))
			return
		}

		ctx := context.WithValue(r.Context(), csrfTokenKey, cookie.Value)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
