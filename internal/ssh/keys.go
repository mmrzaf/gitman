package ssh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	crypto_ssh "golang.org/x/crypto/ssh"
)

var authorizedKeysMu sync.Mutex

// SyncAuthorizedKeys atomically regenerates the authorized_keys file from the
// database. Readers either observe the old complete file or the new complete
// file; they never observe a truncated intermediate file.
func SyncAuthorizedKeys(ctx context.Context, database *db.DB, cfg *config.Config) (err error) {
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

	seenFingerprints := make(map[string]string, len(keys))
	for _, key := range keys {
		parsed, _, _, _, err := crypto_ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(key.PublicKey)))
		if err != nil {
			_ = tmp.Close()
			return fmt.Errorf("SSH key %s is invalid: %w", key.ID, err)
		}
		fingerprint := crypto_ssh.FingerprintSHA256(parsed)
		if existingID, exists := seenFingerprints[fingerprint]; exists {
			_ = tmp.Close()
			return fmt.Errorf("SSH keys %s and %s have the same fingerprint %s", existingID, key.ID, fingerprint)
		}
		seenFingerprints[fingerprint] = key.ID
		pubKey := strings.TrimSpace(string(crypto_ssh.MarshalAuthorizedKey(parsed)))

		forcedCommand := strconv.Quote(fmt.Sprintf("%s serve %s", cfg.BinaryPath, key.ID))
		options := fmt.Sprintf(
			`command=%s,no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-pty`,
			forcedCommand,
		)
		if _, err := fmt.Fprintf(tmp, "%s %s\n", options, pubKey); err != nil {
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
	defer func() {
		if closeErr := dirFile.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	return dirFile.Sync()
}
