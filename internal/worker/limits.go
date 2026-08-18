package worker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type diskLimit struct {
	name     string
	path     string
	maxBytes int64
}

func checkDiskLimits(limits []diskLimit) error {
	for _, limit := range limits {
		if limit.path == "" || limit.maxBytes <= 0 {
			continue
		}
		exceeded, err := directoryUsageExceeds(limit.path, limit.maxBytes)
		if err != nil {
			return fmt.Errorf("check %s disk usage: %w", limit.name, err)
		}
		if exceeded {
			return fmt.Errorf("%s: %w (%d bytes)", limit.name, errDiskLimitExceeded, limit.maxBytes)
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

func directoryUsageExceeds(root string, maxBytes int64) (bool, error) {
	if maxBytes <= 0 {
		return false, nil
	}
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			if os.IsNotExist(err) {
				// Builds mutate their workspace/cache while the watcher scans it.
				// A file disappearing between ReadDir and Stat is normal, not a
				// storage failure. Other I/O errors remain fatal.
				return nil
			}
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		total += info.Size()
		if total > maxBytes {
			return filepath.SkipAll
		}
		return nil
	})
	return total > maxBytes, err
}

func resetDirectoryIfOverLimit(path string, maxBytes int64) error {
	if path == "" || maxBytes <= 0 {
		return nil
	}
	exceeded, err := directoryUsageExceeds(path, maxBytes)
	if err != nil {
		return err
	}
	if !exceeded {
		return nil
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return os.MkdirAll(path, 0o700)
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
