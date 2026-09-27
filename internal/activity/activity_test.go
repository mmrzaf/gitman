package activity

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
)

// seed inserts one repo, one person, and one row of each source kind, at
// distinct, known timestamps so ordering can be checked precisely.
func seed(t *testing.T, database *postgres.DB) (repoID, otherRepoID, personID string) {
	t.Helper()
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	repoID, otherRepoID = "r1", "r2"
	personID = "p1"
	exec(`INSERT INTO people (id, username, password_hash) VALUES ($1, 'darius', 'x')`, personID)
	exec(`INSERT INTO repos (id, name) VALUES ($1, 'waiotech'), ($2, 'cerv')`, repoID, otherRepoID)

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	exec(`INSERT INTO pushes (id, repo_id, person_id, created_at) VALUES ('push1', $1, $2, $3)`, repoID, personID, base.Add(1*time.Minute))
	exec(`INSERT INTO push_updates (id, push_id, kind, name, old_commit, new_commit, is_create)
	      VALUES ('pu1', 'push1', 'branch', 'main', repeat('0',40), repeat('a',40), false)`)

	exec(`INSERT INTO runs (id, repo_id, number, commit_hash, ref_kind, ref_name, trigger, triggered_by, status, finished_at)
	      VALUES ('run1', $1, 1, repeat('a',40), 'branch', 'main', 'push', $2, 'passed', $3)`, repoID, personID, base.Add(2*time.Minute))
	// An unfinished run must never appear in the timeline.
	exec(`INSERT INTO workers (id, hostname) VALUES ('w1', 'host')`)
	exec(`INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, started_at, worker_id, ref_kind, ref_name)
	      VALUES ('run2', $1, 2, repeat('b',40), 'manual', 'running', now(), 'w1', 'branch', 'main')`, repoID)

	exec(`INSERT INTO deployments (id, repo_id, target, version, commit_hash, run_id, person_id, created_at)
	      VALUES ('dep1', $1, 'staging', 'v1', repeat('a',40), 'run1', $2, $3)`, repoID, personID, base.Add(3*time.Minute))

	exec(`INSERT INTO events (id, repo_id, person_id, action, detail, created_at)
	      VALUES ('ev1', $1, $2, $3, 'branch main', $4)`, repoID, personID, RuleSaved, base.Add(4*time.Minute))
	// An instance-wide event with no repository.
	exec(`INSERT INTO events (id, person_id, action, created_at) VALUES ('ev2', $1, $2, $3)`, personID, PersonAdded, base.Add(5*time.Minute))

	// Activity on the other repository must not leak into a scoped feed.
	exec(`INSERT INTO pushes (id, repo_id, person_id, created_at) VALUES ('push2', $1, $2, $3)`, otherRepoID, personID, base.Add(6*time.Minute))
	exec(`INSERT INTO push_updates (id, push_id, kind, name, old_commit, new_commit, is_create)
	      VALUES ('pu2', 'push2', 'branch', 'main', repeat('0',40), repeat('c',40), false)`)

	return repoID, otherRepoID, personID
}

func TestRecentAcrossAllRepos(t *testing.T) {
	database := pgtest.Open(t)
	repoID, _, _ := seed(t, database)
	_ = repoID

	entries, err := NewService(database).Recent(context.Background(), nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 6 {
		t.Fatalf("got %d entries, want 6 (2 pushes, 1 run, 1 deployment, 2 events); entries: %+v", len(entries), entries)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i-1].At.Before(entries[i].At) {
			t.Fatalf("entries are not ordered most-recent-first at index %d", i)
		}
	}
	// Most recent first: the push to the other repo, at base+6min.
	if entries[0].Kind != KindPush || entries[0].RepoName != "cerv" {
		t.Fatalf("entries[0] = %+v", entries[0])
	}
	// The instance-wide "person added" event, at base+5min, comes next.
	if entries[1].Kind != KindEvent || entries[1].Action != PersonAdded {
		t.Fatalf("entries[1] = %+v", entries[1])
	}
}

func TestRecentScopedToOneRepo(t *testing.T) {
	database := pgtest.Open(t)
	repoID, _, _ := seed(t, database)

	entries, err := NewService(database).Recent(context.Background(), &repoID, 10)
	if err != nil {
		t.Fatal(err)
	}
	// The other repo's push and the instance-wide person event are both
	// excluded; the scoped feed has exactly the 4 rows tied to repoID.
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(entries), entries)
	}
	for _, e := range entries {
		if e.RepoName != "" && e.RepoName != "waiotech" {
			t.Errorf("entry from another repo leaked into the scoped feed: %+v", e)
		}
	}
}

func TestPushEntryShape(t *testing.T) {
	database := pgtest.Open(t)
	repoID, _, _ := seed(t, database)

	entries, err := NewService(database).Recent(context.Background(), &repoID, 10)
	if err != nil {
		t.Fatal(err)
	}
	var push *Entry
	for i := range entries {
		if entries[i].Kind == KindPush {
			push = &entries[i]
		}
	}
	if push == nil {
		t.Fatal("no push entry found")
	}
	if push.RefKind != git.KindBranch || push.RefName != "main" || push.UpdateCount != 1 {
		t.Errorf("push entry = %+v", push)
	}
	if !strings.HasPrefix(push.NewCommit, "aaaa") {
		t.Errorf("push NewCommit = %q", push.NewCommit)
	}
	if push.Actor != "darius" {
		t.Errorf("push Actor = %q", push.Actor)
	}
}

func TestMultiRefPushIsSummarized(t *testing.T) {
	database := pgtest.Open(t)
	ctx := context.Background()
	if _, err := database.Pool.Exec(ctx, `INSERT INTO repos (id, name) VALUES ('r1', 'demo')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO pushes (id, repo_id) VALUES ('push1', 'r1')`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c"} {
		if _, err := database.Pool.Exec(ctx, `
			INSERT INTO push_updates (id, push_id, kind, name, old_commit, new_commit)
			VALUES ($1, 'push1', 'branch', $2, repeat('0',40), repeat('a',40))
		`, "pu-"+name, name); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := NewService(database).Recent(ctx, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].UpdateCount != 3 || entries[0].RefName != "" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestRecentLimitsAcrossSources(t *testing.T) {
	database := pgtest.Open(t)
	ctx := context.Background()
	if _, err := database.Pool.Exec(ctx, `INSERT INTO repos (id, name) VALUES ('r1', 'demo')`); err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	for i := 0; i < 5; i++ {
		if _, err := database.Pool.Exec(ctx, `
			INSERT INTO events (id, repo_id, action, created_at) VALUES ($1, 'r1', 'x', $2)
		`, "ev"+string(rune('a'+i)), base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := NewService(database).Recent(ctx, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].At.Before(entries[1].At) {
		t.Fatal("not ordered most-recent-first")
	}
}

// TestFeedQueriesCanUseAnIndex checks that each source of the activity
// feed — the cross-repository one Home reads, and the per-repository
// one the Repository page reads — and the run-claiming query a worker
// runs on every poll can be read from an index instead of sorting or
// scanning the whole table.
func TestFeedQueriesCanUseAnIndex(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	conn, err := database.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	// On empty tables the planner prefers other plans; these settings ask
	// only whether an ordered index read is available at all.
	if _, err := conn.Exec(ctx, `SET enable_seqscan = off; SET enable_bitmapscan = off`); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, `RESET enable_seqscan; RESET enable_bitmapscan`)
	for table, query := range map[string]string{
		"pushes":                        `SELECT id FROM pushes ORDER BY created_at DESC LIMIT 100`,
		"runs":                          `SELECT id FROM runs WHERE finished_at IS NOT NULL ORDER BY finished_at DESC LIMIT 100`,
		"deployments":                   `SELECT id FROM deployments ORDER BY created_at DESC LIMIT 100`,
		"events":                        `SELECT id FROM events ORDER BY created_at DESC LIMIT 100`,
		"runs (repository page)":        `SELECT id FROM runs WHERE finished_at IS NOT NULL AND repo_id = 'r1' ORDER BY finished_at DESC LIMIT 100`,
		"deployments (repository page)": `SELECT id FROM deployments WHERE repo_id = 'r1' ORDER BY created_at DESC LIMIT 100`,
		"runs (claiming)":               `SELECT id FROM runs WHERE status = 'queued' ORDER BY queued_at, id LIMIT 1`,
	} {
		rows, err := conn.Query(ctx, "EXPLAIN "+query)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(line + "\n")
		}
		rows.Close()
		if !strings.Contains(plan.String(), "Index") || strings.Contains(plan.String(), "Sort") {
			t.Errorf("%s: the feed query sorts instead of reading an index:\n%s", table, plan.String())
		}
	}
}
