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

func (app *App) HandleKeysGET(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	keys := app.getKeysForUser(r, user.ID)

	app.renderPage(w, r, "keys.html", PageData{
		Title: "SSH Keys",
		User:  user,
		Data:  KeysPageData{Keys: keys},
	})
}

func (app *App) HandleKeysPOST(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	name := strings.TrimSpace(r.FormValue("name"))
	pubKey := strings.TrimSpace(r.FormValue("public_key"))

	if name == "" || pubKey == "" {
		app.renderPartial(w, r, "keys.html", "keys_panel", PageData{
			User:  user,
			Error: "Name and Public Key are required.",
			Data:  KeysPageData{Keys: app.getKeysForUser(r, user.ID)},
		})
		return
	}

	if !strings.HasPrefix(pubKey, "ssh-") && !strings.HasPrefix(pubKey, "ecdsa-") {
		app.renderPartial(w, r, "keys.html", "keys_panel", PageData{
			User:  user,
			Error: "Invalid SSH key format.",
			Data:  KeysPageData{Keys: app.getKeysForUser(r, user.ID)},
		})
		return
	}
	if len(pubKey) < 80 {
		app.renderPartial(w, r, "keys.html", "keys_panel", PageData{
			User:  user,
			Error: "SSH key is too short.",
			Data:  KeysPageData{Keys: app.getKeysForUser(r, user.ID)},
		})
		return
	}
	pubKey = strings.TrimSpace(pubKey)
	_, _, _, _, err := crypto_ssh.ParseAuthorizedKey([]byte(pubKey))
	if err != nil {
		app.renderPartial(w, r, "keys.html", "keys_panel", PageData{
			User:  user,
			Error: "Invalid SSH key format. Please provide a valid public key.",
			Data:  KeysPageData{Keys: app.getKeysForUser(r, user.ID)},
		})
		return
	}
	err = app.DB.AddSSHKey(r.Context(), user.ID, name, pubKey)
	if err != nil {
		app.renderPartial(w, r, "keys.html", "keys_panel", PageData{
			User:  user,
			Error: "Failed to add SSH key. It might already exist.",
			Data:  KeysPageData{Keys: app.getKeysForUser(r, user.ID)},
		})
		return
	}

	if err := ssh.SyncAuthorizedKeys(r.Context(), app.DB, app.Config); err != nil {
		slog.Warn("failed to sync authorized_keys", "error", err)
	}

	app.renderPartial(w, r, "keys.html", "keys_panel", PageData{
		User:    user,
		Success: "SSH key added successfully.",
		Data:    KeysPageData{Keys: app.getKeysForUser(r, user.ID)},
	})
}

func (app *App) HandleKeyDeletePOST(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	keyID := chi.URLParam(r, "id") // UUID string

	if keyID == "" {
		app.renderPartial(w, r, "keys.html", "keys_panel", PageData{
			User:  user,
			Error: "Invalid key id.",
			Data:  KeysPageData{Keys: app.getKeysForUser(r, user.ID)},
		})
		return
	}

	err := app.DB.DeleteSSHKey(r.Context(), keyID, user.ID)
	if err != nil {
		app.renderPartial(w, r, "keys.html", "keys_panel", PageData{
			User:  user,
			Error: "Failed to delete key.",
			Data:  KeysPageData{Keys: app.getKeysForUser(r, user.ID)},
		})
		return
	}

	if err := ssh.SyncAuthorizedKeys(r.Context(), app.DB, app.Config); err != nil {
		slog.Warn("failed to sync authorized_keys", "error", err)
	}

	app.renderPartial(w, r, "keys.html", "keys_panel", PageData{
		User:    user,
		Success: "SSH Key deleted.",
		Data:    KeysPageData{Keys: app.getKeysForUser(r, user.ID)},
	})
}
