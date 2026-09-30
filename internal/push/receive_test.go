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

// prFixture is a repository for testing PreReceive directly: unlike
// receiveFixture, its rules, visibility and pushing person all start
// empty, so each test sets exactly what it needs.
type prFixture struct {
	repos  *repo.Service
	people *auth.Service
	repo   *repo.Repo
	hook   *Hook
	out    *strings.Builder
	bare   string
	work   string
}

func newPRFixture(t *testing.T) *prFixture {
	t.Helper()
	ctx := context.Background()
	database := pgtest.Open(t)
	store := git.NewStore(t.TempDir())
	t.Cleanup(store.Close)
	people := auth.NewService(database)
	repos := repo.NewService(database, store, "")
	owner, err := people.Create(ctx, "owner", "correct-horse-battery", true, "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := repos.Create(ctx, "demo", "", "main", owner.ID)
	if err != nil {
		t.Fatal(err)
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
	return &prFixture{
		repos: repos, people: people, repo: r, out: out, bare: bare, work: work,
		hook: &Hook{
			DB: database, People: people, Repos: repos, CI: ci.NewService(database), Git: gitRepo,
			Ctx: Context{RepoID: r.ID, PersonID: owner.ID}, PublicURL: "http://gitman.test", Out: out,
		},
	}
}

// asPerson runs the hook as a different, freshly created member (never
// an admin, so push-policy and read-visibility rules are exercised
// rather than bypassed).
func (f *prFixture) asPerson(t *testing.T, username string) {
	t.Helper()
	p, err := f.people.Create(context.Background(), username, "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	f.hook.Ctx.PersonID = p.ID
}

// commitFile pushes a real commit carrying a trivial pipeline directly
// into the bare repository, the way Git has already moved a ref by the
// time pre-receive/post-receive run, and returns its hash.
func (f *prFixture) commitFile(t *testing.T, message, ref string) string {
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

func (f *prFixture) preReceive(t *testing.T, updates []Update) error {
	t.Helper()
	return f.hook.PreReceive(context.Background(), updates)
}

func TestPreReceiveRejectsADisabledPerson(t *testing.T) {
	f := newPRFixture(t)
	f.asPerson(t, "alice")
	if err := f.people.Disable(context.Background(), f.hook.Ctx.PersonID, ""); err != nil {
		t.Fatal(err)
	}
	commit := f.commitFile(t, "one", "refs/heads/feature")
	if err := f.preReceive(t, []Update{{Old: zeroHash, New: commit, Ref: "refs/heads/feature", Kind: git.KindBranch, Name: "feature"}}); err != ErrRejected {
		t.Fatalf("PreReceive = %v, want ErrRejected", err)
	}
	if !strings.Contains(f.out.String(), "alice is disabled and cannot push") {
		t.Errorf("output = %q", f.out.String())
	}
}

func TestPreReceiveRejectsAPushToARepositoryThePersonCannotRead(t *testing.T) {
	f := newPRFixture(t)
	if err := f.repos.SetVisibility(context.Background(), f.repo.ID, repo.VisibilityRestricted, ""); err != nil {
		t.Fatal(err)
	}
	f.asPerson(t, "alice") // not on the reader list
	commit := f.commitFile(t, "one", "refs/heads/feature")
	if err := f.preReceive(t, []Update{{Old: zeroHash, New: commit, Ref: "refs/heads/feature", Kind: git.KindBranch, Name: "feature"}}); err != ErrRejected {
		t.Fatalf("PreReceive = %v, want ErrRejected", err)
	}
	if !strings.Contains(f.out.String(), "alice cannot push to a repository they cannot read") {
		t.Errorf("output = %q", f.out.String())
	}

	// A reader may push once granted, even though the repository stays
	// restricted.
	f.out.Reset()
	if err := f.repos.AddReader(context.Background(), f.repo.ID, f.hook.Ctx.PersonID, "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.preReceive(t, []Update{{Old: zeroHash, New: commit, Ref: "refs/heads/feature", Kind: git.KindBranch, Name: "feature"}}); err != nil {
		t.Fatalf("PreReceive (reader) = %v", err)
	}
}

func TestPreReceiveRejectsAnUnrecognizedRefKind(t *testing.T) {
	f := newPRFixture(t)
	commit := f.commitFile(t, "one", "refs/notes/commits")
	if err := f.preReceive(t, []Update{{Old: zeroHash, New: commit, Ref: "refs/notes/commits"}}); err != ErrRejected {
		t.Fatalf("PreReceive = %v, want ErrRejected", err)
	}
	if !strings.Contains(f.out.String(), "only branches") {
		t.Errorf("output = %q", f.out.String())
	}
}

func TestPreReceiveRejectsAnInvalidNewRefName(t *testing.T) {
	f := newPRFixture(t)
	commit := f.commitFile(t, "one", "refs/heads/tmp")
	if err := f.preReceive(t, []Update{{Old: zeroHash, New: commit, Ref: "refs/heads/a..b", Kind: git.KindBranch, Name: "a..b"}}); err != ErrRejected {
		t.Fatalf("PreReceive = %v, want ErrRejected", err)
	}
}

func TestPreReceiveRejectsANewRefNameLookingLikeACommitHash(t *testing.T) {
	f := newPRFixture(t)
	commit := f.commitFile(t, "one", "refs/heads/tmp")
	hashLike := strings.Repeat("a", 40)
	if err := f.preReceive(t, []Update{{Old: zeroHash, New: commit, Ref: "refs/heads/" + hashLike, Kind: git.KindBranch, Name: hashLike}}); err != ErrRejected {
		t.Fatalf("PreReceive = %v, want ErrRejected", err)
	}
}

func TestPreReceiveRejectsABranchAndTagSharingAName(t *testing.T) {
	f := newPRFixture(t)
	f.commitFile(t, "one", "refs/tags/shared")
	commit := f.commitFile(t, "two", "refs/heads/tmp")
	if err := f.preReceive(t, []Update{{Old: zeroHash, New: commit, Ref: "refs/heads/shared", Kind: git.KindBranch, Name: "shared"}}); err != ErrRejected {
		t.Fatalf("PreReceive = %v, want ErrRejected", err)
	}
}

func TestPreReceivePushPolicy(t *testing.T) {
	cases := []struct {
		name    string
		policy  repo.PushPolicy
		people  []string // usernames given push access, for PushPeople
		pusher  string
		allowed bool
	}{
		{"everyone allows anyone", repo.PushEveryone, nil, "alice", true},
		{"admins denies a member", repo.PushAdmins, nil, "alice", false},
		{"people denies someone not listed", repo.PushPeople, []string{"bob"}, "alice", false},
		{"people allows someone listed", repo.PushPeople, []string{"alice"}, "alice", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newPRFixture(t)
			f.asPerson(t, c.pusher)
			rule := repo.Rule{Kind: git.KindBranch, Pattern: "feature", PushPolicy: c.policy}
			for _, username := range c.people {
				// The pusher itself may already be one of the named
				// people, so look up before creating.
				p, err := f.people.GetByUsername(context.Background(), username)
				if err != nil {
					p, err = f.people.Create(context.Background(), username, "correct-horse-battery", false, "")
					if err != nil {
						t.Fatal(err)
					}
				}
				rule.PushPeople = append(rule.PushPeople, p.ID)
			}
			if err := f.repos.SaveRule(context.Background(), f.repo.ID, rule, ""); err != nil {
				t.Fatal(err)
			}
			commit := f.commitFile(t, "one", "refs/heads/feature")
			err := f.preReceive(t, []Update{{Old: zeroHash, New: commit, Ref: "refs/heads/feature", Kind: git.KindBranch, Name: "feature"}})
			if c.allowed && err != nil {
				t.Errorf("PreReceive = %v, want allowed", err)
			}
			if !c.allowed && err != ErrRejected {
				t.Errorf("PreReceive = %v, want ErrRejected", err)
			}
		})
	}
}

func TestPreReceiveFollowsTheRepositoryDefaultPushWhenNoRuleMatches(t *testing.T) {
	f := newPRFixture(t)
	if err := f.repos.SetDefaultPush(context.Background(), f.repo.ID, repo.PushAdmins, nil, ""); err != nil {
		t.Fatal(err)
	}
	f.asPerson(t, "alice")
	commit := f.commitFile(t, "one", "refs/heads/unmatched")
	if err := f.preReceive(t, []Update{{Old: zeroHash, New: commit, Ref: "refs/heads/unmatched", Kind: git.KindBranch, Name: "unmatched"}}); err != ErrRejected {
		t.Fatalf("PreReceive (member, default admins) = %v, want ErrRejected", err)
	}

	// Force and delete stay allowed on an unmatched ref regardless of the
	// default push policy: only whether the push itself is allowed
	// changes.
	f.out.Reset()
	admin, err := f.people.GetByUsername(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	f.hook.Ctx.PersonID = admin.ID
	if err := f.preReceive(t, []Update{{Old: zeroHash, New: commit, Ref: "refs/heads/unmatched", Kind: git.KindBranch, Name: "unmatched"}}); err != nil {
		t.Fatalf("PreReceive (admin, default admins) = %v", err)
	}
}

func TestPreReceiveProtectsTheDefaultBranchFromDeletion(t *testing.T) {
	f := newPRFixture(t)
	if err := f.repos.SaveRule(context.Background(), f.repo.ID, repo.Rule{
		Kind: git.KindBranch, Pattern: "main", PushPolicy: repo.PushEveryone, AllowDelete: true,
	}, ""); err != nil {
		t.Fatal(err)
	}
	commit := f.commitFile(t, "one", "refs/heads/main")
	if err := f.preReceive(t, []Update{{Old: commit, New: zeroHash, Ref: "refs/heads/main", Kind: git.KindBranch, Name: "main"}}); err != ErrRejected {
		t.Fatalf("PreReceive = %v, want ErrRejected: the default branch must never be deletable, even with AllowDelete", err)
	}
	if !strings.Contains(f.out.String(), "default branch cannot be deleted") {
		t.Errorf("output = %q", f.out.String())
	}
}

func TestPreReceiveDeleteRequiresAllowDelete(t *testing.T) {
	f := newPRFixture(t)
	if err := f.repos.SaveRule(context.Background(), f.repo.ID, repo.Rule{
		Kind: git.KindBranch, Pattern: "feature", PushPolicy: repo.PushEveryone,
	}, ""); err != nil {
		t.Fatal(err)
	}
	commit := f.commitFile(t, "one", "refs/heads/feature")
	if err := f.preReceive(t, []Update{{Old: commit, New: zeroHash, Ref: "refs/heads/feature", Kind: git.KindBranch, Name: "feature"}}); err != ErrRejected {
		t.Fatalf("PreReceive = %v, want ErrRejected", err)
	}

	if err := f.repos.SaveRule(context.Background(), f.repo.ID, repo.Rule{
		Kind: git.KindBranch, Pattern: "feature", PushPolicy: repo.PushEveryone, AllowDelete: true,
	}, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.preReceive(t, []Update{{Old: commit, New: zeroHash, Ref: "refs/heads/feature", Kind: git.KindBranch, Name: "feature"}}); err != nil {
		t.Fatalf("PreReceive (AllowDelete) = %v", err)
	}
}

func TestPreReceiveForcePushRequiresAllowForce(t *testing.T) {
	f := newPRFixture(t)
	if err := f.repos.SaveRule(context.Background(), f.repo.ID, repo.Rule{
		Kind: git.KindBranch, Pattern: "feature", PushPolicy: repo.PushEveryone,
	}, ""); err != nil {
		t.Fatal(err)
	}
	first := f.commitFile(t, "one", "refs/heads/feature")
	// A second, unrelated commit, so moving "feature" to it is not a
	// fast-forward.
	runGit(t, f.work, "checkout", "--quiet", "--orphan", "other")
	runGit(t, f.work, "commit", "--quiet", "--allow-empty", "-m", "unrelated")
	rewritten := runGit(t, f.work, "rev-parse", "HEAD")
	runGit(t, f.work, "push", "--quiet", "--force", f.bare, "HEAD:refs/heads/feature")

	if err := f.preReceive(t, []Update{{Old: first, New: rewritten, Ref: "refs/heads/feature", Kind: git.KindBranch, Name: "feature"}}); err != ErrRejected {
		t.Fatalf("PreReceive = %v, want ErrRejected: a non-fast-forward update needs AllowForce", err)
	}

	if err := f.repos.SaveRule(context.Background(), f.repo.ID, repo.Rule{
		Kind: git.KindBranch, Pattern: "feature", PushPolicy: repo.PushEveryone, AllowForce: true,
	}, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.preReceive(t, []Update{{Old: first, New: rewritten, Ref: "refs/heads/feature", Kind: git.KindBranch, Name: "feature"}}); err != nil {
		t.Fatalf("PreReceive (AllowForce) = %v", err)
	}
}

func TestPreReceiveMovingATagRequiresAllowForce(t *testing.T) {
	f := newPRFixture(t)
	if err := f.repos.SaveRule(context.Background(), f.repo.ID, repo.Rule{
		Kind: git.KindTag, Pattern: "v1", PushPolicy: repo.PushEveryone,
	}, ""); err != nil {
		t.Fatal(err)
	}
	first := f.commitFile(t, "one", "refs/tags/v1")
	second := f.commitFile(t, "two", "refs/tags/v1")

	if err := f.preReceive(t, []Update{{Old: first, New: second, Ref: "refs/tags/v1", Kind: git.KindTag, Name: "v1"}}); err != ErrRejected {
		t.Fatalf("PreReceive = %v, want ErrRejected: moving an existing tag needs AllowForce", err)
	}

	if err := f.repos.SaveRule(context.Background(), f.repo.ID, repo.Rule{
		Kind: git.KindTag, Pattern: "v1", PushPolicy: repo.PushEveryone, AllowForce: true,
	}, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.preReceive(t, []Update{{Old: first, New: second, Ref: "refs/tags/v1", Kind: git.KindTag, Name: "v1"}}); err != nil {
		t.Fatalf("PreReceive (AllowForce) = %v", err)
	}
}

func TestPreReceiveReportsEveryRejectionNotJustTheFirst(t *testing.T) {
	f := newPRFixture(t)
	badName := f.commitFile(t, "one", "refs/heads/tmp")
	if err := f.preReceive(t, []Update{
		{Old: zeroHash, New: badName, Ref: "refs/heads/a..b", Kind: git.KindBranch, Name: "a..b"},
		{Old: zeroHash, New: badName, Ref: "refs/notes/x"},
	}); err != ErrRejected {
		t.Fatalf("PreReceive = %v, want ErrRejected", err)
	}
	out := f.out.String()
	if strings.Count(out, "refs/heads/a..b") == 0 || strings.Count(out, "refs/notes/x") == 0 {
		t.Fatalf("output = %q, want both rejections reported", out)
	}
}

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

// TestPostReceiveSaysWhyARefStartedNoRun is a push to refs that start no
// run: one no rule matches, and one whose rule does not have "run" on.
// Each gets a line saying so; a ref that does run gets none.
func TestPostReceiveSaysWhyARefStartedNoRun(t *testing.T) {
	ctx := context.Background()
	f := newReceiveFixture(t)
	if err := f.hook.Repos.SaveRule(ctx, f.hook.Ctx.RepoID, repo.Rule{Kind: git.KindBranch, Pattern: "release/*", PushPolicy: repo.PushEveryone}, ""); err != nil {
		t.Fatal(err)
	}
	head := f.commit(t, "one", "refs/heads/main")
	runGit(t, f.work, "push", "--quiet", f.bare, "HEAD:refs/heads/develop", "HEAD:refs/heads/release/1")

	if err := f.hook.PostReceive(ctx, []Update{
		{Old: zeroHash, New: head, Ref: "refs/heads/main", Kind: git.KindBranch, Name: "main"},
		{Old: zeroHash, New: head, Ref: "refs/heads/develop", Kind: git.KindBranch, Name: "develop"},
		{Old: zeroHash, New: head, Ref: "refs/heads/release/1", Kind: git.KindBranch, Name: "release/1"},
	}); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	for _, want := range []string{
		"run #1 queued for branch main",
		`no run for branch develop: no ref rule matches it, and only a rule with "run" on starts one.`,
		`no run for branch release/1: the rule for branch "release/*" does not have "run" on.`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "no run for branch main") {
		t.Errorf("a ref that ran was reported as not running:\n%s", out)
	}
}

// refusalRows reads back what PreReceive recorded about refused pushes:
// each refused ref and its reason, in the order reported.
func (f *prFixture) refusalRows(t *testing.T) (pushes int, refs []refusal) {
	t.Helper()
	ctx := context.Background()
	if err := f.hook.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM push_refusals`).Scan(&pushes); err != nil {
		t.Fatal(err)
	}
	rows, err := f.hook.DB.Pool.Query(ctx, `SELECT ref, reason FROM push_refusal_refs ORDER BY refusal_id, position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var r refusal
		if err := rows.Scan(&r.ref, &r.reason); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, r)
	}
	return pushes, refs
}

func TestPreReceiveRecordsWhyAPushWasRefused(t *testing.T) {
	f := newPRFixture(t)
	ctx := context.Background()
	if err := f.repos.SaveRule(ctx, f.repo.ID, repo.Rule{Kind: git.KindBranch, Pattern: "main", PushPolicy: repo.PushAdmins}, ""); err != nil {
		t.Fatal(err)
	}
	f.asPerson(t, "alice")
	f.hook.Ctx.RemoteAddr = "203.0.113.9"
	commit := f.commitFile(t, "one", "refs/heads/tmp")

	err := f.preReceive(t, []Update{
		{Old: zeroHash, New: commit, Ref: "refs/heads/main", Kind: git.KindBranch, Name: "main"},
		{Old: zeroHash, New: commit, Ref: "refs/heads/fine", Kind: git.KindBranch, Name: "fine"},
		{Old: zeroHash, New: commit, Ref: "refs/heads/a..b", Kind: git.KindBranch, Name: "a..b"},
		{Old: zeroHash, New: commit, Ref: "refs/notes/x"},
	})
	if err != ErrRejected {
		t.Fatalf("PreReceive = %v, want ErrRejected", err)
	}

	// What the pusher is told is unchanged: every reason, ref by ref.
	want := "Gitman refused this push:\n" +
		"  refs/heads/main: the rule for branch \"main\" does not allow alice to push here\n" +
		"  refs/heads/a..b: ref name must not contain '..'\n" +
		"  refs/notes/x: only branches (refs/heads/) and tags (refs/tags/) can be pushed\n"
	if f.out.String() != want {
		t.Fatalf("output =\n%s\nwant\n%s", f.out.String(), want)
	}

	// And it is kept: one refused push, with the same reasons in the same
	// order. The ref that was fine has none.
	pushes, refs := f.refusalRows(t)
	if pushes != 1 || len(refs) != 3 {
		t.Fatalf("recorded %d pushes and %d refs, want 1 and 3: %+v", pushes, len(refs), refs)
	}
	for i, r := range refs {
		if line := "  " + r.ref + ": " + r.reason + "\n"; !strings.Contains(f.out.String(), line) {
			t.Errorf("ref %d recorded as %q, which the pusher was not told", i, line)
		}
	}
	if refs[0].ref != "refs/heads/main" || refs[2].ref != "refs/notes/x" {
		t.Errorf("refs are out of order: %+v", refs)
	}
	var person, ip string
	if err := f.hook.DB.Pool.QueryRow(ctx, `SELECT p.username, f.source_ip FROM push_refusals f JOIN people p ON p.id = f.person_id`).Scan(&person, &ip); err != nil {
		t.Fatal(err)
	}
	if person != "alice" || ip != "203.0.113.9" {
		t.Errorf("recorded for %q from %q", person, ip)
	}
}

func TestPreReceiveRecordsAPushRefusedAsAWhole(t *testing.T) {
	f := newPRFixture(t)
	f.asPerson(t, "alice")
	if err := f.people.Disable(context.Background(), f.hook.Ctx.PersonID, ""); err != nil {
		t.Fatal(err)
	}
	commit := f.commitFile(t, "one", "refs/heads/tmp")
	if err := f.preReceive(t, []Update{{Old: zeroHash, New: commit, Ref: "refs/heads/feature", Kind: git.KindBranch, Name: "feature"}}); err != ErrRejected {
		t.Fatalf("PreReceive = %v, want ErrRejected", err)
	}
	pushes, refs := f.refusalRows(t)
	if pushes != 1 || len(refs) != 1 || refs[0].ref != "" || refs[0].reason != "alice is disabled and cannot push" {
		t.Fatalf("recorded %d pushes: %+v", pushes, refs)
	}
}

func TestPreReceiveRecordsNothingForAnAcceptedPush(t *testing.T) {
	f := newPRFixture(t)
	commit := f.commitFile(t, "one", "refs/heads/tmp")
	if err := f.preReceive(t, []Update{{Old: zeroHash, New: commit, Ref: "refs/heads/feature", Kind: git.KindBranch, Name: "feature"}}); err != nil {
		t.Fatalf("PreReceive = %v", err)
	}
	if pushes, refs := f.refusalRows(t); pushes != 0 || len(refs) != 0 {
		t.Fatalf("an accepted push left %d refusals: %+v", pushes, refs)
	}
}
