package worker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// executionLock fences filesystem cleanup against a live process, including a
// worker whose database heartbeat was lost. The kernel releases it on death.
// Lock files live outside the mounted workspace and are never unlinked: deleting
// one could let another process lock a different inode for the same run.
func executionLock(root, runID string) (func(), bool, error) {
	if runID == "" || filepath.Base(runID) != runID || strings.HasPrefix(runID, ".") {
		return nil, false, fmt.Errorf("invalid execution ID")
	}
	dir := filepath.Join(root, ".execution-locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(filepath.Join(dir, runID), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, true, nil
}
