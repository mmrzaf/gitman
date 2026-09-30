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

func TestHistoryCommitsOfARefAndAPath(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	head := mustResolve(t, mustOpen(t, store, repo), "main")

	// The default branch's log: newest first, with the branches and tags at
	// each commit.
	resp, body := b.do(http.MethodGet, "/waiotech/history", nil, nil)
	expect(t, resp, body, http.StatusOK, `id="history-title"`, "Update README", "Initial commit", `data-live-region="history-commits"`,
		`title="branch main"`, `title="tag v1.0.0"`)
	if strings.Contains(body, "On release") {
		t.Error("the default branch's log lists a commit of another branch")
	}
	if strings.Index(body, "Update README") > strings.Index(body, "Initial commit") {
		t.Error("the log is not newest first")
	}

	resp, body = b.do(http.MethodGet, "/waiotech/history?ref=release", nil, nil)
	expect(t, resp, body, http.StatusOK, "On release", "Initial commit")
	resp, body = b.do(http.MethodGet, "/waiotech/history?ref="+url.QueryEscape("release/1.2"), nil, nil)
	expect(t, resp, body, http.StatusOK, "On release/1.2")
	resp, body = b.do(http.MethodGet, "/waiotech/history?ref="+head[:10], nil, nil)
	expect(t, resp, body, http.StatusOK, "Update README", "commit "+head[:7])

	// Filtered to a path: only the commits that touched it.
	resp, body = b.do(http.MethodGet, "/waiotech/history?path=server", nil, nil)
	expect(t, resp, body, http.StatusOK, "Initial commit", `<span class="mono">server</span>`, "All paths")
	if strings.Contains(body, "Update README") {
		t.Error("a path's history lists a commit that did not touch it")
	}
	resp, body = b.do(http.MethodGet, "/waiotech/history?path=README.md", nil, nil)
	expect(t, resp, body, http.StatusOK, "Update README", "Initial commit")
	resp, body = b.do(http.MethodGet, "/waiotech/history?path=nothing/here", nil, nil)
	expect(t, resp, body, http.StatusOK, "No commits")

	// A path is a file's name, never a pathspec, and never leaves the tree.
	resp, body = b.do(http.MethodGet, "/waiotech/history?path="+url.QueryEscape(":(exclude)server"), nil, nil)
	expect(t, resp, body, http.StatusOK, "No commits")
	resp, body = b.do(http.MethodGet, "/waiotech/history?path="+url.QueryEscape("server/../README.md"), nil, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity)
	resp, body = b.do(http.MethodGet, "/waiotech/history?path=%00", nil, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity)

	resp, body = b.do(http.MethodGet, "/waiotech/history?ref=nope", nil, nil)
	expect(t, resp, body, http.StatusNotFound)
	// A ref followed by a path is not a ref.
	resp, body = b.do(http.MethodGet, "/waiotech/history?ref=main/server", nil, nil)
	expect(t, resp, body, http.StatusNotFound)

	// The ref picker's Switch button, without scripting, sends the form.
	resp, body = b.do(http.MethodGet, "/waiotech/history?ref=release&path=which.txt", nil, nil)
	expect(t, resp, body, http.StatusOK, `action="/waiotech/history"`, `name="path" value="which.txt"`, "On release")
}

func TestHistoryShowsMergesRunsAndPaging(t *testing.T) {
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
	for i := 0; i < historyPageSize; i++ {
		runGit(t, work, "commit", "--quiet", "--allow-empty", "-m", fmt.Sprintf("Filler %d", i))
	}
	merged := runGit(t, work, "rev-parse", "HEAD~"+fmt.Sprint(historyPageSize))
	runGit(t, work, "push", "--quiet", "origin", "main")
	syncRepoRefs(t, database, store, repo.ID)
	if _, err := database.Pool.Exec(context.Background(), `
		INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, ref_kind, ref_name, finished_at)
		VALUES ('r-merge', $1, 7, $2, 'manual', 'passed', 'branch', 'main', now())
	`, repo.ID, merged); err != nil {
		t.Fatal(err)
	}

	// The newest page is all filler, and points to the older one.
	resp, body := b.do(http.MethodGet, "/waiotech/history", nil, nil)
	expect(t, resp, body, http.StatusOK, "Filler 0", `href="/waiotech/history?ref=main&amp;skip=30">Older`)
	if strings.Contains(body, "Merge release") || strings.Contains(body, ">Newer<") {
		t.Error("the first page shows the second's commits or a Newer link")
	}

	resp, body = b.do(http.MethodGet, "/waiotech/history?skip=30", nil, nil)
	expect(t, resp, body, http.StatusOK, "Merge release", `<span class="chip">merge</span>`, `href="/waiotech/runs/7"`, "passed", `>Newer<`)
	if strings.Count(body, `<span class="chip">merge</span>`) != 1 {
		t.Errorf("only the merge commit has a merge marker:\n%s", body)
	}
}

func TestHistoryBeforeTheDefaultBranchIsPushed(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	if resp, body := b.do(http.MethodPost, "/repos", url.Values{"name": {"fresh"}}, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create repo: %d\n%s", resp.StatusCode, body)
	}
	resp, body := b.do(http.MethodGet, "/fresh/history", nil, nil)
	expect(t, resp, body, http.StatusOK, "Nothing pushed yet")
	resp, body = b.do(http.MethodGet, "/fresh/history?tab=activity", nil, nil)
	expect(t, resp, body, http.StatusOK, "created the repository")
	// Asking for a ref that is not there is still a 404.
	resp, body = b.do(http.MethodGet, "/fresh/history?ref=main", nil, nil)
	expect(t, resp, body, http.StatusNotFound)
	_ = store
}

func TestHistoryActivityFromRealPushes(t *testing.T) {
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

	b := newBrowser(t, e.server)
	if resp, body := login(b, "darius", "correct-horse-battery", "/"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign in: %d\n%s", resp.StatusCode, body)
	}
	resp, body := b.do(http.MethodGet, "/demo/history?tab=activity", nil, nil)
	expect(t, resp, body, http.StatusOK, `data-live-region="history-activity"`, `aria-current="page">`+"<", "darius")
	for _, want := range []string{" created ", " pushed ", " force-pushed ", " moved ", " deleted "} {
		if !strings.Contains(stripTags(body), want) {
			t.Errorf("Activity has no %q entry:\n%s", want, stripTags(body))
		}
	}
	// A push to main with a "run" rule started a run, which its entry links.
	expect(t, resp, body, http.StatusOK, `href="/demo/runs/1"`)
	// Newest first: the delete came last.
	text := stripTags(body)
	if strings.Index(text, " deleted ") > strings.Index(text, " created ") {
		t.Errorf("Activity is not newest first:\n%s", text)
	}
}

func TestHistoryActivityShowsRefusedPushes(t *testing.T) {
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

	b := newBrowser(t, e.server)
	if resp, body := login(b, "darius", "correct-horse-battery", "/"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign in: %d\n%s", resp.StatusCode, body)
	}
	resp, body := b.do(http.MethodGet, "/demo/history?tab=activity", nil, nil)
	expect(t, resp, body, http.StatusOK, "push was refused", "this rewrites history (a force-push)", "the default branch cannot be deleted")
	if strings.Count(stripTags(body), "push was refused") != 2 {
		t.Errorf("want the two refused pushes listed:\n%s", stripTags(body))
	}
	// Nothing was accepted from either.
	if strings.Contains(stripTags(body), "darius force-pushed") || strings.Contains(stripTags(body), "darius deleted") {
		t.Errorf("a refused push shows as a change:\n%s", stripTags(body))
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
