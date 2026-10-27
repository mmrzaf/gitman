package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"syscall"
	"time"
)

// ErrDiskReserve is returned when a maintenance operation would run with
// less free space than the operator's reserve.
var ErrDiskReserve = errors.New("repository disk reserve is low")

// gcTimeout bounds one full repository repack. It is deliberately long:
// a large repository may legitimately take many minutes, and the caller
// holds the repository mutation lock, so pushes wait for it.
const gcTimeout = 30 * time.Minute

// DiskUsage returns the bytes of regular files in the bare repository,
// without following symlinks. It honours ctx while walking.
func (r *Repo) DiskUsage(ctx context.Context) (int64, error) {
	var used int64
	err := filepath.WalkDir(r.path, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if path != r.path && errors.Is(err, fs.ErrNotExist) {
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
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		used += info.Size()
		return nil
	})
	return used, err
}

// GC repacks the repository and prunes unreachable objects. Pins and
// operation receipts are refs, so Git keeps everything the database still
// retains. It refuses to start below the configured free-space reserve.
func (r *Repo) GC(ctx context.Context) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(r.path, &stat); err != nil {
		return err
	}
	if uint64(stat.Bavail)*uint64(stat.Bsize) < r.diskReserve {
		return fmt.Errorf("%w: need at least %d bytes free", ErrDiskReserve, r.diskReserve)
	}
	opts := r.opts()
	opts.timeout = gcTimeout
	_, err := run(ctx, opts, "gc")
	return err
}
