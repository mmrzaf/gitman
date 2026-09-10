package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
)

const defaultTokenExpiryDays = 90

const defaultTokenScope = models.AccessTokenScopeRepoRead

var allowedTokenExpiryDays = map[int]struct{}{
	30:  {},
	90:  {},
	180: {},
	365: {},
}

type AccessTokenView struct {
	models.AccessToken
	State      string
	StateClass string
	ScopeLabel string
}

type TokensPageData struct {
	PageData
	Tokens   []AccessTokenView
	NewToken string
}

func accessTokenView(token models.AccessToken, now time.Time) AccessTokenView {
	view := AccessTokenView{
		AccessToken: token,
		State:       "Active",
		StateClass:  "success",
		ScopeLabel:  accessTokenScopeLabel(token.Scope),
	}
	if token.ExpiresAt == nil {
		view.State = "Expiry unavailable"
		view.StateClass = "failed"
		return view
	}
	if !token.ExpiresAt.After(now) {
		view.State = "Expired"
		view.StateClass = "failed"
		return view
	}
	if !token.ExpiresAt.After(now.Add(7 * 24 * time.Hour)) {
		view.State = "Expires soon"
		view.StateClass = "pending"
	}
	return view
}

func accessTokenScope(raw string) (models.AccessTokenScope, error) {
	scope := models.AccessTokenScope(strings.TrimSpace(raw))
	if scope == "" {
		scope = defaultTokenScope
	}
	if !scope.Valid() {
		return "", fmt.Errorf("token scope must be read-only or read/write")
	}
	return scope, nil
}

func accessTokenScopeLabel(scope models.AccessTokenScope) string {
	switch scope {
	case models.AccessTokenScopeRepoRead:
		return "Read-only"
	case models.AccessTokenScopeRepoWrite:
		return "Read & write"
	default:
		return "Unknown scope"
	}
}

func generateSecureToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "gm_" + hex.EncodeToString(b), nil
}

func tokenExpiration(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = strconv.Itoa(defaultTokenExpiryDays)
	}
	days, err := strconv.Atoi(raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid token expiration")
	}
	if _, ok := allowedTokenExpiryDays[days]; !ok {
		return time.Time{}, fmt.Errorf("token expiration must be 30, 90, 180, or 365 days")
	}
	return now.AddDate(0, 0, days), nil
}

func (app *App) renderTokensPage(w http.ResponseWriter, r *http.Request, user *models.User, errStr, successStr, newToken string) {
	tokens, err := app.DB.GetUserAccessTokens(r.Context(), user.ID)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Token data is temporarily unavailable", err))
		return
	}
	views := make([]AccessTokenView, 0, len(tokens))
	now := time.Now()
	for _, token := range tokens {
		views = append(views, accessTokenView(token, now))
	}
	app.renderPage(w, r, "tokens.html", &TokensPageData{
		PageData: PageData{Title: "Access Tokens", User: user, Error: errStr, Success: successStr},
		Tokens:   views, NewToken: newToken,
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
	if len(name) > 100 {
		app.renderTokensPage(w, r, user, "Token name is limited to 100 characters.", "", "")
		return
	}
	expiresAt, err := tokenExpiration(r.FormValue("expires_in_days"), time.Now())
	if err != nil {
		app.renderTokensPage(w, r, user, err.Error()+".", "", "")
		return
	}
	scope, err := accessTokenScope(r.FormValue("scope"))
	if err != nil {
		app.renderTokensPage(w, r, user, err.Error()+".", "", "")
		return
	}

	plainToken, err := generateSecureToken()
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindInternal, "Gitman could not create a token", err))
		return
	}

	hash := sha256.Sum256([]byte(plainToken))
	tokenHash := hex.EncodeToString(hash[:])

	tokenID, err := app.DB.CreateAccessTokenWithScope(r.Context(), user.ID, name, tokenHash, scope, &expiresAt)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Token storage is temporarily unavailable", err))
		return
	}
	app.recordAuditEvent(r, user, models.AuditActionTokenCreated, "access_token", tokenID, map[string]string{
		"name":       name,
		"scope":      string(scope),
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
	})

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
	app.recordAuditEvent(r, user, models.AuditActionTokenRevoked, "access_token", tokenID, nil)

	app.renderTokensPage(w, r, user, "", "Token deleted.", "")
}
