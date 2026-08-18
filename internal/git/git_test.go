package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "test.git")
	ctx := context.Background()
	err := InitBareRepo(ctx, repoPath, 512*1024*1024)
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
	out, err := run(context.Background(), repoPath, "config", "--get", "receive.maxInputSize")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "536870912" {
		t.Fatalf("expected receive.maxInputSize 536870912, got %q", got)
	}
}

func TestIsEmpty(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	empty, err := IsEmpty(ctx, repoPath)
	if err != nil || !empty {
		t.Fatalf("new bare repo empty = %v, err = %v", empty, err)
	}
	prepareRepoWithCommit(t, repoPath)
	empty, err = IsEmpty(ctx, repoPath)
	if err != nil || empty {
		t.Fatalf("repo with commit empty = %v, err = %v", empty, err)
	}
}

func TestIsEmptyIncludesDetachedHEADWithoutRefs(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	prepareRepoWithCommit(t, repoPath)

	hashBytes, err := run(ctx, repoPath, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.TrimSpace(string(hashBytes))
	if _, err := run(ctx, repoPath, "update-ref", "--no-deref", "HEAD", hash); err != nil {
		t.Fatal(err)
	}
	if _, err := run(ctx, repoPath, "update-ref", "-d", "refs/heads/main"); err != nil {
		t.Fatal(err)
	}

	empty, err := IsEmpty(ctx, repoPath)
	if err != nil || empty {
		t.Fatalf("detached HEAD-only repo empty = %v, err = %v", empty, err)
	}
	resolved, err := ResolveRef(ctx, repoPath, "")
	if err != nil || resolved != hash {
		t.Fatalf("ResolveRef detached HEAD-only = %q, %v; want %s", resolved, err, hash)
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

func TestGetTreeTreatsPathAsLiteral(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	prepareRepoWithCommit(t, repoPath)

	cloneDir := t.TempDir()
	cmd := exec.Command("git", "clone", repoPath, cloneDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	for _, args := range [][]string{
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test User"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", cloneDir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.Mkdir(filepath.Join(cloneDir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloneDir, "docs", "guide.md"), []byte("guide\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "add docs"}, {"push", "origin", "main"}} {
		if out, err := exec.Command("git", append([]string{"-C", cloneDir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	_, err := GetTree(ctx, repoPath, "main", ":(glob)*")
	if !errors.Is(err, ErrPathNotFound) {
		t.Fatalf("pathspec-like missing directory error = %v, want ErrPathNotFound", err)
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

func TestInitBareRepoRejectsExistingPath(t *testing.T) {
	repoPath := setupTestRepo(t)
	err := InitBareRepo(context.Background(), repoPath, 512*1024*1024)
	if !errors.Is(err, ErrRepoPathExists) {
		t.Fatalf("expected ErrRepoPathExists, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoPath, "HEAD")); err != nil {
		t.Fatalf("existing repository was damaged: %v", err)
	}
}

func TestResolveCommitHash(t *testing.T) {
	repoPath := setupTestRepo(t)
	prepareRepoWithCommit(t, repoPath)
	commits, err := GetCommits(context.Background(), repoPath, "main", 0, 1)
	if err != nil || len(commits) != 1 {
		t.Fatalf("get commits: %v", err)
	}
	resolved, err := ResolveCommitHash(context.Background(), repoPath, commits[0].Hash[:8])
	if err != nil {
		t.Fatal(err)
	}
	if resolved != commits[0].Hash {
		t.Fatalf("expected %s, got %s", commits[0].Hash, resolved)
	}
}

func TestResolveCommitHashSHA256(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "sha256.git")
	cmd := exec.Command("git", "init", "--bare", "--object-format=sha256", repoPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("Git SHA-256 repositories are unavailable: %v\n%s", err, out)
	}
	prepareRepoWithCommit(t, repoPath)
	commits, err := GetCommits(context.Background(), repoPath, "main", 0, 1)
	if err != nil || len(commits) != 1 {
		t.Fatalf("get SHA-256 commits: %v", err)
	}
	if len(commits[0].Hash) != 64 {
		t.Fatalf("commit hash length = %d, want 64", len(commits[0].Hash))
	}
	resolved, err := ResolveCommitHash(context.Background(), repoPath, commits[0].Hash[:12])
	if err != nil {
		t.Fatal(err)
	}
	if resolved != commits[0].Hash {
		t.Fatalf("expected %s, got %s", commits[0].Hash, resolved)
	}
}

func TestResolveBranchCommitHashAndReachability(t *testing.T) {
	repoPath := setupTestRepo(t)
	prepareRepoWithCommit(t, repoPath)
	commit, err := ResolveBranchCommitHash(context.Background(), repoPath, "main")
	if err != nil {
		t.Fatal(err)
	}
	reachable, err := IsCommitReachableFromBranch(context.Background(), repoPath, commit, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !reachable {
		t.Fatal("branch tip should be reachable from branch")
	}
}

func TestSecureRepoPathAllowsCurrentDirectoryRoot(t *testing.T) {
	got, err := SecureRepoPath(".", "owner", "repo")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("owner", "repo.git")
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestGetCommitsByHashes(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	prepareRepoWithCommit(t, repoPath)
	commits, err := GetCommits(ctx, repoPath, "main", 0, 1)
	if err != nil || len(commits) != 1 {
		t.Fatalf("load seed commit: commits=%d err=%v", len(commits), err)
	}

	unknownCanonical := strings.Repeat("a", len(commits[0].Hash))
	byHash, err := GetCommitsByHashes(ctx, repoPath, []string{commits[0].Hash, commits[0].Hash, "not-a-hash", unknownCanonical})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := byHash[commits[0].Hash]
	if !ok {
		t.Fatalf("commit %s missing from batch result: %+v", commits[0].Hash, byHash)
	}
	if got.Message != "initial commit" || got.Author != "Test User" {
		t.Fatalf("unexpected commit metadata: %+v", got)
	}
	if _, ok := byHash[unknownCanonical]; ok {
		t.Fatalf("unknown canonical hash unexpectedly returned: %+v", byHash[unknownCanonical])
	}
}

func TestBlobExists(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	prepareRepoWithCommit(t, repoPath)

	exists, err := BlobExists(ctx, repoPath, "main", "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("README.md should exist")
	}
	exists, err = BlobExists(ctx, repoPath, "main", ".gitman-ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("missing CI config reported as existing")
	}
	exists, err = BlobExists(ctx, repoPath, "main", ".")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("repository tree reported as a blob")
	}
}

func TestSanitizeRefForFilename(t *testing.T) {
	tests := map[string]string{
		"feature/ci-v2": "feature_ci-v2",
		"release 1":     "release1",
		"日本語":           "revision",
		"///":           "___",
	}
	for input, want := range tests {
		if got := SanitizeRefForFilename(input); got != want {
			t.Fatalf("SanitizeRefForFilename(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCommandErrorCapturesStderr(t *testing.T) {
	ctx := context.Background()
	_, err := runGit(ctx, "rev-parse", "--definitely-not-a-real-option")
	if err == nil {
		t.Fatal("expected git command failure")
	}
	var commandErr *CommandError
	if !errors.As(err, &commandErr) {
		t.Fatalf("error type = %T, want *CommandError: %v", err, err)
	}
	if commandErr.ExitCode == 0 || strings.TrimSpace(commandErr.Stderr) == "" {
		t.Fatalf("command error lost exit/stderr: %+v", commandErr)
	}
}

func TestResolveRefDetachedHEAD(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	prepareRepoWithCommit(t, repoPath)
	hashBytes, err := run(ctx, repoPath, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.TrimSpace(string(hashBytes))
	if _, err := run(ctx, repoPath, "update-ref", "--no-deref", "HEAD", hash); err != nil {
		t.Fatal(err)
	}
	branch, err := GetDefaultBranch(ctx, repoPath)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "" {
		t.Fatalf("detached default branch = %q, want empty", branch)
	}
	ref, err := ResolveRef(ctx, repoPath, "")
	if err != nil || ref != hash {
		t.Fatalf("ResolveRef detached default = %q, %v; want %s", ref, err, hash)
	}
	info, err := ResolveRefInfo(ctx, repoPath, "")
	if err != nil || info.Name != hash || info.Kind != RefKindCommit {
		t.Fatalf("ResolveRefInfo detached default = %+v, %v; want commit %s", info, err, hash)
	}
	ref, err = ResolveRef(ctx, repoPath, "HEAD")
	if err != nil || ref != hash {
		t.Fatalf("ResolveRef explicit HEAD = %q, %v; want %s", ref, err, hash)
	}
}

func TestExactCommitRemainsBrowsableAfterRefsAreDeleted(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	prepareRepoWithCommit(t, repoPath)

	hashBytes, err := run(ctx, repoPath, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.TrimSpace(string(hashBytes))
	if _, err := run(ctx, repoPath, "update-ref", "-d", "refs/heads/main"); err != nil {
		t.Fatal(err)
	}

	empty, err := IsEmpty(ctx, repoPath)
	if err != nil || !empty {
		t.Fatalf("repo with no reachable refs empty = %v, err = %v", empty, err)
	}
	if _, err := ResolveRef(ctx, repoPath, ""); !errors.Is(err, ErrRepoEmpty) {
		t.Fatalf("default ref error = %v, want ErrRepoEmpty", err)
	}
	resolved, err := ResolveRef(ctx, repoPath, hash)
	if err != nil || resolved != hash {
		t.Fatalf("ResolveRef exact commit = %q, %v; want %s", resolved, err, hash)
	}
	blob, err := GetBlob(ctx, repoPath, hash, "README.md")
	if err != nil || string(blob) != "# hello\n" {
		t.Fatalf("GetBlob exact dangling commit = %q, %v", blob, err)
	}
	commits, err := GetCommits(ctx, repoPath, hash, 0, 1)
	if err != nil || len(commits) != 1 || commits[0].Hash != hash {
		t.Fatalf("GetCommits exact dangling commit = %+v, %v", commits, err)
	}
	byHash, err := GetCommitsByHashes(ctx, repoPath, []string{hash})
	if err != nil {
		t.Fatalf("GetCommitsByHashes exact dangling commit: %v", err)
	}
	if _, ok := byHash[hash]; !ok {
		t.Fatalf("dangling commit %s missing from batch metadata: %+v", hash, byHash)
	}
}

func TestGitValidDashPrefixedRefsAreOptionSafe(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	prepareRepoWithCommit(t, repoPath)

	hashBytes, err := run(ctx, repoPath, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.TrimSpace(string(hashBytes))
	for _, fullRef := range []string{"refs/heads/-release", "refs/tags/--v1"} {
		if _, err := run(ctx, repoPath, "update-ref", fullRef, hash); err != nil {
			t.Fatalf("create %s: %v", fullRef, err)
		}
	}

	for _, ref := range []string{"-release", "--v1"} {
		resolved, err := ResolveRef(ctx, repoPath, ref)
		if err != nil || resolved != ref {
			t.Fatalf("ResolveRef(%q) = %q, %v", ref, resolved, err)
		}
		resolvedHash, err := ResolveRevisionCommitHash(ctx, repoPath, ref)
		if err != nil || resolvedHash != hash {
			t.Fatalf("ResolveRevisionCommitHash(%q) = %q, %v; want %s", ref, resolvedHash, err, hash)
		}
		commits, err := GetCommits(ctx, repoPath, ref, 0, 1)
		if err != nil || len(commits) != 1 || commits[0].Hash != hash {
			t.Fatalf("GetCommits(%q) = %+v, %v", ref, commits, err)
		}
		tree, err := GetTree(ctx, repoPath, ref, "")
		if err != nil || len(tree) == 0 {
			t.Fatalf("GetTree(%q) = %+v, %v", ref, tree, err)
		}
		files, err := ListFiles(ctx, repoPath, ref)
		if err != nil || len(files) != 1 || files[0] != "README.md" {
			t.Fatalf("ListFiles(%q) = %+v, %v", ref, files, err)
		}
		blob, err := GetBlob(ctx, repoPath, ref, "README.md")
		if err != nil || string(blob) != "# hello\n" {
			t.Fatalf("GetBlob(%q) = %q, %v", ref, blob, err)
		}
		var archive bytes.Buffer
		if err := StreamArchive(ctx, repoPath, ref, "tar", &archive); err != nil {
			t.Fatalf("StreamArchive(%q): %v", ref, err)
		}
		if archive.Len() == 0 {
			t.Fatalf("StreamArchive(%q) returned empty archive", ref)
		}
	}
}

func TestResolveRefDoesNotConfuseDetachedHEADWithBranchNamedHEAD(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	prepareRepoWithCommit(t, repoPath)
	hashBytes, err := run(ctx, repoPath, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.TrimSpace(string(hashBytes))
	if _, err := run(ctx, repoPath, "update-ref", "refs/heads/HEAD", hash); err != nil {
		t.Fatal(err)
	}
	if _, err := run(ctx, repoPath, "update-ref", "--no-deref", "HEAD", hash); err != nil {
		t.Fatal(err)
	}

	defaultInfo, err := ResolveRefInfo(ctx, repoPath, "")
	if err != nil || defaultInfo.Kind != RefKindCommit || defaultInfo.Name != hash {
		t.Fatalf("detached default = %+v, %v; want commit %s", defaultInfo, err, hash)
	}
	explicitInfo, err := ResolveRefInfo(ctx, repoPath, "HEAD")
	if err != nil || explicitInfo.Kind != RefKindBranch || explicitInfo.Name != "HEAD" {
		t.Fatalf("explicit HEAD = %+v, %v; want branch HEAD", explicitInfo, err)
	}
}

func TestCheckBareRepository(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	if err := CheckBareRepository(ctx, repoPath); err != nil {
		t.Fatalf("valid bare repo rejected: %v", err)
	}
	if err := CheckBareRepository(ctx, t.TempDir()); err == nil {
		t.Fatal("non-repository directory accepted")
	}
}

func TestValidateRefNameContextRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := ValidateRefNameContext(ctx, "main")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ValidateRefNameContext cancelled error = %v, want context.Canceled", err)
	}
}

func TestCommandExitCodeRejectsStartFailures(t *testing.T) {
	err := &CommandError{Args: []string{"version"}, ExitCode: -1, Err: errors.New("git executable missing")}
	if code, ok := commandExitCode(err); ok {
		t.Fatalf("start failure reported as process exit code %d", code)
	}
}

func TestResolveRefDoesNotSubstituteAnotherBranchForMissingHEAD(t *testing.T) {
	ctx := context.Background()
	repoPath := setupTestRepo(t)
	prepareRepoWithCommit(t, repoPath)

	commits, err := GetCommits(ctx, repoPath, "main", 0, 1)
	if err != nil || len(commits) != 1 {
		t.Fatalf("get main commit: commits=%d err=%v", len(commits), err)
	}
	hash := commits[0].Hash
	if _, err := run(ctx, repoPath, "update-ref", "refs/heads/develop", hash); err != nil {
		t.Fatal(err)
	}
	if _, err := run(ctx, repoPath, "update-ref", "-d", "refs/heads/main"); err != nil {
		t.Fatal(err)
	}

	branch, err := GetDefaultBranch(ctx, repoPath)
	if err != nil || branch != "main" {
		t.Fatalf("default branch = %q, %v; want symbolic main", branch, err)
	}
	if _, err := ResolveRef(ctx, repoPath, ""); !errors.Is(err, ErrRefNotFound) {
		t.Fatalf("empty ref error = %v, want ErrRefNotFound", err)
	}
	if resolved, err := ResolveRef(ctx, repoPath, "develop"); err != nil || resolved != "develop" {
		t.Fatalf("explicit develop = %q, %v", resolved, err)
	}
}
