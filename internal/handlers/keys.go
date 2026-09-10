package handlers

import (
	"errors"
	"net/http"
	"strings"

	crypto_ssh "golang.org/x/crypto/ssh"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
	"github.com/mmrzaf/gitman/internal/ssh"
)

type KeysPageData struct {
	PageData
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

func (app *App) renderKeysPage(w http.ResponseWriter, r *http.Request, user *models.User, errStr, successStr string) {
	keys, err := app.DB.GetUserSSHKeys(r.Context(), user.ID)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "SSH key data is temporarily unavailable", err))
		return
	}
	for i := range keys {
		if parsed, _, _, _, parseErr := crypto_ssh.ParseAuthorizedKey([]byte(keys[i].PublicKey)); parseErr == nil {
			keys[i].Fingerprint = crypto_ssh.FingerprintSHA256(parsed)
		}
	}
	app.renderPage(w, r, "keys.html", &KeysPageData{
		PageData: PageData{Title: "SSH Keys", User: user, Error: errStr, Success: successStr},
		Keys:     keys,
	})
}

func (app *App) HandleKeysGET(w http.ResponseWriter, r *http.Request) {
	app.renderKeysPage(w, r, GetUser(r), "", "")
}

func (app *App) HandleKeysPOST(w http.ResponseWriter, r *http.Request) {
	if !app.parseWebForm(w, r) {
		return
	}
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
	err = ssh.AddKey(r.Context(), app.DB, app.Config, user.ID, name, pubKey)
	if err != nil {
		if errors.Is(err, db.ErrSSHKeyExists) {
			app.renderKeysPage(w, r, user, "This SSH key is already attached to an account.", "")
			return
		}
		if errors.Is(err, ssh.ErrAuthorizedKeysStateUncertain) {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindInternal, "SSH key activation failed and Gitman could not fully restore the previous state", err))
			return
		}
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "SSH key activation is temporarily unavailable", err))
		return
	}
	app.recordAuditEvent(r, user, models.AuditActionSSHKeyCreated, "ssh_key", crypto_ssh.FingerprintSHA256(parsedKey), map[string]string{"name": name})

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
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.renderKeysPage(w, r, user, "SSH key not found.", "")
		} else {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "SSH key data is temporarily unavailable", err))
		}
		return
	}
	if key.UserID != user.ID {
		app.renderKeysPage(w, r, user, "SSH key not found.", "")
		return
	}

	err = ssh.DeleteKey(r.Context(), app.DB, app.Config, key)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			app.renderKeysPage(w, r, user, "SSH key not found.", "")
		} else if errors.Is(err, ssh.ErrAuthorizedKeysStateUncertain) {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindInternal, "SSH key removal failed and Gitman could not fully restore the previous state", err))
		} else {
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "SSH key activation is temporarily unavailable", err))
		}
		return
	}
	app.recordAuditEvent(r, user, models.AuditActionSSHKeyDeleted, "ssh_key", key.ID, map[string]string{"name": key.Name})

	app.renderKeysPage(w, r, user, "", "SSH key deleted.")
}
