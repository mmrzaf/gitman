package activity

import (
	"context"
	"fmt"
	"time"

	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
)

func insertEvent(ctx context.Context, q postgres.Querier, id, repoID, personID, action, detail string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO events (id, repo_id, person_id, action, detail)
		VALUES ($1, NULLIF($2, ''), NULLIF($3, ''), $4, $5)
	`, id, repoID, personID, action, detail)
	if err != nil {
		return fmt.Errorf("record event %s: %w", action, err)
	}
	return nil
}

func notifyChanged(ctx context.Context, q postgres.Querier, repoID string) error {
	if _, err := q.Exec(ctx, `SELECT pg_notify($1, $2)`, NotifyChannel, repoID); err != nil {
		return fmt.Errorf("notify activity: %w", err)
	}
	return nil
}

// filter is which repositories a recentX query includes: every
// repository (repoID and repoIDs both nil), exactly one (repoID set —
// a repository's own page), or a set of them plus any row naming no
// repository at all (repoIDs set, possibly empty — the repositories one
// particular person may read, for the instance-wide feed).
type filter struct {
	repoID  *string
	repoIDs []string
}

func (f filter) clause(column string) (clause string, args []any) {
	switch {
	case f.repoID != nil:
		return " AND " + column + " = $2", []any{fetchLimit, *f.repoID}
	case f.repoIDs != nil:
		return " AND (" + column + " = ANY($2) OR " + column + " IS NULL)", []any{fetchLimit, f.repoIDs}
	default:
		return "", []any{fetchLimit}
	}
}

// recentPushes summarizes one entry per push: the ref it updated, or how
// many if it updated more than one — the push_updates rows for the
// fetched pushes are looked up in a second, batched query rather than
// one query per push.
func recentPushes(ctx context.Context, q postgres.Querier, f filter) ([]Entry, error) {
	clause, args := f.clause("p.repo_id")
	rows, err := q.Query(ctx, `
		SELECT p.id, repos.name, p.created_at, COALESCE(pe.username, '')
		FROM pushes p
		JOIN repos ON repos.id = p.repo_id
		LEFT JOIN people pe ON pe.id = p.person_id
		WHERE true`+clause+`
		ORDER BY p.created_at DESC
		LIMIT $1
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list recent pushes: %w", err)
	}
	type pushRow struct {
		id       string
		repoName string
		at       time.Time
		actor    string
	}
	var pushRows []pushRow
	var ids []string
	for rows.Next() {
		var pr pushRow
		if err := rows.Scan(&pr.id, &pr.repoName, &pr.at, &pr.actor); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan push: %w", err)
		}
		pushRows = append(pushRows, pr)
		ids = append(ids, pr.id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list recent pushes: %w", err)
	}
	if len(pushRows) == 0 {
		return nil, nil
	}

	type update struct {
		kind, name, old, new string
		isCreate, isDelete   bool
	}
	byPush := map[string][]update{}
	urows, err := q.Query(ctx, `
		SELECT push_id, kind, name, old_commit, new_commit, is_create, is_delete
		FROM push_updates WHERE push_id = ANY($1) ORDER BY push_id
	`, ids)
	if err != nil {
		return nil, fmt.Errorf("list push updates: %w", err)
	}
	for urows.Next() {
		var pushID string
		var u update
		if err := urows.Scan(&pushID, &u.kind, &u.name, &u.old, &u.new, &u.isCreate, &u.isDelete); err != nil {
			urows.Close()
			return nil, fmt.Errorf("scan push update: %w", err)
		}
		byPush[pushID] = append(byPush[pushID], u)
	}
	urows.Close()
	if err := urows.Err(); err != nil {
		return nil, fmt.Errorf("list push updates: %w", err)
	}

	entries := make([]Entry, 0, len(pushRows))
	for _, pr := range pushRows {
		updates := byPush[pr.id]
		e := Entry{Kind: KindPush, RepoName: pr.repoName, At: pr.at, Actor: pr.actor, UpdateCount: len(updates)}
		if len(updates) == 1 {
			u := updates[0]
			e.RefKind, e.RefName = git.Kind(u.kind), u.name
			e.OldCommit, e.NewCommit = u.old, u.new
			e.IsCreate, e.IsDelete = u.isCreate, u.isDelete
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func recentRuns(ctx context.Context, q postgres.Querier, f filter) ([]Entry, error) {
	clause, args := f.clause("r.repo_id")
	rows, err := q.Query(ctx, `
		SELECT repos.name, r.finished_at, COALESCE(p.username, ''), r.number, r.status, r.trigger
		FROM runs r
		JOIN repos ON repos.id = r.repo_id
		LEFT JOIN people p ON p.id = r.triggered_by
		WHERE r.finished_at IS NOT NULL`+clause+`
		ORDER BY r.finished_at DESC
		LIMIT $1
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list recent runs: %w", err)
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		e := Entry{Kind: KindRun}
		if err := rows.Scan(&e.RepoName, &e.At, &e.Actor, &e.RunNumber, &e.Status, &e.Trigger); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func recentDeployments(ctx context.Context, q postgres.Querier, f filter) ([]Entry, error) {
	clause, args := f.clause("d.repo_id")
	rows, err := q.Query(ctx, `
		SELECT repos.name, d.created_at, COALESCE(p.username, ''), d.target, d.version, d.commit_hash
		FROM deployments d
		JOIN repos ON repos.id = d.repo_id
		LEFT JOIN people p ON p.id = d.person_id
		WHERE true`+clause+`
		ORDER BY d.created_at DESC
		LIMIT $1
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list recent deployments: %w", err)
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		e := Entry{Kind: KindDeployment}
		if err := rows.Scan(&e.RepoName, &e.At, &e.Actor, &e.Target, &e.Version, &e.Commit); err != nil {
			return nil, fmt.Errorf("scan deployment: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// recentEvents lists settings changes: people, rules, secrets and
// repositories. A person-level event (adding or disabling someone) has
// no repository and is included whenever the feed is not scoped to one
// particular repository.
func recentEvents(ctx context.Context, q postgres.Querier, f filter) ([]Entry, error) {
	clause, args := f.clause("e.repo_id")
	rows, err := q.Query(ctx, `
		SELECT COALESCE(repos.name, ''), e.created_at, COALESCE(p.username, ''), e.action, e.detail
		FROM events e
		LEFT JOIN repos ON repos.id = e.repo_id
		LEFT JOIN people p ON p.id = e.person_id
		WHERE true`+clause+`
		ORDER BY e.created_at DESC
		LIMIT $1
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list recent events: %w", err)
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		e := Entry{Kind: KindEvent}
		if err := rows.Scan(&e.RepoName, &e.At, &e.Actor, &e.Action, &e.Detail); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// repoRefChanges lists a repository's ref updates newest first, each with
// the run it started: one row per push_updates row, not per push.
func repoRefChanges(ctx context.Context, q postgres.Querier, repoID string, limit int) ([]HistoryEntry, error) {
	rows, err := q.Query(ctx, `
		SELECT u.id, p.created_at, COALESCE(pe.username, ''), u.kind, u.name, u.old_commit, u.new_commit,
		       u.is_create, u.is_delete, u.is_force, COALESCE(r.number, 0), COALESCE(r.status, '')
		FROM push_updates u
		JOIN pushes p ON p.id = u.push_id
		LEFT JOIN people pe ON pe.id = p.person_id
		LEFT JOIN runs r ON r.push_id = p.id AND r.ref_kind = u.kind AND r.ref_name = u.name
		WHERE p.repo_id = $1
		ORDER BY p.created_at DESC, u.id DESC
		LIMIT $2
	`, repoID, limit)
	if err != nil {
		return nil, fmt.Errorf("list ref changes: %w", err)
	}
	defer rows.Close()
	var entries []HistoryEntry
	for rows.Next() {
		e := HistoryEntry{Kind: KindPush}
		var kind string
		var isCreate, isDelete, isForce bool
		if err := rows.Scan(&e.id, &e.At, &e.Actor, &kind, &e.RefName, &e.OldCommit, &e.NewCommit,
			&isCreate, &isDelete, &isForce, &e.RunNumber, &e.RunStatus); err != nil {
			return nil, fmt.Errorf("scan ref change: %w", err)
		}
		e.RefKind = git.Kind(kind)
		e.Change = changeOf(e.RefKind, isCreate, isDelete, isForce)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// repoEvents lists a repository's settings changes newest first.
func repoEvents(ctx context.Context, q postgres.Querier, repoID string, limit int) ([]HistoryEntry, error) {
	rows, err := q.Query(ctx, `
		SELECT e.id, e.created_at, COALESCE(p.username, ''), e.action, e.detail
		FROM events e
		LEFT JOIN people p ON p.id = e.person_id
		WHERE e.repo_id = $1
		ORDER BY e.created_at DESC, e.id DESC
		LIMIT $2
	`, repoID, limit)
	if err != nil {
		return nil, fmt.Errorf("list repository events: %w", err)
	}
	defer rows.Close()
	var entries []HistoryEntry
	for rows.Next() {
		e := HistoryEntry{Kind: KindEvent}
		if err := rows.Scan(&e.id, &e.At, &e.Actor, &e.Action, &e.Detail); err != nil {
			return nil, fmt.Errorf("scan repository event: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// repoRefusals lists a repository's refused pushes newest first, each with
// the reasons it was refused for.
func repoRefusals(ctx context.Context, q postgres.Querier, repoID string, limit int) ([]HistoryEntry, error) {
	rows, err := q.Query(ctx, `
		SELECT f.id, f.created_at, COALESCE(p.username, '')
		FROM push_refusals f
		LEFT JOIN people p ON p.id = f.person_id
		WHERE f.repo_id = $1
		ORDER BY f.created_at DESC, f.id DESC
		LIMIT $2
	`, repoID, limit)
	if err != nil {
		return nil, fmt.Errorf("list refused pushes: %w", err)
	}
	var entries []HistoryEntry
	var ids []string
	for rows.Next() {
		e := HistoryEntry{Kind: KindRefusal}
		if err := rows.Scan(&e.id, &e.At, &e.Actor); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan refused push: %w", err)
		}
		entries = append(entries, e)
		ids = append(ids, e.id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list refused pushes: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil
	}

	// The reasons of every listed refusal, in one query.
	reasons, err := q.Query(ctx, `
		SELECT refusal_id, ref, reason FROM push_refusal_refs
		WHERE refusal_id = ANY($1) ORDER BY refusal_id, position
	`, ids)
	if err != nil {
		return nil, fmt.Errorf("list why pushes were refused: %w", err)
	}
	defer reasons.Close()
	byID := map[string][]Refusal{}
	for reasons.Next() {
		var refusalID string
		var r Refusal
		if err := reasons.Scan(&refusalID, &r.Ref, &r.Reason); err != nil {
			return nil, fmt.Errorf("scan why a push was refused: %w", err)
		}
		r.RefKind, r.RefName, _ = git.SplitFullName(r.Ref)
		byID[refusalID] = append(byID[refusalID], r)
	}
	if err := reasons.Err(); err != nil {
		return nil, fmt.Errorf("list why pushes were refused: %w", err)
	}
	for i := range entries {
		entries[i].Refused = byID[entries[i].id]
	}
	return entries, nil
}
