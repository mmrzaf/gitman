package worker

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
)

func TestWorkspaceBudgetIncludesMetadataButDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "large"), make([]byte, 1000), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "metadata"), make([]byte, 20), 0600); err != nil {
		t.Fatal(err)
	}
	used, err := workspaceUsage(t.Context(), root, 20)
	if err != nil || used != 20 {
		t.Fatalf("usage = %d, %v", used, err)
	}
	if _, err := workspaceUsage(t.Context(), root, 19); !errors.Is(err, errDiskBudget) {
		t.Fatalf("budget was ignored: %v", err)
	}
}

// disappearingFS reproduces removal after enumeration and before metadata reads.
type disappearingFS struct {
	fstest.MapFS
	infoErr error
	readErr error
}

func (tree disappearingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name == "removed-dir" {
		return nil, tree.readErr
	}
	entries, err := tree.MapFS.ReadDir(name)
	for i, entry := range entries {
		if entry.Name() == "removed-file" {
			entries[i] = disappearingEntry{DirEntry: entry, err: tree.infoErr}
		}
	}
	return entries, err
}

type disappearingEntry struct {
	fs.DirEntry
	err error
}

func (entry disappearingEntry) Info() (fs.FileInfo, error) { return nil, entry.err }

// TestMonitorDiskSkipsAWalkThatOutlivesItsCheck makes the usage walk
// slow on the first tick and over budget on the second. The first tick
// must not kill the run with a misleading shutdown reason; the second
// must still stop it for the real budget.
func TestMonitorDiskSkipsAWalkThatOutlivesItsCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopped := make(chan error, 1)
	calls := 0
	usage := func(check context.Context, _ string, _ int64) (int64, error) {
		calls++
		if calls == 1 {
			<-check.Done()
			return 0, check.Err()
		}
		return 0, errDiskBudget
	}
	monitorDiskWith(ctx, t.TempDir(), t.TempDir(), config.Resources{}, func(cause error) {
		stopped <- cause
	}, 20*time.Millisecond, usage)
	select {
	case cause := <-stopped:
		if !errors.Is(cause, errDiskBudget) {
			t.Fatalf("stopped for %v, want the real disk budget", cause)
		}
	default:
		t.Fatal("the monitor did not stop for the second tick's budget breach")
	}
	if calls < 2 {
		t.Fatalf("usage called %d times, want at least 2", calls)
	}
}

func TestWorkspaceUsageAllowsConcurrentRemoval(t *testing.T) {
	tree := disappearingFS{MapFS: fstest.MapFS{
		"retained":         &fstest.MapFile{Data: []byte("bytes")},
		"removed-file":     &fstest.MapFile{Data: []byte("gone")},
		"removed-dir/file": &fstest.MapFile{Data: []byte("gone")},
	}, infoErr: fs.ErrNotExist, readErr: fs.ErrNotExist}
	used, err := filesystemUsage(t.Context(), tree, 5)
	if err != nil || used != 5 {
		t.Fatalf("usage=%d err=%v", used, err)
	}
	for _, field := range []string{"info", "directory"} {
		t.Run(field, func(t *testing.T) {
			inaccessible := tree
			if field == "info" {
				inaccessible.infoErr = fs.ErrPermission
			} else {
				inaccessible.readErr = fs.ErrPermission
			}
			if _, err := filesystemUsage(t.Context(), inaccessible, 5); !errors.Is(err, fs.ErrPermission) {
				t.Fatalf("access error ignored: %v", err)
			}
		})
	}
	if _, err := workspaceUsage(t.Context(), filepath.Join(t.TempDir(), "missing"), 5); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing root ignored: %v", err)
	}
}
