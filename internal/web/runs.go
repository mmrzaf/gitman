package web

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

// maxRenderedLog is how much of a step's output the Run page shows. A
// longer log shows its end — where a failure is — with a link to the
// whole thing.
const maxRenderedLog = 1 << 20

type runPage struct {
	repoFrame
	Run      *ci.RunDetail
	Selected *ci.StepDetail
	Log      []logLine
	// LogAfter is the sequence number of the last log chunk shown, for
	// the live stream to continue from; -1 when none was.
	LogAfter int
	// LogCut reports that the start of the log was left out.
	LogCut bool
	// LogOpen reports that the log's last line has no newline yet: output
	// still being written continues it.
	LogOpen  bool
	Duration time.Duration
	Now      time.Time
}

// logLine is one numbered line of a step's output. Numbers count from
// the start of the whole log, so a line keeps its number, and its link,
// when the start of a long log is left out of the page.
type logLine struct {
	Number int
	Text   string
	Error  bool
}

// errorLinePattern picks out lines that look like a failure — the ones
// worth finding first in a long log. The Run page hands it to its script,
// which marks lines streamed in later the same way.
const errorLinePattern = `(?i)(\berror\b|\bfail(ed|ure)?\b|\bpanic\b|\bfatal\b)`

var errorLine = regexp.MustCompile(errorLinePattern)

// ErrorPattern is errorLinePattern for the page's script: JavaScript
// spells case-insensitivity as a flag, not an inline group.
func (runPage) ErrorPattern() string { return strings.TrimPrefix(errorLinePattern, "(?i)") }

// splitLog numbers text's lines from first. A final line without a
// newline is still a line: output still being written.
func splitLog(text string, first int) []logLine {
	if text == "" {
		return nil
	}
	parts := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	lines := make([]logLine, len(parts))
	for i, part := range parts {
		lines[i] = logLine{Number: first + i, Text: part, Error: errorLine.MatchString(part)}
	}
	return lines
}

// LiveEvents is the event stream a Run page listens to while anything on
// it can still change.
func (p runPage) LiveEvents() string {
	if p.Run.Finished() {
		return ""
	}
	return "/events?run=" + p.Run.ID
}

// runsPageSize is how many runs a repository's Runs page shows at once.
const runsPageSize = 30

type runsPage struct {
	repoFrame
	Runs []ci.Summary
	// Before is the ?before= value that produced this page, for the
	// "Older" link to keep going from.
	Before int64
	// More is the run number the "Older" link continues from, or 0 when
	// this is the last page.
	More int64
}

// LiveEvents keeps the Runs page's in-progress rows current.
func (runsPage) LiveEvents() string { return "/events" }

func (a *App) runs(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoByName(r)
	if err != nil {
		return err
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	runs, err := a.ci.RunsForRepo(r.Context(), repo.ID, before, runsPageSize)
	if err != nil {
		return err
	}
	page := runsPage{repoFrame: repoFrame{Repo: repo, Section: "runs"}, Runs: runs, Before: before}
	if len(runs) == runsPageSize {
		page.More = runs[len(runs)-1].Number
	}
	a.render(w, r, http.StatusOK, "runs", repo.Name+" runs", page)
	return nil
}

// runByNumber resolves the {repo} and {n} path values.
func (a *App) runByNumber(r *http.Request) (*reposvc.Repo, *ci.RunDetail, error) {
	repo, err := a.repoByName(r)
	if err != nil {
		return nil, nil, err
	}
	number, err := strconv.ParseInt(r.PathValue("n"), 10, 64)
	if err != nil || number < 1 {
		return nil, nil, notFound("There is no run %q.", r.PathValue("n"))
	}
	run, err := a.ci.Run(r.Context(), repo.ID, number)
	if errors.Is(err, postgres.ErrNotFound) {
		return nil, nil, notFound("%s has no run #%d.", repo.Name, number)
	}
	return repo, run, err
}

// selectedStep is the step whose log a Run page shows: the one asked for
// with ?step=, else the one running, else the one that failed, else the
// last one that ran.
func selectedStep(r *http.Request, run *ci.RunDetail) *ci.StepDetail {
	if i, err := strconv.Atoi(r.URL.Query().Get("step")); err == nil && i >= 0 && i < len(run.Steps) {
		return &run.Steps[i]
	}
	for _, status := range []ci.StepStatus{ci.StepRunning, ci.StepFailed, ci.StepCancelled} {
		for i := range run.Steps {
			if run.Steps[i].Status == status {
				return &run.Steps[i]
			}
		}
	}
	for i := len(run.Steps) - 1; i >= 0; i-- {
		if run.Steps[i].StartedAt != nil {
			return &run.Steps[i]
		}
	}
	return nil
}

// ansiEscape matches terminal color and cursor sequences, which build
// tools print and a web page cannot show.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

func (a *App) runView(w http.ResponseWriter, r *http.Request) error {
	repo, run, err := a.runByNumber(r)
	if err != nil {
		return err
	}
	now := a.now()
	page := runPage{repoFrame: repoFrame{Repo: repo}, Run: run, Selected: selectedStep(r, run), LogAfter: -1, Now: now}
	if run.StartedAt != nil {
		end := now
		if run.FinishedAt != nil {
			end = *run.FinishedAt
		}
		page.Duration = end.Sub(*run.StartedAt).Round(time.Second)
	}
	if page.Selected != nil {
		var log strings.Builder
		for {
			chunks, err := a.ci.LogChunks(r.Context(), page.Selected.ID, page.LogAfter, 256)
			if err != nil {
				return err
			}
			for _, c := range chunks {
				log.WriteString(c.Content)
				page.LogAfter = c.Sequence
			}
			if len(chunks) < 256 {
				break
			}
		}
		text, first := log.String(), 1
		if len(text) > maxRenderedLog {
			cut := len(text) - maxRenderedLog
			if i := strings.IndexByte(text[cut:], '\n'); i >= 0 {
				cut += i + 1
			}
			first += strings.Count(text[:cut], "\n")
			text, page.LogCut = text[cut:], true
		}
		text = ansiEscape.ReplaceAllString(text, "")
		page.Log, page.LogOpen = splitLog(text, first), text != "" && !strings.HasSuffix(text, "\n")
	}
	a.render(w, r, http.StatusOK, "run", fmt.Sprintf("#%d · %s", run.Number, repo.Name), page)
	return nil
}

// runLog serves one step's whole output as plain text, exactly as
// stored.
func (a *App) runLog(w http.ResponseWriter, r *http.Request) error {
	repo, run, err := a.runByNumber(r)
	if err != nil {
		return err
	}
	i, err := strconv.Atoi(r.URL.Query().Get("step"))
	if err != nil || i < 0 || i >= len(run.Steps) {
		return notFound("Run #%d has no such step.", run.Number)
	}
	step := run.Steps[i]
	setRawHeaders(w.Header(), "text/plain; charset=utf-8", fmt.Sprintf("%s-%d-%s.log", repo.Name, run.Number, safeFileName(step.Name)))
	after := -1
	for {
		chunks, err := a.ci.LogChunks(r.Context(), step.ID, after, 256)
		if err != nil {
			return err
		}
		for _, c := range chunks {
			if _, err := w.Write([]byte(c.Content)); err != nil {
				return nil
			}
			after = c.Sequence
		}
		if len(chunks) < 256 {
			return nil
		}
	}
}

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeFileName(s string) string {
	return strings.Trim(unsafeFileChars.ReplaceAllString(s, "-"), "-")
}

func runPath(repoName string, number int64) string {
	return fmt.Sprintf("/%s/runs/%d", repoName, number)
}

func (a *App) runCancel(w http.ResponseWriter, r *http.Request) error {
	repo, run, err := a.runByNumber(r)
	if err != nil {
		return err
	}
	if _, err := a.mayRun(r, repo, run.RefKind, run.RefName, "cancel its runs"); err != nil {
		return err
	}
	err = a.ci.Cancel(r.Context(), repo.ID, run.Number, personFrom(r).Username)
	switch {
	case errors.Is(err, ci.ErrRunFinished):
		a.redirect(w, r, runPath(repo.Name, run.Number), flashInfo, "The run had already finished.")
		return nil
	case err != nil:
		return err
	}
	message := "Cancelled."
	if run.Status == ci.StatusRunning {
		message = "Stopping the run. Its worker ends it within seconds."
	}
	a.redirect(w, r, runPath(repo.Name, run.Number), flashSuccess, message)
	return nil
}

// runAgain starts a new run of the same commit and ref.
func (a *App) runAgain(w http.ResponseWriter, r *http.Request) error {
	repo, run, err := a.runByNumber(r)
	if err != nil {
		return err
	}
	return a.startRun(w, r, repo, run.Commit, run.RefKind, run.RefName)
}

// commitRun starts a run of a bare commit, with no ref: it resolves no
// target, and gets no Docker or secrets, which only rules grant.
func (a *App) commitRun(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoByName(r)
	if err != nil {
		return err
	}
	gitRepo, err := a.repos.Open(repo)
	if err != nil {
		return err
	}
	commit, err := a.commitNamed(r, gitRepo, repo)
	if err != nil {
		return err
	}
	return a.startRun(w, r, repo, commit, "", "")
}

// mayRun checks that the signed-in person may start or cancel runs of a
// ref, which takes the same permission as pushing to it, and returns what
// the ref's rules allow them. A run of a bare commit has no ref: any
// member may start or cancel one. action completes the refusal, as in
// "so you may not <action>".
func (a *App) mayRun(r *http.Request, repo *reposvc.Repo, kind git.Kind, name, action string) (reposvc.Decision, error) {
	if name == "" {
		return reposvc.Decision{}, nil
	}
	person := personFrom(r)
	rules, err := a.repos.ListRules(r.Context(), repo.ID)
	if err != nil {
		return reposvc.Decision{}, err
	}
	decision := reposvc.Evaluate(rules, kind, name, person.ID, person.IsAdmin, repo.DefaultPushPolicy, repo.DefaultPushPeople)
	if !decision.CanPush {
		return decision, apperr.New(apperr.KindForbidden, fmt.Sprintf("You may not push to %s %s, so you may not %s.", kind, name, action))
	}
	return decision, nil
}

// startRun starts a run by hand. Running a ref's pipeline needs the same
// permission as pushing to that ref.
func (a *App) startRun(w http.ResponseWriter, r *http.Request, repo *reposvc.Repo, commit string, kind git.Kind, name string) error {
	person := personFrom(r)
	decision, err := a.mayRun(r, repo, kind, name, "run it")
	if err != nil {
		return err
	}
	gitRepo, err := a.repos.Open(repo)
	if err != nil {
		return err
	}
	run, err := a.ci.Start(r.Context(), ci.StartParams{
		RepoID: repo.ID, Git: gitRepo, Commit: commit, RefKind: kind, RefName: name,
		PersonID: person.ID, Decision: decision,
	})
	if err != nil {
		return err
	}
	kindOfFlash, message := flashSuccess, fmt.Sprintf("Started run #%d.", run.Number)
	if run.Status != ci.StatusQueued {
		kindOfFlash, message = flashInfo, fmt.Sprintf("Run #%d %s at once: %s", run.Number, run.Status, run.Reason)
	}
	a.redirect(w, r, runPath(repo.Name, run.Number), kindOfFlash, message)
	return nil
}
