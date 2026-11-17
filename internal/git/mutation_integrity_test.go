package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuarantineOrphansPreservesUnexpectedStorage(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	defer store.Close()
	for _, name := range []string{"orphan.git", "unknown+name.git"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(path, 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "marker"), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.QuarantineOrphans(context.Background(), func(repoID string) (bool, error) {
		if repoID != "orphan" {
			t.Fatalf("unexpected repository ID passed to database lookup: %q", repoID)
		}
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".quarantine"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("expected both unknown repositories to be quarantined: %v %v", entries, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "orphan.git-") && !strings.HasPrefix(name, "unknown+name.git-") {
			t.Fatalf("unexpected quarantine entry: %s", name)
		}
		data, err := os.ReadFile(filepath.Join(root, ".quarantine", name, "marker"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(name, string(data)+"-") {
			t.Fatalf("quarantine changed repository content: %q: %q", name, data)
		}
	}
}

func TestQuarantineDoesNotMoveKnownRepositories(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	defer store.Close()
	if err := os.MkdirAll(filepath.Join(root, "known.git"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := store.QuarantineOrphans(context.Background(), func(repoID string) (bool, error) {
		return repoID == "known", nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "known.git")); err != nil {
		t.Fatalf("known repository was moved: %v", err)
	}
}
