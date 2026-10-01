package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreCreateOpenDelete(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	root := t.TempDir()
	store := NewStore(root)
	defer store.Close()

	if err := store.Create(ctx, "abc-123", "develop"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Create(ctx, "abc-123", "develop"); !errors.Is(err, ErrExists) {
		t.Fatalf("second Create = %v, want ErrExists", err)
	}

	repo, err := store.Open("abc-123")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	head, err := run(ctx, repo.opts(), "symbolic-ref", "HEAD")
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	if got := strings.TrimSpace(string(head)); got != "refs/heads/develop" {
		t.Errorf("HEAD = %q, want refs/heads/develop", got)
	}
	if _, err := os.Stat(filepath.Join(repo.path, "hooks")); !errors.Is(err, os.ErrNotExist) {
		t.Error("expected the sample hooks directory to be removed")
	}

	if err := store.Delete("abc-123"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Open("abc-123"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open after Delete = %v, want ErrNotFound", err)
	}
	if err := store.Delete("abc-123"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete = %v, want ErrNotFound", err)
	}
}

func TestStoreRejectsBadIDs(t *testing.T) {
	store := NewStore(t.TempDir())
	defer store.Close()
	for _, id := range []string{"", "../escape", "a/b", ".hidden", "with space"} {
		if _, err := store.Path(id); err == nil {
			t.Errorf("Path(%q): expected an error", id)
		}
	}
}

func TestStoreRejectsBadDefaultBranch(t *testing.T) {
	requireGit(t)
	store := NewStore(t.TempDir())
	defer store.Close()
	for _, branch := range []string{"", "has space", "3f2a91cb"} {
		if err := store.Create(context.Background(), "repo", branch); err == nil {
			t.Errorf("Create with default branch %q: expected an error", branch)
		}
	}
}

func TestStoreSweep(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	defer store.Close()
	for _, name := range []string{".tmp-x-1", ".trash-y-2", "keep.git"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 2 || entries[0].Name() != ".locks" || entries[1].Name() != "keep.git" {
		t.Fatalf("after Sweep: %v, want keep.git and the mutation-lock directory", entries)
	}
}
