package handlers

import (
	"log/slog"
	"net/http"
	"strings"

	crypto_ssh "golang.org/x/crypto/ssh"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/models"
	"github.com/mmrzaf/gitman/internal/ssh"
)

type KeysPageData struct {
	Keys []models.SSHKey
}

func (app *App) getKeysForUser(r *http.Request, userID string) []models.SSHKey {
	keys, err := app.DB.GetUserSSHKeys(r.Context(), userID)
	if err != nil {
		return []models.SSHKey{}
	}
	return keys
}

func (app *App) renderKeysPage(w http.ResponseWriter, r *http.Request, user *models.User, errStr, successStr string) {
	app.renderPage(w, r, "keys.html", PageData{
		Title:   "SSH Keys",
		User:    user,
		Error:   errStr,
		Success: successStr,
		Data:    KeysPageData{Keys: app.getKeysForUser(r, user.ID)},
	})
}

func (app *App) HandleKeysGET(w http.ResponseWriter, r *http.Request) {
	app.renderKeysPage(w, r, GetUser(r), "", "")
}

func (app *App) HandleKeysPOST(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	name := strings.TrimSpace(r.FormValue("name"))
	pubKey := strings.TrimSpace(r.FormValue("public_key"))

	if name == "" || pubKey == "" {
		app.renderKeysPage(w, r, user, "Name and Public Key are required.", "")
		return
	}

	if !strings.HasPrefix(pubKey, "ssh-") && !strings.HasPrefix(pubKey, "ecdsa-") {
		app.renderKeysPage(w, r, user, "Invalid SSH key format.", "")
		return
	}
	if len(pubKey) < 80 {
		app.renderKeysPage(w, r, user, "SSH key is too short.", "")
		return
	}
	pubKey = strings.TrimSpace(pubKey)
	_, _, _, _, err := crypto_ssh.ParseAuthorizedKey([]byte(pubKey))
	if err != nil {
		app.renderKeysPage(w, r, user, "Invalid SSH key format. Please provide a valid public key.", "")
		return
	}
	err = app.DB.AddSSHKey(r.Context(), user.ID, name, pubKey)
	if err != nil {
		app.renderKeysPage(w, r, user, "Failed to add SSH key. It might already exist.", "")
		return
	}

	if err := ssh.SyncAuthorizedKeys(r.Context(), app.DB, app.Config); err != nil {
		slog.Warn("failed to sync authorized_keys", "error", err)
	}

	app.renderKeysPage(w, r, user, "", "SSH key added successfully.")
}

func (app *App) HandleKeyDeletePOST(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	keyID := chi.URLParam(r, "id")

	if keyID == "" {
		app.renderKeysPage(w, r, user, "Invalid key id.", "")
		return
	}

	err := app.DB.DeleteSSHKey(r.Context(), keyID, user.ID)
	if err != nil {
		app.renderKeysPage(w, r, user, "Failed to delete key.", "")
		return
	}

	if err := ssh.SyncAuthorizedKeys(r.Context(), app.DB, app.Config); err != nil {
		slog.Warn("failed to sync authorized_keys", "error", err)
	}

	app.renderKeysPage(w, r, user, "", "SSH key deleted.")
}
