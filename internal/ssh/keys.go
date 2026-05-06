package ssh

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
)

// SyncAuthorizedKeys regenerates the authorized_keys file from all keys in the DB.
// Each entry is prefixed with a forced command that calls "gitman serve <keyID>".
func SyncAuthorizedKeys(ctx context.Context, database *db.DB, cfg *config.Config) error {
	keys, err := database.GetAllSSHKeys(ctx)
	if err != nil {
		return err
	}

	// 0600 is strictly required by SSH daemons for authorized_keys.
	f, err := os.OpenFile(cfg.AuthKeysPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err := f.Close(); err != nil {
			slog.Warn("authorized_keys close error", "error", err)
		}
	}()

	if _, err = f.WriteString("# Managed by Gitman. Do not edit manually.\n"); err != nil {
		return err
	}

	for _, key := range keys {
		pubKey := strings.TrimSpace(key.PublicKey)
		pubKey = strings.ReplaceAll(pubKey, "\n", "")
		pubKey = strings.ReplaceAll(pubKey, "\r", "")
		pubKey = strings.ReplaceAll(pubKey, `"`, "")

		options := fmt.Sprintf(
			`command="%s serve %s",no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-pty`,
			cfg.BinaryPath, key.ID,
		)
		line := fmt.Sprintf("%s %s\n", options, pubKey)

		if _, err := f.WriteString(line); err != nil {
			return err
		}
	}

	return nil
}
