package activity

import (
	"context"
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

// HistoryEntry is one line of a repository's History → Activity: a ref
// changing, a change to its settings, or a push that was refused. Only the
// fields for its Kind are set: KindPush for a ref change, KindEvent for a
// settings change, KindRefusal for a refused push.
type HistoryEntry struct {
	Kind  Kind
	At    time.Time
	Actor string

	// ref change: one push_updates row, not the whole push
	Change    Change
	RefKind   git.Kind
	RefName   string
	OldCommit string
	NewCommit string
	// RunNumber and RunStatus name the run this update started, if it did.
	RunNumber int64
	RunStatus string

	// event
	Action string
	Detail string

	// refusal
	Refused []Refusal

	// id orders entries of the same moment.
	id string
}

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

// ForRepo lists a repository's ref changes, settings changes and refused
// pushes, newest first, skipping the first skip and reporting whether more follow. Each
// source is read to skip+limit+1 rows and merged here, so a page is
// exactly what the merged feed holds at that place however the sources
// interleave.
func (s *Service) ForRepo(ctx context.Context, repoID string, skip, limit int) (entries []HistoryEntry, more bool, err error) {
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
	all := append(append(changes, events...), refusals...)
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
