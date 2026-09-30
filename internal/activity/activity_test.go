package activity

import (
	"context"
	"fmt"
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

func TestForRepoListsRefChangesAndEventsNewestFirst(t *testing.T) {
	database := pgtest.Open(t)
	repoID, _, _ := seed(t, database)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// One push moving three refs at the same moment: each is its own entry.
	exec(`INSERT INTO pushes (id, repo_id, person_id, created_at) VALUES ('push3', $1, 'p1', $2)`, repoID, base.Add(10*time.Minute))
	exec(`INSERT INTO push_updates (id, push_id, kind, name, old_commit, new_commit, is_create, is_delete, is_force) VALUES
	      ('pu3a', 'push3', 'branch', 'new', repeat('0',40), repeat('d',40), true, false, false),
	      ('pu3b', 'push3', 'branch', 'main', repeat('a',40), repeat('e',40), false, false, true),
	      ('pu3c', 'push3', 'tag', 'v1', repeat('a',40), repeat('f',40), false, false, false),
	      ('pu3d', 'push3', 'branch', 'old', repeat('9',40), repeat('0',40), false, true, false)`)
	exec(`INSERT INTO runs (id, repo_id, number, commit_hash, ref_kind, ref_name, trigger, push_id, status, finished_at)
	      VALUES ('run3', $1, 3, repeat('d',40), 'branch', 'new', 'push', 'push3', 'failed', now())`, repoID)

	entries, more, err := NewService(database).ForRepo(ctx, repoID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	// What seed gave this repository (a push update, a finished run, a
	// deployment and an event), the four updates above and the run they
	// started; the other repository's push is not part of it, and neither is
	// the run still running.
	if more || len(entries) != 9 {
		t.Fatalf("got %d entries (more=%v), want 9: %+v", len(entries), more, entries)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i-1].At.Before(entries[i].At) {
			t.Fatalf("entries are not newest first at index %d", i)
		}
	}
	// The run that just finished is the newest thing, then the four updates.
	if e := entries[0]; e.Kind != KindRun || e.RunNumber != 3 || e.RunStatus != "failed" || e.RefName != "new" {
		t.Errorf("the finished run is out of place: %+v", e)
	}
	changes := map[string]RepoEntry{}
	for _, e := range entries[1:5] {
		if e.Kind != KindPush || e.Actor != "darius" {
			t.Fatalf("entry = %+v", e)
		}
		changes[e.RefName] = e
	}
	for name, want := range map[string]Change{"new": Created, "main": ForcePushed, "v1": Moved, "old": Deleted} {
		if got := changes[name].Change; got != want {
			t.Errorf("%s: change = %q, want %q", name, got, want)
		}
	}
	if e := changes["new"]; e.RunNumber != 3 || e.RunStatus != "failed" {
		t.Errorf("the run a push started is missing: %+v", e)
	}
	if e := changes["main"]; e.RunNumber != 0 {
		t.Errorf("a ref that started no run has one: %+v", e)
	}
	rest := entries[5:]
	if rest[0].Kind != KindEvent || rest[0].Action != RuleSaved ||
		rest[1].Kind != KindDeployment || rest[1].Target != "staging" || rest[1].Version != "v1" || rest[1].RunNumber != 1 ||
		rest[2].Kind != KindRun || rest[2].RunStatus != "passed" || rest[2].RefName != "main" ||
		rest[3].Kind != KindPush || rest[3].Change != Pushed {
		t.Errorf("the settings change, deployment, run and older push are out of place: %+v", rest)
	}
}

func TestForRepoPages(t *testing.T) {
	database := pgtest.Open(t)
	repoID, _, _ := seed(t, database)
	ctx := context.Background()
	// Every update of one push shares its moment, so paging must stay exact
	// inside it.
	if _, err := database.Pool.Exec(ctx, `INSERT INTO pushes (id, repo_id, person_id) VALUES ('big', $1, 'p1')`, repoID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := database.Pool.Exec(ctx, `
			INSERT INTO push_updates (id, push_id, kind, name, old_commit, new_commit, is_create)
			VALUES ($1, 'big', 'branch', $2, repeat('0',40), repeat('a',40), true)
		`, "big-"+string(rune('a'+i)), "b"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	svc := NewService(database)
	seen := map[string]bool{}
	skip := 0
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("paging does not end")
		}
		entries, more, err := svc.ForRepo(ctx, repoID, skip, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			key := string(e.Kind) + e.RefName + e.Action
			if seen[key] {
				t.Fatalf("%s appears on two pages", key)
			}
			seen[key] = true
		}
		if !more {
			break
		}
		skip += 2
	}
	if len(seen) != 7 {
		t.Fatalf("paging listed %d entries, want 7: %v", len(seen), seen)
	}
	if entries, more, err := svc.ForRepo(ctx, repoID, 100, 2); err != nil || more || len(entries) != 0 {
		t.Fatalf("past the end = %v, %v, %v", entries, more, err)
	}
}

func TestForRepoListsRefusedPushesWithTheirReasons(t *testing.T) {
	database := pgtest.Open(t)
	repoID, otherRepoID, _ := seed(t, database)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	exec(`INSERT INTO push_refusals (id, repo_id, person_id, created_at) VALUES ('f1', $1, 'p1', $2), ('f2', $3, 'p1', $2)`,
		repoID, base.Add(30*time.Minute), otherRepoID)
	exec(`INSERT INTO push_refusal_refs (refusal_id, position, ref, reason) VALUES
	      ('f1', 1, 'refs/tags/v1', 'moving an existing tag is a force-push'),
	      ('f1', 0, 'refs/heads/main', 'the default branch cannot be deleted'),
	      ('f1', 2, 'refs/notes/x', 'only branches and tags can be pushed'),
	      ('f2', 0, 'refs/heads/other', 'not this repository''s')`)

	entries, _, err := NewService(database).ForRepo(ctx, repoID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	// The refusal, then what seed gave the repository: an event, a
	// deployment, a run and a push.
	if len(entries) != 5 || entries[0].Kind != KindRefusal || entries[0].Actor != "darius" {
		t.Fatalf("entries = %+v", entries)
	}
	got := entries[0].Refused
	if len(got) != 3 || got[0].Ref != "refs/heads/main" || got[1].Ref != "refs/tags/v1" || got[2].Ref != "refs/notes/x" {
		t.Fatalf("reasons are missing or out of the order they were reported: %+v", got)
	}
	if got[0].RefKind != git.KindBranch || got[0].RefName != "main" || got[1].RefKind != git.KindTag || got[2].RefName != "" {
		t.Errorf("refs are not told apart: %+v", got)
	}
}

// A push that makes many of the same change is one line, not a feed's worth.
func TestForRepoGroupsABulkPush(t *testing.T) {
	database := pgtest.Open(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO repos (id, name) VALUES ('r1', 'demo')`)
	exec(`INSERT INTO people (id, username, password_hash) VALUES ('p1', 'darius', 'x')`)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// Twelve tags created at once, three branches created at once (fewer
	// than a bulk), and one tag moved in the same push.
	exec(`INSERT INTO pushes (id, repo_id, person_id, created_at) VALUES ('big', 'r1', 'p1', $1)`, base)
	for i := 1; i <= 12; i++ {
		exec(`INSERT INTO push_updates (id, push_id, kind, name, old_commit, new_commit, is_create)
		      VALUES ($1, 'big', 'tag', $2, repeat('0',40), repeat('a',40), true)`, fmt.Sprintf("t%02d", i), fmt.Sprintf("v1.0.0-beta.%d", i))
	}
	for _, name := range []string{"a", "b", "c"} {
		exec(`INSERT INTO push_updates (id, push_id, kind, name, old_commit, new_commit, is_create)
		      VALUES ($1, 'big', 'branch', $2, repeat('0',40), repeat('b',40), true)`, "b-"+name, name)
	}
	exec(`INSERT INTO push_updates (id, push_id, kind, name, old_commit, new_commit)
	      VALUES ('mv', 'big', 'tag', 'latest', repeat('c',40), repeat('d',40))`)
	// An older push with one update is unaffected.
	exec(`INSERT INTO pushes (id, repo_id, person_id, created_at) VALUES ('old', 'r1', 'p1', $1)`, base.Add(-time.Hour))
	exec(`INSERT INTO push_updates (id, push_id, kind, name, old_commit, new_commit, is_create)
	      VALUES ('o1', 'old', 'branch', 'main', repeat('0',40), repeat('e',40), true)`)

	svc := NewService(database)
	entries, more, err := svc.ForRepo(ctx, "r1", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	// One line for the twelve tags, one each for the three branches, the moved
	// tag and the older push: six, not seventeen.
	if more || len(entries) != 6 {
		t.Fatalf("got %d entries (more=%v), want 6: %+v", len(entries), more, entries)
	}
	var bulk *RepoEntry
	for i := range entries {
		if entries[i].Count > 1 {
			if bulk != nil {
				t.Fatal("two bulk lines")
			}
			bulk = &entries[i]
		}
	}
	if bulk == nil || bulk.Count != 12 || bulk.Change != Created || bulk.RefKind != git.KindTag || bulk.Actor != "darius" {
		t.Fatalf("the bulk line = %+v", bulk)
	}
	if strings.Join(bulk.Names, " ") != "v1.0.0-beta.12 v1.0.0-beta.11 v1.0.0-beta.10" {
		t.Errorf("the bulk line names %v, want the newest three versions", bulk.Names)
	}
	for _, e := range entries {
		if e.Count != 1 && e.Count != 12 {
			t.Errorf("a line counts %d refs: %+v", e.Count, e)
		}
	}

	// Paging counts lines, so a page never splits or repeats a bulk.
	first, more, err := svc.ForRepo(ctx, "r1", 0, 3)
	if err != nil || !more || len(first) != 3 {
		t.Fatalf("first page = %d, more=%v, %v", len(first), more, err)
	}
	rest, more, err := svc.ForRepo(ctx, "r1", 3, 3)
	if err != nil || more || len(rest) != 3 {
		t.Fatalf("second page = %d, more=%v, %v", len(rest), more, err)
	}
}

func TestRefusedSinceListsRecentRefusalsOfTheGivenRepositories(t *testing.T) {
	database := pgtest.Open(t)
	repoID, otherRepoID, _ := seed(t, database)
	ctx := context.Background()
	now := time.Now()
	insert := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	insert(`INSERT INTO push_refusals (id, repo_id, person_id, created_at) VALUES
	        ('new', $1, 'p1', $3), ('old', $1, 'p1', $4), ('other', $2, 'p1', $3)`, repoID, otherRepoID, now.Add(-time.Hour), now.Add(-30*24*time.Hour))
	insert(`INSERT INTO push_refusal_refs (refusal_id, position, ref, reason) VALUES
	        ('new', 1, 'refs/tags/v1', 'second'), ('new', 0, 'refs/heads/main', 'first'), ('old', 0, 'refs/heads/x', 'long ago'),
	        ('other', 0, 'refs/heads/y', 'elsewhere')`)
	svc := NewService(database)

	got, err := svc.RefusedSince(ctx, []string{repoID}, now.Add(-7*24*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RepoName != "waiotech" || got[0].Actor != "darius" || got[0].Ref != "refs/heads/main" || got[0].Reason != "first" {
		t.Fatalf("refused = %+v", got)
	}
	both, err := svc.RefusedSince(ctx, []string{repoID, otherRepoID}, now.Add(-7*24*time.Hour), 10)
	if err != nil || len(both) != 2 {
		t.Fatalf("for two repositories: %+v, %v", both, err)
	}
	if none, err := svc.RefusedSince(ctx, nil, now.Add(-7*24*time.Hour), 10); err != nil || len(none) != 0 {
		t.Fatalf("for no repositories: %+v, %v", none, err)
	}
	if capped, err := svc.RefusedSince(ctx, []string{repoID, otherRepoID}, now.Add(-7*24*time.Hour), 1); err != nil || len(capped) != 1 {
		t.Fatalf("with a limit: %+v, %v", capped, err)
	}
}
