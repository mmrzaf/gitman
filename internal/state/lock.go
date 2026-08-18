package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type Lock struct {
	file *os.File
}

// AcquireShared marks a process as actively using Gitman's mutable state.
// Backups require the exclusive counterpart and therefore cannot race web,
// worker, SSH, or mutating admin processes.
func AcquireShared(dbPath string) (*Lock, error) {
	return acquire(dbPath, syscall.LOCK_SH)
}

// AcquireExclusive acquires the offline-consistency boundary used by backups.
// It is intentionally non-blocking: operators get a clear error instead of a
// backup command silently waiting for long-running web/worker processes.
func AcquireExclusive(dbPath string) (*Lock, error) {
	return acquire(dbPath, syscall.LOCK_EX)
}

func acquire(dbPath string, mode int) (*Lock, error) {
	lockPath, err := stateLockPath(dbPath)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open state lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure state lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), mode|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			if mode == syscall.LOCK_EX {
				return nil, fmt.Errorf("Gitman state is in use; stop web/worker and other Gitman processes before creating a backup")
			}
			return nil, fmt.Errorf("Gitman state is locked for backup")
		}
		return nil, fmt.Errorf("lock Gitman state: %w", err)
	}
	return &Lock{file: file}, nil
}

func stateLockPath(dbPath string) (string, error) {
	if dbPath == "" {
		return "", fmt.Errorf("database path is required for state lock")
	}
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return "", fmt.Errorf("resolve database path for state lock: %w", err)
	}
	parent := filepath.Dir(abs)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("create state lock directory: %w", err)
	}

	// Lock the canonical database location so an operator cannot accidentally
	// bypass backup exclusion by addressing the same SQLite file through a
	// symlink. The database itself may not exist yet on first startup, in which
	// case resolving the already-created parent is sufficient.
	canonical := abs
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		canonical = resolved
	} else if os.IsNotExist(resolveErr) {
		resolvedParent, parentErr := filepath.EvalSymlinks(parent)
		if parentErr != nil {
			return "", fmt.Errorf("resolve database directory for state lock: %w", parentErr)
		}
		canonical = filepath.Join(resolvedParent, filepath.Base(abs))
	} else {
		return "", fmt.Errorf("resolve database path for state lock: %w", resolveErr)
	}
	return canonical + ".state.lock", nil
}

func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	return errors.Join(syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close())
}
