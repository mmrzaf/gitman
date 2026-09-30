package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestHomeCardsHaveTheSameShapeWhateverARepositoryHas: a repository with a
// pushed default branch, a run and two targets, one with nothing pushed and
// nothing shipped, and one that has shipped to only one target all show the
// same places.
func TestHomeCardsHaveTheSameShapeWhateverARepositoryHas(t *testing.T) {
	database, store, b := setupWithStore(t)
	ctx := context.Background()
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	person := mustPerson(t, database, "darius")
	head := mustResolve(t, mustOpen(t, store, repo), "main")
	old := mustResolve(t, mustOpen(t, store, repo), "v1.0.0")
	if resp, body := b.do(http.MethodPost, "/repos", url.Values{"name": {"fresh"}, "description": {"Nothing in it"}}, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create repo: %d\n%s", resp.StatusCode, body)
	}

	insert := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	insert(`INSERT INTO workers (id, hostname) VALUES ('w1', 'host')`)
	insert(`INSERT INTO runs (id, repo_id, number, commit_hash, ref_kind, ref_name, trigger, triggered_by, status, target, worker_id, started_at, finished_at)
	        VALUES ('r1', $1, 1, $2, 'branch', 'main', 'push', $3, 'passed', 'staging', 'w1', now(), now()),
	               ('r2', $1, 2, $4, 'tag', 'v1.0.0', 'push', $3, 'passed', 'production', 'w1', now(), now())`, repo.ID, head, person.ID, old)
	insert(`INSERT INTO deployments (id, repo_id, target, version, commit_hash, run_id, person_id) VALUES
	        ('d1', $1, 'staging', $2, $3, 'r1', $5), ('d2', $1, 'production', 'v1.0.0', $4, 'r2', $5)`, repo.ID, head[:12], head, old, person.ID)

	resp, body := b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, `class="cards"`, `id="card-waiotech"`, `id="card-fresh"`, "Nothing in it")
	card := func(name string) string {
		t.Helper()
		start := strings.Index(body, `<article class="card" aria-labelledby="card-`+name+`"`)
		if start < 0 {
			t.Fatalf("no card for %s", name)
		}
		end := strings.Index(body[start:], "</article>")
		return body[start : start+end]
	}
	waiotech, fresh := card("waiotech"), card("fresh")

	// What landed last on the default branch, and how it ran.
	for _, want := range []string{"Update README", "passed", "<span>#1</span>", `href="/waiotech/commits?ref=main"`} {
		if !strings.Contains(waiotech, want) {
			t.Errorf("waiotech's card lacks %q", want)
		}
	}
	// Every target is a row on every card, in the same order, filled or not.
	for name, c := range map[string]string{"waiotech": waiotech, "fresh": fresh} {
		production, staging := strings.Index(c, "<dt>production</dt>"), strings.Index(c, "<dt>staging</dt>")
		if production < 0 || staging < 0 || production > staging {
			t.Errorf("%s's card does not have a row for each target, in order", name)
		}
	}
	for _, want := range []string{"Nothing pushed yet", "nothing shipped"} {
		if !strings.Contains(fresh, want) {
			t.Errorf("fresh's card lacks %q", want)
		}
	}
	// Production is one commit behind the default branch; staging is at it.
	if !strings.Contains(waiotech, `1 behind</a>`) || !strings.Contains(waiotech, "up to date") {
		t.Errorf("waiotech's targets do not say how far behind each is:\n%s", waiotech)
	}
}

func TestHomeNeedsAttention(t *testing.T) {
	database, store, b := setupWithStore(t)
	ctx := context.Background()
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	person := mustPerson(t, database, "darius")
	head := mustResolve(t, mustOpen(t, store, repo), "main")
	old := mustResolve(t, mustOpen(t, store, repo), "v1.0.0")

	// All is well: nothing to look at, and no strip to say so.
	resp, body := b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK)
	if strings.Contains(body, "Needs attention") {
		t.Error("Home asks for attention when nothing wants it")
	}

	insert := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	insert(`INSERT INTO workers (id, hostname) VALUES ('w1', 'host')`)
	insert(`INSERT INTO runs (id, repo_id, number, commit_hash, ref_kind, ref_name, trigger, triggered_by, status, worker_id, started_at, finished_at)
	        VALUES ('r1', $1, 1, $2, 'branch', 'main', 'push', $3, 'failed', 'w1', now(), now()),
	               ('r2', $1, 2, $4, 'tag', 'v1.0.0', 'push', $3, 'passed', 'w1', now(), now())`, repo.ID, head, person.ID, old)
	insert(`INSERT INTO deployments (id, repo_id, target, version, commit_hash, run_id, person_id) VALUES ('d2', $1, 'production', 'v1.0.0', $2, 'r2', $3)`, repo.ID, old, person.ID)
	insert(`INSERT INTO push_refusals (id, repo_id, person_id, created_at) VALUES ('f1', $1, $2, $3), ('f0', $1, $2, $4)`,
		repo.ID, person.ID, time.Now().Add(-time.Hour), time.Now().Add(-30*24*time.Hour))
	insert(`INSERT INTO push_refusal_refs (refusal_id, position, ref, reason) VALUES ('f1', 0, 'refs/heads/main', 'this rewrites history'), ('f0', 0, 'refs/heads/x', 'a month ago')`)

	resp, body = b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, "Needs attention",
		// The latest run of the default branch failed, and links to it.
		`href="/waiotech/runs/1">waiotech: the latest run of main failed.`,
		// Production lags the default branch, and links to what it lacks.
		`waiotech: production is 1 commit behind main.`, `href="/waiotech/commits?base=`+old+`&amp;ref=main"`,
		// A refusal of the last week, with its reason and a way to the rest.
		`href="/waiotech/activity">waiotech: darius&#39;s push was refused: this rewrites history.`)
	if strings.Contains(body, "a month ago") {
		t.Error("a refusal from a month ago still wants attention")
	}

	// Without a worker, queued runs say so.
	insert(`INSERT INTO runs (id, repo_id, number, commit_hash, ref_kind, ref_name, trigger, triggered_by, status)
	        VALUES ('r3', $1, 3, $2, 'branch', 'main', 'manual', $3, 'queued')`, repo.ID, head, person.ID)
	insert(`UPDATE workers SET stopped_at = now()`)
	resp, body = b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, "Runs are queued and no worker is online")
}

func TestOverviewShowsLatestCommitsAndTheRunStrip(t *testing.T) {
	database, store, b := setupWithStore(t)
	ctx := context.Background()
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	person := mustPerson(t, database, "darius")
	head := mustResolve(t, mustOpen(t, store, repo), "main")

	// No runs yet: no strip, and the latest commits say what landed.
	resp, body := b.do(http.MethodGet, "/waiotech", nil, nil)
	expect(t, resp, body, http.StatusOK, `id="latest-title"`, "Update README", "Initial commit", `href="/waiotech/commits?ref=main">All commits`,
		// Clone leads to the files and the download, which are not in the bar.
		`href="/waiotech@main">`, `href="/waiotech/archive/main.tar.gz"`)
	if strings.Contains(body, `id="strip-title"`) {
		t.Error("the Overview has a run strip with no runs")
	}
	for _, link := range []string{`class="repobar-link" href="/waiotech@main"`, `>Files</a>`} {
		if strings.Contains(body, link) {
			t.Errorf("the repo bar still has Files (%s)", link)
		}
	}

	insert := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	insert(`INSERT INTO workers (id, hostname) VALUES ('w1', 'host')`)
	for i, status := range []string{"passed", "failed", "passed", "cancelled"} {
		insert(`INSERT INTO runs (id, repo_id, number, commit_hash, ref_kind, ref_name, trigger, triggered_by, status, worker_id, started_at, finished_at)
		        VALUES ($1, $2, $3, $4, 'branch', 'main', 'push', $5, $6, 'w1', now(), now())`, "s"+status+string(rune('a'+i)), repo.ID, i+1, head, person.ID, status)
	}
	resp, body = b.do(http.MethodGet, "/waiotech", nil, nil)
	expect(t, resp, body, http.StatusOK, `id="strip-title"`, `data-live-region="strip"`, `title="Run #4 cancelled"`, `title="Run #1 passed"`)
	// Oldest first, so a streak reads left to right.
	if strings.Index(body, `title="Run #1 passed"`) > strings.Index(body, `title="Run #4 cancelled"`) {
		t.Error("the run strip is not oldest first")
	}
	// The latest commit carries the run that ran it.
	expect(t, resp, body, http.StatusOK, `<th scope="col">Run</th>`)
}

func TestCommitsSwapInPlaceAndLinkToTheFiles(t *testing.T) {
	database, store, b := setupWithStore(t)
	signIn(t, database, b, "darius", false)
	seedFilesRepo(t, database, store, b)

	resp, body := b.do(http.MethodGet, "/waiotech/commits", nil, nil)
	expect(t, resp, body, http.StatusOK, `data-swap-region`, `<form class="toolbar commit-picker" method="get" action="/waiotech/commits" data-swap>`,
		`href="/waiotech@main" data-command="Browse the files of main"`)
	// A log does not refresh itself: nothing moves under the person reading it.
	for _, live := range []string{"data-live-events", "data-live-status"} {
		if strings.Contains(body, live) {
			t.Errorf("Commits is live (%s)", live)
		}
	}
	// The Files page belongs to the Overview it is reached from.
	resp, body = b.do(http.MethodGet, "/waiotech@main", nil, nil)
	expect(t, resp, body, http.StatusOK, `class="repobar-link" href="/waiotech" aria-current="page"`)
}
