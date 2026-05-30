package ssh

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
)

var authorizedKeysMu sync.Mutex

// SyncAuthorizedKeys atomically regenerates the authorized_keys file from the
// database. Readers either observe the old complete file or the new complete
// file; they never observe a truncated intermediate file.
func SyncAuthorizedKeys(ctx context.Context, database *db.DB, cfg *config.Config) error {
	authorizedKeysMu.Lock()
	defer authorizedKeysMu.Unlock()

	keys, err := database.GetAllSSHKeys(ctx)
	if err != nil {
		return err
	}

	dir := filepath.Dir(cfg.AuthKeysPath)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}

	tmp, err := os.CreateTemp(dir, ".authorized_keys-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString("# Managed by Gitman. Do not edit manually.\n"); err != nil {
		_ = tmp.Close()
		return err
	}

	for _, key := range keys {
		pubKey := strings.TrimSpace(key.PublicKey)
		pubKey = strings.ReplaceAll(pubKey, "\n", "")
		pubKey = strings.ReplaceAll(pubKey, "\r", "")
		pubKey = strings.ReplaceAll(pubKey, `"`, "")

		forcedCommand := strconv.Quote(fmt.Sprintf("%s serve %s", cfg.BinaryPath, key.ID))
		options := fmt.Sprintf(
			`command=%s,no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-pty`,
			forcedCommand,
		)
		if _, err := tmp.WriteString(fmt.Sprintf("%s %s\n", options, pubKey)); err != nil {
			_ = tmp.Close()
			return err
		}
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, cfg.AuthKeysPath); err != nil {
		return err
	}

	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer dirFile.Close()
	return dirFile.Sync()
}
