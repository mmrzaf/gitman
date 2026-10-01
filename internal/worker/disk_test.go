package worker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
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
