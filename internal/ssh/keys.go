package ssh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
	crypto_ssh "golang.org/x/crypto/ssh"
)

const (
	authorizedKeysLockPollInterval = 25 * time.Millisecond
	authorizedKeysRecoveryTimeout  = 5 * time.Second
)

func shellQuoteArg(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// SyncAuthorizedKeys atomically regenerates the authorized_keys file from the
// database. A persistent flock serializes the complete DB-snapshot-to-publish
// operation across Gitman processes, so a slower writer cannot publish an older
// key set after a newer writer has already completed.
func SyncAuthorizedKeys(ctx context.Context, database *db.DB, cfg *config.Config) (err error) {
	if ctx == nil || database == nil || cfg == nil || strings.TrimSpace(cfg.AuthKeysPath) == "" {
		return fmt.Errorf("authorized_keys synchronization is not configured")
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

	lock, err := acquireAuthorizedKeysLock(ctx, cfg.AuthKeysPath+".lock")
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, releaseAuthorizedKeysLock(lock))
	}()

	// Query only after owning the publication lock. If another Gitman process
	// committed a key mutation while we were waiting, this snapshot includes it.
	keys, err := database.GetAllSSHKeys(ctx)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".authorized_keys-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmpClosed := false
	defer func() {
		if !tmpClosed {
			err = errors.Join(err, tmp.Close())
		}
		if removeErr := os.Remove(tmpPath); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, fmt.Errorf("remove authorized_keys staging file: %w", removeErr))
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.WriteString("# Managed by Gitman. Do not edit manually.\n"); err != nil {
		return err
	}

	seenFingerprints := make(map[string]string, len(keys))
	for _, key := range keys {
		parsed, _, _, _, err := crypto_ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(key.PublicKey)))
		if err != nil {
			return fmt.Errorf("SSH key %s is invalid: %w", key.ID, err)
		}
		fingerprint := crypto_ssh.FingerprintSHA256(parsed)
		if existingID, exists := seenFingerprints[fingerprint]; exists {
			return fmt.Errorf("SSH keys %s and %s have the same fingerprint %s", existingID, key.ID, fingerprint)
		}
		seenFingerprints[fingerprint] = key.ID
		pubKey := strings.TrimSpace(string(crypto_ssh.MarshalAuthorizedKey(parsed)))

		forcedCommand := strconv.Quote(shellQuoteArg(cfg.BinaryPath) + " serve " + shellQuoteArg(key.ID))
		options := fmt.Sprintf(
			`command=%s,no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-pty`,
			forcedCommand,
		)
		if _, err := fmt.Fprintf(tmp, "%s %s\n", options, pubKey); err != nil {
			return err
		}
	}

	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	tmpClosed = true
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

func acquireAuthorizedKeysLock(ctx context.Context, lockPath string) (*os.File, error) {
	if info, err := os.Lstat(lockPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("authorized_keys lock path is not a regular file: %s", lockPath)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		return nil, errors.Join(err, file.Close())
	}

	ticker := time.NewTicker(authorizedKeysLockPollInterval)
	defer ticker.Stop()
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return nil, errors.Join(fmt.Errorf("lock authorized_keys: %w", err), file.Close())
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(fmt.Errorf("lock authorized_keys: %w", ctx.Err()), file.Close())
		case <-ticker.C:
		}
	}
}

func releaseAuthorizedKeysLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return errors.Join(syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close())
}

var ErrAuthorizedKeysStateUncertain = errors.New("authorized_keys state could not be restored")

// AddKey persists an SSH key and publishes the derived authorized_keys file as
// one application-level operation. If publication fails, Gitman restores the
// previous database state before returning whenever possible.
func AddKey(ctx context.Context, database *db.DB, cfg *config.Config, userID, name, publicKey string) error {
	if err := database.AddSSHKey(ctx, userID, name, publicKey); err != nil {
		return err
	}
	if err := SyncAuthorizedKeys(ctx, database, cfg); err != nil {
		recoveryCtx, cancel := context.WithTimeout(context.Background(), authorizedKeysRecoveryTimeout)
		defer cancel()
		rollbackErr := database.DeleteSSHKeyByPublicKey(recoveryCtx, userID, publicKey)
		resyncErr := SyncAuthorizedKeys(recoveryCtx, database, cfg)
		if rollbackErr != nil {
			return errors.Join(ErrAuthorizedKeysStateUncertain, err, rollbackErr, resyncErr)
		}
		return fmt.Errorf("publish authorized_keys: %w", errors.Join(err, resyncErr))
	}
	return nil
}

// DeleteKey removes an SSH key and republishes authorized_keys. The supplied
// key is the immutable snapshot used to restore the row if publication fails.
func DeleteKey(ctx context.Context, database *db.DB, cfg *config.Config, key *models.SSHKey) error {
	if key == nil {
		return fmt.Errorf("SSH key is required")
	}
	if err := database.DeleteSSHKey(ctx, key.ID, key.UserID); err != nil {
		return err
	}
	if err := SyncAuthorizedKeys(ctx, database, cfg); err != nil {
		recoveryCtx, cancel := context.WithTimeout(context.Background(), authorizedKeysRecoveryTimeout)
		defer cancel()
		restoreErr := database.RestoreSSHKey(recoveryCtx, key)
		resyncErr := SyncAuthorizedKeys(recoveryCtx, database, cfg)
		if restoreErr != nil {
			return errors.Join(ErrAuthorizedKeysStateUncertain, err, restoreErr, resyncErr)
		}
		return fmt.Errorf("publish authorized_keys: %w", errors.Join(err, resyncErr))
	}
	return nil
}
