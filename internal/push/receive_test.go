package push

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
	"github.com/mmrzaf/gitman/internal/repo"
)

// receiveFixture is a repository Gitman knows about, whose bare Git
// repository a test moves refs in directly, and a hook serving pushes to
// it the way post-receive does.
type receiveFixture struct {
	db   *postgres.DB
	hook *Hook
	out  *strings.Builder
	bare string
	work string
}

func newReceiveFixture(t *testing.T) *receiveFixture {
	t.Helper()
	ctx := context.Background()
	database := pgtest.Open(t)
	store := git.NewStore(t.TempDir())
	t.Cleanup(store.Close)
	people := auth.NewService(database)
	repos := repo.NewService(database, store, "")
	person, err := people.Create(ctx, "darius", "correct-horse-battery", true, "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := repos.Create(ctx, "demo", "", "main", person.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []repo.Rule{
		{Kind: git.KindBranch, Pattern: "main", PushPolicy: repo.PushEveryone, RunOnPush: true},
		{Kind: git.KindTag, Pattern: "v*", PushPolicy: repo.PushEveryone, RunOnPush: true, AllowDelete: true},
	} {
		if err := repos.SaveRule(ctx, r.ID, rule, person.ID); err != nil {
			t.Fatal(err)
		}
	}
	bare, err := store.Path(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	gitRepo := git.OpenHookRepo(bare, nil)
	t.Cleanup(func() { gitRepo.Close() })

	work := t.TempDir()
	runGit(t, work, "init", "--quiet", "--initial-branch=main")
	out := &strings.Builder{}
	return &receiveFixture{
		db: database, out: out, bare: bare, work: work,
		hook: &Hook{
			DB: database, People: people, Repos: repos, CI: ci.NewService(database), Git: gitRepo,
			Ctx: Context{RepoID: r.ID, PersonID: person.ID}, PublicURL: "http://gitman.test", Out: out,
		},
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=T",
		"GIT_COMMITTER_EMAIL=t@x", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit makes a commit carrying a pipeline and pushes it straight into
// the bare repository as ref, the way Git has already moved a ref by the
// time post-receive runs. It returns the commit.
func (f *receiveFixture) commit(t *testing.T, message, ref string) string {
	t.Helper()
	pipeline := "image: alpine:3.20\nsteps:\n  - name: test\n    run: echo " + message + "\n"
	if err := os.WriteFile(filepath.Join(f.work, ci.FileName), []byte(pipeline), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.work, "add", "-A")
	runGit(t, f.work, "commit", "--quiet", "-m", message)
	runGit(t, f.work, "push", "--quiet", "--force", f.bare, "HEAD:"+ref)
	return runGit(t, f.work, "rev-parse", "HEAD")
}

func (f *receiveFixture) runs(t *testing.T) map[string]string {
	t.Helper()
	rows, err := f.db.Pool.Query(context.Background(), `SELECT commit_hash, status FROM runs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	runs := map[string]string{}
	for rows.Next() {
		var commit, status string
		if err := rows.Scan(&commit, &status); err != nil {
			t.Fatal(err)
		}
		runs[commit] = status
	}
	return runs
}

var zeroHash = strings.Repeat("0", 40)

// TestPostReceiveOfAnOvertakenPushQueuesNothing is two pushes to main
// landing back to back, whose hooks finish in the opposite order: the
// newer commit's run is queued first, then the older push's hook runs.
// It must not queue its now-stale commit — which would supersede, and
// cancel, the newer commit's run.
func TestPostReceiveOfAnOvertakenPushQueuesNothing(t *testing.T) {
	ctx := context.Background()
	f := newReceiveFixture(t)
	older := f.commit(t, "older", "refs/heads/main")
	newer := f.commit(t, "newer", "refs/heads/main")

	if err := f.hook.PostReceive(ctx, []Update{{Old: older, New: newer, Ref: "refs/heads/main", Kind: git.KindBranch, Name: "main"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.hook.PostReceive(ctx, []Update{{Old: zeroHash, New: older, Ref: "refs/heads/main", Kind: git.KindBranch, Name: "main"}}); err != nil {
		t.Fatal(err)
	}
	runs := f.runs(t)
	if len(runs) != 1 || runs[newer] != "queued" {
		t.Fatalf("runs = %v; want only the newer commit's, still queued", runs)
	}
	if !strings.Contains(f.out.String(), "branch main moved again before this push was recorded") {
		t.Fatalf("output = %q", f.out.String())
	}
}

// TestPostReceiveOfADeletedTagCancelsItsQueuedRun is a tag deleted while
// its run waits for a worker: the run must not go on to run, and ship, a
// tag that no longer exists.
func TestPostReceiveOfADeletedTagCancelsItsQueuedRun(t *testing.T) {
	ctx := context.Background()
	f := newReceiveFixture(t)
	tagged := f.commit(t, "release", "refs/tags/v1")
	if err := f.hook.PostReceive(ctx, []Update{{Old: zeroHash, New: tagged, Ref: "refs/tags/v1", Kind: git.KindTag, Name: "v1"}}); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.work, "push", "--quiet", f.bare, ":refs/tags/v1")
	if err := f.hook.PostReceive(ctx, []Update{{Old: tagged, New: zeroHash, Ref: "refs/tags/v1", Kind: git.KindTag, Name: "v1"}}); err != nil {
		t.Fatal(err)
	}
	var status, reason string
	if err := f.db.Pool.QueryRow(ctx, `SELECT status, reason FROM runs`).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" || reason != "The tag v1 was deleted." {
		t.Fatalf("run = %s %q; want cancelled because the tag was deleted", status, reason)
	}
}
