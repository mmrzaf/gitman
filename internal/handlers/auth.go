package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/repository"
	"github.com/mmrzaf/gitman/internal/validate"
)

const sessionDuration = 24 * time.Hour

type AuthPageData struct {
	PageData
	Username string
}

func (app *App) renderAuthPage(w http.ResponseWriter, r *http.Request, page, title, username, message string, status int) {
	data := &AuthPageData{PageData: PageData{Title: title, Error: message}, Username: username}
	if status != 0 && status != http.StatusOK {
		app.renderPageStatus(w, r, page, data, status)
		return
	}
	app.renderPage(w, r, page, data)
}

func (app *App) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    token,
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   app.secureCookie(r),
		SameSite: http.SameSiteStrictMode,
		Path:     "/",
	})
}

func (app *App) HandleLoginGET(w http.ResponseWriter, r *http.Request) {
	if GetUser(r) != nil {
		http.Redirect(w, r, "/repos", http.StatusFound)
		return
	}

	app.renderAuthPage(w, r, "login.html", "Login", "", "", http.StatusOK)
}

func (app *App) HandleLoginPOST(w http.ResponseWriter, r *http.Request) {
	if !app.parseWebForm(w, r) {
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	clientIP := app.clientIP(r)
	if ok, retryAfter := app.loginLimiter().allow(username, clientIP); !ok {
		w.Header().Set("Retry-After", fmt.Sprintf("%.0f", retryAfter.Seconds()))
		app.renderAuthPage(w, r, "login.html", "Login", username, "Too many login attempts. Please try again later.", http.StatusTooManyRequests)
		return
	}

	user, err := app.DB.GetUserByUsername(r.Context(), username)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.loginLimiter().recordFailure(username, clientIP)
			app.renderAuthPage(w, r, "login.html", "Login", username, "Invalid username or password", http.StatusOK)
			return
		}
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Login is temporarily unavailable", err))
		return
	}
	passwordOK, err := db.VerifyPassword(user.PasswordHash, password)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Login is temporarily unavailable", err))
		return
	}
	if !passwordOK {
		app.loginLimiter().recordFailure(username, clientIP)
		app.renderAuthPage(w, r, "login.html", "Login", username, "Invalid username or password", http.StatusOK)
		return
	}
	app.loginLimiter().recordSuccess(username)

	token, err := app.DB.CreateSession(r.Context(), user.ID)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Login is temporarily unavailable", err))
		return
	}

	app.setSessionCookie(w, r, token, time.Now().Add(sessionDuration))

	http.Redirect(w, r, "/repos", http.StatusFound)
}

func (app *App) HandleRegisterGET(w http.ResponseWriter, r *http.Request) {
	if app.Config == nil || !app.Config.AllowRegister {
		app.respondWebError(w, r, apperr.New(apperr.KindNotFound, "Page not found"))
		return
	}
	if GetUser(r) != nil {
		http.Redirect(w, r, "/repos", http.StatusFound)
		return
	}

	app.renderAuthPage(w, r, "register.html", "Register", "", "", http.StatusOK)
}

func (app *App) HandleRegisterPOST(w http.ResponseWriter, r *http.Request) {
	if app.Config == nil || !app.Config.AllowRegister {
		app.respondWebError(w, r, apperr.New(apperr.KindNotFound, "Page not found"))
		return
	}
	if !app.parseWebForm(w, r) {
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	if err := validate.Username(username); err != nil {
		app.renderAuthPage(w, r, "register.html", "Register", username, err.Error()+".", http.StatusOK)
		return
	}

	if err := validate.Password(password); err != nil {
		app.renderAuthPage(w, r, "register.html", "Register", username, err.Error(), http.StatusOK)
		return
	}
	lock, err := repository.LockNamespace(r.Context(), app.Config.ReposPath, username)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Registration is temporarily unavailable", err))
		return
	}
	defer func() {
		if err := lock.Release(); err != nil {
			slog.Error("release registration namespace lock", "request_id", RequestID(r), "error", err)
		}
	}()

	_, err = app.DB.CreateUser(r.Context(), username, password)
	if err != nil {
		if errors.Is(err, db.ErrAlreadyExists) {
			app.renderAuthPage(w, r, "register.html", "Register", username, "Username is already taken.", http.StatusOK)
			return
		}
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Registration is temporarily unavailable", err))
		return
	}

	http.Redirect(w, r, "/login?registered=1", http.StatusSeeOther)
}

func (app *App) HandleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session_token")
	if err != nil && !errors.Is(err, http.ErrNoCookie) {
		app.clearSessionCookie(w, r)
		app.respondWebError(w, r, apperr.Wrap(apperr.KindInvalid, "Invalid session cookie", err))
		return
	}
	if err == nil {
		if err := app.DB.DeleteSession(r.Context(), cookie.Value); err != nil {
			// Clear the browser credential even when the server-side revocation
			// failed, but do not pretend the logout was fully completed.
			app.clearSessionCookie(w, r)
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Gitman could not fully revoke this session", err))
			return
		}
	}

	app.clearSessionCookie(w, r)
	http.Redirect(w, r, "/", http.StatusFound)
}
