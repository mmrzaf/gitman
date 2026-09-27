// Package activity is Gitman's record of what happened: settings changes
// recorded directly (a repository created, a rule saved, a person's role
// changed), merged on read with pushes, finished runs and deployments —
// which already live in their own tables — into one feed ordered by time.
// Home and the Repository page both show this feed, the second scoped to
// one repository.
//
// Record is called by the other services inside their own transactions;
// Service reads the merged feed. store.go holds every SQL statement.
package activity

import (
	"context"
	"sort"
	"time"

	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/id"
	"github.com/mmrzaf/gitman/internal/postgres"
)

// Actions recorded directly to the events table: settings changes with
// no table of their own to be read back from.
const (
	RepoCreated              = "repo.created"
	RepoDeleted              = "repo.deleted"
	RepoDescribed            = "repo.description_changed"
	RepoVisibilityChanged    = "repo.visibility_changed"
	RepoReaderAdded          = "repo.reader_added"
	RepoReaderRemoved        = "repo.reader_removed"
	RepoDefaultPushChanged   = "repo.default_push_changed"
	RepoDefaultBranchChanged = "repo.default_branch_changed"
	RuleSaved                = "rule.saved"
	RuleDeleted              = "rule.deleted"
	SecretSet                = "secret.set"
	SecretDeleted            = "secret.deleted"
	PersonAdded              = "person.added"
	PersonDisabled           = "person.disabled"
	PersonEnabled            = "person.enabled"
	PersonRole               = "person.role"
	PasswordReset            = "person.password_reset"
)

// NotifyChannel is the PostgreSQL channel that carries a repository's ID
// — empty for a change about no one repository — whenever the feed
// changes other than through a run: a push, or a recorded event.
const NotifyChannel = "gitman_activity"

// Record appends one event inside tx, the transaction of the change it
// describes, so an event is written — and published on NotifyChannel —
// exactly when its change commits. repoID and personID may be empty: an
// event about a person has no repository, and an action taken from the
// command line has no acting person.
func Record(ctx context.Context, tx postgres.Tx, repoID, personID, action, detail string) error {
	if err := insertEvent(ctx, tx, id.New(), repoID, personID, action, detail); err != nil {
		return err
	}
	return Changed(ctx, tx, repoID)
}

// Changed publishes on NotifyChannel, when tx commits, that a
// repository's activity changed, for a change kept in its own table
// rather than recorded as an event: a push.
func Changed(ctx context.Context, tx postgres.Tx, repoID string) error {
	return notifyChanged(ctx, tx, repoID)
}

// Kind is what happened, for one entry of the merged feed.
type Kind string

const (
	KindPush       Kind = "push"
	KindRun        Kind = "run"
	KindDeployment Kind = "deployment"
	KindEvent      Kind = "event"
)

// Entry is one line of the feed. Only the fields for its Kind are set;
// the template branches on Kind to know which.
type Entry struct {
	Kind     Kind
	RepoName string
	At       time.Time
	// Actor is who did it: who pushed, started the run, shipped, or
	// changed a setting. Empty when no person did: a change made from
	// the command line.
	Actor string

	// push
	RefKind     git.Kind
	RefName     string
	UpdateCount int
	OldCommit   string
	NewCommit   string
	IsCreate    bool
	IsDelete    bool

	// run
	RunNumber int64
	Status    string
	Trigger   string

	// deployment
	Target  string
	Version string
	Commit  string

	// event
	Action string
	Detail string
}

// fetchLimit is how many rows are fetched from each source before
// merging; the final result is cut to the caller's limit, so this only
// needs to be at least that limit for the merge to be correct.
const fetchLimit = 100

// Service reads the activity feed.
type Service struct {
	db *postgres.DB
}

// NewService returns a Service backed by db.
func NewService(db *postgres.DB) *Service {
	return &Service{db: db}
}

// Recent returns the most recent entries, most recent first, across
// every repository (repoID nil) or scoped to one (repoID set).
func (s *Service) Recent(ctx context.Context, repoID *string, limit int) ([]Entry, error) {
	return s.recent(ctx, filter{repoID: repoID}, limit)
}

// RecentForRepos is Recent for the instance-wide feed shown to someone
// who cannot necessarily read every repository: entries are restricted
// to repoIDs, plus any entry naming no repository at all. Pass every
// repository's ID to get everything an admin would see.
func (s *Service) RecentForRepos(ctx context.Context, repoIDs []string, limit int) ([]Entry, error) {
	if repoIDs == nil {
		repoIDs = []string{}
	}
	return s.recent(ctx, filter{repoIDs: repoIDs}, limit)
}

// The four kinds come from different tables with different shapes, so
// each is fetched with its own query — simpler and cheaper than a
// UNION ALL across mismatched columns — and merged here.
func (s *Service) recent(ctx context.Context, f filter, limit int) ([]Entry, error) {
	q := s.db.Q
	if limit > fetchLimit {
		limit = fetchLimit
	}
	pushes, err := recentPushes(ctx, q, f)
	if err != nil {
		return nil, err
	}
	runs, err := recentRuns(ctx, q, f)
	if err != nil {
		return nil, err
	}
	deploys, err := recentDeployments(ctx, q, f)
	if err != nil {
		return nil, err
	}
	events, err := recentEvents(ctx, q, f)
	if err != nil {
		return nil, err
	}

	all := make([]Entry, 0, len(pushes)+len(runs)+len(deploys)+len(events))
	all = append(all, pushes...)
	all = append(all, runs...)
	all = append(all, deploys...)
	all = append(all, events...)
	sort.Slice(all, func(i, j int) bool { return all[i].At.After(all[j].At) })
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}
