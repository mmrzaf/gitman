package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

// gitCmd runs git in dir with a fixed identity, failing the test on error.
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(baseEnv(),
		"GIT_AUTHOR_NAME=Test Author", "GIT_AUTHOR_EMAIL=author@example.com",
		"GIT_COMMITTER_NAME=Test Committer", "GIT_COMMITTER_EMAIL=committer@example.com",
		"GIT_AUTHOR_DATE=2026-01-02T03:04:05+03:30", "GIT_COMMITTER_DATE=2026-01-02T03:04:05+03:30",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// fixture is a store with one repository and a working clone to commit
// from.
type fixture struct {
	store *Store
	repo  *Repo
	work  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	requireGit(t)
	root := t.TempDir()
	store := NewStore(filepath.Join(root, "repos"))
	t.Cleanup(store.Close)
	if err := store.Create(context.Background(), "repo-1", "main"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	repo, err := store.Open("repo-1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	work := filepath.Join(root, "work")
	gitCmd(t, root, "init", "--quiet", "--initial-branch=main", work)
	return &fixture{store: store, repo: repo, work: work}
}

func (f *fixture) write(t *testing.T, path, content string) {
	t.Helper()
	full := filepath.Join(f.work, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) commit(t *testing.T, message string) string {
	t.Helper()
	gitCmd(t, f.work, "add", "-A")
	gitCmd(t, f.work, "commit", "--quiet", "--allow-empty", "-m", message)
	return gitCmd(t, f.work, "rev-parse", "HEAD")
}

func (f *fixture) push(t *testing.T, refspecs ...string) {
	t.Helper()
	args := append([]string{"push", "--quiet", "--force", f.repo.path}, refspecs...)
	gitCmd(t, f.work, args...)
}

func findRef(refs []Ref, kind Kind, name string) (Ref, bool) {
	for _, r := range refs {
		if r.Kind == kind && r.Name == name {
			return r, true
		}
	}
	return Ref{}, false
}
