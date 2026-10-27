package worker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
)

const (
	diskCheckInterval = 5 * time.Second
)

var errDiskBudget = errors.New("workspace or disk reserve exceeded")

// CheckReserve reports whether root has at least reserve bytes free. It
// is exported for diagnostics that must not duplicate the worker's
// Statfs logic.
func CheckReserve(root string, reserve uint64) error { return reserveAvailable(root, reserve) }

func reserveAvailable(root string, reserve uint64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(root, &stat); err != nil {
		return err
	}
	if stat.Bavail*uint64(stat.Bsize) < reserve {
		return errDiskBudget
	}
	return nil
}

// workspaceUsage counts regular files without following symlinks. The budget
// covers checkout metadata as well as build output, including sparse files.
func workspaceUsage(ctx context.Context, root string, limit int64) (int64, error) {
	return filesystemUsage(ctx, os.DirFS(root), limit)
}

func filesystemUsage(ctx context.Context, tree fs.FS, limit int64) (int64, error) {
	var used int64
	err := fs.WalkDir(tree, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if path != "." && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			// Build tools remove output concurrently with monitoring.
			if path != "." && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if info.Size() > limit-used {
			return errDiskBudget
		}
		used += info.Size()
		return nil
	})
	return used, err
}

// monitorDisk stops execution when its monitored budget or the host reserve
// fails. These are monitored limits, not filesystem quotas; one write can exceed
// them between checks. Socket-enabled pipelines also need operator quotas for
// any child containers they create themselves.
func monitorDisk(ctx context.Context, root, reserveRoot string, resources config.Resources, stop context.CancelCauseFunc) {
	monitorDiskWith(ctx, root, reserveRoot, resources, stop, diskCheckInterval, workspaceUsage)
}

// monitorDiskWith is monitorDisk with the check interval and the usage
// walk injectable for tests. A walk that cannot finish within one check
// is not a budget breach: it only means this tick has no answer, so it
// is skipped and the next one tries again.
func monitorDiskWith(ctx context.Context, root, reserveRoot string, resources config.Resources, stop context.CancelCauseFunc,
	interval time.Duration, usage func(context.Context, string, int64) (int64, error)) {
	resources = resources.WithDefaults()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		check, cancel := context.WithTimeout(ctx, interval)
		_, err := usage(check, root, int64(resources.WorkspaceGiB)<<30)
		if err == nil {
			err = reserveAvailable(reserveRoot, uint64(resources.DiskReserveGiB)<<30)
		}
		skipped := errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil
		cancel()
		if skipped {
			// The walk ran out of its own check budget. Leaving the run
			// alive is the only safe answer; killing it would blame the
			// pipeline for a slow filesystem.
			err = nil
		}
		if err != nil && ctx.Err() == nil {
			stop(fmt.Errorf("monitor workspace disk: %w", err))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
