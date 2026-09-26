package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@x",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// sourceRepo builds a repository with two commits and returns its path
// and the first, older commit.
func sourceRepo(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	dir := t.TempDir()
	gitIn(t, dir, "init", "--quiet", "--initial-branch=main")
	// A local fetch is served by this repository's own upload-pack, which
	// must allow what Gitman's server allows: a reachable commit by hash.
	gitIn(t, dir, "config", "uploadpack.allowReachableSHA1InWant", "true")
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("one\n"), 0o644)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "--quiet", "-m", "one")
	first := gitIn(t, dir, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("two\n"), 0o644)
	gitIn(t, dir, "commit", "--quiet", "-am", "two")
	return dir, first
}

func TestCheckoutFetchesExactlyTheCommit(t *testing.T) {
	src, first := sourceRepo(t)
	ws, err := createWorkspace(t.TempDir(), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.checkout(context.Background(), "file://"+src, "gitman-run", "token", first); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(ws.source(), "file.txt"))
	if err != nil || string(data) != "one\n" {
		t.Fatalf("checked-out file = %q, %v", data, err)
	}
	if head := gitIn(t, ws.source(), "rev-parse", "HEAD"); head != first {
		t.Fatalf("HEAD = %s, want %s", head, first)
	}
	config, _ := os.ReadFile(filepath.Join(ws.source(), ".git", "config"))
	if strings.Contains(string(config), "Authorization") || strings.Contains(string(config), "token") {
		t.Fatal("credentials were written to .git/config")
	}
	if err := ws.remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ws.root); !os.IsNotExist(err) {
		t.Fatal("workspace not removed")
	}
}

func TestCheckoutReportsAMissingCommit(t *testing.T) {
	src, _ := sourceRepo(t)
	ws, err := createWorkspace(t.TempDir(), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	err = ws.checkout(context.Background(), "file://"+src, "gitman-run", "token", strings.Repeat("a", 40))
	if err == nil || !strings.Contains(err.Error(), "git fetch") {
		t.Fatalf("checkout of a missing commit = %v", err)
	}
}

func TestReadSummary(t *testing.T) {
	ws, err := createWorkspace(t.TempDir(), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	content := "target=production\nimage=app:1.4.2\nnot a pair\nbad key!=x\nimage=app:1.4.3\ntoken=uses hunter2-secret\n" +
		"long=" + strings.Repeat("v", 2000) + "\n"
	if err := os.WriteFile(ws.summary(), []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}
	got, err := ws.readSummary(newSecretMasker(map[string]string{"T": "hunter2-secret"}))
	if err != nil {
		t.Fatal(err)
	}
	if got["target"] != "production" || got["image"] != "app:1.4.3" || got["token"] != "uses ***" ||
		len(got["long"]) != maxSummaryValueLen || len(got) != 4 {
		t.Fatalf("summary = %v", got)
	}
}

// TestReadSummaryRefusesWhatIsNotAFile covers a step that replaces
// $GITMAN_SUMMARY: a symbolic link to one of the worker's own files must
// not be followed, and a named pipe must not block the worker.
func TestReadSummaryRefusesWhatIsNotAFile(t *testing.T) {
	masker := newSecretMasker(nil)

	ws, err := createWorkspace(t.TempDir(), "run-link")
	if err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(t.TempDir(), "worker-only")
	if err := os.WriteFile(private, []byte("GITMAN_DATABASE_URL=postgres://gitman:secret@db/gitman\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ws.summary()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, ws.summary()); err != nil {
		t.Fatal(err)
	}
	got, err := ws.readSummary(masker)
	if err == nil || len(got) != 0 {
		t.Fatalf("summary through a symbolic link = %v, %v; want an error and nothing read", got, err)
	}

	ws, err = createWorkspace(t.TempDir(), "run-fifo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ws.summary()); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(ws.summary(), 0o666); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := ws.readSummary(masker)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a named pipe was read as a summary")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a named pipe as the summary blocked")
	}
}

// TestReadSummaryKeepsValuesStorable covers values PostgreSQL text
// cannot hold as written: a NUL byte, invalid UTF-8, and a character cut
// in half by the length limit.
func TestReadSummaryKeepsValuesStorable(t *testing.T) {
	ws, err := createWorkspace(t.TempDir(), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	content := "nul=a\x00b\nlatin=caf\xe9\ncut=" + strings.Repeat("x", maxSummaryValueLen-1) + "é\n"
	if err := os.WriteFile(ws.summary(), []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}
	got, err := ws.readSummary(newSecretMasker(nil))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range got {
		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			t.Errorf("%s = %q is not storable", key, value)
		}
	}
	if got["nul"] != "a�b" || got["latin"] != "caf�" || len(got["cut"]) != maxSummaryValueLen-1 {
		t.Fatalf("summary = %q", got)
	}
}

// TestReadSummarySkipsAnOverlongLine keeps what a run recorded around one
// line too long to read: that line is dropped, not the whole summary.
func TestReadSummarySkipsAnOverlongLine(t *testing.T) {
	ws, err := createWorkspace(t.TempDir(), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	content := "version=1.2.3\nnotes=" + strings.Repeat("n", 70<<10) + "\nimage=app:1\n"
	if err := os.WriteFile(ws.summary(), []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}
	got, err := ws.readSummary(newSecretMasker(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["version"] != "1.2.3" || got["image"] != "app:1" {
		t.Fatalf("summary = %v; want version and image, without the overlong notes", got)
	}
}

// TestPrepareWorkspaceRootIsPrivate keeps the directory holding every
// run's world-writable workspace out of reach of the host's other users,
// including when it already existed with a looser mode.
func TestPrepareWorkspaceRootIsPrivate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := PrepareWorkspaceRoot(root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Fatalf("workspace root mode = %o, want 700", mode)
	}
}
