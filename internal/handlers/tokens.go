package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
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

func (app *App) getTokensForUser(r *http.Request, userID string) []models.AccessToken {
	tokens, err := app.DB.GetUserAccessTokens(r.Context(), userID)
	if err != nil {
		return nil
	}
	return tokens
}

func (app *App) renderTokensPage(w http.ResponseWriter, r *http.Request, user *models.User, errStr, successStr, newToken string) {
	app.renderPage(w, r, "tokens.html", PageData{
		Title:   "Access Tokens",
		User:    user,
		Error:   errStr,
		Success: successStr,
		Data: TokensPageData{
			Tokens:   app.getTokensForUser(r, user.ID),
			NewToken: newToken,
		},
	})
}

func (app *App) HandleTokensGET(w http.ResponseWriter, r *http.Request) {
	app.renderTokensPage(w, r, GetUser(r), "", "", "")
}

func (app *App) HandleTokensPOST(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	name := strings.TrimSpace(r.FormValue("name"))

	if name == "" {
		app.renderTokensPage(w, r, user, "Token name is required.", "", "")
		return
	}

	plainToken, err := generateSecureToken()
	if err != nil {
		slog.Error("failed to generate token", "user_id", user.ID, "error", err)
		app.renderTokensPage(w, r, user, "Failed to create token.", "", "")
		return
	}

	hash := sha256.Sum256([]byte(plainToken))
	tokenHash := hex.EncodeToString(hash[:])

	err = app.DB.CreateAccessToken(r.Context(), user.ID, name, tokenHash)
	if err != nil {
		app.renderTokensPage(w, r, user, "Failed to create token.", "", "")
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
		app.renderTokensPage(w, r, user, "Failed to delete token.", "", "")
		return
	}

	app.renderTokensPage(w, r, user, "", "Token deleted.", "")
}
