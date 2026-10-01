package worker

import (
	"context"
	"errors"
	"fmt"
	"github.com/mmrzaf/gitman/internal/config"
	"io/fs"
	"path/filepath"
	"syscall"
	"time"
)

const (
	diskCheckInterval = 5 * time.Second
)

var errDiskBudget = errors.New("workspace or disk reserve exceeded")

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
	var used int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
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
	resources = resources.WithDefaults()
	ticker := time.NewTicker(diskCheckInterval)
	defer ticker.Stop()
	for {
		check, cancel := context.WithTimeout(ctx, diskCheckInterval)
		_, err := workspaceUsage(check, root, int64(resources.WorkspaceGiB)<<30)
		if err == nil {
			err = reserveAvailable(reserveRoot, uint64(resources.DiskReserveGiB)<<30)
		}
		cancel()
		if err != nil && ctx.Err() == nil {
			stop(fmt.Errorf("%w: %v", errDiskBudget, err))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
