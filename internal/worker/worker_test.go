package worker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/config"

	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
)

func TestShortHash(t *testing.T) {
	if s := shortHash("1234567890"); s != "1234567" {
		t.Errorf("expected 1234567, got %s", s)
	}
	if s := shortHash("abc"); s != "abc" {
		t.Errorf("expected abc, got %s", s)
	}
}

func TestSelectRef(t *testing.T) {
	if ref := selectRef("", ""); ref != "HEAD" {
		t.Errorf("expected HEAD, got %s", ref)
	}
	if ref := selectRef("main", ""); ref != "main" {
		t.Errorf("expected main, got %s", ref)
	}
	if ref := selectRef("main", "v1.0"); ref != "v1.0" {
		t.Errorf("expected v1.0, got %s", ref)
	}
}

func TestResolveRepo(t *testing.T) {
	database, err := db.InitDB("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	// Create user and repo
	user, err := database.CreateUser(ctx, "owner", "OwnerPass1")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := database.CreateRepository(ctx, user.ID, "testrepo", "", false)
	if err != nil {
		t.Fatal(err)
	}

	info, ownerName, err := resolveRepo(ctx, database, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if ownerName != "owner" {
		t.Errorf("expected owner, got %s", ownerName)
	}
	if info.name != "testrepo" || info.id != repoID {
		t.Error("repo info mismatch")
	}
}

func TestResolveRepoNotFound(t *testing.T) {
	database, err := db.InitDB("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	_, _, err = resolveRepo(ctx, database, "nonexistent")
	if err == nil {
		t.Error("expected error")
	}
}

func TestRedactingWriterMasksSecretsAcrossWrites(t *testing.T) {
	var out bytes.Buffer
	writer := newRedactingWriter(&out, []string{"supersecret"})
	if _, err := writer.Write([]byte("token=super")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("secret done")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "supersecret") {
		t.Fatalf("secret leaked: %q", out.String())
	}
	if !strings.Contains(out.String(), "token=*** done") {
		t.Fatalf("unexpected redacted output: %q", out.String())
	}
}

func TestDirectoryUsageExceeds(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "payload"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	exceeded, err := directoryUsageExceeds(root, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !exceeded {
		t.Fatal("expected directory limit to be exceeded")
	}
}

func TestValidateWorkerConfigRejectsUnsafeHeartbeatRatio(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		ReposPath:           filepath.Join(root, "repos"),
		ArtifactsPath:       filepath.Join(root, "artifacts"),
		CacheRoot:           filepath.Join(root, "cache"),
		CILeaseTimeout:      30 * time.Second,
		CIHeartbeatInterval: 15 * time.Second,
		CIContainerUser:     "1000:1000",
		CIWorkspaceRoot:     filepath.Join(root, "workspaces"),
	}
	if err := validateWorkerConfig(cfg); err == nil {
		t.Fatal("expected unsafe heartbeat ratio rejection")
	}
	cfg.CIHeartbeatInterval = 10 * time.Second
	if err := validateWorkerConfig(cfg); err != nil {
		t.Fatalf("safe heartbeat ratio rejected: %v", err)
	}
}

func TestAttemptMetadataRoundTrip(t *testing.T) {
	workspace := t.TempDir()
	if err := writeAttemptMetadata(workspace, "run-id", "attempt-id"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, attemptMetadataFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "run-id\nattempt-id\n" {
		t.Fatalf("unexpected metadata: %q", data)
	}
}

func TestEnsurePrivateDirTightensExistingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "existing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("expected 0700, got %o", got)
	}
}

func TestTranslateDockerHostPath(t *testing.T) {
	cfg := &config.Config{
		CIWorkerPathPrefix: "/data",
		CIHostPathPrefix:   "/srv/gitman/data",
	}
	got, err := translateDockerHostPath(cfg, "/data/ci/workspaces/run/src")
	if err != nil {
		t.Fatal(err)
	}
	want := "/srv/gitman/data/ci/workspaces/run/src"
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
	if _, err := translateDockerHostPath(cfg, "/tmp/outside"); err == nil {
		t.Fatal("expected path-prefix escape rejection")
	}
}

func TestCleanupStaleWorkspacesRemovesOldUnmarkedDirectory(t *testing.T) {
	database, err := db.InitDB("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	root := t.TempDir()
	workspace := filepath.Join(root, "gitman-run-orphan")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(workspace, old, old); err != nil {
		t.Fatal(err)
	}
	cleanupStaleWorkspaces(context.Background(), root, database, time.Now().Add(-2*time.Minute))
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("stale unmarked workspace was not removed: %v", err)
	}
}

func TestCleanupStaleWorkspacesKeepsRecentUnmarkedDirectory(t *testing.T) {
	database, err := db.InitDB("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	root := t.TempDir()
	workspace := filepath.Join(root, "gitman-run-active")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	cleanupStaleWorkspaces(context.Background(), root, database, time.Now().Add(-2*time.Minute))
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("recent unmarked workspace was removed: %v", err)
	}
}

func TestParseCIConfigAcceptsDockerFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), ciConfigFile)
	data := []byte("image: docker:29-cli\ndocker: true\nsteps:\n  - name: test\n    run: docker version\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseCIConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Docker {
		t.Fatal("expected docker socket access to be enabled")
	}
}

func TestAppendDockerSocketArgsRequiresOperatorOptIn(t *testing.T) {
	_, err := appendDockerSocketArgs(nil, &config.Config{}, true, true)
	if err == nil || !strings.Contains(err.Error(), "GITMAN_CI_ALLOW_DOCKER_SOCKET") {
		t.Fatalf("expected operator opt-in error, got %v", err)
	}
}

func TestAppendDockerSocketArgsRequiresRefApproval(t *testing.T) {
	_, err := appendDockerSocketArgs(nil, &config.Config{CIAllowDockerSocket: true}, true, false)
	if err == nil || !strings.Contains(err.Error(), "CI ref") {
		t.Fatalf("expected ref approval error, got %v", err)
	}
}

func TestAppendDockerSocketArgsMountsSocketAndAddsGroup(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	args, err := appendDockerSocketArgs([]string{"run"}, &config.Config{
		CIAllowDockerSocket: true,
		CIDockerSocketPath:  socketPath,
	}, true, true)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		socketPath + ":/var/run/docker.sock",
		"--group-add",
		"DOCKER_HOST=unix:///var/run/docker.sock",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in Docker args: %s", want, joined)
		}
	}
}

func TestParseCIConfigRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), ciConfigFile)
	data := []byte("image: alpine:3.20\nunknown: true\nsteps:\n  - name: test\n    run: echo ok\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseCIConfig(path); err == nil {
		t.Fatal("expected unknown CI config field rejection")
	}
}

func TestParseCIConfigRejectsMultipleDocuments(t *testing.T) {
	path := filepath.Join(t.TempDir(), ciConfigFile)
	data := []byte("image: alpine:3.20\nsteps:\n  - name: test\n    run: echo ok\n---\nimage: alpine:3.20\nsteps:\n  - name: second\n    run: echo no\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseCIConfig(path); err == nil {
		t.Fatal("expected multiple CI config document rejection")
	}
}

func TestValidateWorkerConfigRejectsRootContainerUser(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		ReposPath:           filepath.Join(root, "repos"),
		ArtifactsPath:       filepath.Join(root, "artifacts"),
		CacheRoot:           filepath.Join(root, "cache"),
		CIWorkspaceRoot:     filepath.Join(root, "workspaces"),
		CILeaseTimeout:      30 * time.Second,
		CIHeartbeatInterval: 10 * time.Second,
		CIContainerUser:     "0:0",
	}
	if err := validateWorkerConfig(cfg); err == nil {
		t.Fatal("expected root CI container user rejection")
	}
}

func TestParseCIConfigRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real.yml")
	if err := os.WriteFile(target, []byte("image: alpine:3.20\nsteps:\n  - name: test\n    run: echo ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, ciConfigFile)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := parseCIConfig(link); err == nil {
		t.Fatal("expected symlinked CI config rejection")
	}
}

func TestReconcileManagedContainersIncludesDockerOutput(t *testing.T) {
	database, err := db.InitDB("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	binDir := t.TempDir()
	dockerPath := filepath.Join(binDir, "docker")
	if err := os.WriteFile(dockerPath, []byte("#!/bin/sh\necho 'permission denied on docker socket' >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fmt.Sprintf("%s%c%s", binDir, os.PathListSeparator, os.Getenv("PATH")))

	err = reconcileManagedContainers(context.Background(), database, time.Now())
	if err == nil {
		t.Fatal("expected docker list failure")
	}
	if !strings.Contains(err.Error(), "permission denied on docker socket") {
		t.Fatalf("expected Docker stderr in error, got %v", err)
	}
}

func TestCloneFallsBackForHistoricalCommit(t *testing.T) {
	root := t.TempDir()
	reposRoot := filepath.Join(root, "repos")
	bare := filepath.Join(reposRoot, "owner", "repo.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, "", "init", "--bare", bare)

	work := filepath.Join(root, "source")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, work, "init")
	runGitTest(t, work, "config", "user.email", "test@example.com")
	runGitTest(t, work, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(work, "payload"), []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, work, "add", "payload")
	runGitTest(t, work, "commit", "-m", "first")
	first := strings.TrimSpace(runGitTest(t, work, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(work, "payload"), []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, work, "commit", "-am", "second")
	branch := strings.TrimSpace(runGitTest(t, work, "branch", "--show-current"))
	runGitTest(t, work, "remote", "add", "origin", bare)
	runGitTest(t, work, "push", "origin", branch)

	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	j := &job{
		cfg: &config.Config{
			ReposPath:           reposRoot,
			CIWorkspaceMaxBytes: 100 * 1024 * 1024,
		},
		run:       &models.CIRun{Branch: branch, CommitHash: first},
		repo:      &repoInfo{name: "repo"},
		owner:     "owner",
		workspace: workspace,
		checkout:  filepath.Join(workspace, "src"),
		logWriter: io.Discard,
	}
	if err := j.clone(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(runGitTest(t, j.checkout, "rev-parse", "HEAD"))
	if got != first {
		t.Fatalf("expected historical commit %s, got %s", first, got)
	}
}

func TestCloneFetchesUnadvertisedExactCommit(t *testing.T) {
	root := t.TempDir()
	reposRoot := filepath.Join(root, "repos")
	bare := filepath.Join(reposRoot, "owner", "repo.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, "", "init", "--bare", bare)

	work := filepath.Join(root, "source")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, work, "init")
	runGitTest(t, work, "config", "user.email", "test@example.com")
	runGitTest(t, work, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(work, "payload"), []byte("orphaned"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, work, "add", "payload")
	runGitTest(t, work, "commit", "-m", "orphan candidate")
	commit := strings.TrimSpace(runGitTest(t, work, "rev-parse", "HEAD"))
	branch := strings.TrimSpace(runGitTest(t, work, "branch", "--show-current"))
	runGitTest(t, work, "remote", "add", "origin", bare)
	runGitTest(t, work, "push", "origin", branch)
	// Remove the only advertised ref while leaving the commit object in the bare
	// repository. Exact-commit CI must still be able to execute it.
	runGitTest(t, "", "--git-dir="+bare, "update-ref", "-d", "refs/heads/"+branch)

	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	j := &job{
		cfg: &config.Config{
			ReposPath:           reposRoot,
			CIWorkspaceMaxBytes: 100 * 1024 * 1024,
		},
		run:       &models.CIRun{CommitHash: commit},
		repo:      &repoInfo{name: "repo"},
		owner:     "owner",
		workspace: workspace,
		checkout:  filepath.Join(workspace, "src"),
		logWriter: io.Discard,
	}
	if err := j.clone(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(runGitTest(t, j.checkout, "rev-parse", "HEAD"))
	if got != commit {
		t.Fatalf("expected exact commit %s, got %s", commit, got)
	}
	if gotRemote := strings.TrimSpace(runGitTest(t, j.checkout, "remote", "get-url", "origin")); gotRemote == "" {
		t.Fatal("exact commit checkout lost origin remote")
	}
}

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestCIFailureSummaryIncludesExitCode(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 7").Run()
	if got := ciFailureSummary(context.Background(), err); got != "Pipeline exited with code 7" {
		t.Fatalf("unexpected summary: %q", got)
	}
}

func TestCIFailureSummaryUsesTypedDockerImageError(t *testing.T) {
	got := ciFailureSummary(context.Background(), fmt.Errorf("%w: alpine:test", errDockerImageUnavailable))
	if got != "Runner image is unavailable or invalid on the worker" {
		t.Fatalf("summary = %q", got)
	}
}

func TestWaitForPollReturnsImmediatelyWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if waitForPoll(ctx) {
		t.Fatal("cancelled poll wait reported success")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("cancelled poll wait did not return promptly")
	}
}

func TestLimitedWriterTruncatesOnceAndReportsInputConsumed(t *testing.T) {
	var out bytes.Buffer
	lw := &limitedWriter{w: &out, max: 4}

	if n, err := lw.Write([]byte("abcdef")); err != nil || n != 6 {
		t.Fatalf("first write = (%d, %v), want (6, nil)", n, err)
	}
	if n, err := lw.Write([]byte("gh")); err != nil || n != 2 {
		t.Fatalf("second write = (%d, %v), want (2, nil)", n, err)
	}
	got := out.String()
	if !strings.HasPrefix(got, "abcd") {
		t.Fatalf("output prefix = %q, want %q", got, "abcd")
	}
	if strings.Count(got, "log limit reached") != 1 {
		t.Fatalf("limit notice count = %d, output %q", strings.Count(got, "log limit reached"), got)
	}
}

func TestLimitedWriterPropagatesNoticeWriteFailure(t *testing.T) {
	w := &failAfterWriter{remaining: 4}
	lw := &limitedWriter{w: w, max: 4}
	if n, err := lw.Write([]byte("abcdef")); err == nil || n != 4 {
		t.Fatalf("write = (%d, %v), want 4 and an error", n, err)
	}
}

type failAfterWriter struct {
	remaining int
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, fmt.Errorf("storage unavailable")
	}
	if len(p) > w.remaining {
		p = p[:w.remaining]
	}
	n := len(p)
	w.remaining -= n
	return n, nil
}
