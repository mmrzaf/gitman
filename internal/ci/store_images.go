package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
)

// pipelineFact is one stored pipeline whose Image and Requires references
// belong to the required-image set, and whose Images map declares built images.
type pipelineFact struct {
	RunID    string
	RepoID   string
	RepoName string
	Source   string
	Pipeline []byte
}

// selectRequiredPipelines returns the stored pipeline bytes of every run that
// can still need images: every queued or running run, plus the most recent run
// (with stored bytes) of every existing ref matched by at least one ref rule.
//
// Ref patterns are Gitman's own '*' patterns, which SQL cannot evaluate, so
// refs and rules are matched here with git.MatchPattern and the latest run per
// matched ref is then fetched in one query. A rule with run_on_push off still
// counts: its ref can be started by hand.
func selectRequiredPipelines(ctx context.Context, q postgres.Querier) ([]pipelineFact, bool, error) {
	facts := []pipelineFact{}
	seen := map[string]bool{}
	incomplete := false
	add := func(fact pipelineFact) {
		if fact.RunID != "" && seen[fact.RunID] {
			return
		}
		if fact.RunID != "" {
			seen[fact.RunID] = true
		}
		facts = append(facts, fact)
	}

	rows, err := q.Query(ctx, `
		SELECT r.id, r.repo_id, repos.name, 'run #' || r.number, r.pipeline
		FROM runs r
		JOIN repos ON repos.id = r.repo_id
		WHERE r.status IN ('queued', 'running') AND r.pipeline IS NOT NULL
		ORDER BY r.repo_id, r.number
		LIMIT $1
	`, imageFactsLimit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list required run pipelines: %w", err)
	}
	requiredCount := 0
	for rows.Next() {
		var fact pipelineFact
		if err := rows.Scan(&fact.RunID, &fact.RepoID, &fact.RepoName, &fact.Source, &fact.Pipeline); err != nil {
			rows.Close()
			return nil, false, fmt.Errorf("scan required run pipeline: %w", err)
		}
		requiredCount++
		add(fact)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list required run pipelines: %w", err)
	}
	if requiredCount > imageFactsLimit {
		facts = facts[:imageFactsLimit]
		incomplete = true
	}

	type ruleKey struct {
		repoID string
		kind   git.Kind
	}
	rules := map[ruleKey][]string{}
	rows, err = q.Query(ctx, `SELECT repo_id, kind, pattern FROM ref_rules ORDER BY repo_id, kind, pattern`)
	if err != nil {
		return nil, false, fmt.Errorf("list rules for image facts: %w", err)
	}
	for rows.Next() {
		var repoID, kind, pattern string
		if err := rows.Scan(&repoID, &kind, &pattern); err != nil {
			rows.Close()
			return nil, false, fmt.Errorf("scan rule for image facts: %w", err)
		}
		key := ruleKey{repoID, git.Kind(kind)}
		rules[key] = append(rules[key], pattern)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list rules for image facts: %w", err)
	}

	type ref struct {
		repoID string
		kind   git.Kind
		name   string
	}
	var matched []ref
	rows, err = q.Query(ctx, `SELECT repo_id, kind, name FROM refs ORDER BY repo_id, kind, name`)
	if err != nil {
		return nil, false, fmt.Errorf("list refs for image facts: %w", err)
	}
	for rows.Next() {
		var r ref
		var kind string
		if err := rows.Scan(&r.repoID, &kind, &r.name); err != nil {
			rows.Close()
			return nil, false, fmt.Errorf("scan ref for image facts: %w", err)
		}
		r.kind = git.Kind(kind)
		patterns := rules[ruleKey{r.repoID, r.kind}]
		for _, pattern := range patterns {
			if git.MatchPattern(pattern, r.name) {
				matched = append(matched, r)
				break
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list refs for image facts: %w", err)
	}
	if len(matched) == 0 {
		return facts, incomplete, nil
	}

	repoIDs := make([]string, len(matched))
	kinds := make([]string, len(matched))
	names := make([]string, len(matched))
	for i, r := range matched {
		repoIDs[i], kinds[i], names[i] = r.repoID, string(r.kind), r.name
	}
	rows, err = q.Query(ctx, `
		SELECT DISTINCT ON (refs.repo_id, refs.kind, refs.name)
		       r.id, refs.repo_id, repos.name, refs.kind, refs.name, r.pipeline
		FROM refs
		JOIN repos ON repos.id = refs.repo_id
		JOIN unnest($1::text[], $2::text[], $3::text[]) AS matched(repo_id, kind, name)
		  ON refs.repo_id = matched.repo_id AND refs.kind = matched.kind AND refs.name = matched.name
		JOIN runs r ON r.repo_id = refs.repo_id AND r.ref_kind = refs.kind
		  AND r.ref_name = refs.name AND r.commit_hash = refs.commit_hash
		WHERE r.pipeline IS NOT NULL
		ORDER BY refs.repo_id, refs.kind, refs.name, r.number DESC
		LIMIT $4
	`, repoIDs, kinds, names, imageFactsLimit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list current-head pipelines per ref: %w", err)
	}
	defer rows.Close()
	matchedCount := 0
	for rows.Next() {
		var fact pipelineFact
		var kind, name string
		if err := rows.Scan(&fact.RunID, &fact.RepoID, &fact.RepoName, &kind, &name, &fact.Pipeline); err != nil {
			return nil, false, fmt.Errorf("scan current-head pipeline per ref: %w", err)
		}
		matchedCount++
		fact.Source = kind + " " + name
		add(fact)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list current-head pipelines per ref: %w", err)
	}
	if matchedCount > imageFactsLimit {
		incomplete = true
	}
	if len(facts) > imageFactsLimit {
		facts = facts[:imageFactsLimit]
		incomplete = true
	}
	return facts, incomplete, nil
}

// imageHostRow is one row of image_hosts, with stale already judged by the
// database clock so every caller uses the same clock as collected_at.
type imageHostRow struct {
	EngineID         string
	WorkerID         string
	Hostname         string
	CollectedAt      time.Time
	Stale            bool
	Images           []byte
	CacheSize        int64
	CacheReclaimable int64
	KeepGiB          int
	OlderHours       int
	CacheDisabled    bool
	Automation       string
	AttemptedAt      *time.Time
	PrunedAt         *time.Time
	Reclaimed        int64
	CacheError       string
}

func selectImageHosts(ctx context.Context, q postgres.Querier) ([]imageHostRow, error) {
	rows, err := q.Query(ctx, `
		SELECT host_id, COALESCE(worker_id, ''), hostname, collected_at,
		       collected_at < now() - make_interval(secs => $1),
		       images, cache_size_bytes, cache_reclaimable_bytes,
		       cache_keep_gib, cache_older_than_hours, cache_disabled, automation_mode,
		       cache_attempt_at, cache_pruned_at, cache_reclaimed_bytes, cache_error
		FROM image_hosts
		ORDER BY hostname, host_id
	`, ImageInventoryStaleAfter.Seconds())
	if err != nil {
		return nil, fmt.Errorf("list image hosts: %w", err)
	}
	defer rows.Close()
	var hosts []imageHostRow
	for rows.Next() {
		var host imageHostRow
		if err := rows.Scan(&host.EngineID, &host.WorkerID, &host.Hostname, &host.CollectedAt, &host.Stale,
			&host.Images, &host.CacheSize, &host.CacheReclaimable, &host.KeepGiB, &host.OlderHours,
			&host.CacheDisabled, &host.Automation, &host.AttemptedAt, &host.PrunedAt, &host.Reclaimed, &host.CacheError); err != nil {
			return nil, fmt.Errorf("scan image host: %w", err)
		}
		hosts = append(hosts, host)
	}
	return hosts, rows.Err()
}

func upsertImageHost(ctx context.Context, q postgres.Querier, inventory HostInventory) error {
	images, err := json.Marshal(inventory.Images)
	if err != nil {
		return fmt.Errorf("encode image inventory: %w", err)
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO image_hosts (host_id, worker_id, hostname, images, cache_size_bytes, cache_reclaimable_bytes,
		                         cache_keep_gib, cache_older_than_hours, cache_disabled, automation_mode)
		VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (host_id) DO UPDATE SET
			worker_id = EXCLUDED.worker_id,
			hostname = EXCLUDED.hostname,
			collected_at = now(),
			images = EXCLUDED.images,
			cache_size_bytes = EXCLUDED.cache_size_bytes,
			cache_reclaimable_bytes = EXCLUDED.cache_reclaimable_bytes,
			cache_keep_gib = EXCLUDED.cache_keep_gib,
			cache_older_than_hours = EXCLUDED.cache_older_than_hours,
			cache_disabled = EXCLUDED.cache_disabled,
			automation_mode = EXCLUDED.automation_mode
	`, inventory.EngineID, inventory.WorkerID, inventory.Hostname, images, inventory.CacheSize, inventory.CacheReclaimable,
		inventory.Cache.KeepGiB, inventory.Cache.OlderThanHours, inventory.CacheDisabled, inventory.Automation); err != nil {
		return fmt.Errorf("publish image inventory: %w", err)
	}
	return nil
}

// recordCachePrune stores one cache-prune attempt and returns the error text
// it replaced, so an unchanged recurring failure is not audited every pass.
func recordCachePrune(ctx context.Context, tx postgres.Tx, hostID string, reclaimed int64, cacheError string, success bool) (string, error) {
	var previous string
	if err := tx.QueryRow(ctx, `SELECT cache_error FROM image_hosts WHERE host_id = $1 FOR UPDATE`, hostID).Scan(&previous); err != nil {
		return "", postgres.NormalizeNotFound(err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE image_hosts SET
			cache_attempt_at = now(),
			cache_pruned_at = CASE WHEN $4 THEN now() ELSE cache_pruned_at END,
			cache_reclaimed_bytes = CASE WHEN $4 THEN $2 ELSE cache_reclaimed_bytes END,
			cache_error = $3
		WHERE host_id = $1
	`, hostID, reclaimed, cacheError, success); err != nil {
		return "", fmt.Errorf("record cache prune: %w", err)
	}
	return previous, nil
}

var imageOperationKinds = []string{string(ImageOperationRemove), string(ImageOperationPrune), string(ImageOperationCache)}

type imageOperationPayload struct {
	Targets []ImageTarget    `json:"targets,omitempty"`
	Cache   ImageCachePolicy `json:"cache,omitempty"`
}

func insertImageOperation(ctx context.Context, tx postgres.Tx, id, engineID, actorID string, kind ImageOperationKind, payload imageOperationPayload) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode image operation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO repository_operations (id, repo_id, kind, name, payload, actor_id, engine_id)
		VALUES ($1, NULL, $2, $3, $4, NULLIF($5, ''), $6)
	`, id, string(kind), engineID, b, actorID, engineID); err != nil {
		return fmt.Errorf("queue image operation: %w", err)
	}
	return nil
}

const imageOperationColumns = `
	o.id, o.engine_id, COALESCE(o.worker_id, ''), COALESCE(o.actor_id, ''), COALESCE(p.username, ''),
	o.kind, o.payload, o.created_at, o.claimed_at, o.completed_at, o.attempts, o.error, COALESCE(o.result, '{}'::jsonb)
`

func scanImageOperation(row interface{ Scan(...any) error }) (*ImageOperation, error) {
	var op ImageOperation
	var kind string
	var payload, result []byte
	if err := row.Scan(&op.ID, &op.EngineID, &op.WorkerID, &op.ActorID, &op.Actor, &kind, &payload,
		&op.CreatedAt, &op.ClaimedAt, &op.CompletedAt, &op.Attempts, &op.Error, &result); err != nil {
		return nil, err
	}
	op.Kind = ImageOperationKind(kind)
	var decoded imageOperationPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("decode image operation: %w", err)
	}
	op.Targets, op.Cache = decoded.Targets, decoded.Cache
	var outcome ImageOperationResult
	if len(result) > 0 {
		if err := json.Unmarshal(result, &outcome); err != nil {
			return nil, fmt.Errorf("decode image operation result: %w", err)
		}
	}
	op.Results, op.ReclaimedBytes = outcome.Results, outcome.ReclaimedBytes
	return &op, nil
}

func selectImageOperations(ctx context.Context, q postgres.Querier, limit int) ([]ImageOperation, error) {
	rows, err := q.Query(ctx, `
		SELECT `+imageOperationColumns+`
		FROM repository_operations o
		LEFT JOIN people p ON p.id = o.actor_id
		WHERE o.kind = ANY($1::text[])
		ORDER BY o.created_at DESC, o.id DESC
		LIMIT $2
	`, imageOperationKinds, limit)
	if err != nil {
		return nil, fmt.Errorf("list image operations: %w", err)
	}
	defer rows.Close()
	var operations []ImageOperation
	for rows.Next() {
		op, err := scanImageOperation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan image operation: %w", err)
		}
		operations = append(operations, *op)
	}
	return operations, rows.Err()
}

func selectImageOperation(ctx context.Context, q postgres.Querier, id string) (*ImageOperation, error) {
	op, err := scanImageOperation(q.QueryRow(ctx, `
		SELECT `+imageOperationColumns+`
		FROM repository_operations o
		LEFT JOIN people p ON p.id = o.actor_id
		WHERE o.id = $1
	`, id))
	if err != nil {
		return nil, postgres.NormalizeNotFound(err)
	}
	return op, nil
}

// claimImageOperation atomically leases one host-targeted operation. A lease
// held by a live worker is not stolen. One whose worker stopped, or has not
// heartbeat for five minutes, is reclaimable at once; any lease is reclaimable
// after 30 minutes.
func claimImageOperation(ctx context.Context, tx postgres.Tx, engineID, workerID string) (*ImageOperation, error) {
	op, err := scanImageOperation(tx.QueryRow(ctx, `
		UPDATE repository_operations o
		SET claimed_at = now(), worker_id = $2, attempts = attempts + 1
		WHERE o.id = (
			SELECT o.id
			FROM repository_operations o
			WHERE o.kind = ANY($3::text[])
			  AND o.engine_id = $1
			  AND o.completed_at IS NULL
			  AND NOT (SELECT maintenance FROM instance)
			  AND (o.claimed_at IS NULL
			       OR (o.worker_id IS NULL AND o.claimed_at <= now())
			       OR o.claimed_at < now() - interval '30 minutes'
			       OR (o.worker_id IS NOT NULL AND NOT EXISTS (
			            SELECT 1 FROM workers w
			            WHERE w.id = o.worker_id AND w.stopped_at IS NULL
			              AND w.heartbeat_at >= now() - interval '5 minutes')))
			ORDER BY o.created_at, o.id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING o.id, o.engine_id, COALESCE(o.worker_id, ''), COALESCE(o.actor_id, ''), '',
		          o.kind, o.payload, o.created_at, o.claimed_at, o.completed_at, o.attempts, o.error,
		          COALESCE(o.result, '{}'::jsonb)
	`, engineID, workerID, imageOperationKinds))
	if err != nil {
		return nil, postgres.NormalizeNotFound(err)
	}
	return op, nil
}

// releaseImageOperation returns an operation to the queue with an
// exponential backoff based on its attempt count. A zero delay would make a
// broken daemon or inventory query spin against the exclusive host lock.
func releaseImageOperation(ctx context.Context, q postgres.Querier, id, reason string) error {
	if _, err := q.Exec(ctx, `
		UPDATE repository_operations SET
			claimed_at = now() + make_interval(secs => LEAST(30 * power(2, attempts), 1800)),
			worker_id = NULL,
			error = $2
		WHERE id = $1 AND completed_at IS NULL
	`, id, reason); err != nil {
		return fmt.Errorf("release image operation: %w", err)
	}
	return nil
}

func finishImageOperation(ctx context.Context, tx postgres.Tx, id string, outcome ImageOperationResult, summary string) (bool, error) {
	b, err := json.Marshal(outcome)
	if err != nil {
		return false, fmt.Errorf("encode image operation result: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE repository_operations SET completed_at = now(), result = $2, error = $3
		WHERE id = $1 AND completed_at IS NULL
	`, id, b, summary)
	if err != nil {
		return false, fmt.Errorf("finish image operation: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
