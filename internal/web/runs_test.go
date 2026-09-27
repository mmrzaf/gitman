package web

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

const runsPipeline = "image: alpine:3.20\nsteps:\n  - name: test\n    run: 'true'\n"

func TestRunActions(t *testing.T) {
	database, store, b := setupWithStore(t)
	ctx := context.Background()
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)

	barePath, err := store.Path(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	runGit(t, work, "clone", "--quiet", barePath, ".")
	writeFile(t, work, ci.FileName, []byte(runsPipeline))
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "--quiet", "-m", "Add pipeline")
	runGit(t, work, "push", "--quiet", "origin", "main")
	commit := runGit(t, work, "rev-parse", "HEAD")
	syncRepoRefs(t, database, store, repo.ID)

	resp, body := b.do(http.MethodPost, "/waiotech/runs", url.Values{"ref": {"refs/heads/main"}}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/waiotech/runs/1" {
		t.Fatalf("run main: %d %q\n%s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	resp, body = b.do(http.MethodGet, "/waiotech/runs/1", nil, nil)
	expect(t, resp, body, http.StatusOK, "Started run #1.", "run #1", "queued", ">Cancel<", `data-live-events="/events?run=`)

	resp, _ = b.do(http.MethodPost, "/waiotech/runs/1/cancel", url.Values{}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("cancel: %d", resp.StatusCode)
	}
	resp, body = b.do(http.MethodGet, "/waiotech/runs/1", nil, nil)
	expect(t, resp, body, http.StatusOK, "Cancelled by darius.", "Run again")
	if strings.Contains(body, "data-live-events") {
		t.Error("a finished run still streams updates")
	}

	resp, _ = b.do(http.MethodPost, "/waiotech/runs/1/again", url.Values{}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/waiotech/runs/2" {
		t.Fatalf("run again: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// A run of a protected ref can be started again only by someone who
	// may push to it.
	if err := reposvc.NewService(database, store, "").SaveRule(ctx, repo.ID, reposvc.Rule{
		Kind: git.KindBranch, Pattern: "main", PushPolicy: reposvc.PushAdmins,
	}, ""); err != nil {
		t.Fatal(err)
	}
	gitRepo, err := store.Open(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ci.NewService(database).Start(ctx, ci.StartParams{
		RepoID: repo.ID, Git: gitRepo, Commit: commit, RefKind: git.KindBranch, RefName: "main",
	}); err != nil {
		t.Fatal(err)
	}
	resp, body = b.do(http.MethodPost, "/waiotech/runs/3/again", url.Values{}, nil)
	expect(t, resp, body, http.StatusForbidden, "may not push to branch main")
	// Nor can they cancel it: stopping a protected ref's run, which may be
	// shipping, takes the same permission as starting one.
	resp, body = b.do(http.MethodPost, "/waiotech/runs/3/cancel", url.Values{}, nil)
	expect(t, resp, body, http.StatusForbidden, "may not push to branch main, so you may not cancel its runs")
	if run, err := ci.NewService(database).Run(ctx, repo.ID, 3); err != nil || run.Status != ci.StatusQueued {
		t.Fatalf("run #3 after a refused cancel = %+v, %v", run, err)
	}

	resp, body = b.do(http.MethodGet, "/waiotech/runs/99", nil, nil)
	expect(t, resp, body, http.StatusNotFound, "no run #99")
	resp, _ = b.do(http.MethodGet, "/waiotech/runs/1/log?step=0", nil, nil)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("raw log: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}

// runningRun starts a run of the seeded repository's pipeline and has a
// worker take it and start its first step, returning the run and that
// step, ready for output.
func runningRun(t *testing.T, b *browser, database *postgres.DB, repo *reposvc.Repo, store *git.Store, svc *ci.Service) (*ci.Claim, string) {
	t.Helper()
	ctx := context.Background()
	barePath, err := store.Path(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	runGit(t, work, "clone", "--quiet", barePath, ".")
	writeFile(t, work, ci.FileName, []byte(runsPipeline))
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "--quiet", "-m", "Add pipeline")
	runGit(t, work, "push", "--quiet", "origin", "main")
	syncRepoRefs(t, database, store, repo.ID)
	if resp, body := b.do(http.MethodPost, "/waiotech/runs", url.Values{"ref": {"refs/heads/main"}}, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("start a run: %d\n%s", resp.StatusCode, body)
	}
	if err := svc.RegisterWorker(ctx, "w1", "host"); err != nil {
		t.Fatal(err)
	}
	claim, err := svc.ClaimNext(ctx, "w1")
	if err != nil || claim == nil {
		t.Fatalf("ClaimNext = %v, %v", claim, err)
	}
	step := claim.Steps[0].ID
	if err := svc.StepStarted(ctx, claim.RunID, step); err != nil {
		t.Fatal(err)
	}
	return claim, step
}

// TestRunPageNumbersTheEndOfALongLog covers a log longer than the page
// shows: its end is shown, each line still numbered as in the whole log,
// so a link to a line points at the same line the raw log has there.
func TestRunPageNumbersTheEndOfALongLog(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	svc := ci.NewService(database)
	claim, step := runningRun(t, b, database, repo, store, svc)

	const total = 40000
	var chunk strings.Builder
	sequence := 0
	for n := 1; n <= total; n++ {
		fmt.Fprintf(&chunk, "line %06d of the output, padded to a steady width\n", n)
		if chunk.Len() >= 32<<10 || n == total {
			if err := svc.AppendLog(context.Background(), claim.RunID, step, sequence, chunk.String()); err != nil {
				t.Fatal(err)
			}
			sequence++
			chunk.Reset()
		}
	}

	resp, body := b.do(http.MethodGet, "/waiotech/runs/1?step=0", nil, nil)
	expect(t, resp, body, http.StatusOK, "Only the end of this output is shown.", fmt.Sprintf(`id="L%d"`, total))
	first := regexp.MustCompile(`class="line" id="L(\d+)"><a href="#L\d+" tabindex="-1" aria-hidden="true">\d+</a>line (\d+) `).FindStringSubmatch(body)
	if first == nil {
		t.Fatal("no numbered line in the page")
	}
	if n, _ := strconv.Atoi(first[1]); n <= 1 || fmt.Sprintf("%06d", n) != first[2] {
		t.Fatalf("the first line shown is numbered %s but is line %s of the log", first[1], first[2])
	}
}

// TestLogLinesThatLookLikeFailuresAreMarked is a failing step's output:
// the lines that say so are the ones marked, on the page and for the
// page's script, which marks streamed lines with the same pattern.
func TestLogLinesThatLookLikeFailuresAreMarked(t *testing.T) {
	lines := splitLog("ok  pkg/a\n--- FAIL: TestCharge\npanic: nil map\nerrors.New is fine\nstill going", 7)
	marked := map[int]bool{}
	for _, l := range lines {
		marked[l.Number] = l.Error
	}
	want := map[int]bool{7: false, 8: true, 9: true, 10: false, 11: false}
	for n, isError := range want {
		if marked[n] != isError {
			t.Errorf("line %d marked = %v, want %v", n, marked[n], isError)
		}
	}
	if len(lines) != 5 || lines[4].Text != "still going" {
		t.Fatalf("lines = %+v; a last line without a newline is still a line", lines)
	}
	if _, err := regexp.Compile("(?i)" + runPage{}.ErrorPattern()); err != nil {
		t.Fatalf("the pattern handed to the page's script does not compile: %v", err)
	}
}

// TestEventStreamStripsTerminalEscapes is streamed output with colour
// codes in it: they are removed before the page gets them, the same as
// from a log rendered with the page.
func TestEventStreamStripsTerminalEscapes(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	svc := ci.NewService(database)
	claim, step := runningRun(t, b, database, repo, store, svc)
	if err := svc.AppendLog(context.Background(), claim.RunID, step, 0, "\x1b[32mok\x1b[0m done\n"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, b.server.URL+"/events?run="+claim.RunID+"&step="+step, nil)
	for _, c := range b.cookies {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := bufio.NewScanner(resp.Body)
	for lines.Scan() {
		if data, ok := strings.CutPrefix(lines.Text(), "data: "); ok {
			if data != `{"content":"ok done\n"}` {
				t.Fatalf("log event = %s", data)
			}
			return
		}
	}
	t.Fatal("no log event")
}
