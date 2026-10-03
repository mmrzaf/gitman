package ci

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mmrzaf/gitman/internal/git"
)

// maxReasonLen caps a run's recorded reason, which can include a whole
// list of pipeline validation errors.
const maxReasonLen = 4000

// plan is what a run's pipeline resolves to before it is written down.
type plan struct {
	timeout time.Duration
	status  Status
	reason  string
	target  string
	version string
	steps   []plannedStep
}

type plannedStep struct {
	kind StepKind
	name string
	run  bool
}

func refLabel(kind git.Kind, name string) string {
	if name == "" {
		return "this commit"
	}
	return fmt.Sprintf("%s %s", kind, name)
}

// planRun decides, from a run's pipeline and the ref rule's decision,
// what the run's status, target and steps should be — everything
// decidable at creation time, decided then, so a run that cannot
// proceed fails immediately with a reason that says why.
func planRun(p CreateParams) plan {
	rc := RunContext{Commit: p.Commit, RefKind: p.RefKind, RefName: p.RefName}
	pl := plan{version: rc.Version()}

	if p.PipelineProblem != "" {
		pl.status = StatusFailed
		pl.reason = p.PipelineProblem
		return pl
	}
	cfg, err := Parse(p.Pipeline)
	if err != nil {
		pl.status = StatusFailed
		pl.reason = err.Error()
		return pl
	}

	pl.timeout = cfg.Timeout
	if target, ok := cfg.ResolveTarget(p.RefKind, p.RefName); ok {
		pl.target = target
	}
	label := refLabel(p.RefKind, p.RefName)
	if cfg.Docker && !p.Decision.AllowDocker {
		pl.status = StatusFailed
		pl.reason = fmt.Sprintf("The pipeline uses Docker, but no rule allows Docker for %s.", label)
		return pl
	}
	deploys := false
	for _, step := range cfg.Steps {
		deploys = deploys || step.Type == StepDeploy && step.When.ShouldRun(p.RefKind, pl.target)
	}
	if deploys && !p.Decision.AllowDeploy {
		pl.status = StatusFailed
		pl.reason = fmt.Sprintf("%s resolves to target %q, but no rule allows deployment from it.", capitalize(label), pl.target)
		return pl
	}

	anyRuns := false
	for _, step := range cfg.Steps {
		runs := step.When.ShouldRun(p.RefKind, pl.target)
		anyRuns = anyRuns || runs
		pl.steps = append(pl.steps, plannedStep{name: step.Name, kind: step.Type, run: runs})
	}
	if !anyRuns {
		pl.status = StatusPassed
		pl.reason = fmt.Sprintf("No step applies to %s.", label)
		return pl
	}
	pl.status = StatusQueued
	return pl
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func truncateReason(s string) string {
	if utf8.RuneCountInString(s) <= maxReasonLen {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxReasonLen-1]) + "…"
}
