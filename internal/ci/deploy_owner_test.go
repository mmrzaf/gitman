package ci

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
	"github.com/mmrzaf/gitman/internal/repo"
)

func TestDeployOwnerSurvivesLostWorkerAndRecordingIsIdempotent(t *testing.T) {
	ctx := t.Context()
	db := pgtest.Open(t)
	seedRepo(t, db, "r1", "demo")
	svc := NewService(db)
	pipeline := []byte("image: alpine:3.20\ntargets:\n  staging:\n    branch: '*'\nsteps:\n  - name: deploy\n    type: deploy\n    when: target\n    run: echo deploy\n")
	queue := func(ref, worker string) *Claim {
		t.Helper()
		if err := db.Tx(ctx, func(tx postgres.Tx) error {
			_, err := svc.CreateTx(ctx, tx, CreateParams{RepoID: "r1", Commit: strings.Repeat("a", 40), RefKind: git.KindBranch, RefName: ref, Trigger: TriggerPush, Pipeline: pipeline, Decision: repo.Decision{AllowDeploy: true}})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := svc.RegisterWorker(ctx, worker, "host"); err != nil {
			t.Fatal(err)
		}
		claim, err := svc.ClaimNext(ctx, worker, time.Minute, []string{"alpine:3.20"})
		if err != nil || claim == nil {
			t.Fatalf("claim = %+v, %v", claim, err)
		}
		if err := svc.StepStarted(ctx, claim.RunID, claim.Steps[0].ID); err != nil {
			t.Fatal(err)
		}
		return claim
	}
	first, second := queue("one", "w1"), queue("two", "w2")
	if err := svc.BeginDeployment(ctx, first.RunID, first.Steps[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.BeginDeployment(ctx, second.RunID, second.Steps[0].ID); !errors.Is(err, ErrDeploymentBusy) {
		t.Fatalf("competing deploy was allowed: %v", err)
	}
	if _, err := db.Q.Exec(ctx, `UPDATE workers SET heartbeat_at = now() - interval '1 hour' WHERE id = 'w1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FailLostRuns(ctx, WorkerLostAfter); err != nil {
		t.Fatal(err)
	}
	if err := svc.BeginDeployment(ctx, second.RunID, second.Steps[0].ID); !errors.Is(err, ErrDeploymentBusy) {
		t.Fatalf("heartbeat expiry released ownership: %v", err)
	}
	// The worker cleanup coordinator supplies this confirmation only after
	// terminating or inspecting every retained container on the owner host.
	if err := svc.ConfirmExecutionStopped(ctx, first.RunID); err != nil {
		t.Fatal(err)
	}
	if err := svc.BeginDeployment(ctx, second.RunID, second.Steps[0].ID); err != nil {
		t.Fatal(err)
	}
	zero := 0
	for range 2 {
		if err := svc.StepFinished(ctx, second.RunID, second.Steps[0].ID, StepPassed, &zero); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Finish(ctx, second.RunID, Outcome{Status: StatusPassed}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.Q.QueryRow(ctx, `SELECT count(*) FROM deployments WHERE run_id = $1`, second.RunID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("deployments = %d, %v", count, err)
	}
	detail, err := svc.Run(ctx, "r1", second.Number)
	if err != nil || !detail.Deployed {
		t.Fatalf("deployment result = %+v, %v", detail, err)
	}
	var generation int
	var owner *string
	if err := db.Q.QueryRow(ctx, `SELECT generation, owner_run_id FROM deployment_targets WHERE repo_id = 'r1' AND target = 'staging'`).Scan(&generation, &owner); err != nil || generation != 2 || owner != nil {
		t.Fatalf("owner = %v generation %d, %v", owner, generation, err)
	}
}

func TestClaimRequiresImagesAndPersistsDeadlineAtClaim(t *testing.T) {
	ctx := t.Context()
	db := pgtest.Open(t)
	seedRepo(t, db, "r1", "demo")
	svc := NewService(db)
	created := createQueuedRun(t, db, svc, false)
	if err := svc.RegisterWorker(ctx, "w1", "host"); err != nil {
		t.Fatal(err)
	}
	if claim, err := svc.ClaimNext(ctx, "w1", time.Minute, []string{"unrelated:image"}); err != nil || claim != nil {
		t.Fatalf("unmatched claim = %+v, %v", claim, err)
	}
	claim, err := svc.ClaimNext(ctx, "w1", time.Minute, []string{"alpine:3.20"})
	if err != nil || claim == nil || claim.RunID != created.ID {
		t.Fatalf("matching claim = %+v, %v", claim, err)
	}
	var started, deadline time.Time
	if err := db.Q.QueryRow(ctx, `SELECT started_at, deadline_at FROM runs WHERE id = $1`, claim.RunID).Scan(&started, &deadline); err != nil {
		t.Fatal(err)
	}
	if deadline.Sub(started) != time.Minute || !deadline.Equal(claim.Deadline) {
		t.Fatalf("deadline = %v, started %v, returned %v", deadline, started, claim.Deadline)
	}
}
