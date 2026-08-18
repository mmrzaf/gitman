package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
)

type TokensPageData struct {
	Tokens   []models.AccessToken
	NewToken string
}

func generateSecureToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "gm_" + hex.EncodeToString(b), nil
}

func (app *App) renderTokensPage(w http.ResponseWriter, r *http.Request, user *models.User, errStr, successStr, newToken string) {
	tokens, err := app.DB.GetUserAccessTokens(r.Context(), user.ID)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Token data is temporarily unavailable", err))
		return
	}
	app.renderPage(w, r, "tokens.html", PageData{
		Title:   "Access Tokens",
		User:    user,
		Error:   errStr,
		Success: successStr,
		Data: TokensPageData{
			Tokens:   tokens,
			NewToken: newToken,
		},
	})
}

func (app *App) HandleTokensGET(w http.ResponseWriter, r *http.Request) {
	app.renderTokensPage(w, r, GetUser(r), "", "", "")
}

func (app *App) HandleTokensPOST(w http.ResponseWriter, r *http.Request) {
	if !app.parseWebForm(w, r) {
		return
	}
	user := GetUser(r)
	name := strings.TrimSpace(r.FormValue("name"))

	if name == "" {
		app.renderTokensPage(w, r, user, "Token name is required.", "", "")
		return
	}

	plainToken, err := generateSecureToken()
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindInternal, "Gitman could not create a token", err))
		return
	}

	hash := sha256.Sum256([]byte(plainToken))
	tokenHash := hex.EncodeToString(hash[:])

	err = app.DB.CreateAccessToken(r.Context(), user.ID, name, tokenHash)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Token storage is temporarily unavailable", err))
		return
	}

	app.renderTokensPage(w, r, user, "", "Token created successfully.", plainToken)
}

func (app *App) HandleTokenDeletePOST(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	tokenID := chi.URLParam(r, "id")

	if tokenID == "" {
		app.renderTokensPage(w, r, user, "Invalid token id.", "", "")
		return
	}

	err := app.DB.DeleteAccessToken(r.Context(), tokenID, user.ID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.renderTokensPage(w, r, user, "Token not found.", "", "")
		} else {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Token storage is temporarily unavailable", err))
		}
		return
	}

	app.renderTokensPage(w, r, user, "", "Token deleted.", "")
}
