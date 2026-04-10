package handlers

import (
	"context"
	"html/template"
	"io/fs"
	"net/http"
	"path/filepath"

	"github.com/mmrzaf/gitman"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
)

type contextKey string

const userContextKey contextKey = "user"

var embeddedFiles = gitman.FS

type App struct {
	Config    *config.Config
	DB        *db.DB
	Templates map[string]*template.Template
	StaticFS  http.FileSystem
}

type PageData struct {
	Title   string
	User    *models.User
	Config  *config.Config
	Error   string
	Success string
	Data    any
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

func (app *App) renderTemplate(w http.ResponseWriter, tmplMapKey string, executeName string, data PageData) {
	data.Config = app.Config
	if t, ok := app.Templates[tmplMapKey]; ok {
		err := t.ExecuteTemplate(w, executeName, data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	} else {
		http.Error(w, "Template not found", http.StatusInternalServerError)
	}
}

func (app *App) renderPage(w http.ResponseWriter, page string, data PageData) {
	app.renderTemplate(w, page, "base.html", data)
}

func (app *App) renderPartial(w http.ResponseWriter, tmplMapKey string, partialName string, data PageData) {
	app.renderTemplate(w, tmplMapKey, partialName, data)
}

func (app *App) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session_token")
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}

		user, err := app.DB.GetUserBySession(r.Context(), cookie.Value)
		if err == nil && user != nil {
			ctx := context.WithValue(r.Context(), userContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (app *App) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value("user") == nil {
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
	if user, ok := r.Context().Value("user").(*models.User); ok {
		return user
	}
	return nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
		}
		next.ServeHTTP(w, r)
	})
}
