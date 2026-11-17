package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Home lists the default-branch commit and the newest run on any ref,
// with one deployment row per live target.
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

	// Before anything has run or shipped, the lists are there and say so, and
	// there is no column for runs.
	resp, body := b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, `id="repos-title"`, "Nothing in it", "Nothing pushed yet", "Update README",
		`id="deployments-title"`, "Nothing deployed yet", "Nothing needs attention", `data-live-region="attention"`,
		`data-live-scroll="repos"`, `data-live-scroll="timeline"`)
	if strings.Contains(body, `<th scope="col">Last run</th>`) || strings.Contains(body, `<th scope="col">Comparison</th>`) {
		t.Error("Home has a column with nothing in it")
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
	insert(`INSERT INTO deployments (id, repo_id, target, version, commit_hash, run_id, person_id, source_kind, source_name) VALUES
	        ('d1', $1, 'staging', $2, $3, 'r1', $5, 'branch', 'main'), ('d2', $1, 'production', 'v1.0.0', $4, 'r2', $5, 'tag', 'v1.0.0')`, repo.ID, head[:12], head, old, person.ID)

	resp, body = b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, `<th scope="col">Last run</th>`, "passed", "<span>#2</span>", `id="deployments-title"`,
		`<th scope="col">Comparison</th>`)
	// The repository's row: its latest commit, which leads to it, and its run.
	row := body[strings.Index(body, `href="/waiotech">waiotech</a>`):]
	row = row[:strings.Index(row, "</tr>")]
	for _, want := range []string{"Update README", `href="/waiotech/commit/` + head + `"`, `class="person">Test<`, "<span>#2</span>", "tag v1.0.0"} {
		if !strings.Contains(row, want) {
			t.Errorf("waiotech's row lacks %q:\n%s", want, row)
		}
	}
	// The tag deployment is up to date with its release tag, and staging
	// is up to date with main. A release tag must not be compared to main.
	list := body[strings.Index(body, `id="deployments-title"`):]
	list = list[:strings.Index(list, "</section>")]
	production, staging := strings.Index(list, ">production</div></td>"), strings.Index(list, ">staging</div></td>")
	if production < 0 || staging < 0 || production > staging {
		t.Errorf("the deployments are not one row per target, in order:\n%s", list)
	}
	if strings.Count(list, "Up to date") != 2 || !strings.Contains(list, "vs tag v1.0.0") {
		t.Errorf("deployment comparisons do not follow their own refs:\n%s", list)
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

	// All is well: the card is there, and says so, without the alert look.
	resp, body := b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, "Needs attention", "Nothing needs attention")
	if strings.Contains(body, "is-alert") {
		t.Error("Home looks alarmed when nothing wants attention")
	}

	insert := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	insert(`INSERT INTO workers (id, hostname) VALUES ('w1', 'host')`)
	insert(`INSERT INTO runs (id, repo_id, number, commit_hash, ref_kind, ref_name, trigger, triggered_by, status, worker_id, started_at, finished_at)
	        VALUES ('r1', $1, 1, $2, 'branch', 'main', 'push', $3, 'passed', 'w1', now(), now()),
	               ('r2', $1, 2, $4, 'tag', 'v1.0.0', 'push', $3, 'passed', 'w1', now(), now()),
                   ('r3', $1, 3, $4, 'tag', 'v1.0.0', 'manual', $3, 'failed', 'w1', now(), now())`, repo.ID, head, person.ID, old)
	insert(`INSERT INTO deployments (id, repo_id, target, version, commit_hash, run_id, person_id, source_kind, source_name) VALUES ('d2', $1, 'production', 'v1.0.0', $2, 'r2', $3, 'branch', 'main')`, repo.ID, old, person.ID)
	insert(`INSERT INTO push_refusals (id, repo_id, person_id, created_at) VALUES ('f1', $1, $2, $3), ('f0', $1, $2, $4)`,
		repo.ID, person.ID, time.Now().Add(-time.Hour), time.Now().Add(-30*24*time.Hour))
	insert(`INSERT INTO push_refusal_refs (refusal_id, position, ref, reason) VALUES ('f1', 0, 'refs/heads/main', 'this rewrites history'), ('f0', 0, 'refs/heads/x', 'a month ago')`)

	resp, body = b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, "Needs attention", "is-alert",
		// A failed tag run is newer than the successful default-branch run.
		`href="/waiotech/runs/3">waiotech: run #3 of tag v1.0.0 failed.`, `data-attention-key="run:r3"`, `data-attention-dismiss`,
		// Production lags the default branch, and links to what it lacks.
		`waiotech: production is 1 commit behind branch main.`, `href="/waiotech/commits?base=`+old+`&amp;ref=main"`,
		// A refusal of the last week, with its reason and a way to the rest.
		`href="/waiotech/activity">waiotech: darius&#39;s push was refused: this rewrites history.`)
	if strings.Contains(body, "a month ago") {
		t.Error("a refusal from a month ago still wants attention")
	}

	// Without a worker, queued runs say so.
	insert(`INSERT INTO runs (id, repo_id, number, commit_hash, ref_kind, ref_name, trigger, triggered_by, status)
	        VALUES ('r4', $1, 4, $2, 'branch', 'main', 'manual', $3, 'queued')`, repo.ID, head, person.ID)
	insert(`UPDATE workers SET stopped_at = now()`)
	resp, body = b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, "1 queued run cannot start: no ready worker is available.")
}

// A recently active project appears before alphabetically earlier projects,
// and its latest tag run is displayed in Home.
func TestHomeSortsByRecentActivityAndShowsTagRun(t *testing.T) {
	database, b := setup(t)
	ctx := context.Background()
	signIn(t, database, b, "darius", false)
	for _, q := range []string{
		`INSERT INTO repos (id,name,created_at) VALUES ('alpha','alpha',now()-interval '10 days'), ('zeta','zeta',now()-interval '10 days')`,
		`INSERT INTO runs (id,repo_id,number,commit_hash,ref_kind,ref_name,trigger,status,queued_at,started_at,finished_at)
   VALUES ('z-run','zeta',1,repeat('a',40),'tag','v2.0','push','passed',now(),now(),now())`,
	} {
		if _, err := database.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	resp, body := b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, `href="/zeta">zeta</a>`, `href="/alpha">alpha</a>`, `href="/zeta/runs/1"`, `tag v2.0`)
	if strings.Index(body, `href="/zeta">zeta</a>`) >= strings.Index(body, `href="/alpha">alpha</a>`) {
		t.Error("Home is not ordered by recent activity")
	}
}

func TestOverviewHasBranchesTagsAndTheRunStrip(t *testing.T) {
	database, store, b := setupWithStore(t)
	ctx := context.Background()
	signIn(t, database, b, "darius", false)
	repo := seedFilesRepo(t, database, store, b)
	person := mustPerson(t, database, "darius")
	head := mustResolve(t, mustOpen(t, store, repo), "main")

	// No runs yet: the strip is there and says so. The Overview is what is
	// deployed, branches and tags, with nothing about the latest commits, and
	// no buttons for the files or the download in Clone.
	resp, body := b.do(http.MethodGet, "/waiotech", nil, nil)
	expect(t, resp, body, http.StatusOK, `id="targets-title"`, `id="branches-title"`, `id="tags-title"`, `id="strip-title"`, "No runs yet")
	for _, absent := range []string{`id="latest-title"`, "Latest on", "Browse files", `href="/waiotech/archive/main.tar.gz"`} {
		if strings.Contains(body, absent) {
			t.Errorf("the Overview has %s", absent)
		}
	}
	for _, link := range []string{`class="repobar-link" href="/waiotech@main"`, `>Files</a>`} {
		if !strings.Contains(body, link) {
			t.Errorf("the repo bar lacks Files (%s)", link)
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
	// The Files page has its own selected navigation item.
	resp, body = b.do(http.MethodGet, "/waiotech@main", nil, nil)
	expect(t, resp, body, http.StatusOK, `class="repobar-link" href="/waiotech@main" aria-current="page"`)
}

func TestHomeOperationalPanelsIncludeRepositoriesOutsideListPage(t *testing.T) {
	db, b := setup(t)
	signIn(t, db, b, "darius", false)
	ctx := t.Context()
	for i := 0; i < 26; i++ {
		name := fmt.Sprintf("project-%02d", i)
		if _, err := db.Q.Exec(ctx, `INSERT INTO repos(id,name) VALUES($1,$1)`, name); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		`UPDATE repos SET created_at=now()-interval '30 days' WHERE id='project-25'`,
		`INSERT INTO refs(repo_id,kind,name,commit_hash,updated_at) VALUES('project-25','branch','main',repeat('a',40),now()-interval '30 days')`,
		`INSERT INTO runs(id,repo_id,number,commit_hash,ref_kind,ref_name,trigger,status,queued_at,finished_at) VALUES('outside-run','project-25',1,repeat('a',40),'branch','main','push','failed',now()-interval '30 days',now()-interval '30 days')`,
		`INSERT INTO deployments(id,repo_id,target,version,commit_hash,run_id,created_at) VALUES('outside-deploy','project-25','production','v1',repeat('a',40),'outside-run',now()-interval '30 days')`,
	} {
		if _, err := db.Q.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	resp, body := b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, "project-25: run #1 of branch main failed.", "production")
	repos := body[strings.Index(body, `id="repos-title"`):]
	repos = repos[:strings.Index(repos, "</section>")]
	if strings.Contains(repos, "project-25") {
		t.Fatal("repository list was not paginated")
	}
	for _, path := range []string{"/", "/?after=project-24"} {
		resp, body = b.do(http.MethodGet, path, nil, nil)
		expect(t, resp, body, http.StatusOK, "project-25: run #1 of branch main failed.", "production")
	}
}
