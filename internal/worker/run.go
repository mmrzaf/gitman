package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/mmrzaf/gitman/internal/ci"
)

var errTimedOut = errors.New("run timed out")

// stopped is the cause a run's context ends with when the run must stop
// for a reason recorded outside this worker: it was cancelled, or it
// ended or was deleted elsewhere.
type stopped struct{ reason string }

func (s stopped) Error() string { return s.reason }

// execution runs one claimed run in its workspace, recording its
// progress through journal as it goes.
type execution struct {
	claim   *ci.Claim
	journal *ci.Service
	docker  *Docker
	ws      workspace
	// repoURL is the Git HTTP address the commit is fetched from.
	repoURL        string
	secrets        map[string]string
	defaultTimeout time.Duration
	cancelPoll     time.Duration
	log            *slog.Logger
}

func failed(format string, args ...any) ci.Outcome {
	return ci.Outcome{Status: ci.StatusFailed, Reason: fmt.Sprintf(format, args...)}
}

// internalFailure is the outcome of a failure in Gitman or on the worker's
// host rather than in the pipeline: what failed is logged in full here,
// and the run's reason — which every signed-in person can read — says
// only what went wrong, not a path, a database error or a daemon's
// output.
func (e *execution) internalFailure(what string, err error) ci.Outcome {
	e.log.Error(what, "run", e.claim.RunID, "error", err)
	return failed("%s. The worker's log has the details.", what)
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// run executes the claim and returns how it ended. It never returns
// early without an outcome: every failure, including Gitman's own, is a
// reason a person reads on the run's page.
//
// The pipeline it runs is the one stored with the run when it was
// created — the exact bytes whose Docker, target and secrets were checked
// against the ref's rules then — never the file in the checkout, which
// Git attributes in the pushed commit itself can make read differently.
func (e *execution) run(ctx context.Context) ci.Outcome {
	c := e.claim
	// Progress is recorded even while the worker is shutting down, so
	// the run's page says what happened.
	record := context.WithoutCancel(ctx)
	masker := newSecretMasker(e.secrets)

	cfg, err := ci.Parse(c.Pipeline)
	if err != nil {
		return failed("%s", err.Error())
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = e.defaultTimeout
	}
	// The timeout and cancellation cover the whole run, from the fetch
	// on: a wedged Docker daemon or Git server must not hold a run, and
	// its worker, past its timeout or past a person cancelling it.
	runCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	timer := time.AfterFunc(time.Until(c.Deadline), func() { stop(errTimedOut) })
	defer timer.Stop()
	go e.watchCancellation(runCtx, stop)

	if err := e.ws.checkout(runCtx, e.repoURL, ci.FetchUsername, c.FetchToken, c.Commit); err != nil {
		if o, ok := interrupted(runCtx, ctx, timeout, "while fetching the commit"); ok {
			return o
		}
		return e.internalFailure(fmt.Sprintf("Could not fetch commit %s", short(c.Commit)), err)
	}
	for _, image := range append([]string{cfg.Image}, cfg.Requires...) {
		ok, err := e.docker.ImageExists(runCtx, image)
		if o, stop := interrupted(runCtx, ctx, timeout, "while checking its images"); stop {
			return o
		}
		if err != nil {
			return e.internalFailure(fmt.Sprintf("Could not check for image %s", image), err)
		}
		if !ok {
			return failed("Image %s is not on this worker. Gitman never pulls images: pull it on the worker's Docker host.", image)
		}
	}

	rc := ci.RunContext{Repo: c.RepoName, RunNumber: c.Number, Commit: c.Commit, RefKind: c.RefKind, RefName: c.RefName, Target: c.Target}
	env := cfg.ResolvedEnv(rc)
	env["GITMAN_SUMMARY"] = containerSummary

	outcome := ci.Outcome{Status: ci.StatusPassed}
	for _, step := range c.Steps {
		if step.Skipped {
			continue
		}
		if step.Index >= len(cfg.Steps) || cfg.Steps[step.Index].Name != step.Name {
			outcome = failed("Step %q is not step %d of the run's pipeline.", step.Name, step.Index+1)
			break
		}
		if err := e.journal.StepStarted(record, c.RunID, step.ID); err != nil {
			if errors.Is(err, ci.ErrRunEnded) {
				// Failed as lost, cancelled or deleted while this worker
				// was cut off: the run's end is already recorded, and no
				// further step may start.
				outcome = ci.Outcome{Status: ci.StatusCancelled}
				break
			}
			outcome = e.internalFailure(fmt.Sprintf("Could not record the start of step %q", step.Name), err)
			break
		}
		stepOutcome, stepStatus, exitCode := e.runStep(runCtx, ctx, record, step, cfg, env, masker, timeout)

		if stepOutcome != nil && stepOutcome.RecoveryRequired {
			return *stepOutcome
		}
		finishCtx, stopFinish := context.WithTimeout(record, 30*time.Second)
		err := retryBriefly(finishCtx, 3, time.Second, func(ctx context.Context) error {
			return e.journal.StepFinished(ctx, c.RunID, step.ID, stepStatus, exitCode)
		})
		stopFinish()
		if err != nil {
			o := e.internalFailure("Could not record the step's execution receipt", err)
			o.RecoveryRequired = true
			return o
		}
		// Keep the exited container until its receipt has committed. Failed removal
		// leaves a stopped container for periodic cleanup and is safe for the workspace.
		if err := e.docker.stopContainer("gitman-" + c.RunID + "-" + strconv.Itoa(step.Index)); err != nil {
			o := e.internalFailure("Could not confirm container cleanup", err)
			o.RecoveryRequired = true
			return o
		}

		if err := e.journal.ContainerRemoved(record, c.RunID, "gitman-"+c.RunID+"-"+strconv.Itoa(step.Index)); err != nil {
			o := e.internalFailure("Could not record confirmed cleanup", err)
			o.RecoveryRequired = true
			return o
		}
		if stepOutcome != nil {
			outcome = *stepOutcome
			break
		}
	}

	summary, err := e.ws.readSummary(masker)
	if err != nil {
		e.log.Warn("could not read a run's summary", "run", c.RunID, "error", err)
	}
	outcome.Summary = summary
	return outcome
}

// interrupted reports how the run ends when runCtx has ended — cancelled,
// timed out, or the worker shutting down — with during saying what the
// run was doing, and ok false while it is still going.
func interrupted(runCtx, workerCtx context.Context, timeout time.Duration, during string) (ci.Outcome, bool) {
	if runCtx.Err() == nil {
		return ci.Outcome{}, false
	}
	var stop stopped
	switch cause := context.Cause(runCtx); {
	case errors.As(cause, &stop):
		return ci.Outcome{Status: ci.StatusCancelled}, true
	case errors.Is(cause, errTimedOut):
		return failed("The run passed its %s timeout %s.", timeout, during), true
	case errors.Is(cause, errDiskBudget):
		return failed("The run exceeded its configured workspace budget or the host's disk reserve."), true
	case workerCtx.Err() != nil:
		return failed("The worker was shut down %s.", during), true
	}
	return failed("The run was stopped %s.", during), true
}

// runStep runs one step and reports its status. A non-nil outcome ends
// the run with it.
func (e *execution) runStep(runCtx, workerCtx, record context.Context, step ci.ClaimedStep, cfg *ci.Config,
	env map[string]string, masker *secretMasker, timeout time.Duration) (*ci.Outcome, ci.StepStatus, *int) {
	c := e.claim
	logs := newLogWriter(record, func(ctx context.Context, sequence int, content string) error {
		return e.journal.AppendLog(ctx, c.RunID, step.ID, sequence, content)
	}, masker)
	defer func() {
		if err := logs.Close(); err != nil {
			e.log.Warn("could not store a step's output", "run", c.RunID, "step", step.Name, "error", err)
		}
	}()

	name := "gitman-" + c.RunID + "-" + strconv.Itoa(step.Index)
	if err := e.journal.PlanContainer(runCtx, c.RunID, step.ID, name); err != nil {
		if errors.Is(err, ci.ErrRunEnded) {
			o := ci.Outcome{Status: ci.StatusCancelled}
			return &o, ci.StepCancelled, nil
		}
		o := e.internalFailure("Could not record container recovery state", err)
		return &o, ci.StepFailed, nil
	}

	if step.Type == ci.StepDeploy {
		waiting := false
		for {
			err := e.journal.BeginDeployment(runCtx, c.RunID, step.ID)
			if err == nil {
				break
			}
			if errors.Is(err, ci.ErrDeploymentSuperseded) {
				o := ci.Outcome{Status: ci.StatusCancelled, Reason: "A newer run already deployed this target."}
				return &o, ci.StepSkipped, nil
			}
			if errors.Is(err, ci.ErrRunEnded) {
				o := ci.Outcome{Status: ci.StatusCancelled}
				return &o, ci.StepCancelled, nil
			}
			if !errors.Is(err, ci.ErrDeploymentBusy) {
				o := e.internalFailure("Could not acquire deployment ownership", err)
				return &o, ci.StepFailed, nil
			}
			if !waiting {
				logs.note("waiting for exclusive deployment ownership of " + c.Target)
				waiting = true
			}
			select {
			case <-runCtx.Done():
				o, _ := interrupted(runCtx, workerCtx, timeout, "while waiting for deployment ownership")
				return &o, ci.StepFailed, nil
			case <-time.After(time.Second):
			}
		}
	}
	code, err := e.docker.Run(runCtx, containerSpec{
		Name: name,
		Created: func(ctx context.Context, containerID string) error {
			return e.journal.ContainerCreated(ctx, c.RunID, step.ID, containerID)
		},
		RunID:        c.RunID,
		Image:        cfg.Image,
		Script:       cfg.Steps[step.Index].Run,
		Source:       e.ws.source(),
		Meta:         e.ws.meta(),
		Env:          env,
		Secrets:      e.secrets,
		DockerSocket: cfg.Docker,
	}, logs)

	var uncertain *TerminationUnknown
	if errors.As(err, &uncertain) {
		o := e.internalFailure("Container termination is unconfirmed; workspace retained for recovery", err)
		o.RecoveryRequired = true
		return &o, ci.StepRunning, nil
	}

	var stop stopped
	switch cause := context.Cause(runCtx); {
	case err != nil && errors.As(cause, &stop):
		logs.note("stopped: " + stop.reason)
		return &ci.Outcome{Status: ci.StatusCancelled}, ci.StepCancelled, nil
	case err != nil && errors.Is(cause, errTimedOut):
		logs.note(fmt.Sprintf("stopped: the run passed its %s timeout", timeout))
		o := failed("The run passed its %s timeout during step %q.", timeout, step.Name)
		return &o, ci.StepFailed, nil
	case err != nil && errors.Is(cause, errDiskBudget):
		logs.note("stopped: workspace budget or disk reserve exceeded")
		o := failed("The run exceeded its configured workspace budget or the host's disk reserve.")
		return &o, ci.StepFailed, nil
	case err != nil && workerCtx.Err() != nil:
		logs.note("stopped: the worker was shut down")
		o := failed("The worker was shut down during step %q.", step.Name)
		return &o, ci.StepFailed, nil
	case err != nil:
		o := e.internalFailure(fmt.Sprintf("Step %q could not be started", step.Name), err)
		return &o, ci.StepFailed, nil
	case code != 0:
		o := failed("Step %q exited with code %d.", step.Name, code)
		return &o, ci.StepFailed, &code
	}
	return nil, ci.StepPassed, &code
}

// watchCancellation stops the run when someone cancels it, or when it
// has ended or been deleted without this worker, checking until the run
// ends.
func (e *execution) watchCancellation(runCtx context.Context, stop context.CancelCauseFunc) {
	ticker := time.NewTicker(e.cancelPoll)
	defer ticker.Stop()
	for {
		select {
		case <-runCtx.Done():
			return
		case <-ticker.C:
			reason, err := e.journal.StopReason(runCtx, e.claim.RunID)
			if err != nil {
				if runCtx.Err() == nil {
					e.log.Warn("could not check whether the run should stop", "run", e.claim.RunID, "error", err)
				}
				continue
			}
			if reason != "" {
				stop(stopped{reason: reason})
				return
			}
		}
	}
}
