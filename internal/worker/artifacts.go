package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (j *job) collectArtifacts(ctx context.Context) (err error) {
	if ctx.Err() != nil {
		return nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	active, err := j.database.IsCIRunAttemptActive(checkCtx, j.run.ID, j.run.AttemptID)
	if err != nil {
		return fmt.Errorf("%w: verify publication lease: %w", errArtifactPublication, err)
	}
	if !active {
		return nil
	}

	info, err := os.Lstat(j.artifactsStagingDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect staging directory: %w", errArtifactPublication, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: staging path is not a directory", errArtifactPublication)
	}

	dstDir := filepath.Join(j.cfg.ArtifactsPath, "files", j.owner, j.repo.name, j.run.ID, j.run.AttemptID)
	if err := ensurePrivateDir(dstDir); err != nil {
		return fmt.Errorf("%w: create destination: %w", errArtifactPublication, err)
	}
	published := false
	defer func() {
		if published {
			return
		}
		if cleanupErr := os.RemoveAll(dstDir); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("clean partial artifact publication: %w", cleanupErr))
		}
	}()

	var totalBytes int64
	var fileCount int
	limitNoted := false

	err = filepath.WalkDir(j.artifactsStagingDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk artifact %s: %w", filepath.Base(path), walkErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == j.artifactsStagingDir || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			rel, err := filepath.Rel(j.artifactsStagingDir, path)
			if err != nil {
				return fmt.Errorf("resolve symlink artifact path: %w", err)
			}
			j.logf("WARN: skipping symlink artifact: %s", rel)
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect artifact %s: %w", filepath.Base(path), err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if j.cfg.CIArtifactMaxFiles > 0 && fileCount >= j.cfg.CIArtifactMaxFiles {
			if !limitNoted {
				j.logf("WARN: artifact file limit reached; remaining files were not published")
				limitNoted = true
			}
			return filepath.SkipAll
		}
		if j.cfg.CIArtifactMaxBytes > 0 && totalBytes+info.Size() > j.cfg.CIArtifactMaxBytes {
			rel, err := filepath.Rel(j.artifactsStagingDir, path)
			if err != nil {
				return fmt.Errorf("resolve oversized artifact path: %w", err)
			}
			j.logf("WARN: artifact byte limit reached; skipping %s", rel)
			return nil
		}

		rel, err := filepath.Rel(j.artifactsStagingDir, path)
		if err != nil {
			return fmt.Errorf("resolve artifact path: %w", err)
		}
		rel = filepath.Clean(rel)
		if rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			return fmt.Errorf("unsafe artifact path %q", rel)
		}
		dst := filepath.Join(dstDir, rel)
		if !pathInside(dstDir, dst) {
			return fmt.Errorf("artifact path escaped destination: %q", rel)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return fmt.Errorf("create artifact directory %q: %w", filepath.Dir(rel), err)
		}
		if err := copyRegularFileNoFollow(path, dst, info.Size()); err != nil {
			return fmt.Errorf("publish artifact %q: %w", rel, err)
		}
		fileCount++
		totalBytes += info.Size()
		j.logf("Artifact saved: %s", rel)
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return fmt.Errorf("%w: %w", errArtifactPublication, err)
	}
	published = true
	return nil
}

func copyRegularFileNoFollow(src, dst string, expectedSize int64) (err error) {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink artifacts are not allowed")
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("non-regular artifacts are not allowed")
	}
	if expectedSize >= 0 && info.Size() != expectedSize {
		return fmt.Errorf("artifact changed during collection")
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := in.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	openedInfo, err := in.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.Mode().IsRegular() {
		return fmt.Errorf("non-regular artifact opened")
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	closed := false
	keep := false
	defer func() {
		if !closed {
			err = errors.Join(err, out.Close())
		}
		if !keep {
			if removeErr := os.Remove(dst); removeErr != nil && !os.IsNotExist(removeErr) {
				err = errors.Join(err, removeErr)
			}
		}
	}()

	written, err := io.Copy(out, in)
	if err != nil {
		return err
	}
	if expectedSize >= 0 && written != expectedSize {
		return fmt.Errorf("artifact changed during collection: copied %d bytes, expected %d", written, expectedSize)
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		closed = true
		return err
	}
	closed = true
	keep = true
	return nil
}

func pathInside(base, candidate string) bool {
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(baseAbs, candidateAbs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
