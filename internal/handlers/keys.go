package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	crypto_ssh "golang.org/x/crypto/ssh"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
	"github.com/mmrzaf/gitman/internal/ssh"
)

type KeysPageData struct {
	Keys []models.SSHKey
}

func supportedSSHKeyType(keyType string) bool {
	switch keyType {
	case crypto_ssh.KeyAlgoRSA,
		crypto_ssh.KeyAlgoECDSA256,
		crypto_ssh.KeyAlgoECDSA384,
		crypto_ssh.KeyAlgoECDSA521,
		crypto_ssh.KeyAlgoED25519,
		crypto_ssh.KeyAlgoSKECDSA256,
		crypto_ssh.KeyAlgoSKED25519:
		return true
	default:
		return false
	}
}

func (app *App) getKeysForUser(r *http.Request, userID string) []models.SSHKey {
	keys, err := app.DB.GetUserSSHKeys(r.Context(), userID)
	if err != nil {
		return []models.SSHKey{}
	}
	for i := range keys {
		if parsed, _, _, _, parseErr := crypto_ssh.ParseAuthorizedKey([]byte(keys[i].PublicKey)); parseErr == nil {
			keys[i].Fingerprint = crypto_ssh.FingerprintSHA256(parsed)
		}
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
	if len(name) > 100 {
		app.renderKeysPage(w, r, user, "Name is limited to 100 characters.", "")
		return
	}
	if len(pubKey) > 16*1024 {
		app.renderKeysPage(w, r, user, "SSH public key is too large.", "")
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
	parsedKey, _, _, _, err := crypto_ssh.ParseAuthorizedKey([]byte(pubKey))
	if err != nil {
		app.renderKeysPage(w, r, user, "Invalid SSH key format. Please provide a valid public key.", "")
		return
	}
	if !supportedSSHKeyType(parsedKey.Type()) {
		app.renderKeysPage(w, r, user, "Unsupported SSH key type. Use RSA, ECDSA, Ed25519, or an OpenSSH security key.", "")
		return
	}
	pubKey = strings.TrimSpace(string(crypto_ssh.MarshalAuthorizedKey(parsedKey)))
	fingerprint := crypto_ssh.FingerprintSHA256(parsedKey)
	allKeys, err := app.DB.GetAllSSHKeys(r.Context())
	if err != nil {
		app.renderKeysPage(w, r, user, "Failed to verify existing SSH keys.", "")
		return
	}
	for _, existing := range allKeys {
		key, _, _, _, parseErr := crypto_ssh.ParseAuthorizedKey([]byte(existing.PublicKey))
		if parseErr == nil && crypto_ssh.FingerprintSHA256(key) == fingerprint {
			app.renderKeysPage(w, r, user, "This SSH key is already attached to an account.", "")
			return
		}
	}
	err = app.DB.AddSSHKey(r.Context(), user.ID, name, pubKey)
	if err != nil {
		if errors.Is(err, db.ErrSSHKeyExists) {
			app.renderKeysPage(w, r, user, "This SSH key is already attached to an account.", "")
			return
		}
		app.renderKeysPage(w, r, user, "Failed to add SSH key.", "")
		return
	}

	if err := ssh.SyncAuthorizedKeys(r.Context(), app.DB, app.Config); err != nil {
		slog.Error("failed to sync authorized_keys after adding key", "error", err)
		if rollbackErr := app.DB.DeleteSSHKeyByPublicKey(r.Context(), user.ID, pubKey); rollbackErr != nil {
			slog.Error("failed to rollback SSH key after sync failure", "error", rollbackErr)
		}
		_ = ssh.SyncAuthorizedKeys(r.Context(), app.DB, app.Config)
		app.renderKeysPage(w, r, user, "The SSH key could not be activated. No change was kept.", "")
		return
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
	key, err := app.DB.GetSSHKeyByID(r.Context(), keyID)
	if err != nil || key == nil || key.UserID != user.ID {
		app.renderKeysPage(w, r, user, "SSH key not found.", "")
		return
	}

	err = app.DB.DeleteSSHKey(r.Context(), keyID, user.ID)
	if err != nil {
		app.renderKeysPage(w, r, user, "Failed to delete key.", "")
		return
	}

	if err := ssh.SyncAuthorizedKeys(r.Context(), app.DB, app.Config); err != nil {
		slog.Error("failed to sync authorized_keys after deleting key", "error", err)
		if restoreErr := app.DB.AddSSHKey(r.Context(), user.ID, key.Name, key.PublicKey); restoreErr != nil {
			slog.Error("failed to restore SSH key after sync failure", "error", restoreErr)
		}
		_ = ssh.SyncAuthorizedKeys(r.Context(), app.DB, app.Config)
		app.renderKeysPage(w, r, user, "The SSH key could not be removed. No change was kept.", "")
		return
	}

	app.renderKeysPage(w, r, user, "", "SSH key deleted.")
}
