package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
	"github.com/mmrzaf/gitman/internal/push"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

// gitmanBinary is the real gitman binary, built once for this package's
// tests: the hook scripts they install exec it as "gitman hook <name>",
// exactly as a real instance's do.
var gitmanBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gitman-web-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	gitmanBinary = filepath.Join(dir, "gitman")
	build := exec.Command("go", "build", "-o", gitmanBinary, "github.com/mmrzaf/gitman/cmd/gitman")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build gitman: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type gitHTTPEnv struct {
	t      *testing.T
	db     *postgres.DB
	cfg    *config.Config
	server *httptest.Server
	repo   *reposvc.Repo
	work   string
}

type gitCredential struct {
	username, token string
}

func setupGitHTTP(t *testing.T) *gitHTTPEnv {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	database := pgtest.Open(t)
	dataDir := t.TempDir()
	cfg := &config.Config{
		DatabaseURL: os.Getenv("GITMAN_TEST_DATABASE_URL"),
		DataDir:     dataDir,
		PublicURL:   "https://git.example.com",
		Port:        8080,
		LogLevel:    "info",
		LogFormat:   "text",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := push.Install(cfg.HooksPath(), gitmanBinary); err != nil {
		t.Fatal(err)
	}

	store := git.NewStore(cfg.ReposPath())
	t.Cleanup(store.Close)
	repoRecord, err := reposvc.NewService(database, store, "").Create(context.Background(), "demo", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}

	app, err := New(cfg, testServices(database, store, cfg.SecretKey), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.handler)
	t.Cleanup(server.Close)

	return &gitHTTPEnv{t: t, db: database, cfg: cfg, server: server, repo: repoRecord, work: filepath.Join(t.TempDir(), "work")}
}

func (e *gitHTTPEnv) person(username string, admin bool, scope auth.Scope) (*auth.Person, gitCredential) {
	e.t.Helper()
	ctx := context.Background()
	p, err := auth.NewService(e.db).GetByUsername(ctx, username)
	if err != nil {
		if p, err = auth.NewService(e.db).Create(ctx, username, "correct-horse-battery", admin, ""); err != nil {
			e.t.Fatal(err)
		}
	}
	plain, _, err := auth.NewService(e.db).CreateToken(ctx, p.ID, string(scope)+" token", scope, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return p, gitCredential{username: username, token: plain}
}

func (e *gitHTTPEnv) url(c gitCredential) string {
	u, _ := url.Parse(e.server.URL + "/demo.git")
	u.User = url.UserPassword(c.username, c.token)
	return u.String()
}

// git runs a Git client command and returns its combined output and
// whether it succeeded.
func (e *gitHTTPEnv) git(dir string, args ...string) (string, bool) {
	e.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + e.t.TempDir(), "LANG=C", "LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	}
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

func (e *gitHTTPEnv) mustGit(dir string, args ...string) string {
	e.t.Helper()
	out, ok := e.git(dir, args...)
	if !ok {
		e.t.Fatalf("git %s failed:\n%s", strings.Join(args, " "), out)
	}
	return out
}

func (e *gitHTTPEnv) initWork(c gitCredential) {
	e.t.Helper()
	e.mustGit(".", "init", "--quiet", "--initial-branch=main", e.work)
	e.mustGit(e.work, "remote", "add", "origin", e.url(c))
}

func (e *gitHTTPEnv) commit(file, content string) string {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(e.work, file)), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.work, file), []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
	e.mustGit(e.work, "add", "-A")
	e.mustGit(e.work, "commit", "--quiet", "-m", "change "+file)
	return strings.TrimSpace(e.mustGit(e.work, "rev-parse", "HEAD"))
}

func (e *gitHTTPEnv) saveRule(r reposvc.Rule) {
	e.t.Helper()
	if err := reposvc.NewService(e.db, nil, "").SaveRule(context.Background(), e.repo.ID, r, ""); err != nil {
		e.t.Fatal(err)
	}
}

func (e *gitHTTPEnv) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func expectRejected(t *testing.T, out string, ok bool, want string) {
	t.Helper()
	if ok {
		t.Fatalf("expected the push to be refused, it succeeded:\n%s", out)
	}
	if !strings.Contains(out, want) {
		t.Fatalf("expected output to contain %q, got:\n%s", want, out)
	}
}

func TestPushRecordsAndIndexes(t *testing.T) {
	e := setupGitHTTP(t)
	_, cred := e.person("darius", false, auth.ScopeWrite)
	e.initWork(cred)
	head := e.commit("README.md", "# demo\n")

	e.mustGit(e.work, "push", "--quiet", "origin", "main")

	if n := e.count(`SELECT count(*) FROM pushes WHERE repo_id = $1`, e.repo.ID); n != 1 {
		t.Fatalf("pushes = %d, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM push_updates WHERE is_create AND commit_count = 1 AND new_commit = $1`, head); n != 1 {
		t.Fatalf("expected one create update counting one commit")
	}
	refs, err := reposvc.NewService(e.db, nil, "").ListRefs(context.Background(), e.repo.ID)
	if err != nil || len(refs) != 1 || refs[0].Name != "main" || refs[0].Commit != head {
		t.Fatalf("ref index = %+v, %v", refs, err)
	}

	// Unprotected refs can be force-pushed and deleted.
	e.mustGit(e.work, "push", "--quiet", "origin", "main:feature")
	e.mustGit(e.work, "commit", "--quiet", "--amend", "-m", "rewritten")
	e.mustGit(e.work, "push", "--quiet", "--force", "origin", "HEAD:feature")
	e.mustGit(e.work, "push", "--quiet", "origin", "--delete", "feature")
	refs, _ = reposvc.NewService(e.db, nil, "").ListRefs(context.Background(), e.repo.ID)
	if len(refs) != 1 {
		t.Fatalf("expected the deleted branch to leave the index, got %+v", refs)
	}
}

func TestCloneWithReadToken(t *testing.T) {
	e := setupGitHTTP(t)
	_, writer := e.person("darius", false, auth.ScopeWrite)
	_, reader := e.person("bob", false, auth.ScopeRead)
	e.initWork(writer)
	e.commit("hello.txt", "hello\n")
	e.mustGit(e.work, "push", "--quiet", "origin", "main")

	clone := filepath.Join(t.TempDir(), "clone")
	e.mustGit(".", "clone", "--quiet", e.url(reader), clone)
	data, err := os.ReadFile(filepath.Join(clone, "hello.txt"))
	if err != nil || string(data) != "hello\n" {
		t.Fatalf("cloned content = %q, %v", data, err)
	}
	// Protocol v2 and v0 both work for fetching.
	e.mustGit(clone, "-c", "protocol.version=0", "fetch", "--quiet", "origin")
}

func TestAuthentication(t *testing.T) {
	e := setupGitHTTP(t)
	_, reader := e.person("bob", false, auth.ScopeRead)

	out, ok := e.git(".", "ls-remote", e.url(gitCredential{"bob", "not-a-token"}))
	if ok || !strings.Contains(out, "Authentication failed") {
		t.Fatalf("bad token: ok=%v\n%s", ok, out)
	}
	out, ok = e.git(".", "ls-remote", e.url(gitCredential{"someone-else", reader.token}))
	if ok || !strings.Contains(out, "Authentication failed") {
		t.Fatalf("mismatched username: ok=%v\n%s", ok, out)
	}
	out, ok = e.git(".", "ls-remote", strings.Replace(e.url(reader), "demo.git", "missing.git", 1))
	if ok || !strings.Contains(out, "Repository not found") {
		t.Fatalf("missing repository: ok=%v\n%s", ok, out)
	}

	resp, err := http.Get(e.server.URL + "/demo.git/info/refs?service=git-upload-pack")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("anonymous request: status %d, WWW-Authenticate %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}

	e.initWork(reader)
	e.commit("a.txt", "a\n")
	out, ok = e.git(e.work, "push", "origin", "main")
	expectRejected(t, out, ok, "can only read")
}

func TestNamingRules(t *testing.T) {
	e := setupGitHTTP(t)
	_, cred := e.person("darius", false, auth.ScopeWrite)
	e.initWork(cred)
	e.commit("a.txt", "a\n")
	e.mustGit(e.work, "push", "--quiet", "origin", "main", "main:v1")

	out, ok := e.git(e.work, "push", "origin", "main:refs/tags/v1")
	expectRejected(t, out, ok, "a branch and a tag cannot share a name")

	out, ok = e.git(e.work, "push", "origin", "main:3f2a91cb")
	expectRejected(t, out, ok, "looks like a commit hash")

	out, ok = e.git(e.work, "push", "origin", "main:refs/notes/x")
	expectRejected(t, out, ok, "only branches")

	out, ok = e.git(e.work, "push", "origin", "--delete", "main")
	expectRejected(t, out, ok, "default branch cannot be deleted")

	// Creating a branch and a tag of the same name in one push is refused
	// too, and — since pushes are atomic — nothing from it lands.
	out, ok = e.git(e.work, "push", "origin", "main:same", "main:refs/tags/same")
	expectRejected(t, out, ok, "cannot share a name")
	if n := e.count(`SELECT count(*) FROM refs WHERE repo_id = $1 AND name = 'same'`, e.repo.ID); n != 0 {
		t.Fatalf("expected no ref named 'same' after a refused push, found %d", n)
	}
}

func TestRuleEnforcement(t *testing.T) {
	e := setupGitHTTP(t)
	_, adminCred := e.person("lead", true, auth.ScopeWrite)
	_, cred := e.person("darius", false, auth.ScopeWrite)
	e.saveRule(reposvc.Rule{Kind: git.KindBranch, Pattern: "main", PushPolicy: reposvc.PushEveryone})
	e.saveRule(reposvc.Rule{Kind: git.KindBranch, Pattern: "release/*", PushPolicy: reposvc.PushAdmins})
	e.saveRule(reposvc.Rule{Kind: git.KindTag, Pattern: "v*", PushPolicy: reposvc.PushEveryone})

	e.initWork(cred)
	e.commit("a.txt", "a\n")
	e.mustGit(e.work, "push", "--quiet", "origin", "main")

	e.mustGit(e.work, "commit", "--quiet", "--amend", "-m", "rewritten")
	out, ok := e.git(e.work, "push", "--force", "origin", "main")
	expectRejected(t, out, ok, "force-push")

	out, ok = e.git(e.work, "push", "origin", "HEAD:release/1.0")
	expectRejected(t, out, ok, `does not allow darius to push`)

	e.mustGit(e.work, "reset", "--quiet", "--hard", "origin/main")
	e.mustGit(e.work, "tag", "v1.0.0")
	e.mustGit(e.work, "push", "--quiet", "origin", "v1.0.0")
	e.commit("b.txt", "b\n")
	e.mustGit(e.work, "tag", "--force", "v1.0.0")
	out, ok = e.git(e.work, "push", "--force", "origin", "v1.0.0")
	expectRejected(t, out, ok, "moving an existing tag")

	// Several refusals in one push are all reported.
	out, ok = e.git(e.work, "push", "--force", "origin", "HEAD:release/2.0", "v1.0.0")
	expectRejected(t, out, ok, "release/2.0")
	if !strings.Contains(out, "refs/tags/v1.0.0") {
		t.Fatalf("expected every refusal to be listed:\n%s", out)
	}

	// Admins satisfy any push policy, but not force rules.
	e.mustGit(e.work, "remote", "set-url", "origin", e.url(adminCred))
	e.mustGit(e.work, "push", "--quiet", "origin", "HEAD:release/1.0")
}

func TestPushCreatesRuns(t *testing.T) {
	e := setupGitHTTP(t)
	_, cred := e.person("darius", false, auth.ScopeWrite)
	e.saveRule(reposvc.Rule{Kind: git.KindBranch, Pattern: "main", PushPolicy: reposvc.PushEveryone, RunOnPush: true, AllowShip: true})
	e.saveRule(reposvc.Rule{Kind: git.KindBranch, Pattern: "docker/*", PushPolicy: reposvc.PushEveryone, RunOnPush: true})

	e.initWork(cred)
	e.commit(".gitman.yml", `image: alpine:3.20
targets:
  staging:
    branch: main
steps:
  - name: test
    run: echo test
  - name: deploy
    when: target
    run: echo deploy
`)
	out := e.mustGit(e.work, "push", "origin", "main")
	if !strings.Contains(out, "run #1 queued for branch main, shipping to staging") ||
		!strings.Contains(out, "https://git.example.com/demo/runs/1") {
		t.Fatalf("push output does not announce the run:\n%s", out)
	}
	var status, target string
	if err := e.db.Pool.QueryRow(context.Background(),
		`SELECT status, target FROM runs WHERE repo_id = $1 AND number = 1`, e.repo.ID).Scan(&status, &target); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || target != "staging" {
		t.Fatalf("run 1 = %s/%s", status, target)
	}
	if n := e.count(`SELECT count(*) FROM steps s JOIN runs r ON r.id = s.run_id WHERE r.number = 1 AND s.status = 'pending'`); n != 2 {
		t.Fatalf("pending steps = %d, want 2", n)
	}

	// A second push supersedes the queued run.
	e.commit("a.txt", "a\n")
	e.mustGit(e.work, "push", "--quiet", "origin", "main")
	var reason string
	if err := e.db.Pool.QueryRow(context.Background(),
		`SELECT status, reason FROM runs WHERE repo_id = $1 AND number = 1`, e.repo.ID).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" || reason != "Superseded by #2." {
		t.Fatalf("run 1 after a newer push = %s (%q)", status, reason)
	}

	// A pipeline that needs Docker fails at creation where no rule
	// allows it, and says why.
	e.commit(".gitman.yml", "image: docker:29-cli\ndocker: true\nsteps:\n  - name: build\n    run: docker build .\n")
	out = e.mustGit(e.work, "push", "origin", "HEAD:docker/x")
	if !strings.Contains(out, "failed before starting") || !strings.Contains(out, "no rule allows Docker") {
		t.Fatalf("expected a failed-at-creation run:\n%s", out)
	}

	// An invalid pipeline is reported to the pusher, not silently queued.
	e.commit(".gitman.yml", "image: alpine\nsteps: []\n")
	out = e.mustGit(e.work, "push", "origin", "HEAD:main")
	if !strings.Contains(out, "failed before starting") || !strings.Contains(out, "at least one step") {
		t.Fatalf("expected an invalid-pipeline report:\n%s", out)
	}
}

func TestRunFetchToken(t *testing.T) {
	e := setupGitHTTP(t)
	ctx := context.Background()
	_, cred := e.person("darius", false, auth.ScopeWrite)
	e.initWork(cred)
	commit := e.commit("a.txt", "a\n")
	e.mustGit(e.work, "push", "--quiet", "origin", "main")

	ciService := ci.NewService(e.db)
	if err := e.db.Tx(ctx, func(tx postgres.Tx) error {
		_, err := ciService.CreateTx(ctx, tx, ci.CreateParams{
			RepoID: e.repo.ID, Commit: commit, RefKind: git.KindBranch, RefName: "main", Trigger: ci.TriggerManual,
			Pipeline: []byte("image: alpine:3.20\nsteps:\n  - name: test\n    run: 'true'\n"),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := ciService.RegisterWorker(ctx, "w1", "host"); err != nil {
		t.Fatal(err)
	}
	claim, err := ciService.ClaimNext(ctx, "w1")
	if err != nil || claim == nil {
		t.Fatalf("ClaimNext = %+v, %v", claim, err)
	}
	runURL := func(repo string) string {
		u, _ := url.Parse(e.server.URL + "/" + repo + ".git")
		u.User = url.UserPassword(ci.FetchUsername, claim.FetchToken)
		return u.String()
	}

	dir := t.TempDir()
	e.mustGit(dir, "init", "--quiet")
	e.mustGit(dir, "fetch", "--quiet", "--depth=1", runURL("demo"), commit)

	e.mustGit(dir, "checkout", "--quiet", "--detach", "FETCH_HEAD")
	out, ok := e.git(dir, "push", runURL("demo"), "HEAD:refs/heads/sneaky")
	expectRejected(t, out, ok, "can only fetch")

	// A second repository, for the token to be refused on. The refusal
	// happens before its files are ever opened.
	if _, err := reposvc.NewService(e.db, git.NewStore(t.TempDir()), "").Create(ctx, "other", "", "main", ""); err != nil {
		t.Fatal(err)
	}
	out, ok = e.git(dir, "ls-remote", runURL("other"))
	if ok || !strings.Contains(out, "Repository not found") {
		t.Fatalf("a run's token reached another repository: ok=%v\n%s", ok, out)
	}

	if err := ciService.Finish(ctx, claim.RunID, ci.Outcome{Status: ci.StatusPassed}); err != nil {
		t.Fatal(err)
	}
	out, ok = e.git(dir, "ls-remote", runURL("demo"))
	if ok || !strings.Contains(out, "its run has finished") {
		t.Fatalf("a finished run's token still works: ok=%v\n%s", ok, out)
	}
}

// TestRestrictedRepositoryOverGitHTTP is the transport-level half of the
// read-visibility rule: a restricted repository must be exactly as
// unreachable over Git — clone, fetch, and push alike — as a name that
// does not exist, for anyone who is not one of its readers or an admin.
func TestRestrictedRepositoryOverGitHTTP(t *testing.T) {
	e := setupGitHTTP(t)
	ctx := context.Background()
	repos := reposvc.NewService(e.db, nil, "")
	if err := repos.SetVisibility(ctx, e.repo.ID, reposvc.VisibilityRestricted, ""); err != nil {
		t.Fatal(err)
	}
	reader, readerCred := e.person("bea", false, auth.ScopeWrite)
	_, outsiderCred := e.person("oscar", false, auth.ScopeWrite)
	_, adminCred := e.person("lead", true, auth.ScopeWrite)
	if err := repos.AddReader(ctx, e.repo.ID, reader.ID, ""); err != nil {
		t.Fatal(err)
	}

	out, ok := e.git(".", "ls-remote", e.url(outsiderCred))
	if ok || !strings.Contains(out, "Repository not found") {
		t.Fatalf("a non-reader cloned a restricted repository: ok=%v\n%s", ok, out)
	}

	for _, cred := range []gitCredential{readerCred, adminCred} {
		out, ok := e.git(".", "ls-remote", e.url(cred))
		if !ok {
			t.Fatalf("%s (a reader/admin) could not clone the restricted repository:\n%s", cred.username, out)
		}
	}
}

// TestGitConcurrencyLimitAnswersBusyWithRetryAfter covers R2-4's Git HTTP
// concurrency limit: once every slot is taken, the next request is
// refused at once with 503 and Retry-After rather than left to queue
// behind requests that could each run for as long as a large clone or
// push takes; freeing a slot lets the next request through again.
func TestGitConcurrencyLimitAnswersBusyWithRetryAfter(t *testing.T) {
	app, err := New(&config.Config{PublicURL: "http://gitman.test", Port: 8080}, Services{},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	h := app.limitGitConcurrency(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	})

	for i := 0; i < gitConcurrencyLimit; i++ {
		app.gitSlots <- struct{}{}
	}

	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/w/info/refs", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status while every slot is taken = %d, want 503", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("expected a Retry-After header while every slot is taken")
	}
	if called != 0 {
		t.Error("the handler must not run once every slot is taken")
	}

	<-app.gitSlots
	w = httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/w/info/refs", nil))
	if w.Code != http.StatusOK || called != 1 {
		t.Fatalf("status = %d, called = %d, want 200 and 1 once a slot frees up", w.Code, called)
	}
}
