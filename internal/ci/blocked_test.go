package ci

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
)

func TestClassifyBlock(t *testing.T) {
	need := []string{"alpine:3.20", "go:1.24"}
	cases := map[string]struct {
		workers     []WorkerStatus
		maintenance bool
		kind        BlockKind
		permanent   bool
		missing     string
	}{
		"no workers":      {kind: BlockNoWorkers},
		"maintenance":     {workers: []WorkerStatus{{Ready: true, Online: true, Images: need}}, maintenance: true, kind: BlockMaintenance},
		"ready has all":   {workers: []WorkerStatus{{Ready: true, Online: true, Images: need}}},
		"all stopped":     {workers: []WorkerStatus{{Reason: "Stopped"}}, kind: BlockOffline},
		"unhealthy":       {workers: []WorkerStatus{{Online: true, Reason: "Docker is unavailable"}}, kind: BlockUnhealthy},
		"ready lacks":     {workers: []WorkerStatus{{Ready: true, Online: true, Images: []string{"alpine:3.20"}}}, kind: BlockMissingImages, permanent: true, missing: "go:1.24"},
		"closest worker":  {workers: []WorkerStatus{{Ready: true, Online: true}, {Ready: true, Online: true, Images: []string{"go:1.24"}}}, kind: BlockMissingImages, permanent: true, missing: "alpine:3.20"},
		"unready has all": {workers: []WorkerStatus{{Ready: true, Online: true}, {Online: true, Images: need, Reason: "Available disk is below the configured reserve"}}, kind: BlockUnhealthy},
		"offline has all": {workers: []WorkerStatus{{Ready: true, Online: true}, {Images: need}}, kind: BlockOffline},
		"unready unknown": {workers: []WorkerStatus{{Online: true, Reason: "Image inventory is unavailable"}}, kind: BlockUnhealthy},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b := classifyBlock(need, c.workers, c.maintenance)
			if c.kind == "" {
				if b != nil {
					t.Fatalf("blocker = %+v, want none", b)
				}
				return
			}
			if b == nil || b.Kind != c.kind || b.Permanent != c.permanent || strings.Join(b.Missing, ",") != c.missing {
				t.Fatalf("blocker = %+v, want %s permanent=%v missing=%q", b, c.kind, c.permanent, c.missing)
			}
			if b.Message() == "" {
				t.Fatal("blocker has no message")
			}
		})
	}
}

func TestExpireBlockedRuns(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	seedRepo(t, db, "r1", "demo")
	svc := NewService(db)
	run := createQueuedRun(t, db, svc, false)
	age := func() {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, `UPDATE runs SET queued_at = now() - make_interval(secs => $1) - interval '1 minute' WHERE id = $2`, MaxQueueWait.Seconds(), run.ID); err != nil {
			t.Fatal(err)
		}
	}
	expire := func() int {
		t.Helper()
		n, err := svc.ExpireBlockedRuns(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	status := func() (string, string) {
		t.Helper()
		var s, reason string
		if err := db.Q.QueryRow(ctx, `SELECT status, reason FROM runs WHERE id = $1`, run.ID).Scan(&s, &reason); err != nil {
			t.Fatal(err)
		}
		return s, reason
	}

	// An old run is never expired because workers are away.
	age()
	if n := expire(); n != 0 {
		t.Fatalf("expired %d runs with no workers", n)
	}
	if err := svc.RegisterWorker(ctx, "w1", "host"); err != nil {
		t.Fatal(err)
	}
	if n := expire(); n != 0 {
		t.Fatalf("expired %d runs with an unready worker", n)
	}

	// A ready worker without the image does not expire a young run.
	if err := svc.SetWorkerReadiness(ctx, "w1", true, "Ready", []string{"other:1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE runs SET queued_at = now() WHERE id = $1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if n := expire(); n != 0 {
		t.Fatalf("expired %d young runs", n)
	}
	b, err := svc.QueuedBlock(ctx, run.ID)
	if err != nil || b == nil || b.Kind != BlockMissingImages || strings.Join(b.Missing, ",") != "docker.io/library/alpine:3.20" {
		t.Fatalf("block = %+v, %v", b, err)
	}

	// Once the worker gains the image the run is no longer blocked.
	age()
	if err := svc.SetWorkerReadiness(ctx, "w1", true, "Ready", []string{"alpine:3.20"}); err != nil {
		t.Fatal(err)
	}
	if b, err := svc.QueuedBlock(ctx, run.ID); err != nil || b != nil {
		t.Fatalf("block with image present = %+v, %v", b, err)
	}
	if n := expire(); n != 0 {
		t.Fatalf("expired %d runs a worker can start", n)
	}

	// Without it, the old run ends with a reason, its steps skipped.
	if err := svc.SetWorkerReadiness(ctx, "w1", true, "Ready", []string{"other:1"}); err != nil {
		t.Fatal(err)
	}
	if n := expire(); n != 1 {
		t.Fatalf("expired %d runs, want 1", n)
	}
	st, reason := status()
	if st != "failed" || !strings.Contains(reason, "alpine:3.20") || !strings.Contains(reason, "Expired") {
		t.Fatalf("run = %s %q", st, reason)
	}
	var pending int
	if err := db.Q.QueryRow(ctx, `SELECT count(*) FROM steps WHERE run_id = $1 AND status IN ('pending','running')`, run.ID).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("open steps = %d, %v", pending, err)
	}
	if claim, err := svc.ClaimNext(ctx, "w1", time.Minute, []string{"alpine:3.20"}); err != nil || claim != nil {
		t.Fatalf("expired run was claimed: %+v, %v", claim, err)
	}
	if n := expire(); n != 0 {
		t.Fatalf("expired a finished run again: %d", n)
	}
}

// A claim and the expiry sweep racing for one run end with exactly one winner,
// and a run that started is never failed.
func TestExpireBlockedRunsDoesNotRaceClaims(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	seedRepo(t, db, "r1", "demo")
	svc := NewService(db)
	run := createQueuedRun(t, db, svc, false)
	if err := svc.RegisterWorker(ctx, "w1", "host"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetWorkerReadiness(ctx, "w1", true, "Ready", []string{"other:1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE runs SET queued_at = now() - interval '48 hours' WHERE id = $1`, run.ID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var claimed bool
	var expired int
	wg.Add(2)
	go func() {
		defer wg.Done()
		claim, err := svc.ClaimNext(ctx, "w1", time.Minute, []string{"alpine:3.20"})
		if err != nil {
			t.Error(err)
		}
		claimed = claim != nil
	}()
	go func() {
		defer wg.Done()
		var err error
		if expired, err = svc.ExpireBlockedRuns(ctx); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	var status string
	if err := db.Q.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1`, run.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	switch {
	case claimed && expired == 0 && status == "running":
	case !claimed && expired == 1 && status == "failed":
	default:
		t.Fatalf("claimed=%v expired=%d status=%s", claimed, expired, status)
	}
}

// Concurrent workers never take the same run, and a worker lacking a run's
// images takes none of them, however many race.
func TestConcurrentWorkersClaimEachRunOnce(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	seedRepo(t, db, "r1", "demo")
	svc := NewService(db)
	const runs = 6
	for i := range runs {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO runs (id, repo_id, number, commit_hash, ref_kind, ref_name, trigger, status, pipeline, required_images)
			VALUES ($1, 'r1', $2::bigint, repeat('a', 40), 'branch', 'b' || $2::text, 'push', 'queued', 'image: alpine:3.20'::bytea, '{docker.io/library/alpine:3.20}')`, fmt.Sprintf("run%d", i), i+1); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"w1", "w2", "w3", "bare"} {
		if err := svc.RegisterWorker(ctx, id, "host"); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	claimed := map[string]string{}
	var wg sync.WaitGroup
	for _, id := range []string{"w1", "w2", "w3", "bare"} {
		images := []string{"alpine:3.20"}
		if id == "bare" {
			images = []string{"other:1"}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				claim, err := svc.ClaimNext(ctx, id, time.Minute, images)
				if err != nil {
					t.Error(err)
					return
				}
				if claim == nil {
					return
				}
				mu.Lock()
				if prev, dup := claimed[claim.RunID]; dup {
					t.Errorf("run %s claimed by %s and %s", claim.RunID, prev, id)
				}
				claimed[claim.RunID] = id
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(claimed) != runs {
		t.Fatalf("claimed %d of %d runs", len(claimed), runs)
	}
	for run, id := range claimed {
		if id == "bare" {
			t.Fatalf("worker without the image claimed %s", run)
		}
	}
}

// A worker restart moves a run from offline to startable without any change to
// the run, and a stopped worker never expires it.
func TestQueuedBlockFollowsWorkerRestart(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	seedRepo(t, db, "r1", "demo")
	svc := NewService(db)
	run := createQueuedRun(t, db, svc, false)
	kind := func() BlockKind {
		t.Helper()
		b, err := svc.QueuedBlock(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if b == nil {
			return ""
		}
		return b.Kind
	}
	if got := kind(); got != BlockNoWorkers {
		t.Fatalf("no workers: %q", got)
	}
	if err := svc.RegisterWorker(ctx, "w1", "host"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetWorkerReadiness(ctx, "w1", true, "Ready", []string{"alpine:3.20"}); err != nil {
		t.Fatal(err)
	}
	if got := kind(); got != "" {
		t.Fatalf("ready worker: %q", got)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE workers SET heartbeat_at = now() - make_interval(secs => $1) - interval '1 second'`, WorkerReadyAfter.Seconds()); err != nil {
		t.Fatal(err)
	}
	if got := kind(); got != BlockOffline {
		t.Fatalf("silent worker: %q", got)
	}
	if err := svc.Heartbeat(ctx, "w1", 0); err != nil {
		t.Fatal(err)
	}
	if got := kind(); got != "" {
		t.Fatalf("worker after a fresh heartbeat: %q", got)
	}
	if err := svc.StopWorker(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	if got := kind(); got != BlockOffline {
		t.Fatalf("stopped worker: %q", got)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE runs SET queued_at = now() - interval '72 hours'`); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.ExpireBlockedRuns(ctx); err != nil || n != 0 {
		t.Fatalf("expired %d with the worker stopped: %v", n, err)
	}
	if err := svc.RegisterWorker(ctx, "w1", "host"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetWorkerReadiness(ctx, "w1", true, "Ready", []string{"alpine:3.20"}); err != nil {
		t.Fatal(err)
	}
	if got := kind(); got != "" {
		t.Fatalf("restarted worker: %q", got)
	}
	if claim, err := svc.ClaimNext(ctx, "w1", time.Minute, []string{"alpine:3.20"}); err != nil || claim == nil || claim.RunID != run.ID {
		t.Fatalf("restarted worker could not claim the old run: %+v, %v", claim, err)
	}
}
