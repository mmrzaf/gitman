package ci

import (
	"time"

	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/repo"
)

// PostgreSQL LISTEN/NOTIFY channels. Both carry a run ID: NotifyChannel
// whenever a run or one of its steps changes state, LogChannel whenever
// a step of the run writes more output.
const (
	NotifyChannel = "gitman_runs"
	LogChannel    = "gitman_logs"
)

// Status is a run's state.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusPassed    Status = "passed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// StepStatus is a step's state.
type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepRunning   StepStatus = "running"
	StepPassed    StepStatus = "passed"
	StepFailed    StepStatus = "failed"
	StepSkipped   StepStatus = "skipped"
	StepCancelled StepStatus = "cancelled"
)

// Trigger is what started a run.
type Trigger string

const (
	TriggerPush   Trigger = "push"
	TriggerManual Trigger = "manual"
)

// CreateParams describes a run to create.
type CreateParams struct {
	RepoID  string
	Commit  string
	RefKind git.Kind
	RefName string
	Trigger Trigger
	// PersonID is who pushed or who started the run by hand.
	PersonID string
	// PushID links a push-triggered run to the push that caused it.
	PushID string
	// Pipeline is the content of the pipeline file at Commit, and
	// PipelineProblem, when not empty, is why it could not be read; both
	// come from LoadPipeline.
	Pipeline        []byte
	PipelineProblem string
	// Decision is what the repository's ref rules allow for the run's
	// ref.
	Decision repo.Decision
}

// Created summarizes a newly created run.
type Created struct {
	ID      string
	Number  int64
	Status  Status
	Reason  string
	Target  string
	Version string
}

// Summary is a run as shown in a list: enough to identify it and show
// its outcome, without its steps.
type Summary struct {
	Deployed bool
	ID       string
	RepoName string
	Number   int64
	RefKind  git.Kind
	RefName  string
	// Commit is the commit the run is of.
	Commit  string
	Trigger Trigger
	Status  Status
	Reason  string
	// Target is the context resolved for the ref. A successful explicit
	// deploy step, rather than run completion, records a deployment.
	Target string
	// Actor is who triggered the run: who pushed, or who started it by
	// hand.
	Actor      string
	QueuedAt   time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// Ran reports whether the run started and has finished, so Took is how
// long it ran.
func (s Summary) Ran() bool { return s.StartedAt != nil && s.FinishedAt != nil }

// Took is how long a run that Ran ran.
func (s Summary) Took() time.Duration {
	if !s.Ran() {
		return 0
	}
	return s.FinishedAt.Sub(*s.StartedAt)
}

// FetchUsername is the HTTP Basic auth username a worker fetches a run's
// commit with, its password being the run's fetch token. It is a
// reserved name, so no person can have it.
const FetchUsername = "gitman-run"

// Claim is a run a worker has taken, with everything it needs to run it.
type Claim struct {
	// Deadline is persisted in the claim transaction, before any preparation.
	Deadline time.Time
	RunID    string
	RepoID   string
	RepoName string
	Number   int64
	Commit   string
	RefKind  git.Kind
	RefName  string
	Target   string
	Version  string
	// AllowSecrets is the ref rule's decision, as it was when the run was
	// created.
	AllowSecrets bool
	// Pipeline is the pipeline file the run was created from, whose
	// Docker use, target and steps its ref's rules were checked against.
	// A worker runs exactly this, never the file in its own checkout.
	Pipeline []byte
	// FetchToken is the plain token the worker fetches the commit with.
	// Only its hash is stored; it is valid while the run is running.
	FetchToken string
	Steps      []ClaimedStep
}

// ClaimedStep is one step of a claimed run. A step skipped at creation
// (its "when" did not match) stays skipped.
type ClaimedStep struct {
	Type    StepKind
	ID      string
	Index   int
	Name    string
	Skipped bool
}

// Outcome is how a run ended.
type Outcome struct {
	// RecoveryRequired leaves the run running until containers and receipts are reconciled.
	RecoveryRequired bool
	Status           Status
	Reason           string
	// Summary holds the key=value lines the run wrote to $GITMAN_SUMMARY.
	Summary map[string]string
}

// RunDetail is everything the Run page shows about one run.
type RunDetail struct {
	Summary
	RepoID       string
	Version      string
	AllowSecrets bool
	// CancelRequested is set while a running run is being stopped.
	CancelRequested bool
	Steps           []StepDetail
	// Results holds the key=value lines the run wrote to
	// $GITMAN_SUMMARY, in key order.
	Results []SummaryEntry
}

// Finished reports whether the run has ended.
func (r *RunDetail) Finished() bool {
	return r.Status != StatusQueued && r.Status != StatusRunning
}

// StepDetail is one step of a run, as shown on the Run page.
type StepDetail struct {
	Type              StepKind
	ID                string
	Index             int
	Name              string
	Status            StepStatus
	ExitCode          *int
	StartedAt         *time.Time
	FinishedAt        *time.Time
	LogRecordingError string
	LogsExpiredAt     *time.Time
	LogBytes          int64
}

// Duration is how long the step ran, or has been running as of now; zero
// if it never started.
func (s StepDetail) Duration(now time.Time) time.Duration {
	if s.StartedAt == nil {
		return 0
	}
	end := now
	if s.FinishedAt != nil {
		end = *s.FinishedAt
	}
	return end.Sub(*s.StartedAt).Round(time.Second)
}

// SummaryEntry is one key=value line of a run's summary.
type SummaryEntry struct {
	Key   string
	Value string
}

// LogChunk is one stored piece of a step's output.
type LogChunk struct {
	Sequence int
	Content  string
}

// LogTail is a bounded display window with original line numbering.
type LogTail struct {
	Text  string
	After int
	First int
	Cut   bool
}
