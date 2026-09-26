package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
)

// TestRetryBrieflyRecoversFromATransientFailure covers what Worker.stop
// relies on to record itself stopped even when the database refuses the
// first try or two: Run's deferred call to it has no context left to
// retry against forever the way finish does.
func TestRetryBrieflyRecoversFromATransientFailure(t *testing.T) {
	calls := 0
	err := retryBriefly(context.Background(), 4, time.Millisecond, func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("database is down")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryBriefly = %v, want nil after recovering", err)
	}
	if calls != 3 {
		t.Fatalf("op was called %d times, want exactly 3 (stop retrying once it succeeds)", calls)
	}
}

func TestRetryBrieflyGivesUpAfterItsAttemptsAreExhausted(t *testing.T) {
	calls := 0
	sentinel := errors.New("database is down")
	err := retryBriefly(context.Background(), 3, time.Millisecond, func(context.Context) error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("retryBriefly = %v, want the last attempt's error", err)
	}
	if calls != 3 {
		t.Fatalf("op was called %d times, want exactly 3, not retried forever", calls)
	}
}

// TestFinishDoesNotLeaveARunRunning records the end of a run whose
// summary the database refuses. The worker must still record the end —
// without the summary — rather than leave the run running while the
// worker stays alive and the lost-worker check never looks at it.
func TestFinishDoesNotLeaveARunRunning(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	if _, err := database.Pool.Exec(ctx, `INSERT INTO repos (id, name) VALUES ('r1', 'demo')`); err != nil {
		t.Fatal(err)
	}
	svc := ci.NewService(database)
	err := database.Tx(ctx, func(tx postgres.Tx) error {
		_, err := svc.CreateTx(ctx, tx, ci.CreateParams{
			RepoID: "r1", Commit: strings.Repeat("a", 40), RefKind: git.KindBranch, RefName: "main",
			Trigger: ci.TriggerPush, Pipeline: []byte("image: alpine:3.20\nsteps:\n  - name: test\n    run: 'true'\n"),
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	w := New(Config{}, database, svc, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := svc.RegisterWorker(ctx, w.id, "host"); err != nil {
		t.Fatal(err)
	}
	claim, err := svc.ClaimNext(ctx, w.id)
	if err != nil || claim == nil {
		t.Fatalf("ClaimNext = %v, %v", claim, err)
	}

	outcome := ci.Outcome{Status: ci.StatusPassed, Summary: map[string]string{"bad": "a\x00b"}}
	if !w.finish(ctx, claim.RunID, outcome, w.log) {
		t.Fatal("finish gave up")
	}
	var status, reason string
	if err := database.Pool.QueryRow(ctx, `SELECT status, reason FROM runs WHERE id = $1`, claim.RunID).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "passed" || reason != "The run's summary could not be stored." {
		t.Fatalf("run = %s %q", status, reason)
	}
}

// queueRun creates repository r1 and one queued run in it.
func queueRun(t *testing.T, database *postgres.DB, svc *ci.Service) {
	t.Helper()
	ctx := context.Background()
	if _, err := database.Pool.Exec(ctx, `INSERT INTO repos (id, name) VALUES ('r1', 'demo')`); err != nil {
		t.Fatal(err)
	}
	err := database.Tx(ctx, func(tx postgres.Tx) error {
		_, err := svc.CreateTx(ctx, tx, ci.CreateParams{
			RepoID: "r1", Commit: strings.Repeat("a", 40), RefKind: git.KindBranch, RefName: "main",
			Trigger: ci.TriggerPush, Pipeline: []byte("image: alpine:3.20\nsteps:\n  - name: test\n    run: 'true'\n"),
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func runStatus(t *testing.T, database *postgres.DB) string {
	t.Helper()
	var status string
	if err := database.Pool.QueryRow(context.Background(), `SELECT status FROM runs`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

// TestDrainClaimsNothingWhileDockerIsDown is a worker whose Docker daemon
// is down: it must leave queued runs for other workers, not claim each
// one only to fail it.
func TestDrainClaimsNothingWhileDockerIsDown(t *testing.T) {
	database := pgtest.Open(t)
	svc := ci.NewService(database)
	queueRun(t, database, svc)
	fake := newFakeDocker(t, "alpine:3.20")
	t.Setenv("FAKE_DOCKER_DOWN", "1")
	w := New(Config{WorkspaceRoot: t.TempDir()}, database, svc, nil, fake.docker, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := svc.RegisterWorker(context.Background(), w.id, "host"); err != nil {
		t.Fatal(err)
	}
	w.drain(context.Background())
	if status := runStatus(t, database); status != "queued" {
		t.Fatalf("run status = %s; want it left queued", status)
	}
}

// TestBeatWaitsBeforeJudgingOtherWorkers is a database back after an
// outage longer than LostAfter: every worker's last heartbeat is stale,
// including those of workers still running their runs. The first worker
// to reach the database again must not fail their runs before they have
// had the chance to heartbeat too.
func TestBeatWaitsBeforeJudgingOtherWorkers(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := ci.NewService(database)
	queueRun(t, database, svc)
	if err := svc.RegisterWorker(ctx, "other", "host"); err != nil {
		t.Fatal(err)
	}
	if claim, err := svc.ClaimNext(ctx, "other"); err != nil || claim == nil {
		t.Fatalf("ClaimNext = %v, %v", claim, err)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE workers SET heartbeat_at = now() - interval '5 minutes'`); err != nil {
		t.Fatal(err)
	}

	w := New(Config{}, database, svc, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := svc.RegisterWorker(ctx, w.id, "host"); err != nil {
		t.Fatal(err)
	}
	// Its own heartbeats have only just started succeeding again.
	w.beat(ctx)
	if status := runStatus(t, database); status != "running" {
		t.Fatalf("run status = %s right after the database came back; want running", status)
	}
	// Once it has been heartbeating for LostAfter, a worker still silent
	// is lost.
	w.healthySince = time.Now().Add(-LostAfter - time.Second)
	w.beat(ctx)
	if status := runStatus(t, database); status != "failed" {
		t.Fatalf("run status = %s; want failed once the other worker stayed silent", status)
	}
}

// TestHeartbeatKeepsBeatingWhileCleanupIsStuck is a cleanup pass that
// cannot finish — here a "docker ps" that never answers; on a real host,
// removing a very large workspace. The worker's heartbeat must go on
// meanwhile, or other workers would fail its live runs as lost.
func TestHeartbeatKeepsBeatingWhileCleanupIsStuck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	database := pgtest.Open(t)
	svc := ci.NewService(database)
	fake := newFakeDocker(t)
	t.Setenv("FAKE_DOCKER_HANG", "ps")
	w := New(Config{WorkspaceRoot: t.TempDir()}, database, svc, nil, fake.docker, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.heartbeatEvery, w.cleanupEvery = 50*time.Millisecond, 50*time.Millisecond
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	time.Sleep(time.Second)
	var sinceStart time.Duration
	if err := database.Pool.QueryRow(ctx, `
		SELECT extract(epoch FROM heartbeat_at - started_at) * interval '1 second' FROM workers WHERE id = $1
	`, w.id).Scan(&sinceStart); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
	if sinceStart < 700*time.Millisecond {
		t.Fatalf("last heartbeat %s after start; the heartbeat stopped while cleanup was stuck", sinceStart)
	}
}
