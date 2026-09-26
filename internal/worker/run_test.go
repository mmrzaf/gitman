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
)

// runFixture is a real run, queued and then claimed against PostgreSQL
// exactly as a worker claims one, and the source Git repository holding
// the pipeline its commit checks out.
type runFixture struct {
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
			Trigger: ci.TriggerManual, Pipeline: []byte(pipeline),
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
	claim, err := svc.ClaimNext(ctx, "w1")
	if err != nil {
		t.Fatal(err)
	}
	return &runFixture{svc: svc, claim: claim, url: "file://" + src, fake: newFakeDocker(t, images...)}
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
