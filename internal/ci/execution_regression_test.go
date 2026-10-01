package ci

import (
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
	"github.com/mmrzaf/gitman/internal/repo"
	"strings"
	"testing"
	"time"
)

func TestSkippedDeployDoesNotRequireDeploymentPermission(t *testing.T) {
	p := []byte("image: alpine:3.20\ntargets:\n  staging:\n    branch: develop\n  production:\n    branch: main\nsteps:\n  - name: build\n    run: echo build\n  - name: deploy\n    type: deploy\n    when: production\n    run: echo deploy\n")
	pl := planRun(CreateParams{Commit: strings.Repeat("a", 40), RefKind: git.KindBranch, RefName: "develop", Pipeline: p})
	if pl.status != StatusQueued {
		t.Fatalf("build-only staging run was rejected: status=%s reason=%s", pl.status, pl.reason)
	}
}
func TestRecoveredDeployReceiptRecordsDeployment(t *testing.T) {
	db := pgtest.Open(t)
	ctx := t.Context()
	seedRepo(t, db, "r1", "demo")
	s := NewService(db)
	p := []byte("image: alpine:3.20\ntargets:\n  production:\n    branch: main\nsteps:\n  - name: deploy\n    type: deploy\n    when: target\n    run: echo deploy\n")
	if err := db.Tx(ctx, func(tx postgres.Tx) error {
		_, e := s.CreateTx(ctx, tx, CreateParams{RepoID: "r1", Commit: strings.Repeat("a", 40), RefKind: git.KindBranch, RefName: "main", Trigger: TriggerPush, Pipeline: p, Decision: repo.Decision{AllowDeploy: true}})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterWorker(ctx, "w", "host"); err != nil {
		t.Fatal(err)
	}
	c, e := s.ClaimNext(ctx, "w", time.Minute, []string{"alpine:3.20"})
	if e != nil || c == nil {
		t.Fatal(e)
	}
	for _, e := range []error{s.StepStarted(ctx, c.RunID, c.Steps[0].ID), s.PlanContainer(ctx, c.RunID, c.Steps[0].ID, "container"), s.BeginDeployment(ctx, c.RunID, c.Steps[0].ID), s.Finish(ctx, c.RunID, Outcome{Status: StatusFailed, Reason: "interrupted"}), s.RecoverStepExit(ctx, c.RunID, "container", 0), s.RecoverStepExit(ctx, c.RunID, "container", 0), s.ConfirmExecutionStopped(ctx, c.RunID)} {
		if e != nil {
			t.Fatal(e)
		}
	}
	live, e := s.LiveForRepo(ctx, "r1")
	if e != nil {
		t.Fatal(e)
	}
	if len(live) != 1 {
		t.Fatalf("confirmed deploy exit 0 produced %d deployment records", len(live))
	}
}

func TestDeploymentAllowsFollowingChecks(t *testing.T) {
	pipeline := []byte("image: alpine\ntargets:\n  production:\n    branch: main\nsteps:\n  - name: deploy\n    type: deploy\n    when: target\n    run: echo deploy\n  - name: health\n    when: target\n    run: echo health\n")
	pl := planRun(CreateParams{Commit: strings.Repeat("a", 40), RefKind: git.KindBranch, RefName: "main", Pipeline: pipeline, Decision: repo.Decision{AllowDeploy: true}})
	if pl.status != StatusQueued || len(pl.steps) != 2 {
		t.Fatalf("plan=%+v", pl)
	}
}

func TestOlderAutomaticDeploymentIsSuperseded(t *testing.T) {
	db := pgtest.Open(t)
	ctx := t.Context()
	seedRepo(t, db, "r1", "demo")
	s := NewService(db)
	pipeline := []byte("image: alpine\ntargets:\n  production:\n    branch: main\nsteps:\n  - name: deploy\n    type: deploy\n    when: target\n    run: echo deploy\n")
	if err := s.RegisterWorker(ctx, "w", "host"); err != nil {
		t.Fatal(err)
	}
	createClaim := func(trigger Trigger) *Claim {
		t.Helper()
		if err := db.Tx(ctx, func(tx postgres.Tx) error {
			_, err := s.CreateTx(ctx, tx, CreateParams{RepoID: "r1", Commit: strings.Repeat("a", 40), RefKind: git.KindBranch, RefName: "main", Trigger: trigger, Pipeline: pipeline, Decision: repo.Decision{AllowDeploy: true}})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		claim, err := s.ClaimNext(ctx, "w", time.Minute, []string{"docker.io/library/alpine:latest"})
		if err != nil || claim == nil {
			t.Fatalf("claim=%v err=%v", claim, err)
		}
		if err := s.StepStarted(ctx, claim.RunID, claim.Steps[0].ID); err != nil {
			t.Fatal(err)
		}
		return claim
	}
	old := createClaim(TriggerPush)
	newer := createClaim(TriggerPush)
	if err := s.BeginDeployment(ctx, newer.RunID, newer.Steps[0].ID); err != nil {
		t.Fatal(err)
	}
	code := 0
	if err := s.StepFinished(ctx, newer.RunID, newer.Steps[0].ID, StepPassed, &code); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginDeployment(ctx, old.RunID, old.Steps[0].ID); err != ErrDeploymentSuperseded {
		t.Fatalf("old deployment: %v", err)
	}
	// An explicit rerun is an intentional deployment, even of an older commit.
	manual := createClaim(TriggerManual)
	if err := s.BeginDeployment(ctx, manual.RunID, manual.Steps[0].ID); err != nil {
		t.Fatalf("manual deployment: %v", err)
	}
}

func TestHistoricalLogQuotaDoesNotRejectRun(t *testing.T) {
	db := pgtest.Open(t)
	ctx := t.Context()
	seedRepo(t, db, "r1", "demo")
	s := NewService(db)
	if _, err := db.Q.Exec(ctx, `INSERT INTO repository_storage(repo_id,log_bytes) VALUES('r1',1073741824)`); err != nil {
		t.Fatal(err)
	}
	var run *Created
	err := db.Tx(ctx, func(tx postgres.Tx) error {
		var err error
		run, err = s.CreateTx(ctx, tx, CreateParams{RepoID: "r1", Commit: strings.Repeat("a", 40), RefKind: git.KindBranch, RefName: "main", Trigger: TriggerPush, Pipeline: []byte("image: alpine\nsteps:\n  - name: build\n    run: echo build\n")})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusQueued {
		t.Fatalf("status=%s reason=%s", run.Status, run.Reason)
	}
}
