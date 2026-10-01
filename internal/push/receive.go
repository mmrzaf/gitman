package push

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/repo"
)

// maxUpdates caps how many ref updates one push may carry.
const maxUpdates = 1000

// commitCountCap is where counting a push's commits stops; the UI shows
// the cap as "1000+".
const commitCountCap = 1000

// ErrRejected is returned by ValidatePush when at least one update was
// refused; the reasons have already been written to the hook's output.
var ErrRejected = errors.New("push rejected")

// refusal is why one ref of a push was refused; ref is empty when the
// whole push was.
type refusal struct {
	ref, reason string
}

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

// ParseUpdates reads the "<old> <new> <ref>" commands from proc-receive.
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
	// DB runs the push-recording transaction, which spans the repo and ci
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

// say writes one line for the pusher to see. A failure writing it is
// left unchecked: Out is the pusher's own connection, the only place
// such a failure could be reported, so there is nowhere to report it to.
func (h *Hook) say(format string, args ...any) {
	_, _ = fmt.Fprintf(h.Out, format+"\n", args...)
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

func (pc *pushContext) who() repo.Who {
	return repo.Who{ID: pc.person.ID, Username: pc.person.Username, IsAdmin: pc.person.IsAdmin}
}

// ValidatePush checks every update against Gitman's ref naming rules and
// the repository's ref rules. It refuses the whole push if any update is
// refused; ApplyPush changes refs only after validation succeeds. It
// explains every refusal, not just the first, so one retry fixes them
// all.
func (h *Hook) ValidatePush(ctx context.Context, updates []Update) error {
	pc, err := h.load(ctx)
	if err != nil {
		return err
	}
	if pc.person.Disabled() {
		h.say("Gitman: %s is disabled and cannot push.", pc.person.Username)
		return h.refused(ctx, pc, refusal{reason: pc.person.Username + " is disabled and cannot push"})
	}
	readable, err := h.Repos.CanRead(ctx, pc.repo, pc.person.ID, pc.person.IsAdmin)
	if err != nil {
		return err
	}
	if !readable {
		h.say("Gitman: %s cannot push to a repository they cannot read.", pc.person.Username)
		return h.refused(ctx, pc, refusal{reason: pc.person.Username + " cannot push to a repository they cannot read"})
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

	var rejections []refusal
	reject := func(u Update, format string, args ...any) {
		rejections = append(rejections, refusal{ref: u.Ref, reason: fmt.Sprintf(format, args...)})
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

		if u.IsDelete() {
			if _, reason := repo.CheckDelete(pc.repo, pc.rules, u.Kind, u.Name, pc.who()); reason != "" {
				reject(u, "%s", reason)
			}
			continue
		}

		d := pc.decide(u)
		if !d.CanPush {
			reject(u, "%s does not allow %s to push here", d.RuleLabel(), pc.person.Username)
			continue
		}

		switch u.Kind {
		case git.KindBranch:
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
					reject(u, "this rewrites history (a force-push), which %s does not allow", d.RuleLabel())
					continue
				}
			}

		case git.KindTag:
			if _, err := h.Git.ResolveCommit(ctx, u.New); err != nil {
				if errors.Is(err, git.ErrNotFound) {
					reject(u, "a tag must point at a commit")
					continue
				}
				return fmt.Errorf("inspect %s: %w", u.New, err)
			}
			if !u.IsCreate() && !d.AllowForce {
				reject(u, "moving an existing tag is a force-push, which %s does not allow", d.RuleLabel())
				continue
			}
		}
	}

	if len(rejections) == 0 {
		return nil
	}
	h.say("Gitman refused this push:")
	for _, r := range rejections {
		h.say("  %s: %s", r.ref, r.reason)
	}
	return h.refused(ctx, pc, rejections...)
}

// refused records a refused push, for History's Activity, and returns
// ErrRejected. The pusher already has the reasons; a failure to keep them
// is said, and never changes the refusal.
func (h *Hook) refused(ctx context.Context, pc *pushContext, refusals ...refusal) error {
	err := h.DB.Tx(ctx, func(tx postgres.Tx) error {
		if err := insertRefusal(ctx, tx, pc.repo.ID, pc.person.ID, h.Ctx.RemoteAddr, refusals); err != nil {
			return err
		}
		return activity.Changed(ctx, tx, pc.repo.ID)
	})
	if err != nil {
		h.say("Gitman could not record this refusal: %v", err)
	}
	return ErrRejected
}

// updateRecord captures one authorized update before
// writing anything.
type updateRecord struct {
	Update
	Commits  int
	Capped   bool
	Decision repo.Decision
	Commit   string // the commit the ref now resolves to
	Pipeline []byte
	Problem  string
	// force reports a branch moved to a commit that does not descend from
	// the old one: a rewrite of its history.
	Force bool
}

func (h *Hook) prepareRecords(ctx context.Context, pc *pushContext, updates []Update) ([]updateRecord, error) {
	var err error
	// Everything that reads Git happens before the transaction, which
	// then holds its locks only for the writes.
	records := make([]updateRecord, 0, len(updates))
	for _, u := range updates {
		if u.Kind == "" {
			continue
		}
		rec := updateRecord{Update: u, Decision: pc.decide(u)}
		switch {
		case u.IsDelete():
		case u.IsCreate():
			rec.Commits, rec.Capped, err = h.Git.CountNewCommits(ctx, u.New, u.Kind, u.Name, commitCountCap)
		default:
			rec.Commits, rec.Capped, err = h.Git.CountCommits(ctx, u.New, []string{u.Old}, commitCountCap)
		}
		if err != nil {
			return nil, fmt.Errorf("count commits for %s: %w", u.Ref, err)
		}
		if !u.IsDelete() {
			if rec.Commit, err = h.Git.ResolveCommit(ctx, u.New); err != nil {
				return nil, fmt.Errorf("resolve %s: %w", u.Ref, err)
			}
			// Metadata is required to record the push, so unreadable commits
			// must be rejected before any ref or durable intent is changed.
			if _, err := h.Git.Commit(ctx, rec.Commit); err != nil {
				return nil, fmt.Errorf("read commit for %s: %w", u.Ref, err)
			}
			if u.Kind == git.KindBranch && !u.IsCreate() {
				ff, err := h.Git.IsAncestor(ctx, u.Old, u.New)
				if err != nil {
					return nil, fmt.Errorf("check fast-forward for %s: %w", u.Ref, err)
				}
				rec.Force = !ff
			}
			if rec.Decision.RunOnPush {
				rec.Pipeline, rec.Problem, err = ci.LoadPipeline(ctx, h.Git, rec.Commit)
				if err != nil {
					return nil, err
				}
			}
		}
		records = append(records, rec)
	}
	return records, nil
}

func (h *Hook) recordPush(ctx context.Context, pc *pushContext, records []updateRecord, operationID string) error {
	pushID := operationID
	var created []*ci.Created
	var createdFor, notRun []updateRecord
	err := h.DB.Tx(ctx, func(tx postgres.Tx) error {
		var pending bool
		if err := tx.QueryRow(ctx, `SELECT completed_at IS NULL FROM repository_operations WHERE id=$1 FOR UPDATE`, operationID).Scan(&pending); err != nil {
			return err
		}
		if !pending {
			return nil
		}
		// The ref index lock comes first, before any row that refers to
		// the repository: deleting a repository takes the same lock
		// before its row, so the two never wait on each other.
		refs, err := h.Repos.SyncRefsTx(ctx, tx, pc.repo.ID, h.Git, pc.person.ID)
		if err != nil {
			return err
		}
		if err := insertPush(ctx, tx, pushID, pc.repo.ID, pc.person.ID, h.Ctx.RemoteAddr); err != nil {
			return err
		}
		for _, rec := range records {
			if err := insertPushUpdate(ctx, tx, pushID, rec.Kind, rec.Name, rec.Old, rec.New,
				rec.IsCreate(), rec.IsDelete(), rec.Force, rec.Commits, rec.Capped); err != nil {
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
			if !rec.Decision.RunOnPush {
				notRun = append(notRun, rec)
				continue
			}
			if now != rec.Commit {
				return fmt.Errorf("git refs diverge from operation receipt for %s", rec.Ref)
			}
			run, err := h.CI.CreateTx(ctx, tx, ci.CreateParams{
				RepoID:          pc.repo.ID,
				Commit:          rec.Commit,
				RefKind:         rec.Kind,
				RefName:         rec.Name,
				Trigger:         ci.TriggerPush,
				PersonID:        pc.person.ID,
				PushID:          pushID,
				Pipeline:        rec.Pipeline,
				PipelineProblem: rec.Problem,
				Decision:        rec.Decision,
			})
			if err != nil {
				return fmt.Errorf("create run for %s: %w", rec.Ref, err)
			}
			created = append(created, run)
			createdFor = append(createdFor, rec)
		}
		if err := repo.CompleteOperationTx(ctx, tx, operationID); err != nil {
			return err
		}
		return activity.Changed(ctx, tx, pc.repo.ID)
	})
	if err != nil {
		return err
	}

	for i, run := range created {
		h.reportRun(pc.repo.Name, createdFor[i], run)
	}
	if slices.ContainsFunc(created, func(run *ci.Created) bool { return run.Status == ci.StatusQueued }) {
		// Only advice: the push and its runs are already recorded, so a
		// failed check leaves the notice out rather than failing a push
		// that succeeded.
		if online, err := h.CI.AnyWorkerReady(ctx); err == nil && !online {
			h.say("Gitman: no worker is ready, so queued runs wait until one is available.")
		}
	}
	for _, rec := range notRun {
		if rec.Decision.MatchedRule == nil {
			h.say("Gitman: no run for %s %s: no ref rule matches it, and only a rule with \"run\" on starts one.", rec.Kind, rec.Name)
		} else {
			h.say("Gitman: no run for %s %s: %s does not have \"run\" on.", rec.Kind, rec.Name, rec.Decision.RuleLabel())
		}
	}
	return nil
}

func (h *Hook) reportRun(repoName string, rec updateRecord, run *ci.Created) {
	link := fmt.Sprintf("%s/%s/runs/%d", h.PublicURL, repoName, run.Number)
	label := fmt.Sprintf("%s %s", rec.Kind, rec.Name)
	switch run.Status {
	case ci.StatusQueued:
		if run.Target != "" {
			h.say("Gitman: run #%d queued for %s, target context %s", run.Number, label, run.Target)
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
