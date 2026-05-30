package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "test.git")
	ctx := context.Background()
	err := InitBareRepo(ctx, repoPath)
	if err != nil {
		t.Fatal(err)
	}
	return repoPath
}

func prepareRepoWithCommit(t *testing.T, repoPath string) {
	t.Helper()
	cloneDir := t.TempDir()
	cmd := exec.Command("git", "clone", repoPath, cloneDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("clone failed: %v\n%s", err, out)
	}
	readme := filepath.Join(cloneDir, "README.md")
	if err := os.WriteFile(readme, []byte("# hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmds := [][]string{
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test User"},
		{"checkout", "-b", "main"},
		{"add", "."},
		{"commit", "-m", "initial commit"},
		{"push", "origin", "main"},
	}
	for _, args := range cmds {
		cmd := exec.Command("git", append([]string{"-C", cloneDir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
}

func TestInitBareRepo(t *testing.T) {
	repoPath := setupTestRepo(t)
	if _, err := os.Stat(filepath.Join(repoPath, "HEAD")); os.IsNotExist(err) {
		t.Error("HEAD not found")
	}
}

func TestIsEmpty(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	if !IsEmpty(ctx, repoPath) {
		t.Error("new bare repo should be empty")
	}
	prepareRepoWithCommit(t, repoPath)
	if IsEmpty(ctx, repoPath) {
		t.Error("repo with commit should not be empty")
	}
}

func TestGetDefaultBranch(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	prepareRepoWithCommit(t, repoPath)
	branch, err := GetDefaultBranch(ctx, repoPath)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "main" {
		t.Errorf("expected main, got %s", branch)
	}
}

func TestGetBranches(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	prepareRepoWithCommit(t, repoPath)
	branches, err := GetBranches(ctx, repoPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 || branches[0] != "main" {
		t.Errorf("expected [main], got %v", branches)
	}
}

func TestResolveRef(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	_, err := ResolveRef(ctx, repoPath, "main")
	if err == nil {
		t.Error("expected ErrRepoEmpty")
	}
	prepareRepoWithCommit(t, repoPath)
	ref, err := ResolveRef(ctx, repoPath, "main")
	if err != nil || ref != "main" {
		t.Errorf("expected main, got %v err=%v", ref, err)
	}
	ref, err = ResolveRef(ctx, repoPath, "")
	if err != nil || ref != "main" {
		t.Errorf("expected empty ref to resolve to main, got %v err=%v", ref, err)
	}

	_, err = ResolveRef(ctx, repoPath, "nonexistent")
	if !errors.Is(err, ErrRefNotFound) {
		t.Errorf("expected ErrRefNotFound, got %v", err)
	}
}

func TestGetCommits(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	prepareRepoWithCommit(t, repoPath)
	commits, err := GetCommits(ctx, repoPath, "main", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 {
		t.Errorf("expected 1 commit, got %d", len(commits))
	}
	if commits[0].Message != "initial commit" {
		t.Errorf("message mismatch: %s", commits[0].Message)
	}
}

func TestGetTree(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	prepareRepoWithCommit(t, repoPath)
	tree, err := GetTree(ctx, repoPath, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range tree {
		if e.Name == "README.md" && e.Type == "blob" {
			found = true
			break
		}
	}
	if !found {
		t.Error("README.md not found in tree")
	}
}

func TestGetBlob(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	prepareRepoWithCommit(t, repoPath)
	content, err := GetBlob(ctx, repoPath, "main", "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "# hello\n" {
		t.Errorf("unexpected blob content: %q", string(content))
	}
}

func TestGetBlobSize(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	prepareRepoWithCommit(t, repoPath)
	size, err := GetBlobSize(ctx, repoPath, "main", "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if size <= 0 {
		t.Errorf("invalid blob size: %d", size)
	}
}

func TestStreamArchive(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	prepareRepoWithCommit(t, repoPath)
	var buf bytes.Buffer
	err := StreamArchive(ctx, repoPath, "main", "zip", &buf)
	if err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Error("archive empty")
	}
}

func TestSecureRepoPath(t *testing.T) {
	base := "/data/repos"
	path, err := SecureRepoPath(base, "user", "repo")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(base, "user", "repo.git") {
		t.Errorf("unexpected path: %s", path)
	}
	_, err = SecureRepoPath(base, "..", "repo")
	if err == nil {
		t.Error("expected error for path traversal")
	}
	_, err = SecureRepoPath(base, "user", "../repo")
	if err == nil {
		t.Error("expected error for invalid repo name")
	}
}

func TestDeleteRepo(t *testing.T) {
	repoPath := setupTestRepo(t)
	err := DeleteRepo(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(repoPath); !os.IsNotExist(err) {
		t.Error("repo still exists")
	}
}
