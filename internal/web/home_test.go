package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestHomeListsRepositoriesAndWhatIsDeployed: every repository is a row, with
// what landed last on its default branch and how it ran, and every target
// something is live on is a row of the list of deployments.
func TestHomeListsRepositoriesAndWhatIsDeployed(t *testing.T) {
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

	// Before anything has run or shipped, there are no columns or lists for
	// either.
	resp, body := b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, `id="repos-title"`, "Nothing in it", "Nothing pushed yet", "Update README", `<div class="region" data-live-region="deployments"></div>`)
	if strings.Contains(body, `<th scope="col">Run</th>`) || strings.Contains(body, `id="deployments-title"`) {
		t.Error("Home has a column or a list with nothing in it")
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

	resp, body = b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, `<th scope="col">Run</th>`, "passed", "<span>#1</span>", `id="deployments-title"`,
		`<th scope="col">Not shipped</th>`)
	// The repository's row: its latest commit, which leads to it, and its run.
	row := body[strings.Index(body, `href="/waiotech">waiotech</a>`):]
	row = row[:strings.Index(row, "</tr>")]
	for _, want := range []string{"Update README", `href="/waiotech/commit/` + head + `"`, `class="person">Test<`, "<span>#1</span>"} {
		if !strings.Contains(row, want) {
			t.Errorf("waiotech's row lacks %q:\n%s", want, row)
		}
	}
	// The deployments: production is one commit behind the default branch,
	// staging is at it.
	list := body[strings.Index(body, `id="deployments-title"`):]
	list = list[:strings.Index(list, "</section>")]
	production, staging := strings.Index(list, "<td>production</td>"), strings.Index(list, "<td>staging</td>")
	if production < 0 || staging < 0 || production > staging {
		t.Errorf("the deployments are not one row per target, in order:\n%s", list)
	}
	if !strings.Contains(list, `>1 commit</a>`) || strings.Count(list, "Up to date") != 1 {
		t.Errorf("the deployments do not say how far behind each target is:\n%s", list)
	}
	// Two columns, then the list of small cards: attention, then activity.
	if strings.Index(body, `id="timeline-title"`) < strings.Index(body, `id="deployments-title"`) {
		t.Error("Activity comes before the lists")
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

func TestOverviewHasBranchesTagsAndTheRunStrip(t *testing.T) {
	database, store, b := setupWithStore(t)
	ctx := context.Background()
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	person := mustPerson(t, database, "darius")
	head := mustResolve(t, mustOpen(t, store, repo), "main")

	// No runs yet: no strip. The Overview is branches and tags, with what
	// leads to the files and the download beside them.
	resp, body := b.do(http.MethodGet, "/waiotech", nil, nil)
	expect(t, resp, body, http.StatusOK, `id="branches-title"`, `id="tags-title"`,
		`href="/waiotech@main">`, `href="/waiotech/archive/main.tar.gz"`)
	for _, absent := range []string{`id="strip-title"`, `id="latest-title"`, "Latest on"} {
		if strings.Contains(body, absent) {
			t.Errorf("the Overview has %s", absent)
		}
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
