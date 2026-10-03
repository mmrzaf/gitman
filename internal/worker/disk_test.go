package worker

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
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
