package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
	"github.com/mmrzaf/gitman/internal/worker/dockertest"
)

// newAdminEnv builds an adminEnv backed by a real, migrated database and
// a real bare-repository store, for tests that exercise an admin command
// directly rather than through runAdmin's argument parsing. It also
// returns the database and store themselves, for tests that need to seed
// rows directly or drive real Git operations against a repository.
func newAdminEnv(t *testing.T) (*adminEnv, *postgres.DB, *git.Store) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	database := pgtest.Open(t)
	store := git.NewStore(t.TempDir())
	t.Cleanup(store.Close)
	var out bytes.Buffer
	env := &adminEnv{
		cfg:    &config.Config{PublicURL: "http://gitman.test"},
		people: auth.NewService(database),
		repos:  reposvc.NewService(database, store, "a very secret passphrase, at least 32 bytes long"),
		ci:     ci.NewService(database),
		out:    &out,
	}
	return env, database, store
}

func outputOf(env *adminEnv) string {
	return env.out.(*bytes.Buffer).String()
}

func TestWorkerCleanupCommandIsRegistered(t *testing.T) {
	if _, ok := adminGroups["worker"]["cleanup"]; !ok {
		t.Fatal(`expected a "worker cleanup" admin command to be registered`)
	}
}

func TestAdminWorkerCleanupRejectsArguments(t *testing.T) {
	if err := adminWorkerCleanup(t.Context(), &adminEnv{}, []string{"unexpected"}); err == nil {
		t.Fatal("expected an error for an unexpected argument")
	}
}

// TestAdminWorkerCleanupFailsLostRunsBeforeRemovingLeftovers is the
// host whose only worker was killed outright: nothing else there fails
// that worker's run, and a run still recorded as running keeps its
// container and workspace. The command must fail the run first, then
// remove what it left behind — and only among this instance's containers.
func TestAdminWorkerCleanupFailsLostRunsBeforeRemovingLeftovers(t *testing.T) {
	ctx := t.Context()
	database := pgtest.Open(t)
	if _, err := database.Pool.Exec(ctx, `INSERT INTO repos (id, name) VALUES ('r1', 'demo')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO workers (id, hostname, heartbeat_at) VALUES ('w1', 'host', now() - interval '10 minutes')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, worker_id, started_at)
		VALUES ('run1', 'r1', 1, 'abc123', 'manual', 'running', 'w1', now() - interval '11 minutes')
	`); err != nil {
		t.Fatal(err)
	}
	fake := dockertest.New(t)
	t.Setenv("FAKE_DOCKER_PS", "c1 run1\n")
	cfg := &config.Config{DataDir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(cfg.WorkspacesPath(), "run1", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	ciService := ci.NewService(database)
	var out bytes.Buffer
	env := &adminEnv{cfg: cfg, ci: ciService, out: &out, dockerBinary: fake.Binary}
	if err := adminWorkerCleanup(ctx, env, nil); err != nil {
		t.Fatalf("adminWorkerCleanup: %v", err)
	}

	var status string
	if err := database.Pool.QueryRow(ctx, `SELECT status FROM runs WHERE id = 'run1'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Errorf("run status = %q, want failed", status)
	}
	if got := out.String(); got != "Failed 1 runs, removed 1 containers and 1 workspaces.\n" {
		t.Errorf("output = %q", got)
	}
	instance, err := ciService.InstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ps := fake.CallsTo(t, "ps")
	if len(ps) != 1 || !slices.Contains(ps[0], "label=gitman.instance="+instance) {
		t.Errorf("ps calls = %q; want one, filtered to this instance's containers", ps)
	}
	if rm := fake.CallsTo(t, "rm"); len(rm) != 1 || rm[0][len(rm[0])-1] != "c1" {
		t.Errorf("rm calls = %q; want c1 removed", rm)
	}
}

func TestRepoVisibilityAndDefaultPushCommandsAreRegistered(t *testing.T) {
	for _, action := range []string{"visibility", "default-push", "default-branch"} {
		if _, ok := adminGroups["repo"][action]; !ok {
			t.Fatalf("expected a %q admin repo command to be registered", action)
		}
	}
	for _, action := range []string{"add", "remove", "list"} {
		if _, ok := adminGroups["reader"][action]; !ok {
			t.Fatalf("expected a %q admin reader command to be registered", action)
		}
	}
}

func TestAdminRepoVisibility(t *testing.T) {
	ctx := t.Context()
	env, _, _ := newAdminEnv(t)
	repo, err := env.repos.Create(ctx, "demo", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := adminRepoVisibility(ctx, env, []string{"demo", "restricted"}); err != nil {
		t.Fatalf("adminRepoVisibility: %v", err)
	}
	got, err := env.repos.GetByID(ctx, repo.ID)
	if err != nil || got.Visibility != reposvc.VisibilityRestricted {
		t.Fatalf("GetByID after adminRepoVisibility = %+v, %v", got, err)
	}
	if got := outputOf(env); got != "demo is now restricted.\n" {
		t.Errorf("output = %q", got)
	}

	if err := adminRepoVisibility(ctx, env, []string{"demo", "not-a-visibility"}); err == nil {
		t.Error("expected an invalid visibility to be rejected")
	}
	if err := adminRepoVisibility(ctx, env, []string{"no-such-repo", "everyone"}); err == nil {
		t.Error("expected an unknown repository to be rejected")
	}
}

func TestAdminRepoDefaultBranch(t *testing.T) {
	ctx := t.Context()
	env, _, store := newAdminEnv(t)
	repo, err := env.repos.Create(ctx, "demo", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := adminRepoDefaultBranch(ctx, env, []string{"demo", "develop"}); err == nil {
		t.Fatal("expected a branch the repository does not have to be rejected")
	}

	bare, err := store.Path(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	for _, args := range [][]string{
		{"clone", "--quiet", bare, "."},
		{"commit", "--quiet", "--allow-empty", "-m", "one"},
		{"push", "--quiet", "origin", "HEAD:refs/heads/develop"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	if err := adminRepoDefaultBranch(ctx, env, []string{"demo", "develop"}); err != nil {
		t.Fatalf("adminRepoDefaultBranch: %v", err)
	}
	got, err := env.repos.GetByID(ctx, repo.ID)
	if err != nil || got.DefaultBranch != "develop" {
		t.Fatalf("GetByID after adminRepoDefaultBranch = %+v, %v", got, err)
	}
	if got := outputOf(env); got != "demo's default branch is now develop.\n" {
		t.Errorf("output = %q", got)
	}
}

func TestAdminRepoDefaultPush(t *testing.T) {
	ctx := t.Context()
	env, _, _ := newAdminEnv(t)
	repo, err := env.repos.Create(ctx, "demo", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := env.people.Create(ctx, "alice", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}

	if err := adminRepoDefaultPush(ctx, env, []string{"--push", "people", "--people", "alice", "demo"}); err != nil {
		t.Fatalf("adminRepoDefaultPush: %v", err)
	}
	got, err := env.repos.GetByID(ctx, repo.ID)
	if err != nil || got.DefaultPushPolicy != reposvc.PushPeople || len(got.DefaultPushPeople) != 1 || got.DefaultPushPeople[0] != alice.ID {
		t.Fatalf("GetByID after adminRepoDefaultPush = %+v, %v", got, err)
	}

	if err := adminRepoDefaultPush(ctx, env, []string{"--push", "people", "demo"}); err == nil {
		t.Error("expected a people policy naming nobody to be rejected")
	}
}

func TestAdminReaderAddRemoveList(t *testing.T) {
	ctx := t.Context()
	env, _, _ := newAdminEnv(t)
	if _, err := env.repos.Create(ctx, "demo", "", "main", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := env.people.Create(ctx, "alice", "correct-horse-battery", false, ""); err != nil {
		t.Fatal(err)
	}

	if err := adminReaderList(ctx, env, []string{"demo"}); err != nil {
		t.Fatalf("adminReaderList (empty): %v", err)
	}
	if got := outputOf(env); got != "No explicit readers.\n" {
		t.Errorf("output = %q", got)
	}

	if err := adminReaderAdd(ctx, env, []string{"demo", "alice"}); err != nil {
		t.Fatalf("adminReaderAdd: %v", err)
	}
	if err := adminReaderAdd(ctx, env, []string{"demo", "no-such-person"}); err == nil {
		t.Error("expected an unknown username to be rejected")
	}

	env.out.(*bytes.Buffer).Reset()
	if err := adminReaderList(ctx, env, []string{"demo"}); err != nil {
		t.Fatalf("adminReaderList: %v", err)
	}
	if got := outputOf(env); got != "alice\n" {
		t.Errorf("output = %q, want alice listed", got)
	}

	if err := adminReaderRemove(ctx, env, []string{"demo", "alice"}); err != nil {
		t.Fatalf("adminReaderRemove: %v", err)
	}
	if err := adminReaderRemove(ctx, env, []string{"demo", "alice"}); err == nil {
		t.Error("expected removing a non-reader to be rejected")
	}
}

func TestAdminPersonAdd(t *testing.T) {
	ctx := t.Context()
	env, _, _ := newAdminEnv(t)

	if err := adminPersonAdd(ctx, env, []string{"--admin", "alice"}); err != nil {
		t.Fatalf("adminPersonAdd: %v", err)
	}
	if got := outputOf(env); !strings.HasPrefix(got, "Added alice (admin).\nPassword: ") {
		t.Errorf("output = %q", got)
	}
	p, err := env.people.GetByUsername(ctx, "alice")
	if err != nil || !p.IsAdmin {
		t.Fatalf("GetByUsername after adminPersonAdd = %+v, %v", p, err)
	}

	if err := adminPersonAdd(ctx, env, []string{"alice"}); err == nil {
		t.Error("expected a duplicate username to be rejected")
	}
}

func TestAdminPersonList(t *testing.T) {
	ctx := t.Context()
	env, _, _ := newAdminEnv(t)
	if err := adminPersonAdd(ctx, env, []string{"--admin", "alice"}); err != nil {
		t.Fatal(err)
	}
	env.out.(*bytes.Buffer).Reset()
	if err := adminPersonList(ctx, env, nil); err != nil {
		t.Fatalf("adminPersonList: %v", err)
	}
	got := outputOf(env)
	if !strings.Contains(got, "USERNAME") || !strings.Contains(got, "alice") || !strings.Contains(got, "admin") {
		t.Errorf("output = %q", got)
	}
}

func TestAdminPersonDisableEnable(t *testing.T) {
	ctx := t.Context()
	env, _, _ := newAdminEnv(t)
	if err := adminPersonAdd(ctx, env, []string{"alice"}); err != nil {
		t.Fatal(err)
	}

	if err := adminPersonDisable(ctx, env, []string{"alice"}); err != nil {
		t.Fatalf("adminPersonDisable: %v", err)
	}
	p, err := env.people.GetByUsername(ctx, "alice")
	if err != nil || !p.Disabled() {
		t.Fatalf("GetByUsername after disable = %+v, %v", p, err)
	}

	if err := adminPersonEnable(ctx, env, []string{"alice"}); err != nil {
		t.Fatalf("adminPersonEnable: %v", err)
	}
	p, err = env.people.GetByUsername(ctx, "alice")
	if err != nil || p.Disabled() {
		t.Fatalf("GetByUsername after enable = %+v, %v", p, err)
	}

	if err := adminPersonDisable(ctx, env, []string{"no-such-person"}); err == nil {
		t.Error("expected an unknown username to be rejected")
	}
}

func TestAdminPersonRole(t *testing.T) {
	ctx := t.Context()
	env, _, _ := newAdminEnv(t)
	if err := adminPersonAdd(ctx, env, []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	// A second admin, so demoting alice below doesn't hit "this is the
	// only enabled admin."
	if err := adminPersonAdd(ctx, env, []string{"--admin", "bob"}); err != nil {
		t.Fatal(err)
	}

	if err := adminPersonRole(ctx, env, []string{"alice", "admin"}); err != nil {
		t.Fatalf("adminPersonRole: %v", err)
	}
	p, err := env.people.GetByUsername(ctx, "alice")
	if err != nil || !p.IsAdmin {
		t.Fatalf("GetByUsername after promoting = %+v, %v", p, err)
	}

	if err := adminPersonRole(ctx, env, []string{"alice", "member"}); err != nil {
		t.Fatalf("adminPersonRole (demote): %v", err)
	}
	p, err = env.people.GetByUsername(ctx, "alice")
	if err != nil || p.IsAdmin {
		t.Fatalf("GetByUsername after demoting = %+v, %v", p, err)
	}

	if err := adminPersonRole(ctx, env, []string{"alice", "not-a-role"}); err == nil {
		t.Error("expected an invalid role to be rejected")
	}
}

func TestAdminPersonResetPassword(t *testing.T) {
	ctx := t.Context()
	env, _, _ := newAdminEnv(t)
	if err := adminPersonAdd(ctx, env, []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	env.out.(*bytes.Buffer).Reset()
	if err := adminPersonResetPassword(ctx, env, []string{"alice"}); err != nil {
		t.Fatalf("adminPersonResetPassword: %v", err)
	}
	if got := outputOf(env); !strings.Contains(got, "Password: ") {
		t.Errorf("output = %q", got)
	}
	if err := adminPersonResetPassword(ctx, env, []string{"no-such-person"}); err == nil {
		t.Error("expected an unknown username to be rejected")
	}
}

func TestAdminTokenCreate(t *testing.T) {
	ctx := t.Context()
	env, _, _ := newAdminEnv(t)
	if err := adminPersonAdd(ctx, env, []string{"alice"}); err != nil {
		t.Fatal(err)
	}

	if err := adminTokenCreate(ctx, env, []string{"--write", "--days", "30", "alice", "laptop"}); err != nil {
		t.Fatalf("adminTokenCreate: %v", err)
	}
	got := outputOf(env)
	if !strings.Contains(got, `Created write token "laptop" for alice`) || !strings.Contains(got, "Token: ") {
		t.Errorf("output = %q", got)
	}

	if err := adminTokenCreate(ctx, env, []string{"--days", "-1", "alice", "bad"}); err == nil {
		t.Error("expected a negative --days to be rejected")
	}
	if err := adminTokenCreate(ctx, env, []string{"no-such-person", "x"}); err == nil {
		t.Error("expected an unknown username to be rejected")
	}
}

func TestAdminRepoCreateListDelete(t *testing.T) {
	ctx := t.Context()
	env, _, _ := newAdminEnv(t)

	if err := adminRepoCreate(ctx, env, []string{"--description", "Plant maintenance", "demo"}); err != nil {
		t.Fatalf("adminRepoCreate: %v", err)
	}
	if got := outputOf(env); !strings.Contains(got, "Created demo.") || !strings.Contains(got, "Clone: ") {
		t.Errorf("output = %q", got)
	}
	if err := adminRepoCreate(ctx, env, []string{"demo"}); err == nil {
		t.Error("expected a duplicate repository name to be rejected")
	}

	env.out.(*bytes.Buffer).Reset()
	if err := adminRepoList(ctx, env, nil); err != nil {
		t.Fatalf("adminRepoList: %v", err)
	}
	if got := outputOf(env); !strings.Contains(got, "demo") || !strings.Contains(got, "Plant maintenance") {
		t.Errorf("output = %q", got)
	}

	if err := adminRepoDelete(ctx, env, []string{"demo"}); err != nil {
		t.Fatalf("adminRepoDelete: %v", err)
	}
	if _, err := env.repos.GetByName(ctx, "demo"); err == nil {
		t.Error("expected the repository to be gone after delete")
	}
	if err := adminRepoDelete(ctx, env, []string{"demo"}); err == nil {
		t.Error("expected deleting an unknown repository to be rejected")
	}
}

func TestAdminRepoSync(t *testing.T) {
	ctx := t.Context()
	env, _, store := newAdminEnv(t)
	repo, err := env.repos.Create(ctx, "demo", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.Path(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("clone", "--quiet", path, ".")
	run("commit", "--quiet", "--allow-empty", "-m", "one")
	run("push", "--quiet", "origin", "main")

	if err := adminRepoSync(ctx, env, []string{"demo"}); err != nil {
		t.Fatalf("adminRepoSync: %v", err)
	}
	if got := outputOf(env); got != "Synced 1 branches and tags of demo.\n" {
		t.Errorf("output = %q", got)
	}
	if err := adminRepoSync(ctx, env, []string{"no-such-repo"}); err == nil {
		t.Error("expected an unknown repository to be rejected")
	}
}

func TestAdminRuleListSetDelete(t *testing.T) {
	ctx := t.Context()
	env, _, _ := newAdminEnv(t)
	repo, err := env.repos.Create(ctx, "demo", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.people.Create(ctx, "alice", "correct-horse-battery", false, ""); err != nil {
		t.Fatal(err)
	}

	env.out.(*bytes.Buffer).Reset()
	if err := adminRuleList(ctx, env, []string{"demo"}); err != nil {
		t.Fatalf("adminRuleList (empty): %v", err)
	}
	if got := outputOf(env); !strings.Contains(got, "No rules") {
		t.Errorf("output = %q", got)
	}

	if err := adminRuleSet(ctx, env, []string{"--push", "people", "--people", "alice", "--force", "--run", "demo", "branch", "main"}); err != nil {
		t.Fatalf("adminRuleSet: %v", err)
	}
	rules, err := env.repos.ListRules(ctx, repo.ID)
	if err != nil || len(rules) != 1 || rules[0].PushPolicy != reposvc.PushPeople || !rules[0].AllowForce || !rules[0].RunOnPush {
		t.Fatalf("ListRules after adminRuleSet = %+v, %v", rules, err)
	}

	env.out.(*bytes.Buffer).Reset()
	if err := adminRuleList(ctx, env, []string{"demo"}); err != nil {
		t.Fatalf("adminRuleList: %v", err)
	}
	if got := outputOf(env); !strings.Contains(got, "people: alice") || !strings.Contains(got, "force") {
		t.Errorf("output = %q", got)
	}

	if err := adminRuleSet(ctx, env, []string{"demo", "not-a-kind", "x"}); err == nil {
		t.Error("expected an invalid ref kind to be rejected")
	}

	if err := adminRuleDelete(ctx, env, []string{"demo", "branch", "main"}); err != nil {
		t.Fatalf("adminRuleDelete: %v", err)
	}
	if err := adminRuleDelete(ctx, env, []string{"demo", "branch", "main"}); err == nil {
		t.Error("expected deleting an already-deleted rule to be rejected")
	}
}

func TestAdminRunCancel(t *testing.T) {
	ctx := t.Context()
	env, database, _ := newAdminEnv(t)
	repo, err := env.repos.Create(ctx, "demo", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := adminRunCancel(ctx, env, []string{"demo", "1"}); err == nil {
		t.Error("expected cancelling a nonexistent run to be rejected")
	}

	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status)
		VALUES ('run1', $1, 1, 'abc123', 'manual', 'queued')
	`, repo.ID); err != nil {
		t.Fatal(err)
	}

	if err := adminRunCancel(ctx, env, []string{"demo", "1"}); err != nil {
		t.Fatalf("adminRunCancel: %v", err)
	}
	var status string
	if err := database.Pool.QueryRow(ctx, `SELECT status FROM runs WHERE id = 'run1'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" {
		t.Errorf("run status = %q, want cancelled", status)
	}

	if err := adminRunCancel(ctx, env, []string{"demo", "1"}); err == nil {
		t.Error("expected cancelling an already-finished run to be rejected")
	}
	if err := adminRunCancel(ctx, env, []string{"demo", "not-a-number"}); err == nil {
		t.Error("expected a non-numeric run number to be rejected")
	}
}
