package ci

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/mmrzaf/gitman/internal/postgres"
)

// BlockKind says why a queued run cannot start.
type BlockKind string

const (
	// BlockMaintenance: the instance is in maintenance and starts nothing.
	BlockMaintenance BlockKind = "maintenance"
	// BlockNoWorkers: no worker has ever registered, or all were pruned.
	BlockNoWorkers BlockKind = "no_workers"
	// BlockOffline: workers are registered but stopped or silent.
	BlockOffline BlockKind = "offline"
	// BlockUnhealthy: a worker is up but not ready (Docker, disk, recovery).
	BlockUnhealthy BlockKind = "unhealthy"
	// BlockMissingImages: ready workers exist, none has every required image.
	BlockMissingImages BlockKind = "missing_images"
)

// Blocker explains why a queued run is not starting. A run that is merely
// waiting for a busy worker has no Blocker.
type Blocker struct {
	Kind BlockKind
	// Missing lists the images the closest ready worker lacks.
	Missing []string
	// Detail is the readiness reason of an unhealthy worker.
	Detail string
}

// Message is the one-sentence explanation shown to people.
func (b Blocker) Message() string {
	switch b.Kind {
	case BlockMaintenance:
		return "The instance is in maintenance mode, so no run starts. It will start when maintenance ends."
	case BlockNoWorkers:
		return "No worker has registered. The run starts when a worker comes online."
	case BlockOffline:
		return "Every worker is stopped or not responding. The run starts when one comes back."
	case BlockUnhealthy:
		msg := "A worker is online but not ready"
		if b.Detail != "" {
			msg += " (" + b.Detail + ")"
		}
		return msg + ". The run starts when it is ready."
	case BlockMissingImages:
		return "No ready worker has the required " + plural(len(b.Missing), "image") + " " + strings.Join(b.Missing, ", ") +
			". Make " + pluralPronoun(len(b.Missing)) + " available on a worker host to start the run."
	}
	return ""
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func pluralPronoun(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// classifyBlock decides why a queued run needing required cannot start on
// workers, or returns nil when some ready worker could take it. Workers that
// are not ready are judged by their last known images: an unready worker's inventory may simply be unreadable.
func classifyBlock(required []string, workers []WorkerStatus, maintenance bool) *Blocker {
	if maintenance {
		return &Blocker{Kind: BlockMaintenance}
	}
	has := func(w WorkerStatus) bool { return containsAll(w.Images, required) }
	var ready []WorkerStatus
	var recoverable *WorkerStatus
	var online *WorkerStatus
	for i, w := range workers {
		switch {
		case w.Ready:
			if has(w) {
				return nil
			}
			ready = append(ready, w)
		case has(w) && (recoverable == nil || !recoverable.Online && w.Online):
			recoverable = &workers[i]
		}
		if !w.Ready && w.Online && online == nil {
			online = &workers[i]
		}
	}
	if recoverable != nil {
		return notReady(*recoverable)
	}
	if len(ready) > 0 {
		var best []string
		for i, w := range ready {
			missing := absentImages(required, w.Images)
			if i == 0 || len(missing) < len(best) {
				best = missing
			}
		}
		return &Blocker{Kind: BlockMissingImages, Missing: best}
	}
	if online != nil {
		return notReady(*online)
	}
	if len(workers) > 0 {
		return &Blocker{Kind: BlockOffline}
	}
	return &Blocker{Kind: BlockNoWorkers}
}

func notReady(w WorkerStatus) *Blocker {
	if !w.Online {
		return &Blocker{Kind: BlockOffline}
	}
	return &Blocker{Kind: BlockUnhealthy, Detail: w.Reason}
}

func containsAll(have, need []string) bool {
	return len(absentImages(need, have)) == 0
}

func absentImages(need, have []string) []string {
	var missing []string
	for _, image := range need {
		if !slices.Contains(have, image) {
			missing = append(missing, image)
		}
	}
	return missing
}

// QueuedBlock explains why queued run runID is not starting, or returns nil
// when it is only waiting its turn.
func (s *Service) QueuedBlock(ctx context.Context, runID string) (*Blocker, error) {
	var required []string
	var maintenance bool
	if err := s.db.Q.QueryRow(ctx, `SELECT required_images, (SELECT maintenance FROM instance) FROM runs WHERE id = $1 AND status = 'queued'`, runID).Scan(&required, &maintenance); err != nil {
		return nil, postgres.NormalizeNotFound(err)
	}
	workers, err := selectWorkers(ctx, s.db.Q)
	if err != nil {
		return nil, err
	}
	return classifyBlock(required, workers, maintenance), nil
}

// BlockGroup is a group of queued runs waiting for the same reason.
type BlockGroup struct {
	Count   int
	Blocker Blocker
	// RepoName and Number identify the oldest run in the group, to link to.
	RepoName string
	Number   int64

	runID string
}

// QueuedCount is how many runs are queued.
func (s *Service) QueuedCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Q.QueryRow(ctx, `SELECT count(*) FROM runs WHERE status = 'queued'`).Scan(&n)
	return n, err
}

// QueuedBlockGroups groups blocked queued runs by cause. Runs missing different
// images are separate groups, since their fixes differ.
func (s *Service) QueuedBlockGroups(ctx context.Context) ([]BlockGroup, error) {
	index := map[string]int{}
	var groups []BlockGroup
	err := s.eachQueuedBlock(ctx, s.db.Q, nil, func(id string, b *Blocker) error {
		if b == nil {
			return nil
		}
		key := string(b.Kind) + "|" + strings.Join(b.Missing, ",") + "|" + b.Detail
		if i, ok := index[key]; ok {
			groups[i].Count++
			return nil
		}
		index[key] = len(groups)
		groups = append(groups, BlockGroup{Count: 1, Blocker: *b, runID: id})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if err := s.db.Q.QueryRow(ctx, `SELECT repos.name, runs.number FROM runs JOIN repos ON repos.id = runs.repo_id WHERE runs.id = $1`, groups[i].runID).Scan(&groups[i].RepoName, &groups[i].Number); err != nil {
			return nil, err
		}
	}
	return groups, nil
}

// QueuedBlockCounts reports how many queued runs wait for each reason, for a
// dashboard or a doctor check. Runs only waiting their turn are not counted.
func (s *Service) QueuedBlockCounts(ctx context.Context) (map[BlockKind]int, error) {
	return s.queuedBlockCounts(ctx, nil)
}

// QueuedBlockCountsForRepos counts only blocked runs the caller can read.
// Home uses this so an aggregate count never discloses activity in a
// restricted repository.
func (s *Service) QueuedBlockCountsForRepos(ctx context.Context, repoIDs []string) (map[BlockKind]int, error) {
	return s.queuedBlockCounts(ctx, repoIDs)
}

func (s *Service) queuedBlockCounts(ctx context.Context, repoIDs []string) (map[BlockKind]int, error) {
	counts := map[BlockKind]int{}
	err := s.eachQueuedBlock(ctx, s.db.Q, repoIDs, func(_ string, b *Blocker) error {
		if b != nil {
			counts[b.Kind]++
		}
		return nil
	})
	return counts, err
}

// eachQueuedBlock classifies currently queued runs. A non-nil repoIDs
// restricts the scan to those repositories.
func (s *Service) eachQueuedBlock(ctx context.Context, q postgres.Querier, repoIDs []string, fn func(runID string, b *Blocker) error) error {
	workers, err := selectWorkers(ctx, q)
	if err != nil {
		return err
	}
	var maintenance bool
	if err := q.QueryRow(ctx, `SELECT maintenance FROM instance`).Scan(&maintenance); err != nil {
		return err
	}
	rows, err := q.Query(ctx, `
		SELECT id, required_images FROM runs
		WHERE status = 'queued' AND ($1::text[] IS NULL OR repo_id = ANY($1))
		ORDER BY queued_at, id LIMIT 1000`, repoIDs)
	if err != nil {
		return fmt.Errorf("list queued runs: %w", err)
	}
	type queued struct {
		id       string
		required []string
	}
	var runs []queued
	for rows.Next() {
		var r queued
		if err := rows.Scan(&r.id, &r.required); err != nil {
			rows.Close()
			return err
		}
		runs = append(runs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range runs {
		if err := fn(r.id, classifyBlock(r.required, workers, maintenance)); err != nil {
			return err
		}
	}
	return nil
}
