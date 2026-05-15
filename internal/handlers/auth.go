package handlers

import (
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/admin"
	"github.com/mmrzaf/gitman/internal/db"
)

var usernameRegex = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9_-]*[a-zA-Z0-9])?$`)

func (app *App) HandleLoginGET(w http.ResponseWriter, r *http.Request) {
	if GetUser(r) != nil {
		http.Redirect(w, r, "/repos", http.StatusFound)
		return
	}

	app.renderPage(w, r, "login.html", PageData{
		Title: "Login",
	})
}

func (app *App) HandleLoginPOST(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	user, err := app.DB.GetUserByUsername(r.Context(), username)
	if err != nil || user == nil || !db.VerifyPassword(user.PasswordHash, password) {
		app.renderPage(w, r, "login.html", PageData{
			Title: "Login",
			Error: "Invalid username or password",
		})
		return
	}

	token, err := app.DB.CreateSession(r.Context(), user.ID)
	if err != nil {
		app.renderPage(w, r, "login.html", PageData{
			Title: "Login",
			Error: "Internal error, please try again.",
		})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    token,
		Expires:  time.Now().Add(24 * time.Hour),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		Path:     "/",
	})

	http.Redirect(w, r, "/repos", http.StatusFound)
}

func (app *App) HandleRegisterGET(w http.ResponseWriter, r *http.Request) {
	if app.Config == nil || !app.Config.AllowRegister {
		http.NotFound(w, r)
		return
	}
	if GetUser(r) != nil {
		http.Redirect(w, r, "/repos", http.StatusFound)
		return
	}

	app.renderPage(w, r, "register.html", PageData{
		Title: "Register",
	})
}

func (app *App) HandleRegisterPOST(w http.ResponseWriter, r *http.Request) {
	if app.Config == nil || !app.Config.AllowRegister {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	if len(username) < 3 || len(username) > 32 {
		app.renderPage(w, r, "register.html", PageData{
			Title: "Register",
			Error: "Username must be between 3 and 32 characters.",
		})
		return
	}

	if !usernameRegex.MatchString(username) {
		app.renderPage(w, r, "register.html", PageData{
			Title: "Register",
			Error: "Username may only contain letters, numbers, dashes and underscores.",
		})
		return
	}

	if len(password) < 8 {
		app.renderPage(w, r, "register.html", PageData{
			Title: "Register",
			Error: "Password must be at least 8 characters.",
		})
		return
	}
	if err := admin.IsPasswordStrong(password); err != nil {
		app.renderPage(w, r, "register.html", PageData{
			Title: "Register",
			Error: err.Error(),
		})
		return
	}
	_, err := app.DB.CreateUser(r.Context(), username, password)
	if err != nil {
		app.renderPage(w, r, "register.html", PageData{
			Title: "Register",
			Error: "Username might already be taken.",
		})
		return
	}

	http.Redirect(w, r, "/login?registered=1", http.StatusSeeOther)
}

func (app *App) HandleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session_token")
	if err == nil {
		if err = app.DB.DeleteSession(r.Context(), cookie.Value); err != nil {
			slog.Warn("delete session failed during logout", "error", err)
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    "",
		MaxAge:   -1,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})

	http.Redirect(w, r, "/", http.StatusFound)
}
