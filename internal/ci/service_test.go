package ci

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
	"github.com/mmrzaf/gitman/internal/repo"
)

const shipping = `image: alpine:3.20
targets:
  production:
    tag: v*
steps:
  - name: test
    run: go test ./...
  - name: release
    when: target
    run: ./release.sh
`

func TestPlanResolvesTargetAndSteps(t *testing.T) {
	pl := planRun(CreateParams{
		Commit: strings.Repeat("a", 40), RefKind: git.KindTag, RefName: "v1.4.2",
		Pipeline: []byte(shipping), Decision: repo.Decision{AllowShip: true},
	})
	if pl.status != StatusQueued || pl.target != "production" || pl.version != "v1.4.2" {
		t.Fatalf("plan = %+v", pl)
	}
	if len(pl.steps) != 2 || !pl.steps[0].run || !pl.steps[1].run {
		t.Fatalf("steps = %+v", pl.steps)
	}
}

func TestPlanSkipsTargetStepsWithoutTarget(t *testing.T) {
	pl := planRun(CreateParams{
		Commit: strings.Repeat("a", 40), RefKind: git.KindBranch, RefName: "develop",
		Pipeline: []byte(shipping),
	})
	if pl.status != StatusQueued || pl.target != "" {
		t.Fatalf("plan = %+v", pl)
	}
	if !pl.steps[0].run || pl.steps[1].run {
		t.Fatalf("steps = %+v", pl.steps)
	}
}

func TestPlanFailures(t *testing.T) {
	commit := strings.Repeat("a", 40)
	cases := map[string]struct {
		params CreateParams
		reason string
	}{
		"problem reading the file": {
			CreateParams{Commit: commit, PipelineProblem: "There is no .gitman.yml at aaaaaaa."},
			"There is no .gitman.yml",
		},
		"invalid pipeline": {
			CreateParams{Commit: commit, Pipeline: []byte("image: x\nsteps: []\n")},
			"at least one step",
		},
		"docker not allowed": {
			CreateParams{Commit: commit, RefKind: git.KindBranch, RefName: "main",
				Pipeline: []byte("image: docker:29-cli\ndocker: true\nsteps:\n  - name: b\n    run: docker build .\n")},
			"no rule allows Docker for branch main",
		},
		"shipping not allowed": {
			CreateParams{Commit: commit, RefKind: git.KindTag, RefName: "v1", Pipeline: []byte(shipping)},
			`Tag v1 resolves to target "production", but no rule allows shipping from it.`,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			pl := planRun(c.params)
			if pl.status != StatusFailed || !strings.Contains(pl.reason, c.reason) {
				t.Fatalf("plan = %s %q, want failed with %q", pl.status, pl.reason, c.reason)
			}
		})
	}
}

func TestPlanPassesWhenNoStepApplies(t *testing.T) {
	pl := planRun(CreateParams{
		Commit: strings.Repeat("a", 40), RefKind: git.KindBranch, RefName: "main",
		Pipeline: []byte("image: alpine\nsteps:\n  - name: release\n    when: tag\n    run: ./release.sh\n"),
	})
	if pl.status != StatusPassed || !strings.Contains(pl.reason, "No step applies") {
		t.Fatalf("plan = %s %q", pl.status, pl.reason)
	}
}

func TestTruncateReason(t *testing.T) {
	long := strings.Repeat("é", maxReasonLen+10)
	got := truncateReason(long)
	if n := len([]rune(got)); n != maxReasonLen {
		t.Fatalf("truncated length = %d runes, want %d", n, maxReasonLen)
	}
}

func TestCreateNumbersAndSupersedes(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	if _, err := database.Pool.Exec(ctx, `INSERT INTO repos (id, name) VALUES ('r1', 'demo')`); err != nil {
		t.Fatal(err)
	}
	params := CreateParams{
		RepoID: "r1", Commit: strings.Repeat("a", 40), RefKind: git.KindBranch, RefName: "main",
		Trigger: TriggerPush, Pipeline: []byte(shipping),
	}

	var first, second *Created
	if err := database.Tx(ctx, func(tx postgres.Tx) error {
		var err error
		first, err = NewService(database).CreateTx(ctx, tx, params)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Tx(ctx, func(tx postgres.Tx) error {
		var err error
		second, err = NewService(database).CreateTx(ctx, tx, params)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if first.Number != 1 || second.Number != 2 {
		t.Fatalf("numbers = %d, %d", first.Number, second.Number)
	}

	var status, reason string
	if err := database.Pool.QueryRow(ctx, `SELECT status, reason FROM runs WHERE id = $1`, first.ID).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" || reason != "Superseded by #2." {
		t.Fatalf("first run = %s %q", status, reason)
	}
	var pending int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM steps WHERE run_id = $1 AND status = 'pending'`, first.ID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("superseded run still has %d pending steps", pending)
	}

	// A run a worker already started is left alone by later pushes.
	if err := NewService(database).RegisterWorker(ctx, "w1", "host"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		UPDATE runs SET status = 'running', started_at = now(), worker_id = 'w1' WHERE id = $1
	`, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.Tx(ctx, func(tx postgres.Tx) error {
		_, err := NewService(database).CreateTx(ctx, tx, params)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1`, second.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Fatalf("running run became %s after a newer push", status)
	}
}

func TestCreateFailedRunIsFinished(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	if _, err := database.Pool.Exec(ctx, `INSERT INTO repos (id, name) VALUES ('r1', 'demo')`); err != nil {
		t.Fatal(err)
	}
	var run *Created
	if err := database.Tx(ctx, func(tx postgres.Tx) error {
		var err error
		run, err = NewService(database).CreateTx(ctx, tx, CreateParams{
			RepoID: "r1", Commit: strings.Repeat("b", 40), RefKind: git.KindBranch, RefName: "main",
			Trigger: TriggerManual, PipelineProblem: "There is no .gitman.yml at bbbbbbb.",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var finished bool
	if err := database.Pool.QueryRow(ctx, `SELECT finished_at IS NOT NULL FROM runs WHERE id = $1`, run.ID).Scan(&finished); err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusFailed || !finished {
		t.Fatalf("run = %+v, finished=%v", run, finished)
	}
}

func seedRepo(t *testing.T, database *postgres.DB, repoID, name string) {
	t.Helper()
	if _, err := database.Pool.Exec(context.Background(), `INSERT INTO repos (id, name) VALUES ($1, $2)`, repoID, name); err != nil {
		t.Fatal(err)
	}
}

// insertTestRun inserts a run directly. A running one is recorded as
// started on worker w-test, which it creates.
func insertTestRun(t *testing.T, database *postgres.DB, id, repoID string, number int64, kind, name, status string, finished bool) {
	t.Helper()
	ctx := context.Background()
	var finishedAt any
	if finished {
		finishedAt = time.Now()
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO workers (id, hostname) VALUES ('w-test', 'host') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO runs (id, repo_id, number, commit_hash, ref_kind, ref_name, trigger, status, finished_at,
		                  started_at, worker_id)
		VALUES ($1, $2, $3, repeat('a',40), $4, $5, 'push', $6, $7,
		        CASE WHEN $6 <> 'queued' THEN now() END, CASE WHEN $6 = 'running' THEN 'w-test' END)
	`, id, repoID, number, kind, name, status, finishedAt); err != nil {
		t.Fatal(err)
	}
}

func TestInProgress(t *testing.T) {
	database := pgtest.Open(t)
	seedRepo(t, database, "r1", "demo")
	insertTestRun(t, database, "run1", "r1", 1, "branch", "main", "queued", false)
	insertTestRun(t, database, "run2", "r1", 2, "branch", "main", "running", false)
	insertTestRun(t, database, "run3", "r1", 3, "branch", "main", "passed", true)

	list, err := NewService(database).InProgress(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("InProgress = %d runs, want 2", len(list))
	}
	for _, s := range list {
		if s.Status != StatusQueued && s.Status != StatusRunning {
			t.Errorf("InProgress included a finished run: %+v", s)
		}
	}
}

func TestLatestRunPerRef(t *testing.T) {
	database := pgtest.Open(t)
	seedRepo(t, database, "r1", "demo")
	insertTestRun(t, database, "run1", "r1", 1, "branch", "main", "passed", true)
	insertTestRun(t, database, "run2", "r1", 2, "branch", "main", "failed", true)
	insertTestRun(t, database, "run3", "r1", 3, "tag", "v1", "passed", true)

	latest, err := NewService(database).LatestRunPerRef(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 2 {
		t.Fatalf("LatestRunPerRef = %d entries, want 2: %+v", len(latest), latest)
	}
	if latest["branch/main"].Number != 2 || latest["branch/main"].Status != StatusFailed {
		t.Errorf("branch/main = %+v", latest["branch/main"])
	}
	if latest["tag/v1"].Number != 3 {
		t.Errorf("tag/v1 = %+v", latest["tag/v1"])
	}
}

func seedDeployments(t *testing.T, database *postgres.DB) {
	t.Helper()
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO repos (id, name) VALUES ('r1', 'a'), ('r2', 'b')`)
	exec(`INSERT INTO runs (id, repo_id, number, commit_hash, ref_kind, ref_name, trigger, status, finished_at)
	      VALUES ('run1', 'r1', 1, repeat('a',40), 'branch', 'main', 'push', 'passed', now())`)
	exec(`INSERT INTO deployments (id, repo_id, target, version, commit_hash, run_id, created_at)
	      VALUES ('d1', 'r1', 'staging', 'old', repeat('a',40), 'run1', now() - interval '1 hour')`)
	exec(`INSERT INTO deployments (id, repo_id, target, version, commit_hash, run_id, created_at)
	      VALUES ('d2', 'r1', 'staging', 'new', repeat('b',40), 'run1', now())`)
	exec(`INSERT INTO deployments (id, repo_id, target, version, commit_hash)
	      VALUES ('d3', 'r2', 'staging', 'other', repeat('c',40))`)
}

func TestLiveForRepo(t *testing.T) {
	database := pgtest.Open(t)
	seedDeployments(t, database)
	live, err := NewService(database).LiveForRepo(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].Version != "new" {
		t.Fatalf("LiveForRepo = %+v", live)
	}
}

func TestLatestDeploymentPerRef(t *testing.T) {
	database := pgtest.Open(t)
	seedDeployments(t, database)
	latest, err := NewService(database).LatestDeploymentPerRef(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 1 {
		t.Fatalf("LatestDeploymentPerRef = %+v", latest)
	}
	if d := latest["branch/main"]; d.Version != "new" {
		t.Errorf("branch/main = %+v", d)
	}
}

func TestLatestDeploymentPerRefSkipsDeploymentsWithoutARun(t *testing.T) {
	database := pgtest.Open(t)
	ctx := context.Background()
	if _, err := database.Pool.Exec(ctx, `INSERT INTO repos (id, name) VALUES ('r1', 'a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO deployments (id, repo_id, target, version, commit_hash) VALUES ('d1', 'r1', 'staging', 'v1', repeat('a',40))
	`); err != nil {
		t.Fatal(err)
	}
	latest, err := NewService(database).LatestDeploymentPerRef(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 0 {
		t.Fatalf("LatestDeploymentPerRef = %+v, want empty (deployment has no run to attribute a ref to)", latest)
	}
}

func createQueuedRun(t *testing.T, database *postgres.DB, svc *Service, allowSecrets bool) *Created {
	t.Helper()
	ctx := context.Background()
	var run *Created
	err := database.Tx(ctx, func(tx postgres.Tx) error {
		var err error
		run, err = svc.CreateTx(ctx, tx, CreateParams{
			RepoID: "r1", Commit: strings.Repeat("a", 40), RefKind: git.KindBranch, RefName: "main",
			Trigger: TriggerPush, Pipeline: []byte("image: alpine:3.20\ntargets:\n  staging:\n    branch: main\nsteps:\n  - name: test\n    run: 'true'\n  - name: release\n    when: tag\n    run: 'true'\n"),
			Decision: repo.Decision{AllowShip: true, AllowSecrets: allowSecrets},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestRunLifecycle(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	seedRepo(t, database, "r1", "demo")
	svc := NewService(database)
	if err := svc.RegisterWorker(ctx, "w1", "host"); err != nil {
		t.Fatal(err)
	}
	run := createQueuedRun(t, database, svc, true)

	claim, err := svc.ClaimNext(ctx, "w1")
	if err != nil || claim == nil || claim.RunID != run.ID || claim.RepoName != "demo" || !claim.AllowSecrets || claim.Target != "staging" {
		t.Fatalf("ClaimNext = %+v, %v", claim, err)
	}
	if len(claim.Steps) != 2 || claim.Steps[0].Skipped || !claim.Steps[1].Skipped {
		t.Fatalf("claimed steps = %+v", claim.Steps)
	}
	if again, err := svc.ClaimNext(ctx, "w2"); err != nil || again != nil {
		t.Fatalf("second ClaimNext = %+v, %v; a run must be claimed once", again, err)
	}

	repoID, err := svc.AuthenticateFetch(ctx, claim.FetchToken)
	if err != nil || repoID != "r1" {
		t.Fatalf("AuthenticateFetch = %q, %v", repoID, err)
	}

	step := claim.Steps[0]
	if err := svc.StepStarted(ctx, run.ID, step.ID); err != nil {
		t.Fatal(err)
	}
	for i, chunk := range []string{"hello ", "world\n"} {
		if err := svc.AppendLog(ctx, run.ID, step.ID, i, chunk); err != nil {
			t.Fatal(err)
		}
	}
	zero := 0
	if err := svc.StepFinished(ctx, run.ID, step.ID, StepPassed, &zero); err != nil {
		t.Fatal(err)
	}
	if err := svc.Finish(ctx, run.ID, Outcome{Status: StatusPassed, Summary: map[string]string{"image": "demo:v1"}}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.AuthenticateFetch(ctx, claim.FetchToken); !errors.Is(err, ErrInvalidFetchToken) {
		t.Fatalf("fetch token after the run finished = %v, want ErrInvalidFetchToken", err)
	}
	live, err := svc.LiveForRepo(ctx, "r1")
	if err != nil || len(live) != 1 || live[0].Target != "staging" {
		t.Fatalf("a passing run with a target must record a deployment: %+v, %v", live, err)
	}
	var status, summary, logs string
	if err := database.Pool.QueryRow(ctx, `
		SELECT r.status, (SELECT value FROM run_summary WHERE run_id = r.id AND key = 'image'),
		       (SELECT string_agg(content, '' ORDER BY sequence) FROM step_logs WHERE step_id = $2)
		FROM runs r WHERE r.id = $1`, run.ID, step.ID).Scan(&status, &summary, &logs); err != nil {
		t.Fatal(err)
	}
	if status != "passed" || summary != "demo:v1" || logs != "hello world\n" {
		t.Fatalf("recorded run = %s, %q, %q", status, summary, logs)
	}
}

func TestCancel(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	seedRepo(t, database, "r1", "demo")
	svc := NewService(database)

	queued := createQueuedRun(t, database, svc, false)
	if err := svc.Cancel(ctx, "r1", queued.Number, "darius"); err != nil {
		t.Fatal(err)
	}
	var status, reason string
	if err := database.Pool.QueryRow(ctx, `SELECT status, reason FROM runs WHERE id = $1`, queued.ID).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" || reason != "Cancelled by darius." {
		t.Fatalf("queued run after Cancel = %s %q", status, reason)
	}
	if err := svc.Cancel(ctx, "r1", queued.Number, "darius"); !errors.Is(err, ErrRunFinished) {
		t.Fatalf("second Cancel = %v, want ErrRunFinished", err)
	}

	running := createQueuedRun(t, database, svc, false)
	if err := svc.RegisterWorker(ctx, "w1", "host"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClaimNext(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Cancel(ctx, "r1", running.Number, "darius"); err != nil {
		t.Fatal(err)
	}
	if reason, err := svc.StopReason(ctx, running.ID); err != nil || reason != "the run was cancelled" {
		t.Fatalf("StopReason = %q, %v", reason, err)
	}
	if err := svc.Finish(ctx, running.ID, Outcome{Status: StatusCancelled}); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT status, reason FROM runs WHERE id = $1`, running.ID).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" || reason != "Cancelled by darius." {
		t.Fatalf("running run after cancellation = %s %q; the requester's reason must be kept", status, reason)
	}
	if err := svc.Cancel(ctx, "r1", 999, "darius"); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("Cancel(no such run) = %v", err)
	}
}

func TestFailLostRuns(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	seedRepo(t, database, "r1", "demo")
	svc := NewService(database)
	run := createQueuedRun(t, database, svc, false)
	if err := svc.RegisterWorker(ctx, "w1", "host"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClaimNext(ctx, "w1"); err != nil {
		t.Fatal(err)
	}

	if n, err := svc.FailLostRuns(ctx, time.Minute); err != nil || n != 0 {
		t.Fatalf("FailLostRuns with a live worker = %d, %v", n, err)
	}
	if err := svc.StopWorker(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.FailLostRuns(ctx, time.Minute); err != nil || n != 1 {
		t.Fatalf("FailLostRuns after the worker stopped = %d, %v", n, err)
	}
	var status, reason string
	if err := database.Pool.QueryRow(ctx, `SELECT status, reason FROM runs WHERE id = $1`, run.ID).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || reason != "The worker running it stopped responding." {
		t.Fatalf("lost run = %s %q", status, reason)
	}
}

// TestFailLostRunsComparesAgainstTheDatabasesClock backdates a worker's
// heartbeat using the database's own clock, never the test process's,
// because that's what a real heartbeat always looks like however far a
// worker host's clock has drifted from the database's. FailLostRuns must
// compare against that same clock, not the caller's, for its threshold
// to mean anything under clock skew between hosts.
func TestFailLostRunsComparesAgainstTheDatabasesClock(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	seedRepo(t, database, "r1", "demo")
	svc := NewService(database)
	createQueuedRun(t, database, svc, false)
	if err := svc.RegisterWorker(ctx, "w1", "host"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClaimNext(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE workers SET heartbeat_at = clock_timestamp() - interval '90 seconds' WHERE id = 'w1'`); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.FailLostRuns(ctx, 5*time.Minute); err != nil || n != 0 {
		t.Fatalf("FailLostRuns(5m) with a 90s-old heartbeat = %d, %v; want still alive", n, err)
	}
	if n, err := svc.FailLostRuns(ctx, time.Minute); err != nil || n != 1 {
		t.Fatalf("FailLostRuns(1m) with a 90s-old heartbeat = %d, %v; want lost", n, err)
	}
}

func TestPruneRunsKeepsEachRefsLatestAndDeployments(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	seedRepo(t, database, "r1", "demo")
	insertTestRun(t, database, "old1", "r1", 1, "branch", "main", "passed", true)
	insertTestRun(t, database, "old2", "r1", 2, "branch", "main", "failed", true)
	insertTestRun(t, database, "tag1", "r1", 3, "tag", "v1", "passed", true)
	insertTestRun(t, database, "live", "r1", 4, "branch", "main", "running", false)
	if _, err := database.Pool.Exec(ctx, `UPDATE runs SET finished_at = now() - interval '200 days' WHERE finished_at IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO deployments (id, repo_id, target, version, commit_hash, run_id) VALUES ('d1', 'r1', 'production', 'v1', 'abc', 'old1')`); err != nil {
		t.Fatal(err)
	}

	n, err := NewService(database).PruneRuns(ctx, time.Now().Add(-90*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PruneRuns = %d, %v; want only main's older run pruned", n, err)
	}
	var remaining []string
	rows, err := database.Pool.Query(ctx, `SELECT id FROM runs ORDER BY number`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		remaining = append(remaining, id)
	}
	rows.Close()
	if strings.Join(remaining, ",") != "old2,tag1,live" {
		t.Fatalf("remaining runs = %v", remaining)
	}
	var deployments int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM deployments WHERE id = 'd1' AND run_id IS NULL`).Scan(&deployments); err != nil || deployments != 1 {
		t.Fatalf("the deployment of a pruned run must stay, unlinked: %d, %v", deployments, err)
	}
}

// TestALostRunStopsItsWorker covers a worker cut off from the database
// long enough for another worker to fail its run as lost: once it can
// reach the database again, it is told to stop, and what it records
// afterwards does not bring the run's steps back to life.
func TestALostRunStopsItsWorker(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	seedRepo(t, database, "r1", "demo")
	svc := NewService(database)
	run := createQueuedRun(t, database, svc, false)
	if err := svc.RegisterWorker(ctx, "w1", "host"); err != nil {
		t.Fatal(err)
	}
	claim, err := svc.ClaimNext(ctx, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if reason, err := svc.StopReason(ctx, run.ID); err != nil || reason != "" {
		t.Fatalf("StopReason while running = %q, %v", reason, err)
	}
	if err := svc.StopWorker(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.FailLostRuns(ctx, time.Minute); err != nil || n != 1 {
		t.Fatalf("FailLostRuns = %d, %v", n, err)
	}
	if reason, err := svc.StopReason(ctx, run.ID); err != nil || reason != "the run has already ended" {
		t.Fatalf("StopReason after the run was failed as lost = %q, %v", reason, err)
	}

	step := claim.Steps[0]
	if err := svc.StepStarted(ctx, run.ID, step.ID); !errors.Is(err, ErrRunEnded) {
		t.Fatalf("StepStarted on a run already failed as lost = %v, want ErrRunEnded", err)
	}
	zero := 0
	if err := svc.StepFinished(ctx, run.ID, step.ID, StepPassed, &zero); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := database.Pool.QueryRow(ctx, `SELECT status FROM steps WHERE id = $1`, step.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(StepSkipped) {
		t.Fatalf("step of a lost run = %s, want it left as the run ended it (skipped)", status)
	}

	if _, err := database.Pool.Exec(ctx, `DELETE FROM repos WHERE id = 'r1'`); err != nil {
		t.Fatal(err)
	}
	if reason, err := svc.StopReason(ctx, run.ID); err != nil || reason != "the run no longer exists" {
		t.Fatalf("StopReason after the repository was deleted = %q, %v", reason, err)
	}
}

// TestAppendLogIsIgnoredOnceItsRunHasEnded covers a worker that kept
// writing a step's output after its run was already failed as lost: the
// chunk must not appear in the log, the same way the step's own status
// stays as the run ended it rather than what the worker reports after.
func TestAppendLogIsIgnoredOnceItsRunHasEnded(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	seedRepo(t, database, "r1", "demo")
	svc := NewService(database)
	run := createQueuedRun(t, database, svc, false)
	if err := svc.RegisterWorker(ctx, "w1", "host"); err != nil {
		t.Fatal(err)
	}
	claim, err := svc.ClaimNext(ctx, "w1")
	if err != nil {
		t.Fatal(err)
	}
	step := claim.Steps[0]
	if err := svc.AppendLog(ctx, run.ID, step.ID, 0, "before\n"); err != nil {
		t.Fatal(err)
	}
	if err := svc.StopWorker(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.FailLostRuns(ctx, time.Minute); err != nil || n != 1 {
		t.Fatalf("FailLostRuns = %d, %v", n, err)
	}
	if err := svc.AppendLog(ctx, run.ID, step.ID, 1, "after\n"); err != nil {
		t.Fatal(err)
	}
	chunks, err := svc.LogChunks(ctx, step.ID, -1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Content != "before\n" {
		t.Fatalf("LogChunks after the run ended = %+v, want only the chunk written before it ended", chunks)
	}
}

// TestAppendLogAcceptsTheSameChunkTwice covers a worker retrying a chunk
// whose first attempt was stored but not acknowledged.
func TestAppendLogAcceptsTheSameChunkTwice(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	seedRepo(t, database, "r1", "demo")
	svc := NewService(database)
	run := createQueuedRun(t, database, svc, false)
	if err := svc.RegisterWorker(ctx, "w1", "host"); err != nil {
		t.Fatal(err)
	}
	claim, err := svc.ClaimNext(ctx, "w1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := svc.AppendLog(ctx, run.ID, claim.Steps[0].ID, 0, "once\n"); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	chunks, err := svc.LogChunks(ctx, claim.Steps[0].ID, -1, 10)
	if err != nil || len(chunks) != 1 || chunks[0].Content != "once\n" {
		t.Fatalf("LogChunks = %+v, %v", chunks, err)
	}
}

// TestAnyWorkerOnline is what tells a person why a queued run is not
// starting: a worker is online from the moment it registers until it
// stops, or until its heartbeat is WorkerLostAfter old.
func TestAnyWorkerOnline(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)
	online := func() bool {
		t.Helper()
		ok, err := svc.AnyWorkerOnline(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	if online() {
		t.Fatal("online with no worker ever registered")
	}
	if err := svc.RegisterWorker(ctx, "w1", "host"); err != nil {
		t.Fatal(err)
	}
	if !online() {
		t.Fatal("not online right after a worker registered")
	}
	if _, err := database.Pool.Exec(ctx,
		`UPDATE workers SET heartbeat_at = now() - make_interval(secs => $1) - interval '1 second'`,
		WorkerLostAfter.Seconds()); err != nil {
		t.Fatal(err)
	}
	if online() {
		t.Fatal("online with only a heartbeat older than WorkerLostAfter")
	}
	if err := svc.Heartbeat(ctx, "w1", 0); err != nil {
		t.Fatal(err)
	}
	if !online() {
		t.Fatal("not online after a fresh heartbeat")
	}
	if err := svc.StopWorker(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	if online() {
		t.Fatal("online after the only worker stopped")
	}
}
