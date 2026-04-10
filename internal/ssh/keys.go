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

func SyncAuthorizedKeys(ctx context.Context, database *db.DB, cfg *config.Config) error {
	keys, err := database.GetAllSSHKeys(ctx)
	if err != nil {
		return err
	}

	// 0600 is strictly required by SSH daemons for authorized_keys
	f, err := os.OpenFile(cfg.AuthKeysPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err := f.Close(); err != nil {
			slog.Info("file close error: %v", err)
		}
	}()

	if _, err = f.WriteString("# Managed by Gitman. Do not edit manually.\n"); err != nil {
		slog.Info("file write error: %v", err)

	}

	for _, key := range keys {
		// Strict sanitization: Prevent newline injection attacks inside authorized_keys
		pubKey := strings.TrimSpace(key.PublicKey)
		pubKey = strings.ReplaceAll(pubKey, "\n", "")
		pubKey = strings.ReplaceAll(pubKey, "\r", "")
		pubKey = strings.ReplaceAll(pubKey, `"`, "") // Prevent escaping

		options := fmt.Sprintf(`command="%s serve %d",no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-pty`, cfg.BinaryPath, key.ID)
		line := fmt.Sprintf("%s %s\n", options, pubKey)

		if _, err := f.WriteString(line); err != nil {
			return err
		}
	}

	return nil
}
