package ci

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
	"github.com/mmrzaf/gitman/internal/repo"
)

func TestDeploymentRemembersActualSourceAfterRunPruned(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	seedRepo(t, db, "r1", "demo")
	svc := NewService(db)
	pipeline := []byte("image: alpine:3.20\ntargets:\n  staging:\n    branch: develop\nsteps:\n  - name: deploy\n    type: deploy\n    when: target\n    run: echo deployed\n")
	var created *Created
	if err := db.Tx(ctx, func(tx postgres.Tx) error {
		var err error
		created, err = svc.CreateTx(ctx, tx, CreateParams{
			RepoID: "r1", Commit: strings.Repeat("a", 40), RefKind: git.KindBranch, RefName: "develop",
			Trigger: TriggerPush, Pipeline: pipeline, Decision: repo.Decision{AllowDeploy: true},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RegisterWorker(ctx, "worker", "host"); err != nil {
		t.Fatal(err)
	}
	claim, err := svc.ClaimNext(ctx, "worker", time.Minute, []string{"alpine:3.20"})
	if err != nil || claim == nil || claim.RunID != created.ID {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	step := claim.Steps[0]
	if err := svc.StepStarted(ctx, claim.RunID, step.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.BeginDeployment(ctx, claim.RunID, step.ID); err != nil {
		t.Fatal(err)
	}
	zero := 0
	if err := svc.StepFinished(ctx, claim.RunID, step.ID, StepPassed, &zero); err != nil {
		t.Fatal(err)
	}
	if err := svc.Finish(ctx, claim.RunID, Outcome{Status: StatusPassed}); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		live, err := svc.LiveForRepo(ctx, "r1")
		if err != nil || len(live) != 1 {
			t.Fatalf("live=%+v err=%v", live, err)
		}
		if live[0].SourceKind != git.KindBranch || live[0].SourceName != "develop" || live[0].Commit != strings.Repeat("a", 40) {
			t.Fatalf("deployment source not retained: %+v", live[0])
		}
		byRef, err := svc.LatestDeploymentPerRef(ctx, "r1")
		if err != nil {
			t.Fatal(err)
		}
		if d, ok := byRef["branch/develop"]; !ok || d.SourceName != "develop" {
			t.Fatalf("latest deployment for develop: %+v", byRef)
		}
	}
	check()
	// The latest deployment is independent of the originating run's retention.
	if _, err := db.Pool.Exec(ctx, `DELETE FROM runs WHERE id=$1`, claim.RunID); err != nil {
		t.Fatal(err)
	}
	check()
}
