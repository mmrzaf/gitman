package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// MutationLock fences repository filesystem mutations across processes, including
// database outages. Lock files remain in place so every process locks one inode.
func (s *Store) MutationLock(ctx context.Context, key string) (func(), error) {
	if !repoIDPattern.MatchString(key) {
		return nil, fmt.Errorf("invalid mutation key")
	}
	dir := filepath.Join(s.root, ".locks")
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, key), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// QuarantineOrphans preserves unexpected repository directories for inspection.
// Nothing unknown is automatically deleted.
func (s *Store) QuarantineOrphans(ctx context.Context, known func(string) (bool, error)) error {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if len(name) < 5 || name[len(name)-4:] != ".git" {
			continue
		}
		repoID := name[:len(name)-4]
		unlock, err := s.MutationLock(ctx, repoID)
		if err != nil {
			return err
		}
		exists, err := known(repoID)
		if err == nil && !exists {
			err = os.MkdirAll(filepath.Join(s.root, ".quarantine"), 0750)
			if err == nil {
				err = os.Rename(filepath.Join(s.root, name), filepath.Join(s.root, ".quarantine", name+"-"+randomSuffix()))
			}
		}
		unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) MutationLock(ctx context.Context, repoID string) (func(), error) {
	return (&Store{root: filepath.Dir(r.path)}).MutationLock(ctx, repoID)
}

// CheckPushCapacity reserves room for the largest admitted pack in addition to
// the host reserve. This is checked before receive-pack accepts request bodies.
func (s *Store) CheckPushCapacity(incoming int64) error {
	if err := os.MkdirAll(s.root, 0750); err != nil {
		return err
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.root, &stat); err != nil {
		return err
	}
	if incoming < 0 {
		incoming = MaxPushBytes
	}
	incoming = min(incoming, MaxPushBytes)
	if uint64(stat.Bavail)*uint64(stat.Bsize) < s.diskReserve+uint64(incoming) {
		return fmt.Errorf("repository storage is below the configured disk reserve plus the incoming pack budget")
	}
	return nil
}
