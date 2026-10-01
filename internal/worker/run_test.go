package worker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
	"github.com/mmrzaf/gitman/internal/repo"
)

// runFixture is a real run, queued and then claimed against PostgreSQL
// exactly as a worker claims one, and the source Git repository holding
// the pipeline its commit checks out.
type runFixture struct {
	db    *postgres.DB
	svc   *ci.Service
	claim *ci.Claim
	url   string
	fake  *fakeDocker
}

// newRunFixture creates a repository and queues a run of pipeline at its
// source repository's own HEAD commit.
func newRunFixture(t *testing.T, pipeline string, images ...string) *runFixture {
	t.Helper()
	return buildRunFixture(t, pipeline, pipeline, "", images...)
}

// newRunFixtureAtCommit is newRunFixture with the run pointed at commit
// instead of HEAD, so a test can queue a run against a commit its source
// repository never actually has.
func newRunFixtureAtCommit(t *testing.T, pipeline, commit string, images ...string) *runFixture {
	t.Helper()
	return buildRunFixture(t, pipeline, pipeline, commit, images...)
}

// buildRunFixture queues a run created from pipeline, at a commit whose
// own pipeline file holds committed: the two differ only when a test
// needs the checkout to disagree with what the run was created from.
func buildRunFixture(t *testing.T, pipeline, committed, commit string, images ...string) *runFixture {
	t.Helper()
	database := pgtest.Open(t)
	svc := ci.NewService(database)
	ctx := context.Background()

	src, _ := sourceRepo(t)
	if err := os.WriteFile(filepath.Join(src, ci.FileName), []byte(committed), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, src, "add", "-A")
	gitIn(t, src, "commit", "--quiet", "-m", "pipeline")
	if commit == "" {
		commit = gitIn(t, src, "rev-parse", "HEAD")
	}

	if _, err := database.Pool.Exec(ctx, `INSERT INTO repos (id, name) VALUES ('demo', 'demo')`); err != nil {
		t.Fatal(err)
	}
	var created *ci.Created
	err := database.Tx(ctx, func(tx postgres.Tx) error {
		var err error
		created, err = svc.CreateTx(ctx, tx, ci.CreateParams{
			RepoID: "demo", Commit: commit, RefKind: git.KindBranch, RefName: "main",
			Trigger: ci.TriggerManual, Pipeline: []byte(pipeline), Decision: repo.Decision{AllowDeploy: true},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != ci.StatusQueued {
		t.Fatalf("run was not queued: status=%s reason=%q", created.Status, created.Reason)
	}

	if err := svc.RegisterWorker(ctx, "w1", "host"); err != nil {
		t.Fatal(err)
	}
	claim, err := svc.ClaimNext(ctx, "w1", 30*time.Minute, []string{"alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	return &runFixture{db: database, svc: svc, claim: claim, url: "file://" + src, fake: newFakeDocker(t, images...)}
}

// execute runs the fixture's claim to completion, records its end the
// way a worker does, and returns the outcome alongside the run's
// settled detail: steps, statuses and logs, as PostgreSQL now has them.
func (f *runFixture) execute(t *testing.T, secrets map[string]string, timeout time.Duration) (ci.Outcome, *ci.RunDetail) {
	t.Helper()
	ctx := context.Background()
	ws, err := createWorkspace(t.TempDir(), f.claim.RunID)
	if err != nil {
		t.Fatal(err)
	}
	e := &execution{claim: f.claim, journal: f.svc, docker: f.fake.docker, ws: ws, repoURL: f.url,
		secrets: secrets, defaultTimeout: timeout, cancelPoll: 50 * time.Millisecond,
		log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	cfg, err := ci.Parse(f.claim.Pipeline)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timeout > 0 {
		timeout = cfg.Timeout
	}
	f.claim.Deadline = time.Now().Add(timeout)
	outcome := e.run(ctx)
	if err := f.svc.Finish(ctx, f.claim.RunID, outcome); err != nil {
		t.Fatal(err)
	}
	detail, err := f.svc.Run(ctx, f.claim.RepoID, f.claim.Number)
	if err != nil {
		t.Fatal(err)
	}
	return outcome, detail
}

// stepEvents summarizes a run's settled steps as "start <name>|finish
// <name> <status>" entries, in step order, the way the run actually
// touched them: a step never started or finished contributes nothing,
// matching a skipped step or one the run never reached.
func stepEvents(detail *ci.RunDetail) string {
	var parts []string
	for _, s := range detail.Steps {
		if s.StartedAt != nil {
			parts = append(parts, "start "+s.Name)
		}
		if s.FinishedAt != nil {
			parts = append(parts, fmt.Sprintf("finish %s %s", s.Name, s.Status))
		}
	}
	return strings.Join(parts, "|")
}

// stepLog returns everything AppendLog stored for one step.
func stepLog(t *testing.T, svc *ci.Service, stepID string) string {
	t.Helper()
	chunks, err := svc.LogChunks(context.Background(), stepID, -1, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(c.Content)
	}
	return b.String()
}

const passingPipeline = `image: alpine:3.20
steps:
  - name: test
    run: |
      echo "testing $GITMAN_REPO #$GITMAN_RUN with $DEPLOY_TOKEN"
      echo "result=ok" >> "$GITMAN_SUMMARY"
  - name: release
    when: tag
    run: echo never on a branch
  - name: report
    run: cat file.txt
`

func TestRunPassesStepsInOrderAndSkipsWhatDoesNotApply(t *testing.T) {
	f := newRunFixture(t, passingPipeline, "alpine:3.20")
	outcome, detail := f.execute(t, map[string]string{"DEPLOY_TOKEN": "hunter2-secret"}, time.Minute)

	if outcome.Status != ci.StatusPassed || outcome.Reason != "" || outcome.Summary["result"] != "ok" {
		t.Fatalf("outcome = %+v", outcome)
	}
	want := "start test|finish test passed|start report|finish report passed"
	if got := stepEvents(detail); got != want {
		t.Fatalf("events = %s, want %s", got, want)
	}
	want0 := fmt.Sprintf("testing demo #%d with ***\n", f.claim.Number)
	if got := stepLog(t, f.svc, detail.Steps[0].ID); got != want0 {
		t.Fatalf("step output = %q; the secret must be masked", got)
	}
	if got := stepLog(t, f.svc, detail.Steps[2].ID); got != "two\n" {
		t.Fatalf("second step ran against %q, want the checked-out commit's file", got)
	}
}

func TestRunStopsAtTheFirstFailure(t *testing.T) {
	f := newRunFixture(t, `image: alpine:3.20
steps:
  - name: build
    run: echo broken; exit 2
  - name: deploy
    run: echo must not run
`, "alpine:3.20")
	outcome, detail := f.execute(t, nil, time.Minute)
	if outcome.Status != ci.StatusFailed || outcome.Reason != `Step "build" exited with code 2.` {
		t.Fatalf("outcome = %+v", outcome)
	}
	// "deploy" never started; Finish settles it to skipped along with
	// the run itself failing, the same as a step the run never reached.
	if got := stepEvents(detail); got != "start build|finish build failed|finish deploy skipped" {
		t.Fatalf("events = %s; the step after a failure must not start", got)
	}
}

func TestRunRefusesAMissingImage(t *testing.T) {
	f := newRunFixture(t, passingPipeline)
	outcome, detail := f.execute(t, nil, time.Minute)
	if outcome.Status != ci.StatusFailed || !strings.Contains(outcome.Reason, "Image alpine:3.20 is not on this worker") {
		t.Fatalf("outcome = %+v", outcome)
	}
	if events := stepEvents(detail); strings.Contains(events, "start ") || len(f.fake.runCalls(t)) != 0 {
		t.Fatalf("no step may start when an image is missing: events = %s", events)
	}
}

func TestRunCanBeCancelled(t *testing.T) {
	f := newRunFixture(t, `image: alpine:3.20
steps:
  - name: long
    run: echo started; sleep 30
  - name: after
    run: echo must not run
`, "alpine:3.20")

	// Cancel once the step has actually printed something, the way a
	// person clicking Cancel races a real run: at some point after its
	// container is under way, never before.
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(stepLog(t, f.svc, f.claim.Steps[0].ID), "started") {
				_ = f.svc.Cancel(context.Background(), f.claim.RepoID, f.claim.Number, "tester")
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	start := time.Now()
	outcome, detail := f.execute(t, nil, time.Minute)
	if outcome.Status != ci.StatusCancelled {
		t.Fatalf("outcome = %+v", outcome)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancellation did not stop the step promptly")
	}
	// "after" never started; Finish settles it to cancelled along with
	// the run itself, the same as a step the run never reached.
	if got := stepEvents(detail); got != "start long|finish long cancelled|finish after cancelled" {
		t.Fatalf("events = %s", got)
	}
	log := stepLog(t, f.svc, detail.Steps[0].ID)
	if !strings.Contains(log, "started") || !strings.Contains(log, "stopped: the run was cancelled") {
		t.Fatalf("step output = %q", log)
	}
}

func TestRunTimesOut(t *testing.T) {
	f := newRunFixture(t, `image: alpine:3.20
steps:
  - name: long
    run: sleep 30
`, "alpine:3.20")
	// The timeout covers the fetch too, so it leaves room for that before
	// the step it is meant to cut short.
	outcome, detail := f.execute(t, nil, 3*time.Second)
	if outcome.Status != ci.StatusFailed || outcome.Reason != `The run passed its 3s timeout during step "long".` {
		t.Fatalf("outcome = %+v", outcome)
	}
	if log := stepLog(t, f.svc, detail.Steps[0].ID); !strings.Contains(log, "passed its 3s timeout") {
		t.Fatalf("step output = %q", log)
	}
}

func TestRunReportsAFailedCheckout(t *testing.T) {
	f := newRunFixtureAtCommit(t, passingPipeline, strings.Repeat("a", 40), "alpine:3.20")
	outcome, _ := f.execute(t, nil, time.Minute)
	if outcome.Status != ci.StatusFailed || outcome.Reason != "Could not fetch commit aaaaaaa. The worker's log has the details." {
		t.Fatalf("outcome = %+v", outcome)
	}
}

// TestRunUsesThePipelineItWasCreatedFrom is a checkout whose pipeline
// file reads differently from the one the run was created from — which a
// pushed commit's own Git attributes can arrange. The worker must run
// what the ref's rules were checked against: here, no Docker socket.
func TestRunUsesThePipelineItWasCreatedFrom(t *testing.T) {
	created := "image: alpine:3.20\nsteps:\n  - name: build\n    run: echo checked\n"
	committed := "image: alpine:3.20\ndocker: true\nsteps:\n  - name: build\n    run: echo unchecked\n"
	f := buildRunFixture(t, created, committed, "", "alpine:3.20")
	outcome, detail := f.execute(t, nil, time.Minute)
	if outcome.Status != ci.StatusPassed {
		t.Fatalf("outcome = %+v", outcome)
	}
	if got := stepLog(t, f.svc, detail.Steps[0].ID); got != "checked\n" {
		t.Fatalf("step output = %q; want the created pipeline's step", got)
	}
	if joined := strings.Join(f.fake.runCalls(t)[0], " "); strings.Contains(joined, "docker.sock") {
		t.Fatalf("the Docker socket was mounted from the checkout's pipeline: %s", joined)
	}
}

// TestRunTimeoutCoversTheImageCheck is a Docker daemon that stops
// answering before the first step: the run's timeout and a person's
// cancellation must still end it, not leave it running for as long as
// the daemon stays wedged.
func TestRunTimeoutCoversTheImageCheck(t *testing.T) {
	f := newRunFixture(t, passingPipeline, "alpine:3.20")
	t.Setenv("FAKE_DOCKER_HANG", "image")
	start := time.Now()
	outcome, _ := f.execute(t, nil, 500*time.Millisecond)
	if outcome.Status != ci.StatusFailed || outcome.Reason != "The run passed its 500ms timeout while checking its images." {
		t.Fatalf("outcome = %+v", outcome)
	}
	if elapsed := time.Since(start); elapsed > clientWaitDelay+5*time.Second {
		t.Fatalf("the run took %s to end", elapsed)
	}
}

func TestRunCancellationCoversTheImageCheck(t *testing.T) {
	f := newRunFixture(t, passingPipeline, "alpine:3.20")
	t.Setenv("FAKE_DOCKER_HANG", "image")
	if err := f.svc.Cancel(context.Background(), f.claim.RepoID, f.claim.Number, "tester"); err != nil {
		t.Fatal(err)
	}
	outcome, detail := f.execute(t, nil, time.Minute)
	if outcome.Status != ci.StatusCancelled || detail.Status != ci.StatusCancelled {
		t.Fatalf("outcome = %+v, run = %s", outcome, detail.Status)
	}
}

// TestRunStartsNoStepOnceTheRunHasEnded is a worker cut off long enough
// for its run to be failed as lost: when it reaches the next step, it
// must not start another container for a run that has already ended.
func TestRunStartsNoStepOnceTheRunHasEnded(t *testing.T) {
	f := newRunFixture(t, passingPipeline, "alpine:3.20")
	ctx := context.Background()
	if err := f.svc.StopWorker(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	if n, err := f.svc.FailLostRuns(ctx, time.Minute); err != nil || n != 1 {
		t.Fatalf("FailLostRuns = %d, %v", n, err)
	}
	_, detail := f.execute(t, nil, time.Minute)
	if calls := f.fake.runCalls(t); len(calls) != 0 {
		t.Fatalf("started %d containers for a run that had already ended", len(calls))
	}
	if detail.Status != ci.StatusFailed || detail.Reason != "The worker running it stopped responding." {
		t.Fatalf("run = %s %q; want the lost-worker failure kept", detail.Status, detail.Reason)
	}
}

// TestRunReasonsKeepInternalsOut is a failure on the worker's side — the
// Docker daemon refusing the image check. Every signed-in person reads a
// run's reason, so it must not carry the daemon's own output.
func TestRunReasonsKeepInternalsOut(t *testing.T) {
	f := newRunFixture(t, passingPipeline, "alpine:3.20")
	t.Setenv("FAKE_DOCKER_DOWN", "1")
	outcome, _ := f.execute(t, nil, time.Minute)
	if outcome.Status != ci.StatusFailed || outcome.Reason != "Could not check for image alpine:3.20. The worker's log has the details." {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestUnconfirmedCleanupRetainsWorkspaceAndRecoveryNeverReruns(t *testing.T) {
	f := newRunFixture(t, "image: alpine:3.20\nsteps:\n  - name: test\n    run: echo executed-once\n", "alpine:3.20")
	root, transport := t.TempDir(), t.TempDir()
	if err := os.Symlink(strings.TrimPrefix(f.url, "file://"), filepath.Join(transport, "demo.git")); err != nil {
		t.Fatal(err)
	}
	w := New(Config{WorkspaceRoot: root, WebURL: "file://" + transport, DefaultTimeout: time.Minute}, f.db, f.svc, nil, f.fake.docker, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.id = "w1"
	t.Setenv("FAKE_DOCKER_RM_FAIL", "1")
	outcome := w.execute(t.Context(), f.claim, w.log)
	if !outcome.RecoveryRequired {
		t.Fatalf("unconfirmed removal finalized execution: %+v", outcome)
	}
	if _, err := os.Stat(filepath.Join(root, f.claim.RunID)); err != nil {
		t.Fatal("workspace was removed before termination")
	}
	if w.recoverExecutions(t.Context()) {
		t.Fatal("failed removal was mistaken for recovery")
	}
	t.Setenv("FAKE_DOCKER_RM_FAIL", "")
	if !w.recoverExecutions(t.Context()) {
		t.Fatal("recovery did not complete after Docker recovered")
	}
	if calls := f.fake.CallsTo(t, "start"); len(calls) != 1 {
		t.Fatalf("script was rerun: %v", calls)
	}
	detail, err := f.svc.Run(t.Context(), f.claim.RepoID, f.claim.Number)
	if err != nil || detail.Status != ci.StatusFailed || detail.Steps[0].ExitCode == nil || *detail.Steps[0].ExitCode != 0 {
		t.Fatalf("recovered result = %+v, %v", detail, err)
	}
	if _, err := os.Stat(filepath.Join(root, f.claim.RunID)); !os.IsNotExist(err) {
		t.Fatalf("confirmed cleanup retained workspace: %v", err)
	}
}

func TestExplicitDeployRecordsOnlyAfterItsScriptPasses(t *testing.T) {
	f := newRunFixture(t, "image: alpine:3.20\ntargets:\n  staging:\n    branch: main\nsteps:\n  - name: build\n    run: echo build\n  - name: deploy\n    type: deploy\n    when: target\n    run: echo deployed\n", "alpine:3.20")
	outcome, detail := f.execute(t, nil, time.Minute)
	if outcome.Status != ci.StatusPassed || !detail.Deployed || detail.Steps[1].Type != ci.StepDeploy {
		t.Fatalf("deploy result = %+v, %+v", outcome, detail)
	}
	live, err := f.svc.LiveForRepo(t.Context(), f.claim.RepoID)
	if err != nil || len(live) != 1 || live[0].Target != "staging" {
		t.Fatalf("deployments = %+v, %v", live, err)
	}
}

func TestPostDeploymentFailureKeepsDeploymentReceipt(t *testing.T) {
	f := newRunFixture(t, "image: alpine:3.20\ntargets:\n  production:\n    branch: main\nsteps:\n  - name: deploy\n    type: deploy\n    when: target\n    run: echo deployed\n  - name: health\n    when: target\n    run: exit 7\n", "alpine:3.20")
	outcome, detail := f.execute(t, nil, time.Minute)
	if outcome.Status != ci.StatusFailed || !detail.Deployed || detail.Steps[0].Status != ci.StepPassed || detail.Steps[1].Status != ci.StepFailed {
		t.Fatalf("outcome=%+v detail=%+v", outcome, detail)
	}
	live, err := f.svc.LiveForRepo(t.Context(), f.claim.RepoID)
	if err != nil || len(live) != 1 {
		t.Fatalf("deployments=%v err=%v", live, err)
	}
}

func TestLeftoverCleanupPersistsDeploymentBeforeRemoval(t *testing.T) {
	f := newRunFixture(t, "image: alpine:3.20\ntargets:\n  production:\n    branch: main\nsteps:\n  - name: deploy\n    type: deploy\n    when: target\n    run: echo deployed\n", "alpine:3.20")
	ctx := t.Context()
	root := t.TempDir()
	ws, err := createWorkspace(root, f.claim.RunID)
	if err != nil {
		t.Fatal(err)
	}
	step := f.claim.Steps[0]
	name := "retained-deploy"
	for _, err := range []error{f.svc.StepStarted(ctx, f.claim.RunID, step.ID), f.svc.PlanContainer(ctx, f.claim.RunID, step.ID, name), f.svc.BeginDeployment(ctx, f.claim.RunID, step.ID)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	spec := containerSpec{Name: name, RunID: f.claim.RunID, Image: "alpine:3.20", Script: "echo deployed", Source: ws.source(), Meta: ws.meta(), Created: func(ctx context.Context, id string) error {
		return f.svc.ContainerCreated(ctx, f.claim.RunID, step.ID, id)
	}}
	if code, err := f.fake.docker.Run(ctx, spec, &strings.Builder{}); err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if err := f.svc.Finish(ctx, f.claim.RunID, failed("Worker lost")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DOCKER_PS", name+" "+f.claim.RunID+"\n")
	containers, _, err := RemoveLeftovers(ctx, f.fake.docker, root, f.svc.RunningRunIDs, f.svc.RecoverStepExit, f.svc.ConfirmContainersRemoved)
	if err != nil || containers != 1 {
		t.Fatalf("containers=%d err=%v", containers, err)
	}
	live, err := f.svc.LiveForRepo(ctx, f.claim.RepoID)
	if err != nil || len(live) != 1 {
		t.Fatalf("deployments=%v err=%v", live, err)
	}
}
