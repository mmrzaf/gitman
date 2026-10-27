package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
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

// hostExecutionLock serializes image removals and cache pruning against every
// run on a Docker host. Runs take it shared; image work takes it exclusive.
// Workers that share an engine also share a workspace root (enforced by
// BindWorkerEngine), so they meet on this one file. The lock is non-blocking:
// a caller that cannot have it waits or tries again later, never queues
// behind a build it cannot see.
func hostExecutionLock(root string, exclusive bool) (func(), bool, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(filepath.Join(root, ".image-execution.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, true, nil
}

// waitHostExecution waits for the shared side of hostExecutionLock, so a run
// claimed while a prune is finishing starts as soon as the prune releases it
// rather than failing for a reason it did not cause.
func waitHostExecution(ctx context.Context, root string) (func(), error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		release, locked, err := hostExecutionLock(root, false)
		if err != nil {
			return nil, err
		}
		if locked {
			return release, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
