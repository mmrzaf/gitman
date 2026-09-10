package worker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type diskLimit struct {
	name          string
	path          string
	maxBytes      int64
	maxEntries    int64
	minFreeBytes  int64
	minFreeInodes int64
}

type directoryUsage struct {
	bytes   int64
	entries int64
}

func checkDiskLimits(limits []diskLimit) error {
	for _, limit := range limits {
		if limit.path == "" {
			continue
		}
		if limit.minFreeBytes > 0 || limit.minFreeInodes > 0 {
			if err := checkFilesystemHeadroom([]string{limit.path}, limit.minFreeBytes, limit.minFreeInodes); err != nil {
				return err
			}
		}
		if limit.maxBytes <= 0 && limit.maxEntries <= 0 {
			continue
		}
		usage, err := measureDirectoryUsage(limit.path, limit.maxBytes, limit.maxEntries)
		if err != nil {
			return fmt.Errorf("check %s disk usage: %w", limit.name, err)
		}
		if limit.maxBytes > 0 && usage.bytes > limit.maxBytes {
			return fmt.Errorf("%s: %w: byte limit %d exceeded", limit.name, errDiskLimitExceeded, limit.maxBytes)
		}
		if limit.maxEntries > 0 && usage.entries > limit.maxEntries {
			return fmt.Errorf("%s: %w: entry limit %d exceeded", limit.name, errDiskLimitExceeded, limit.maxEntries)
		}
	}
	return nil
}

func runCommandWithDiskLimits(ctx context.Context, cmd *exec.Cmd, limits []diskLimit) error {
	if err := checkDiskLimits(limits); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start command: %w", err)
	}

	done := make(chan struct{})
	violations := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := checkDiskLimits(limits); err != nil {
					select {
					case violations <- err:
					default:
					}
					if cmd.Process != nil {
						if killErr := cmd.Process.Kill(); killErr != nil {
							slog.Warn("failed to kill disk-limit command", "pid", cmd.Process.Pid, "error", killErr)
						}
					}
					return
				}
			}
		}
	}()

	err := cmd.Wait()
	close(done)
	select {
	case violation := <-violations:
		return violation
	default:
		return err
	}
}

func watchDiskLimits(ctx context.Context, containerName string, limits []diskLimit, violations chan<- error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	check := func() bool {
		err := checkDiskLimits(limits)
		if err == nil {
			return false
		}
		select {
		case violations <- err:
		default:
		}
		if removeErr := forceRemoveContainer(containerName); removeErr != nil {
			slog.Warn("failed to remove disk-limit CI container", "container", containerName, "error", removeErr)
		}
		return true
	}
	if check() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if check() {
				return
			}
		}
	}
}

func measureDirectoryUsage(root string, maxBytes, maxEntries int64) (directoryUsage, error) {
	var usage directoryUsage
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return usage, nil
	} else if err != nil {
		return usage, err
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if path == root {
			return nil
		}
		usage.entries++
		if maxEntries > 0 && usage.entries > maxEntries {
			return filepath.SkipAll
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > 0 && usage.bytes > math.MaxInt64-info.Size() {
			usage.bytes = math.MaxInt64
		} else {
			usage.bytes += info.Size()
		}
		if maxBytes > 0 && usage.bytes > maxBytes {
			return filepath.SkipAll
		}
		return nil
	})
	return usage, err
}

func resetDirectoryIfOverLimit(path string, maxBytes, maxEntries int64) error {
	if path == "" || (maxBytes <= 0 && maxEntries <= 0) {
		return nil
	}
	usage, err := measureDirectoryUsage(path, maxBytes, maxEntries)
	if err != nil {
		return err
	}
	exceeded := (maxBytes > 0 && usage.bytes > maxBytes) || (maxEntries > 0 && usage.entries > maxEntries)
	if !exceeded {
		return nil
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return os.MkdirAll(path, 0o700)
}

type filesystemHeadroom struct {
	availableBytes  int64
	availableInodes int64
}

func filesystemAvailable(path string) (filesystemHeadroom, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return filesystemHeadroom{}, err
	}
	bytes := saturatingProduct(uint64(stat.Bavail), uint64(stat.Bsize))
	inodes := uint64(stat.Ffree)
	if inodes > math.MaxInt64 {
		inodes = math.MaxInt64
	}
	return filesystemHeadroom{availableBytes: bytes, availableInodes: int64(inodes)}, nil
}

func saturatingProduct(a, b uint64) int64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a > uint64(math.MaxInt64)/b {
		return math.MaxInt64
	}
	return int64(a * b)
}

func saturatingAdd(values ...int64) int64 {
	var total int64
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if total > math.MaxInt64-value {
			return math.MaxInt64
		}
		total += value
	}
	return total
}

func checkFilesystemHeadroom(paths []string, minBytes, minInodes int64) error {
	seen := make(map[string]struct{})
	for _, path := range paths {
		if path == "" {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve storage path %s: %w", path, err)
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		headroom, err := filesystemAvailable(abs)
		if err != nil {
			return fmt.Errorf("inspect storage path %s: %w", abs, err)
		}
		if minBytes > 0 && headroom.availableBytes < minBytes {
			return fmt.Errorf("%w: %s has %d bytes available; need at least %d", errWorkerStorageUnavailable, abs, headroom.availableBytes, minBytes)
		}
		if minInodes > 0 && headroom.availableInodes < minInodes {
			return fmt.Errorf("%w: %s has %d inodes available; need at least %d", errWorkerStorageUnavailable, abs, headroom.availableInodes, minInodes)
		}
	}
	return nil
}

type limitedWriter struct {
	mu      sync.Mutex
	w       io.Writer
	max     int64
	written int64
	noticed bool
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()

	if lw.max <= 0 {
		return lw.w.Write(p)
	}
	remaining := lw.max - lw.written
	if remaining <= 0 {
		if err := lw.writeLimitNotice(); err != nil {
			return 0, err
		}
		// Output beyond the configured limit is intentionally discarded. Report
		// it as consumed so the child process does not fail merely because its log
		// was truncated.
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		n, err := lw.w.Write(p[:remaining])
		lw.written += int64(n)
		if err != nil {
			return n, err
		}
		if int64(n) != remaining {
			return n, io.ErrShortWrite
		}
		if err := lw.writeLimitNotice(); err != nil {
			return n, err
		}
		return len(p), nil
	}
	n, err := lw.w.Write(p)
	lw.written += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

func (lw *limitedWriter) writeLimitNotice() error {
	if lw.noticed {
		return nil
	}
	const notice = "\n[gitman] log limit reached; further output suppressed\n"
	n, err := io.WriteString(lw.w, notice)
	if err != nil {
		return err
	}
	if n != len(notice) {
		return io.ErrShortWrite
	}
	lw.noticed = true
	return nil
}
