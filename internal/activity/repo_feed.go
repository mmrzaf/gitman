package activity

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/mmrzaf/gitman/internal/git"
)

// Change is what a push did to one ref.
type Change string

const (
	Created     Change = "created"
	Pushed      Change = "pushed"
	ForcePushed Change = "force-pushed"
	Deleted     Change = "deleted"
	// Moved is a tag pointed at a different commit.
	Moved Change = "moved"
)

// Refusal is why one ref of a push was refused.
type Refusal struct {
	// Ref is the full ref name, empty when the whole push was refused.
	Ref string
	// RefKind and RefName are set when Ref is a branch or a tag.
	RefKind git.Kind
	RefName string
	Reason  string
}

// RepoEntry is one line of a repository's own feed: a ref changing
// (KindPush), a push that was refused (KindRefusal), a run that finished
// (KindRun), a version shipped (KindDeployment), or a change to its
// settings (KindEvent). Only the fields for its Kind are set.
type RepoEntry struct {
	Kind  Kind
	At    time.Time
	Actor string

	// ref change: one push_updates row, not the whole push; a run that
	// finished names its ref too
	Change    Change
	RefKind   git.Kind
	RefName   string
	OldCommit string
	NewCommit string
	// RunNumber and RunStatus name the run this update started, if it did,
	// or are the run that finished.
	RunNumber int64
	RunStatus string

	// Count is how many refs this line is about: one, unless a push made so
	// many of the same change that they are one line. Names are the first
	// of them, newest version first.
	Count int
	Names []string

	// deployment
	Target  string
	Version string
	Commit  string

	// event
	Action string
	Detail string

	// refusal
	Refused []Refusal

	// id orders entries of the same moment.
	id string
}

// bulkNamed is how many refs of a bulk change a line names.
const bulkNamed = 3

// changeOf classifies one ref update.
func changeOf(kind git.Kind, isCreate, isDelete, isForce bool) Change {
	switch {
	case isDelete:
		return Deleted
	case isCreate:
		return Created
	case isForce:
		return ForcePushed
	case kind == git.KindTag:
		return Moved
	}
	return Pushed
}

// RefusedPush is a push Gitman refused, for a summary: who, where, and the
// first reason given.
type RefusedPush struct {
	RepoName string
	Actor    string
	At       time.Time
	// Ref is the full ref name the reason is about, empty when the whole
	// push was refused.
	Ref    string
	Reason string
}

// RefusedSince lists the pushes refused in any of repoIDs since a time,
// newest first, up to limit.
func (s *Service) RefusedSince(ctx context.Context, repoIDs []string, since time.Time, limit int) ([]RefusedPush, error) {
	if repoIDs == nil {
		repoIDs = []string{}
	}
	return refusedSince(ctx, s.db.Q, repoIDs, since, limit)
}

// ForRepo lists a repository's ref changes, refused pushes, finished runs,
// shipped versions and settings changes, newest first, skipping the first skip and reporting whether more follow. Each
// source is read to skip+limit+1 rows and merged here, so a page is
// exactly what the merged feed holds at that place however the sources
// interleave.
func (s *Service) ForRepo(ctx context.Context, repoID string, skip, limit int) (entries []RepoEntry, more bool, err error) {
	want := skip + limit + 1
	changes, err := repoRefChanges(ctx, s.db.Q, repoID, want)
	if err != nil {
		return nil, false, err
	}
	events, err := repoEvents(ctx, s.db.Q, repoID, want)
	if err != nil {
		return nil, false, err
	}
	refusals, err := repoRefusals(ctx, s.db.Q, repoID, want)
	if err != nil {
		return nil, false, err
	}
	runs, err := repoRuns(ctx, s.db.Q, repoID, want)
	if err != nil {
		return nil, false, err
	}
	deployments, err := repoDeployments(ctx, s.db.Q, repoID, want)
	if err != nil {
		return nil, false, err
	}
	all := slices.Concat(changes, events, refusals, runs, deployments)
	sort.Slice(all, func(i, j int) bool {
		if !all[i].At.Equal(all[j].At) {
			return all[i].At.After(all[j].At)
		}
		return all[i].id > all[j].id
	})
	if skip >= len(all) {
		return nil, false, nil
	}
	all = all[skip:]
	if len(all) > limit {
		return all[:limit], true, nil
	}
	return all, false, nil
}
