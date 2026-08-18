package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mmrzaf/gitman/internal/validate"
)

const namespaceLockPollInterval = 25 * time.Millisecond

// NamespaceLock serializes mutations to one username's repository namespace
// across Gitman processes. Lock files are persistent: removing a lock file
// while another process has the inode open would allow two independent locks
// for the same username.
type NamespaceLock struct {
	file *os.File
}

// LockNamespace acquires the cross-process lock for one username. User
// creation/deletion and repository creation/deletion take this same lock so an
// old account namespace cannot be recreated or populated while its storage is
// being quarantined.
func LockNamespace(ctx context.Context, reposRoot, username string) (*NamespaceLock, error) {
	if ctx == nil {
		return nil, fmt.Errorf("namespace lock context is nil")
	}
	if err := validate.StorageName(username); err != nil {
		return nil, fmt.Errorf("invalid namespace owner: %w", err)
	}
	if reposRoot == "" {
		return nil, fmt.Errorf("repository root is empty")
	}

	lockRoot := filepath.Join(reposRoot, ".gitman-locks")
	if err := os.MkdirAll(lockRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create namespace lock directory: %w", err)
	}
	info, err := os.Lstat(lockRoot)
	if err != nil {
		return nil, fmt.Errorf("inspect namespace lock directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("namespace lock path is not a real directory: %s", lockRoot)
	}

	lockPath := filepath.Join(lockRoot, username+".lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open namespace lock: %w", err)
	}
	closeOnError := func(primary error) (*NamespaceLock, error) {
		return nil, errors.Join(primary, file.Close())
	}

	ticker := time.NewTicker(namespaceLockPollInterval)
	defer ticker.Stop()
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &NamespaceLock{file: file}, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return closeOnError(fmt.Errorf("acquire namespace lock: %w", err))
		}

		select {
		case <-ctx.Done():
			return closeOnError(fmt.Errorf("acquire namespace lock: %w", ctx.Err()))
		case <-ticker.C:
		}
	}
}

// Release relinquishes the namespace lock. The lock file itself intentionally
// remains on disk; see NamespaceLock.
func (l *NamespaceLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}
