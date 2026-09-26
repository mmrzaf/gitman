package push

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/id"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/repo"
)

// maxUpdates caps how many ref updates one push may carry.
const maxUpdates = 1000

// commitCountCap is where counting a push's commits stops; the UI shows
// the cap as "1000+".
const commitCountCap = 1000

// ErrRejected is returned by PreReceive when at least one update was
// refused; the reasons have already been written to the hook's output.
var ErrRejected = errors.New("push rejected")

// Update is one line of a hook's input: a ref moving from Old to New.
type Update struct {
	Old, New string
	Ref      string
	// Kind and Name are set when Ref is a branch or a tag.
	Kind git.Kind
	Name string
}

// IsCreate reports whether the update creates the ref.
func (u Update) IsCreate() bool { return git.IsZeroHash(u.Old) }

// IsDelete reports whether the update deletes the ref.
func (u Update) IsDelete() bool { return git.IsZeroHash(u.New) }

// ParseUpdates reads the "<old> <new> <ref>" lines Git gives pre-receive
// and post-receive on standard input.
func ParseUpdates(r io.Reader) ([]Update, error) {
	var updates []Update
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if len(updates) == maxUpdates {
			return nil, fmt.Errorf("a push may update at most %d refs", maxUpdates)
		}
		fields := strings.SplitN(line, " ", 3)
		if len(fields) != 3 || !git.IsHash(fields[0]) || !git.IsHash(fields[1]) || fields[2] == "" {
			return nil, fmt.Errorf("malformed ref update %q", line)
		}
		u := Update{Old: fields[0], New: fields[1], Ref: fields[2]}
		u.Kind, u.Name, _ = git.SplitFullName(u.Ref)
		updates = append(updates, u)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read ref updates: %w", err)
	}
	return updates, nil
}

// Hook serves one push's hooks.
type Hook struct {
	// DB runs the post-receive transaction, which spans the repo and ci
	// services so a push's record, ref index and runs commit together.
	DB     *postgres.DB
	People *auth.Service
	Repos  *repo.Service
	CI     *ci.Service
	// Git is the repository the push is going into, opened with the
	// environment Git gave the hook.
	Git *git.Repo
	Ctx Context
	// PublicURL is the base for run links printed to the pusher.
	PublicURL string
	// Out receives messages for the pusher; Git shows them prefixed
	// with "remote:".
	Out io.Writer
}

func (h *Hook) say(format string, args ...any) {
	fmt.Fprintf(h.Out, format+"\n", args...)
}

// pushContext is what both hooks load before looking at the updates.
type pushContext struct {
	repo   *repo.Repo
	person *auth.Person
	rules  []repo.Rule
}

func (h *Hook) load(ctx context.Context) (*pushContext, error) {
	repoRecord, err := h.Repos.GetByID(ctx, h.Ctx.RepoID)
	if err != nil {
		return nil, fmt.Errorf("load repository: %w", err)
	}
	person, err := h.People.GetByID(ctx, h.Ctx.PersonID)
	if err != nil {
		return nil, fmt.Errorf("load person: %w", err)
	}
	rules, err := h.Repos.ListRules(ctx, repoRecord.ID)
	if err != nil {
		return nil, err
	}
	return &pushContext{repo: repoRecord, person: person, rules: rules}, nil
}

func (pc *pushContext) decide(u Update) repo.Decision {
	return repo.Evaluate(pc.rules, u.Kind, u.Name, pc.person.ID, pc.person.IsAdmin, pc.repo.DefaultPushPolicy, pc.repo.DefaultPushPeople)
}

// ruleLabel names the rule behind a decision in a rejection message.
func ruleLabel(d repo.Decision) string {
	if d.MatchedRule == nil {
		return "no rule"
	}
	return fmt.Sprintf("the rule for %s %q", d.MatchedRule.Kind, d.MatchedRule.Pattern)
}

// PreReceive checks every update against Gitman's ref naming rules and
// the repository's ref rules. It refuses the whole push if any update is
// refused — Git applies a push atomically when pre-receive fails — and
// explains every refusal, not just the first, so one retry fixes them
// all.
func (h *Hook) PreReceive(ctx context.Context, updates []Update) error {
	pc, err := h.load(ctx)
	if err != nil {
		return err
	}
	if pc.person.Disabled() {
		h.say("Gitman: %s is disabled and cannot push.", pc.person.Username)
		return ErrRejected
	}
	readable, err := h.Repos.CanRead(ctx, pc.repo, pc.person.ID, pc.person.IsAdmin)
	if err != nil {
		return err
	}
	if !readable {
		h.say("Gitman: %s cannot push to a repository they cannot read.", pc.person.Username)
		return ErrRejected
	}

	existing, err := h.Git.Refs(ctx)
	if err != nil {
		return fmt.Errorf("list refs: %w", err)
	}
	type key struct {
		kind git.Kind
		name string
	}
	present := make(map[key]bool, len(existing))
	for _, r := range existing {
		present[key{r.Kind, r.Name}] = true
	}
	for _, u := range updates {
		if u.Kind == "" {
			continue
		}
		if u.IsDelete() {
			delete(present, key{u.Kind, u.Name})
		} else {
			present[key{u.Kind, u.Name}] = true
		}
	}

	var rejections []string
	reject := func(u Update, format string, args ...any) {
		rejections = append(rejections, fmt.Sprintf("%s: %s", u.Ref, fmt.Sprintf(format, args...)))
	}

	for _, u := range updates {
		if u.Kind == "" {
			reject(u, "only branches (refs/heads/) and tags (refs/tags/) can be pushed")
			continue
		}
		if u.IsCreate() && !u.IsDelete() {
			if err := git.ValidateName(u.Name); err != nil {
				reject(u, "%v", err)
				continue
			}
			if git.LooksLikeCommitHash(u.Name) {
				reject(u, "the name looks like a commit hash, which would make links to it ambiguous")
				continue
			}
			if other := u.Kind.Other(); present[key{other, u.Name}] {
				reject(u, "a %s named %q already exists; a branch and a tag cannot share a name", other, u.Name)
				continue
			}
		}

		d := pc.decide(u)
		if !d.CanPush {
			reject(u, "%s does not allow %s to push here", ruleLabel(d), pc.person.Username)
			continue
		}

		switch {
		case u.IsDelete():
			if u.Kind == git.KindBranch && u.Name == pc.repo.DefaultBranch {
				reject(u, "the default branch cannot be deleted")
				continue
			}
			if !d.AllowDelete {
				reject(u, "%s does not allow deleting it", ruleLabel(d))
				continue
			}

		case u.Kind == git.KindBranch:
			typ, err := h.Git.ObjectType(ctx, u.New)
			if err != nil {
				return fmt.Errorf("inspect %s: %w", u.New, err)
			}
			if typ != git.TypeCommit {
				reject(u, "a branch must point at a commit, not a %s", typ)
				continue
			}
			if !u.IsCreate() {
				ff, err := h.Git.IsAncestor(ctx, u.Old, u.New)
				if err != nil {
					return fmt.Errorf("check fast-forward for %s: %w", u.Ref, err)
				}
				if !ff && !d.AllowForce {
					reject(u, "this rewrites history (a force-push), which %s does not allow", ruleLabel(d))
					continue
				}
			}

		case u.Kind == git.KindTag:
			if _, err := h.Git.ResolveCommit(ctx, u.New); err != nil {
				if errors.Is(err, git.ErrNotFound) {
					reject(u, "a tag must point at a commit")
					continue
				}
				return fmt.Errorf("inspect %s: %w", u.New, err)
			}
			if !u.IsCreate() && !d.AllowForce {
				reject(u, "moving an existing tag is a force-push, which %s does not allow", ruleLabel(d))
				continue
			}
		}
	}

	if len(rejections) == 0 {
		return nil
	}
	h.say("Gitman refused this push:")
	for _, r := range rejections {
		h.say("  %s", r)
	}
	return ErrRejected
}

// updateRecord is what post-receive learns about one update before
// writing anything.
type updateRecord struct {
	Update
	commits  int
	capped   bool
	decision repo.Decision
	commit   string // the commit the ref now resolves to
	pipeline []byte
	problem  string
}

// PostReceive records the push, brings the ref index up to date, and
// creates a run for every updated ref whose rule runs pipelines on push.
// Git has already accepted the push when this runs; a failure here is
// reported to the pusher but cannot undo the push.
func (h *Hook) PostReceive(ctx context.Context, updates []Update) error {
	pc, err := h.load(ctx)
	if err != nil {
		return err
	}

	// Everything that reads Git happens before the transaction, which
	// then holds its locks only for the writes.
	records := make([]updateRecord, 0, len(updates))
	for _, u := range updates {
		if u.Kind == "" {
			continue
		}
		rec := updateRecord{Update: u, decision: pc.decide(u)}
		switch {
		case u.IsDelete():
		case u.IsCreate():
			rec.commits, rec.capped, err = h.Git.CountNewCommits(ctx, u.New, u.Kind, u.Name, commitCountCap)
		default:
			rec.commits, rec.capped, err = h.Git.CountCommits(ctx, u.New, []string{u.Old}, commitCountCap)
		}
		if err != nil {
			return fmt.Errorf("count commits for %s: %w", u.Ref, err)
		}
		if !u.IsDelete() {
			if rec.commit, err = h.Git.ResolveCommit(ctx, u.New); err != nil {
				return fmt.Errorf("resolve %s: %w", u.Ref, err)
			}
			if rec.decision.RunOnPush {
				rec.pipeline, rec.problem, err = ci.LoadPipeline(ctx, h.Git, rec.commit)
				if err != nil {
					return err
				}
			}
		}
		records = append(records, rec)
	}
	var created []*ci.Created
	var createdFor, moved []updateRecord
	err = h.DB.Tx(ctx, func(tx postgres.Tx) error {
		// The ref index lock comes first, before any row that refers to
		// the repository: deleting a repository takes the same lock
		// before its row, so the two never wait on each other.
		refs, err := h.Repos.SyncRefsTx(ctx, tx, pc.repo.ID, h.Git, pc.person.ID)
		if err != nil {
			return err
		}
		pushID := id.New()
		if err := insertPush(ctx, tx, pushID, pc.repo.ID, pc.person.ID, h.Ctx.RemoteAddr); err != nil {
			return err
		}
		for _, rec := range records {
			if err := insertPushUpdate(ctx, tx, pushID, rec.Kind, rec.Name, rec.Old, rec.New,
				rec.IsCreate(), rec.IsDelete(), rec.commits, rec.capped); err != nil {
				return err
			}
		}
		current := make(map[string]string, len(refs))
		for _, r := range refs {
			current[string(r.Kind)+"/"+r.Name] = r.Commit
		}
		for _, rec := range records {
			now, exists := current[string(rec.Kind)+"/"+rec.Name]
			if rec.IsDelete() {
				if exists {
					continue
				}
				if err := h.CI.CancelQueuedTx(ctx, tx, pc.repo.ID, rec.Kind, rec.Name,
					fmt.Sprintf("The %s %s was deleted.", rec.Kind, rec.Name)); err != nil {
					return err
				}
				continue
			}
			if !rec.decision.RunOnPush {
				continue
			}
			// Hooks of pushes that land back to back can finish in
			// either order. A ref a later push has already moved on is
			// that push's to run: a run of this commit would be queued
			// after, and supersede, the newer one.
			if now != rec.commit {
				moved = append(moved, rec)
				continue
			}
			run, err := h.CI.CreateTx(ctx, tx, ci.CreateParams{
				RepoID:          pc.repo.ID,
				Commit:          rec.commit,
				RefKind:         rec.Kind,
				RefName:         rec.Name,
				Trigger:         ci.TriggerPush,
				PersonID:        pc.person.ID,
				PushID:          pushID,
				Pipeline:        rec.pipeline,
				PipelineProblem: rec.problem,
				Decision:        rec.decision,
			})
			if err != nil {
				return fmt.Errorf("create run for %s: %w", rec.Ref, err)
			}
			created = append(created, run)
			createdFor = append(createdFor, rec)
		}
		return activity.Changed(ctx, tx, pc.repo.ID)
	})
	if err != nil {
		return err
	}

	for i, run := range created {
		h.reportRun(pc.repo.Name, createdFor[i], run)
	}
	for _, rec := range moved {
		h.say("Gitman: %s %s moved again before this push was recorded; the later push runs its pipeline.", rec.Kind, rec.Name)
	}
	return nil
}

func (h *Hook) reportRun(repoName string, rec updateRecord, run *ci.Created) {
	link := fmt.Sprintf("%s/%s/runs/%d", h.PublicURL, repoName, run.Number)
	label := fmt.Sprintf("%s %s", rec.Kind, rec.Name)
	switch run.Status {
	case ci.StatusQueued:
		if run.Target != "" {
			h.say("Gitman: run #%d queued for %s, shipping to %s", run.Number, label, run.Target)
		} else {
			h.say("Gitman: run #%d queued for %s", run.Number, label)
		}
	case ci.StatusPassed:
		h.say("Gitman: run #%d for %s had nothing to do: %s", run.Number, label, run.Reason)
	default:
		h.say("Gitman: run #%d for %s failed before starting:", run.Number, label)
		for _, line := range strings.Split(run.Reason, "\n") {
			h.say("  %s", line)
		}
	}
	h.say("  %s", link)
}
