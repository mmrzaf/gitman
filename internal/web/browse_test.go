package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

// TestTooLargeToShow is a git operation that ran out of time: it reaches
// the person as "too large to show," even wrapped inside a *git.Error the
// way a real timeout from run or stream is; any other error passes
// through unchanged.
func TestTooLargeToShow(t *testing.T) {
	if err := tooLargeToShow(context.DeadlineExceeded); apperr.KindOf(err) != apperr.KindTooLarge {
		t.Errorf("a deadline exceeded error = %v, want KindTooLarge", err)
	}
	wrapped := &git.Error{Args: []string{"diff-tree"}, Err: context.DeadlineExceeded}
	if err := tooLargeToShow(wrapped); apperr.KindOf(err) != apperr.KindTooLarge {
		t.Errorf("a wrapped deadline exceeded error = %v, want KindTooLarge", err)
	}
	other := errors.New("boom")
	if err := tooLargeToShow(other); !errors.Is(err, other) {
		t.Errorf("an unrelated error = %v, want it passed through unchanged", err)
	}
}

func TestSplitRepoRef(t *testing.T) {
	cases := []struct {
		seg, name, refAndPath string
		hasRef                bool
	}{
		{"waiotech", "waiotech", "", false},
		{"waiotech@develop", "waiotech", "develop", true},
		{"waiotech@release/1.2/server/app.py", "waiotech", "release/1.2/server/app.py", true},
		{"waiotech@", "waiotech", "", true},
	}
	for _, c := range cases {
		name, refAndPath, hasRef := splitRepoRef(c.seg)
		if name != c.name || refAndPath != c.refAndPath || hasRef != c.hasRef {
			t.Errorf("splitRepoRef(%q) = %q, %q, %v; want %q, %q, %v",
				c.seg, name, refAndPath, hasRef, c.name, c.refAndPath, c.hasRef)
		}
	}
}

func TestRefPath(t *testing.T) {
	if got := refPath("waiotech", "develop", ""); got != "/waiotech@develop" {
		t.Errorf("refPath(root) = %q", got)
	}
	if got := refPath("waiotech", "develop", "server/app.py"); got != "/waiotech@develop/server/app.py" {
		t.Errorf("refPath(file) = %q", got)
	}
}

func TestBreadcrumb(t *testing.T) {
	if got := breadcrumb(""); got != nil {
		t.Errorf("breadcrumb(\"\") = %v, want nil", got)
	}
	got := breadcrumb("server/app.py")
	want := []breadcrumbPart{{"server", "server"}, {"app.py", "server/app.py"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("breadcrumb = %+v, want %+v", got, want)
	}
}

func TestDirOf(t *testing.T) {
	cases := map[string]string{"": "", "app.py": "", "server/app.py": "server", "a/b/c.py": "a/b"}
	for in, want := range cases {
		if got := dirOf(in); got != want {
			t.Errorf("dirOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitLines(t *testing.T) {
	if got := splitLines("a\nb\nc\n"); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("splitLines(trailing newline) = %v", got)
	}
	if got := splitLines("a\nb"); len(got) != 2 {
		t.Errorf("splitLines(no trailing newline) = %v", got)
	}
	if got := splitLines(""); len(got) != 1 || got[0] != "" {
		t.Errorf("splitLines(empty) = %v", got)
	}
}

func TestLooksBinary(t *testing.T) {
	if looksBinary([]byte("hello\nworld\n")) {
		t.Error("plain text flagged as binary")
	}
	if !looksBinary([]byte("hello\x00world")) {
		t.Error("content with a NUL byte not flagged as binary")
	}
}

// runGit runs a Git client command with a fixed identity, failing the
// test on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, name string, content []byte) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedFilesRepo creates a repository through the web app, pushes real
// commits and branches directly into its bare directory (bypassing the
// HTTP Git transport, which git_http_test.go already covers end to end),
// and syncs the ref index the way post-receive normally would.
func seedFilesRepo(t *testing.T, database *postgres.DB, store *git.Store, b *browser) *reposvc.Repo {
	t.Helper()
	resp, body := b.do(http.MethodPost, "/repos", url.Values{"name": {"waiotech"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create repo: %d\n%s", resp.StatusCode, body)
	}
	repo, err := reposvc.NewService(database, nil, "").GetByName(context.Background(), "waiotech")
	if err != nil {
		t.Fatal(err)
	}
	barePath, err := store.Path(repo.ID)
	if err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	runGit(t, work, "clone", "--quiet", barePath, ".")
	writeFile(t, work, "README.md", []byte("# hello\n"))
	writeFile(t, work, "server/app.py", []byte("print('hi')\n"))
	writeFile(t, work, "binary.bin", []byte("BIN\x00DATA"))
	writeFile(t, work, "big.bin", bytes.Repeat([]byte("x"), maxFileDisplayBytes+1))
	// Paths shaped like pages under a repository, which must still be
	// browsable as files.
	writeFile(t, work, "runs/42", []byte("not a run\n"))
	writeFile(t, work, "settings/info/refs", []byte("not settings\n"))
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "--quiet", "-m", "Initial commit")
	firstCommit := runGit(t, work, "rev-parse", "HEAD")

	// A branch and, in the separate tag namespace, a tag that shares its
	// name as a prefix — Git itself refuses two BRANCHES named "release"
	// and "release/1.2" (one ref path can't be both a file and a
	// directory), so this is the realistic case for the
	// longest-prefix-match rule: two refs of different kinds that share
	// a name prefix.
	runGit(t, work, "branch", "release")
	runGit(t, work, "checkout", "--quiet", "release")
	writeFile(t, work, "which.txt", []byte("release\n"))
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "--quiet", "-m", "On release")

	runGit(t, work, "checkout", "--quiet", "main")
	writeFile(t, work, "which.txt", []byte("release/1.2\n"))
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "--quiet", "-m", "On release/1.2")
	tagCommit := runGit(t, work, "rev-parse", "HEAD")
	runGit(t, work, "tag", "release/1.2", tagCommit)
	runGit(t, work, "reset", "--quiet", "--hard", firstCommit)
	writeFile(t, work, "README.md", []byte("# hello, updated\n"))
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "--quiet", "-m", "Update README")

	runGit(t, work, "tag", "v1.0.0", firstCommit)
	runGit(t, work, "push", "--quiet", "origin", "main", "release", "v1.0.0", "release/1.2")

	syncRepoRefs(t, database, store, repo.ID)
	return repo
}

// syncRepoRefs brings the ref index up to date from the repository's
// actual Git state, the way post-receive does after a real push.
func syncRepoRefs(t *testing.T, database *postgres.DB, store *git.Store, repoID string) {
	t.Helper()
	svc := reposvc.NewService(database, store, "")
	r, err := svc.GetByID(context.Background(), repoID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncRefs(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

func mustRepo(t *testing.T, database *postgres.DB, name string) *reposvc.Repo {
	t.Helper()
	repo, err := reposvc.NewService(database, nil, "").GetByName(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func mustResolve(t *testing.T, gitRepo *git.Repo, rev string) string {
	t.Helper()
	hash, err := gitRepo.ResolveCommit(context.Background(), rev)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestFilesDirectoryAndFile(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	seedFilesRepo(t, database, store, b)

	resp, body := b.do(http.MethodGet, "/waiotech@main", nil, nil)
	expect(t, resp, body, http.StatusOK, "README.md", "server", "binary.bin")

	resp, body = b.do(http.MethodGet, "/waiotech@main/server/app.py", nil, nil)
	expect(t, resp, body, http.StatusOK, "print(&#39;hi&#39;)", "Raw", "Permalink")

	resp, body = b.do(http.MethodGet, "/waiotech@main/server", nil, nil)
	expect(t, resp, body, http.StatusOK, "app.py")

	resp, body = b.do(http.MethodGet, "/waiotech@main/binary.bin", nil, nil)
	expect(t, resp, body, http.StatusOK, "binary file")

	resp, body = b.do(http.MethodGet, "/waiotech@main/big.bin", nil, nil)
	expect(t, resp, body, http.StatusOK, "too large")

	resp, body = b.do(http.MethodGet, "/waiotech@main/no/such/file", nil, nil)
	expect(t, resp, body, http.StatusNotFound, "no/such/file")

	resp, body = b.do(http.MethodGet, "/waiotech@no-such-ref", nil, nil)
	expect(t, resp, body, http.StatusNotFound, "not a branch, tag, or commit")

	// Without JavaScript, the ref picker submits ?ref= and lands on the
	// same path at the chosen ref.
	resp, body = b.do(http.MethodGet, "/waiotech@main/server?ref=release/1.2", nil, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/waiotech@release/1.2/server" {
		t.Fatalf("ref picker without JavaScript: %d %q\n%s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	resp, body = b.do(http.MethodGet, "/waiotech@main/server", nil, nil)
	expect(t, resp, body, http.StatusOK, `<form class="ref-picker" method="get">`, `class="btn btn-sm no-js-only">Switch</button>`)

	// The file finder does nothing without JavaScript to open its dialog,
	// so it stays hidden rather than sitting there inert.
	if !strings.Contains(body, `class="btn js-only" data-file-finder-open`) {
		t.Fatalf("Find file button lost its js-only class, so it would show with nothing to make it work:\n%s", body)
	}
}

func TestFilesRaw(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	seedFilesRepo(t, database, store, b)

	resp, body := b.do(http.MethodGet, "/waiotech@main/server/app.py?raw", nil, nil)
	if resp.StatusCode != http.StatusOK || body != "print('hi')\n" {
		t.Fatalf("raw file: %d %q", resp.StatusCode, body)
	}
	// Raw files are downloads, with exact bytes and no browser execution.
	if resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("raw Content-Type = %q, want safe plain text", resp.Header.Get("Content-Type"))
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.Contains(got, "app.py") {
		t.Errorf("Content-Disposition = %q", got)
	}

	resp, _ = b.do(http.MethodGet, "/waiotech@main/binary.bin?raw", nil, nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("raw binary: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	resp, body = b.do(http.MethodGet, "/waiotech@main/big.bin?raw", nil, nil)
	if resp.StatusCode != http.StatusOK || body != strings.Repeat("x", maxFileDisplayBytes+1) {
		t.Fatalf("large raw download: status %d, size %d", resp.StatusCode, len(body))
	}

	resp, body = b.do(http.MethodGet, "/waiotech@main?raw", nil, nil)
	expect(t, resp, body, http.StatusNotFound, "raw view")
}

func TestLongestPrefixRefMatch(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	seedFilesRepo(t, database, store, b)

	resp, body := b.do(http.MethodGet, "/waiotech@release/which.txt?raw", nil, nil)
	if resp.StatusCode != http.StatusOK || body != "release\n" {
		t.Fatalf("release/which.txt = %d %q", resp.StatusCode, body)
	}
	resp, body = b.do(http.MethodGet, "/waiotech@release/1.2/which.txt?raw", nil, nil)
	if resp.StatusCode != http.StatusOK || body != "release/1.2\n" {
		t.Fatalf("release/1.2/which.txt = %d %q", resp.StatusCode, body)
	}
}

func TestFilesAtTagAndCommit(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	seedFilesRepo(t, database, store, b)

	resp, body := b.do(http.MethodGet, "/waiotech@v1.0.0/README.md?raw", nil, nil)
	if resp.StatusCode != http.StatusOK || body != "# hello\n" {
		t.Fatalf("tag content = %d %q", resp.StatusCode, body)
	}

	gitRepo, err := store.Open(mustRepo(t, database, "waiotech").ID)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := gitRepo.Refs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var tagCommit string
	for _, ref := range refs {
		if ref.Kind == git.KindTag && ref.Name == "v1.0.0" {
			tagCommit = ref.Commit
		}
	}
	if tagCommit == "" {
		t.Fatal("v1.0.0 not found in Refs()")
	}
	short := tagCommit[:10]
	resp, body = b.do(http.MethodGet, "/waiotech@"+short+"/README.md?raw", nil, nil)
	if resp.StatusCode != http.StatusOK || body != "# hello\n" {
		t.Fatalf("bare commit content = %d %q", resp.StatusCode, body)
	}
}

func TestPermalinkSurvivesRefMovement(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	seedFilesRepo(t, database, store, b)

	resp, body := b.do(http.MethodGet, "/waiotech@main/README.md", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("files page: %d", resp.StatusCode)
	}
	idx := strings.Index(body, ">Permalink<")
	if idx < 0 {
		t.Fatal("no Permalink link found on the page")
	}
	linkStart := strings.LastIndex(body[:idx], `href="`) + len(`href="`)
	linkEnd := strings.Index(body[linkStart:], `"`)
	permalink := body[linkStart : linkStart+linkEnd]
	if !strings.HasPrefix(permalink, "/waiotech@") || strings.Contains(permalink, "@main") {
		t.Fatalf("permalink = %q, want a commit-hash ref", permalink)
	}

	resp, body = b.do(http.MethodGet, permalink+"?raw", nil, nil)
	if resp.StatusCode != http.StatusOK || body != "# hello, updated\n" {
		t.Fatalf("permalink content = %d %q", resp.StatusCode, body)
	}

	// Move main forward; the permalink must still show the old content.
	repo := mustRepo(t, database, "waiotech")
	barePath, err := store.Path(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	runGit(t, work, "clone", "--quiet", barePath, ".")
	writeFile(t, work, "README.md", []byte("# hello, moved on\n"))
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "--quiet", "-m", "Move main forward")
	runGit(t, work, "push", "--quiet", "origin", "main")
	syncRepoRefs(t, database, store, repo.ID)

	resp, body = b.do(http.MethodGet, permalink+"?raw", nil, nil)
	if resp.StatusCode != http.StatusOK || body != "# hello, updated\n" {
		t.Fatalf("permalink after main moved = %d %q, want the old content unchanged", resp.StatusCode, body)
	}
	resp, body = b.do(http.MethodGet, "/waiotech@main/README.md?raw", nil, nil)
	if resp.StatusCode != http.StatusOK || body != "# hello, moved on\n" {
		t.Fatalf("@main after moving = %d %q", resp.StatusCode, body)
	}
}

func TestPathHistoryAndLastChanged(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	seedFilesRepo(t, database, store, b)

	resp, body := b.do(http.MethodGet, "/waiotech@main/README.md", nil, nil)
	expect(t, resp, body, http.StatusOK, "Last changed in", "Update README", `href="/waiotech/commits?path=README.md&amp;ref=main"`)
	if strings.Contains(body, "data-tab-panel") {
		t.Error("Files still has tabs")
	}
	// The old History tab's address leads to the commits, filtered to the path.
	resp, body = b.do(http.MethodGet, "/waiotech@main/README.md?tab=history", nil, nil)
	expect(t, resp, body, http.StatusMovedPermanently)
	if got := resp.Header.Get("Location"); got != "/waiotech/commits?path=README.md&ref=main" {
		t.Errorf("old history address redirects to %q", got)
	}
	resp, body = b.do(http.MethodGet, "/waiotech@main?tab=history&skip=20", nil, nil)
	expect(t, resp, body, http.StatusMovedPermanently)
	if got := resp.Header.Get("Location"); got != "/waiotech/commits?ref=main&skip=20" {
		t.Errorf("old history address with skip redirects to %q", got)
	}
}

func TestCommitView(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	seedFilesRepo(t, database, store, b)

	gitRepo, err := store.Open(mustRepo(t, database, "waiotech").ID)
	if err != nil {
		t.Fatal(err)
	}
	commits, _, err := gitRepo.Log(context.Background(), mustResolve(t, gitRepo, "main"), "", 0, 1)
	if err != nil || len(commits) == 0 {
		t.Fatal(err)
	}
	head := commits[0]

	resp, body := b.do(http.MethodGet, "/waiotech/commit/"+head.Hash, nil, nil)
	expect(t, resp, body, http.StatusOK, "Update README", "modified", "README.md")

	resp, body = b.do(http.MethodGet, "/waiotech/commit/"+head.Hash[:10], nil, nil)
	expect(t, resp, body, http.StatusOK, "Update README")

	resp, body = b.do(http.MethodGet, "/waiotech/commit/0000000", nil, nil)
	expect(t, resp, body, http.StatusNotFound, "is not a commit")

	// Only a hash names a commit here: not a ref, and not Git's search
	// syntax, which would scan every commit message.
	for _, rev := range []string{"main", ":%2FUpdate", "main~1"} {
		resp, body = b.do(http.MethodGet, "/waiotech/commit/"+rev, nil, nil)
		expect(t, resp, body, http.StatusNotFound, "is not a commit")
	}
}

// TestCompareAddressesStillWork: beta 21 compared two refs at
// /compare/{base}...{head}, and links to it outlive the release.
func TestCompareAddressesStillWork(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	seedFilesRepo(t, database, store, b)

	resp, body := b.do(http.MethodGet, "/waiotech/compare/release...release/1.2", nil, nil)
	expect(t, resp, body, http.StatusMovedPermanently)
	if got := resp.Header.Get("Location"); got != "/waiotech/commits?base=release&ref=release%2F1.2" {
		t.Errorf("redirects to %q", got)
	}
	resp, body = b.do(http.MethodGet, "/waiotech/compare", nil, nil)
	expect(t, resp, body, http.StatusMovedPermanently)
	if got := resp.Header.Get("Location"); got != "/waiotech/commits" {
		t.Errorf("redirects to %q", got)
	}
	for _, path := range []string{"/waiotech/compare/nonsense", "/waiotech/compare/...main", "/waiotech/compare/main..."} {
		resp, body = b.do(http.MethodGet, path, nil, nil)
		expect(t, resp, body, http.StatusNotFound, "needs")
	}
}

func TestTreePaths(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	seedFilesRepo(t, database, store, b)

	resp, body := b.do(http.MethodGet, "/waiotech/tree-paths?ref=main", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tree-paths: %d", resp.StatusCode)
	}
	var paths []string
	if err := json.Unmarshal([]byte(body), &paths); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, body)
	}
	found := map[string]bool{}
	for _, p := range paths {
		found[p] = true
	}
	for _, want := range []string{"README.md", "server/app.py", "binary.bin"} {
		if !found[want] {
			t.Errorf("tree-paths missing %q: %v", want, paths)
		}
	}

	resp, body = b.do(http.MethodGet, "/waiotech/tree-paths", nil, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity, "ref is required")
}

func TestFilesShapedLikePages(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	seedFilesRepo(t, database, store, b)
	for path, want := range map[string]string{
		"/waiotech@main/runs/42?raw":            "not a run\n",
		"/waiotech@main/settings/info/refs?raw": "not settings\n",
	} {
		resp, body := b.do(http.MethodGet, path, nil, nil)
		if resp.StatusCode != http.StatusOK || body != want {
			t.Errorf("GET %s = %d %q, want the file", path, resp.StatusCode, body)
		}
	}
}

// TestTreeWalkIsBoundedByEntriesNotOnlyFiles is a tree built to be
// expensive: three levels of 150 directories that all share the level
// below, ending in an empty tree. It is five objects and no files, yet
// walking it naively visits over three million directories.
func TestTreeWalkIsBoundedByEntriesNotOnlyFiles(t *testing.T) {
	dir := t.TempDir()
	gitCmd := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gitCmd("", "init", "--quiet", "--bare")
	tree := gitCmd("", "mktree")
	for level := 0; level < 3; level++ {
		var listing strings.Builder
		for i := 0; i < 150; i++ {
			fmt.Fprintf(&listing, "040000 tree %s\td%03d\n", tree, i)
		}
		tree = gitCmd(listing.String(), "mktree")
	}
	gitRepo := git.OpenHookRepo(dir, nil)
	defer gitRepo.Close()

	walk := treeWalk{git: gitRepo}
	start := time.Now()
	if err := walk.dir(context.Background(), tree, ""); err != nil {
		t.Fatal(err)
	}
	if walk.entries > maxTreeEntries || len(walk.paths) != 0 {
		t.Fatalf("walked %d entries and found %d paths; want at most %d entries", walk.entries, len(walk.paths), maxTreeEntries)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the walk took %s", elapsed)
	}
}
