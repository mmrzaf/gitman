package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

func TestCommitsOfARefAndAPath(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	head := mustResolve(t, mustOpen(t, store, repo), "main")

	// The default branch's log: newest first, with the branches and tags at
	// each commit.
	resp, body := b.do(http.MethodGet, "/waiotech/commits", nil, nil)
	expect(t, resp, body, http.StatusOK, `id="commits-title"`, "Update README", "Initial commit",
		`title="branch main"`, `title="tag v1.0.0"`)
	if strings.Contains(body, "On release") {
		t.Error("the default branch's log lists a commit of another branch")
	}
	if strings.Index(body, "Update README") > strings.Index(body, "Initial commit") {
		t.Error("the log is not newest first")
	}

	resp, body = b.do(http.MethodGet, "/waiotech/commits?ref=release", nil, nil)
	expect(t, resp, body, http.StatusOK, "On release", "Initial commit")
	resp, body = b.do(http.MethodGet, "/waiotech/commits?ref="+url.QueryEscape("release/1.2"), nil, nil)
	expect(t, resp, body, http.StatusOK, "On release/1.2")
	resp, body = b.do(http.MethodGet, "/waiotech/commits?ref="+head[:10], nil, nil)
	expect(t, resp, body, http.StatusOK, "Update README", `value="`+head[:10]+`"`)

	// Filtered to a path: only the commits that touched it.
	resp, body = b.do(http.MethodGet, "/waiotech/commits?path=server", nil, nil)
	expect(t, resp, body, http.StatusOK, "Initial commit", `<span class="mono">server</span>`, "All paths")
	if strings.Contains(body, "Update README") {
		t.Error("a path's log lists a commit that did not touch it")
	}
	resp, body = b.do(http.MethodGet, "/waiotech/commits?path=README.md", nil, nil)
	expect(t, resp, body, http.StatusOK, "Update README", "Initial commit")
	resp, body = b.do(http.MethodGet, "/waiotech/commits?path=nothing/here", nil, nil)
	expect(t, resp, body, http.StatusOK, "No commits")

	// A path is a file's name, never a pathspec, and never leaves the tree.
	resp, body = b.do(http.MethodGet, "/waiotech/commits?path="+url.QueryEscape(":(exclude)server"), nil, nil)
	expect(t, resp, body, http.StatusOK, "No commits")
	resp, body = b.do(http.MethodGet, "/waiotech/commits?path="+url.QueryEscape("server/../README.md"), nil, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity)
	resp, body = b.do(http.MethodGet, "/waiotech/commits?path=%00", nil, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity)

	resp, body = b.do(http.MethodGet, "/waiotech/commits?ref=nope", nil, nil)
	expect(t, resp, body, http.StatusNotFound)
	// A ref followed by a path is not a ref.
	resp, body = b.do(http.MethodGet, "/waiotech/commits?ref=main/server", nil, nil)
	expect(t, resp, body, http.StatusNotFound)

	// Without scripting the pickers are text boxes that keep the chosen
	// path, sent by the Show button.
	resp, body = b.do(http.MethodGet, "/waiotech/commits?ref=release&path=which.txt", nil, nil)
	expect(t, resp, body, http.StatusOK, `action="/waiotech/commits"`, `name="path" value="which.txt"`, `name="ref" value="release"`,
		`name="base" value=""`, `<option value="main">branch</option>`, `<option value="v1.0.0">tag</option>`, "On release")
}

func TestCommitsShowMergesRunsAndPaging(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)

	barePath, err := store.Path(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	runGit(t, work, "clone", "--quiet", barePath, ".")
	runGit(t, work, "merge", "--quiet", "--no-ff", "-m", "Merge release", "origin/release")
	for i := 0; i < commitsPageSize; i++ {
		runGit(t, work, "commit", "--quiet", "--allow-empty", "-m", fmt.Sprintf("Filler %d", i))
	}
	merged := runGit(t, work, "rev-parse", "HEAD~"+fmt.Sprint(commitsPageSize))
	runGit(t, work, "push", "--quiet", "origin", "main")
	syncRepoRefs(t, database, store, repo.ID)
	if _, err := database.Pool.Exec(context.Background(), `
		INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, ref_kind, ref_name, finished_at)
		VALUES ('r-merge', $1, 7, $2, 'manual', 'passed', 'branch', 'main', now())
	`, repo.ID, merged); err != nil {
		t.Fatal(err)
	}

	// The newest page is all filler, and points to the older one.
	resp, body := b.do(http.MethodGet, "/waiotech/commits", nil, nil)
	expect(t, resp, body, http.StatusOK, "Filler 0", `href="/waiotech/commits?ref=main&amp;skip=30" data-swap>Older`)
	if strings.Contains(body, "Merge release") || strings.Contains(body, ">Newer<") {
		t.Error("the first page shows the second's commits or a Newer link")
	}

	resp, body = b.do(http.MethodGet, "/waiotech/commits?skip=30", nil, nil)
	expect(t, resp, body, http.StatusOK, "Merge release", `<span class="chip">merge</span>`, `href="/waiotech/runs/7"`, "passed", `>Newer<`)
	if strings.Count(body, `<span class="chip">merge</span>`) != 1 {
		t.Errorf("only the merge commit has a merge marker:\n%s", body)
	}
}

func TestCommitsBeforeTheDefaultBranchIsPushed(t *testing.T) {
	database, _, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	if resp, body := b.do(http.MethodPost, "/repos", url.Values{"name": {"fresh"}}, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create repo: %d\n%s", resp.StatusCode, body)
	}
	resp, body := b.do(http.MethodGet, "/fresh/commits", nil, nil)
	expect(t, resp, body, http.StatusOK, "Nothing pushed yet")
	// Asking for a ref that is not there is still a 404.
	resp, body = b.do(http.MethodGet, "/fresh/commits?ref=main", nil, nil)
	expect(t, resp, body, http.StatusNotFound)
}

// TestCompareListsWhatOneSideHasThatTheOtherLacks is the page's main job:
// the commits between two refs, then the changes they make, for a branch, a
// tag or a commit on either side.
func TestCompareListsWhatOneSideHasThatTheOtherLacks(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	head := mustResolve(t, mustOpen(t, store, repo), "main")

	// Tag to tag: what release/1.2 added to v1.0.0.
	resp, body := b.do(http.MethodGet, "/waiotech/commits?base=v1.0.0&ref="+url.QueryEscape("release/1.2"), nil, nil)
	expect(t, resp, body, http.StatusOK, "On release/1.2", "which.txt",
		"Changes", "is 1 commit ahead of and 0 behind", "Common ancestor", `value="v1.0.0"`, `value="release/1.2"`,
		// The results are two tabs on one page: the commits and the files they change.
		`data-tab="commits" aria-current="page"`, `id="panel-commits" data-tab-panel="commits">`,
		`data-tab="changes"`, `id="panel-changes" data-tab-panel="changes" hidden>`)
	if strings.Contains(body, "Initial commit") {
		t.Error("the comparison lists a commit both tags have")
	}
	if strings.Contains(body, `data-live-events`) {
		t.Error("a comparison, which carries a diff, refreshes itself")
	}
	expect(t, resp, body, http.StatusOK, `href="/waiotech/commits?base=release%2F1.2&amp;ref=v1.0.0"`) // swap sides

	// Diverged: release has a commit main lacks and lacks one main has.
	resp, body = b.do(http.MethodGet, "/waiotech/commits?base=main&ref=release", nil, nil)
	expect(t, resp, body, http.StatusOK, "is 1 commit ahead of and 1 behind", "On release", "See the 1 commit main has")
	if strings.Contains(body, "Update README") {
		t.Error("the comparison lists a commit only the base has")
	}

	// A commit is either side, abbreviated as in any address.
	resp, body = b.do(http.MethodGet, "/waiotech/commits?base="+head[:10]+"&ref=release", nil, nil)
	expect(t, resp, body, http.StatusOK, "On release", `value="`+head[:10]+`"`)

	// The same thing on both sides has nothing between.
	resp, body = b.do(http.MethodGet, "/waiotech/commits?base=main&ref=main", nil, nil)
	expect(t, resp, body, http.StatusOK, "are at the same commit")
	if strings.Contains(body, "data-tabs") || strings.Contains(body, "changed file") {
		t.Error("a comparison of a ref with itself shows empty results")
	}

	// The files tab is an address of its own, open on the files.
	resp, body = b.do(http.MethodGet, "/waiotech/commits?base=v1.0.0&ref="+url.QueryEscape("release/1.2")+"&tab=changes", nil, nil)
	expect(t, resp, body, http.StatusOK, `data-tab="changes" aria-current="page"`, `id="panel-commits" data-tab-panel="commits" hidden>`,
		`id="panel-changes" data-tab-panel="changes">`, "which.txt")

	// No base is the log again, with nothing compared.
	resp, body = b.do(http.MethodGet, "/waiotech/commits?base=&ref=main", nil, nil)
	expect(t, resp, body, http.StatusOK, "Update README")
	if strings.Contains(body, "changed file") {
		t.Error("a log shows changes")
	}

	// A side that is not there, or is a ref plus a path, is not found.
	for _, bad := range []string{"nope", "main/server", "--all", "a...b"} {
		resp, body = b.do(http.MethodGet, "/waiotech/commits?base="+url.QueryEscape(bad)+"&ref=main", nil, nil)
		expect(t, resp, body, http.StatusNotFound)
		resp, body = b.do(http.MethodGet, "/waiotech/commits?base=main&ref="+url.QueryEscape(bad), nil, nil)
		expect(t, resp, body, http.StatusNotFound)
	}
}

func TestActivityFromRealPushes(t *testing.T) {
	e := setupGitHTTP(t)
	_, cred := e.person("darius", false, auth.ScopeWrite)
	e.saveRule(reposvc.Rule{Kind: git.KindBranch, Pattern: "main", PushPolicy: reposvc.PushEveryone, AllowForce: true, RunOnPush: true})
	e.initWork(cred)

	e.commit("README.md", "# demo\n")
	e.mustGit(e.work, "push", "--quiet", "origin", "main")
	first := e.commit("a.txt", "a\n")
	e.mustGit(e.work, "push", "--quiet", "origin", "main")
	e.mustGit(e.work, "tag", "v1", first)
	e.mustGit(e.work, "push", "--quiet", "origin", "v1")
	e.mustGit(e.work, "checkout", "--quiet", "-b", "feature")
	e.commit("f.txt", "f\n")
	e.mustGit(e.work, "push", "--quiet", "origin", "feature")
	// A rewrite of main, and a tag pointed somewhere else.
	e.mustGit(e.work, "checkout", "--quiet", "main")
	e.mustGit(e.work, "reset", "--quiet", "--hard", "HEAD~1")
	e.commit("b.txt", "b\n")
	e.mustGit(e.work, "push", "--quiet", "--force", "origin", "main")
	e.mustGit(e.work, "tag", "-f", "v1", "HEAD")
	e.mustGit(e.work, "push", "--quiet", "--force", "origin", "v1")
	e.mustGit(e.work, "push", "--quiet", "origin", "--delete", "feature")

	if n := e.count(`SELECT count(*) FROM push_updates WHERE is_force`); n != 1 {
		t.Fatalf("%d updates are recorded as forced, want the one rewrite of main", n)
	}

	b := signInTo(t, e, "darius")
	resp, body := b.do(http.MethodGet, "/demo/activity", nil, nil)
	expect(t, resp, body, http.StatusOK, `data-live-region="activity"`, "darius")
	text := stripTags(body)
	for _, want := range []string{" created ", " pushed ", " force-pushed ", " moved ", " deleted "} {
		if !strings.Contains(text, want) {
			t.Errorf("Activity has no %q entry:\n%s", want, text)
		}
	}
	// A push to main with a "run" rule started a run, which its entry links.
	expect(t, resp, body, http.StatusOK, `href="/demo/runs/1"`)
	// Newest first: the delete came last.
	if strings.Index(text, " deleted ") > strings.Index(text, " created ") {
		t.Errorf("Activity is not newest first:\n%s", text)
	}
	// The Overview shows the same feed, with a way to the rest.
	resp, body = b.do(http.MethodGet, "/demo", nil, nil)
	expect(t, resp, body, http.StatusOK, `data-live-region="timeline"`, `href="/demo/activity"`, "force-pushed")
}

func TestActivityShowsRefusedPushes(t *testing.T) {
	e := setupGitHTTP(t)
	_, cred := e.person("darius", false, auth.ScopeWrite)
	e.saveRule(reposvc.Rule{Kind: git.KindBranch, Pattern: "main", PushPolicy: reposvc.PushEveryone})
	e.initWork(cred)
	e.commit("README.md", "# demo\n")
	e.mustGit(e.work, "push", "--quiet", "origin", "main")
	e.commit("a.txt", "a\n")
	e.mustGit(e.work, "push", "--quiet", "origin", "main")

	// Two refs refused at once, one of them a rewrite of main.
	e.mustGit(e.work, "reset", "--quiet", "--hard", "HEAD~1")
	e.commit("b.txt", "b\n")
	out, ok := e.git(e.work, "push", "--force", "origin", "main", "main:refs/heads/keep")
	expectRejected(t, out, ok, "this rewrites history")
	out, ok = e.git(e.work, "push", "origin", "--delete", "main")
	expectRejected(t, out, ok, "the default branch cannot be deleted")

	b := signInTo(t, e, "darius")
	resp, body := b.do(http.MethodGet, "/demo/activity", nil, nil)
	expect(t, resp, body, http.StatusOK, "push was refused", "this rewrites history (a force-push)", "the default branch cannot be deleted")
	if strings.Count(stripTags(body), "push was refused") != 2 {
		t.Errorf("want the two refused pushes listed:\n%s", stripTags(body))
	}
	// Nothing was accepted from either.
	if strings.Contains(stripTags(body), "darius force-pushed") || strings.Contains(stripTags(body), "darius deleted") {
		t.Errorf("a refused push shows as a change:\n%s", stripTags(body))
	}
}

func TestActivityPagesAndListsRunsAndShipments(t *testing.T) {
	database, store, b := setupWithStore(t)
	ctx := context.Background()
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	person := mustPerson(t, database, "darius")
	head := mustResolve(t, mustOpen(t, store, repo), "main")

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO workers (id, hostname) VALUES ('w1', 'host')`)
	exec(`INSERT INTO runs (id, repo_id, number, commit_hash, ref_kind, ref_name, trigger, triggered_by, status, target, worker_id, started_at, finished_at)
	      VALUES ('r1', $1, 1, $2, 'branch', 'main', 'push', $3, 'passed', 'staging', 'w1', now(), now())`, repo.ID, head, person.ID)
	exec(`INSERT INTO deployments (id, repo_id, target, version, commit_hash, run_id, person_id)
	      VALUES ('d1', $1, 'staging', $2, $3, 'r1', $4)`, repo.ID, head[:12], head, person.ID)

	resp, body := b.do(http.MethodGet, "/waiotech/activity", nil, nil)
	expect(t, resp, body, http.StatusOK, `id="activity-title"`, `href="/waiotech/runs/1">Run #1</a> passed`, "shipped", "to <strong>staging</strong>")

	// A page holds activityPageSize entries; the rest are a click away.
	for i := 0; i < activityPageSize; i++ {
		exec(`INSERT INTO events (id, repo_id, person_id, action) VALUES ($1, $2, $3, 'rule.saved')`, fmt.Sprintf("e%d", i), repo.ID, person.ID)
	}
	resp, body = b.do(http.MethodGet, "/waiotech/activity", nil, nil)
	expect(t, resp, body, http.StatusOK, `href="/waiotech/activity?skip=30">Older`)
	if strings.Contains(body, ">Newer<") {
		t.Error("the first page has a Newer link")
	}
	resp, body = b.do(http.MethodGet, "/waiotech/activity?skip=30", nil, nil)
	expect(t, resp, body, http.StatusOK, `>Newer<`)
	resp, body = b.do(http.MethodGet, "/waiotech/activity?skip=abc", nil, nil)
	expect(t, resp, body, http.StatusOK)
}

func TestOverviewHasNoTabsAndLinksToWhatIsNotDeployed(t *testing.T) {
	database, store, b := setupWithStore(t)
	ctx := context.Background()
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	person := mustPerson(t, database, "darius")
	gitRepo := mustOpen(t, store, repo)
	head, old := mustResolve(t, gitRepo, "main"), mustResolve(t, gitRepo, "v1.0.0")

	// Twelve tags more than the Overview lists.
	bare, err := store.Path(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		runGit(t, bare, "tag", fmt.Sprintf("r%02d", i), head)
	}
	syncRepoRefs(t, database, store, repo.ID)

	// Production is at the old commit, staging at the default branch's.
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO deployments (id, repo_id, target, version, commit_hash, person_id)
	      VALUES ('d1', $1, 'production', 'v1.0.0', $2, $4), ('d2', $1, 'staging', $3, $5, $4)`,
		repo.ID, old, head[:12], person.ID, head)

	resp, body := b.do(http.MethodGet, "/waiotech", nil, nil)
	expect(t, resp, body, http.StatusOK, `id="branches-title"`, `id="tags-title"`, `data-live-region="branches"`, `data-live-region="tags"`)
	for _, tabs := range []string{"data-tabs", "data-tab-panel", `role="tab"`} {
		if strings.Contains(body, tabs) {
			t.Errorf("the Overview has tabs (%s)", tabs)
		}
	}
	// Only production lacks anything, so only its row counts commits not
	// shipped; staging, at the default branch, is up to date.
	expect(t, resp, body, http.StatusOK, `<th scope="col">Not shipped</th>`, `href="/waiotech/commits?base=`+old+`&amp;ref=main">1 commit</a>`, "Up to date")
	if n := strings.Count(body, "Up to date"); n != 1 {
		t.Fatalf("%d targets are up to date, want only staging", n)
	}

	// Twelve tags of thirteen? The newest ten, and a way to all of them.
	expect(t, resp, body, http.StatusOK, `All 14 tags`, `href="/waiotech?tags=all"`)
	if n := strings.Count(body, `aria-label="Run tag `); n > overviewTags {
		t.Errorf("%d tags listed, at most %d", n, overviewTags)
	}
	for _, path := range []string{"/waiotech?tags=all", "/waiotech?tab=tags"} {
		resp, body = b.do(http.MethodGet, path, nil, nil)
		expect(t, resp, body, http.StatusOK, "r00", "r11", "v1.0.0")
		if strings.Contains(body, "All 14 tags") {
			t.Errorf("%s still offers all tags", path)
		}
	}
}

// stripTags is a page's text, its markup removed and its whitespace made
// single spaces.
func stripTags(html string) string {
	var out strings.Builder
	inTag := false
	for _, r := range html {
		switch {
		case r == '<':
			inTag = true
			out.WriteRune(' ')
		case r == '>':
			inTag = false
			out.WriteRune(' ')
		case !inTag:
			out.WriteRune(r)
		}
	}
	return " " + strings.Join(strings.Fields(out.String()), " ") + " "
}

func mustOpen(t *testing.T, store *git.Store, repo *reposvc.Repo) *git.Repo {
	t.Helper()
	gitRepo, err := store.Open(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	return gitRepo
}

func mustOpenByName(t *testing.T, database *postgres.DB, store *git.Store, name string) *git.Repo {
	t.Helper()
	return mustOpen(t, store, mustRepo(t, database, name))
}

// A column nothing would fill is left out, so a repository that has not
// run a pipeline has no columns about runs.
func TestColumnsFollowWhatThereIs(t *testing.T) {
	database, store, b := setupWithStore(t)
	ctx := context.Background()
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	person := mustPerson(t, database, "darius")
	head := mustResolve(t, mustOpen(t, store, repo), "main")

	// No runs, no deployments.
	resp, body := b.do(http.MethodGet, "/waiotech", nil, nil)
	expect(t, resp, body, http.StatusOK, `<th scope="col">Commit</th>`, `<th scope="col">Updated</th>`)
	for _, absent := range []string{"Last run", "Last shipped"} {
		if strings.Contains(body, absent) {
			t.Errorf("the Overview has a %q column with nothing in it", absent)
		}
	}
	resp, body = b.do(http.MethodGet, "/waiotech/commits", nil, nil)
	expect(t, resp, body, http.StatusOK, `<th scope="col">Refs</th>`)
	if strings.Contains(body, `<th scope="col">Run</th>`) {
		t.Error("the commits have a Run column with no run in it")
	}
	resp, body = b.do(http.MethodGet, "/waiotech/runs", nil, nil)
	expect(t, resp, body, http.StatusOK, "No runs yet")

	// A passed run that shipped: the columns appear.
	if _, err := database.Pool.Exec(ctx, `INSERT INTO workers (id, hostname) VALUES ('w1', 'host')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO runs (id, repo_id, number, commit_hash, ref_kind, ref_name, trigger, triggered_by, status, target, worker_id, started_at, finished_at)
		VALUES ('r1', $1, 1, $2, 'branch', 'main', 'push', $3, 'passed', 'staging', 'w1', now(), now())`, repo.ID, head, person.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO deployments (id, repo_id, target, version, commit_hash, run_id, person_id)
		VALUES ('d1', $1, 'staging', 'v1', $2, 'r1', $3)`, repo.ID, head, person.ID); err != nil {
		t.Fatal(err)
	}
	resp, body = b.do(http.MethodGet, "/waiotech", nil, nil)
	expect(t, resp, body, http.StatusOK, "Last run", "Last shipped")
	resp, body = b.do(http.MethodGet, "/waiotech/commits", nil, nil)
	expect(t, resp, body, http.StatusOK, `<th scope="col">Run</th>`)
	resp, body = b.do(http.MethodGet, "/waiotech/runs", nil, nil)
	expect(t, resp, body, http.StatusOK, `<th scope="col">Took</th>`, `<th scope="col">Shipped to</th>`)
}

// A long diff opens its first files and leaves the rest closed, each one a
// click, or a link in the list of changed files, away.
func TestLongDiffsOpenOnlyTheFirstFiles(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	bare, err := store.Path(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	runGit(t, work, "clone", "--quiet", bare, ".")
	runGit(t, work, "checkout", "--quiet", "-b", "wide")
	for i := 0; i < 14; i++ {
		writeFile(t, work, fmt.Sprintf("wide/file%02d.txt", i), []byte("x\n"))
	}
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "--quiet", "-m", "Many files")
	runGit(t, work, "push", "--quiet", "origin", "wide")
	syncRepoRefs(t, database, store, repo.ID)

	resp, body := b.do(http.MethodGet, "/waiotech/commits?base=main&ref=wide&tab=changes", nil, nil)
	expect(t, resp, body, http.StatusOK, `Changes <span class="tab-count">14</span>`, `data-diff-all="open"`, `data-diff-all="closed"`)
	if strings.Contains(body, "changed files") {
		t.Error("the Changes tab repeats its count in a title under the tab")
	}
	if n := strings.Count(body, `<details class="diff-file" id="diff-`); n != 14 {
		t.Fatalf("%d diffs, want 14", n)
	}
	if n := strings.Count(body, `<details class="diff-file" id="diff-`) - strings.Count(body, `" open>`); n != 14 {
		t.Errorf("%d of 14 diffs are closed, want all of them: the list is for choosing what to open", n)
	}

	// A change of a few files opens them, and has nothing to expand all of.
	runGit(t, work, "checkout", "--quiet", "main")
	runGit(t, work, "checkout", "--quiet", "-b", "narrow")
	for i := 0; i < 3; i++ {
		writeFile(t, work, fmt.Sprintf("narrow/file%d.txt", i), []byte("y\n"))
	}
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "--quiet", "-m", "Few files")
	runGit(t, work, "push", "--quiet", "origin", "narrow")
	syncRepoRefs(t, database, store, repo.ID)
	resp, body = b.do(http.MethodGet, "/waiotech/commits?base=main&ref=narrow&tab=changes", nil, nil)
	expect(t, resp, body, http.StatusOK, `Changes <span class="tab-count">3</span>`, `id="diff-0" open>`, `id="diff-2" open>`)
	resp, body = b.do(http.MethodGet, "/waiotech/commit/"+mustResolve(t, mustOpen(t, store, repo), "narrow"), nil, nil)
	expect(t, resp, body, http.StatusOK, "3 changed files", `id="diff-1" open>`)
	resp, body = b.do(http.MethodGet, "/waiotech/commits?base=main&ref=release&tab=changes", nil, nil)
	expect(t, resp, body, http.StatusOK, `Changes <span class="tab-count">1</span>`)
	if strings.Contains(body, "data-diff-all") || strings.Contains(body, `class="panel-header"><h2 class="panel-title" id="diff-summary-title"`) {
		t.Error("a single file offers Expand all")
	}
}

func TestTagsAreNewestVersionFirst(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	bare, err := store.Path(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"v2.0.0-beta.2", "v2.0.0-beta.10", "v2.0.0-beta.9", "v2.0.0-beta.1"} {
		runGit(t, bare, "tag", name, "main")
	}
	syncRepoRefs(t, database, store, repo.ID)

	resp, body := b.do(http.MethodGet, "/waiotech", nil, nil)
	expect(t, resp, body, http.StatusOK)
	at := func(s string) int { return strings.Index(body, `aria-label="Run tag `+s+`"`) }
	newestFirst := []string{"v2.0.0-beta.10", "v2.0.0-beta.9", "v2.0.0-beta.2", "v2.0.0-beta.1", "v1.0.0"}
	for i := 1; i < len(newestFirst); i++ {
		if at(newestFirst[i-1]) < 0 || at(newestFirst[i-1]) > at(newestFirst[i]) {
			t.Errorf("Overview tag %s is not before %s", newestFirst[i-1], newestFirst[i])
		}
	}
	// And so are the choices a picker offers.
	_, body = b.do(http.MethodGet, "/waiotech/commits", nil, nil)
	first := strings.Index(body, `<option value="v2.0.0-beta.10">tag`)
	last := strings.Index(body, `<option value="v1.0.0">tag`)
	if first < 0 || last < 0 || first > last || strings.Index(body, `<option value="main">branch`) > first {
		t.Errorf("the picker's refs are not branches, then tags newest first")
	}
}

// Pushing many tags at once is one line of activity, which names the newest
// and counts the rest, not a feed of its own.
func TestABulkPushIsOneLineOfActivity(t *testing.T) {
	e := setupGitHTTP(t)
	_, cred := e.person("darius", false, auth.ScopeWrite)
	e.initWork(cred)
	e.commit("README.md", "# demo\n")
	e.mustGit(e.work, "push", "--quiet", "origin", "main")
	refs := []string{}
	for i := 1; i <= 8; i++ {
		e.mustGit(e.work, "tag", fmt.Sprintf("v1.0.0-beta.%d", i))
		refs = append(refs, fmt.Sprintf("v1.0.0-beta.%d", i))
	}
	e.mustGit(e.work, append([]string{"push", "--quiet", "origin"}, refs...)...)

	b := signInTo(t, e, "darius")
	resp, body := b.do(http.MethodGet, "/demo/activity", nil, nil)
	text := stripTags(body)
	expect(t, resp, body, http.StatusOK, "v1.0.0-beta.8", "v1.0.0-beta.7", "v1.0.0-beta.6")
	if !strings.Contains(text, "darius created 8 tags") || !strings.Contains(text, "and 5 more") {
		t.Errorf("the push reads:\n%s", text)
	}
	if strings.Contains(text, "v1.0.0-beta.1 ") || strings.Count(text, "created") > 3 {
		t.Errorf("the push is listed tag by tag:\n%s", text)
	}
}
