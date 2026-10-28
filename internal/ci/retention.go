package ci

import (
	"context"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
)

// imageHostRetention is how long a decommissioned host inventory is kept.
const imageHostRetention = 30 * 24 * time.Hour

// PruneRetention keeps log storage independent from visible run metadata. Only
// current-head summaries and the latest deployment of each target survive age.
func (s *Service) PruneRetention(ctx context.Context, now time.Time, p config.Retention) error {
	for {
		tag, err := s.db.Q.Exec(ctx, `WITH expired AS (SELECT s.id FROM steps s JOIN runs r ON r.id=s.run_id WHERE r.finished_at<$1 AND s.logs_expired_at IS NULL AND (s.container_name IS NULL OR s.container_removed_at IS NOT NULL) LIMIT 50), removed AS (DELETE FROM step_logs WHERE step_id IN (SELECT id FROM expired)) UPDATE steps SET logs_expired_at=now() WHERE id IN (SELECT id FROM expired)`, now.AddDate(0, 0, -p.Logs))
		if err != nil {
			return err
		}
		if tag.RowsAffected() < 50 {
			break
		}
	}
	if _, err := s.PruneRuns(ctx, now.AddDate(0, 0, -p.Runs)); err != nil {
		return err
	}
	imageBefore := now.Add(-imageHostRetention)
	if _, err := s.db.Q.Exec(ctx, `UPDATE repository_operations SET completed_at=now(), error='Image host inventory expired before the operation ran.' WHERE completed_at IS NULL AND repo_id IS NULL AND engine_id IN (SELECT host_id FROM image_hosts WHERE collected_at < $1)`, imageBefore); err != nil {
		return err
	}
	if _, err := s.db.Q.Exec(ctx, `DELETE FROM image_hosts WHERE collected_at < $1 AND NOT EXISTS (SELECT 1 FROM repository_operations o WHERE o.engine_id = image_hosts.host_id AND o.completed_at IS NULL)`, imageBefore); err != nil {
		return err
	}
	auditBefore := now.AddDate(0, 0, -p.Audit)
	for _, table := range []string{"pushes", "push_refusals", "events", "repository_operations"} {
		predicate := "created_at < $1"
		if table == "repository_operations" {
			predicate = "completed_at < $1"
		}
		for {
			tag, err := s.db.Q.Exec(ctx, `DELETE FROM `+table+` WHERE id IN (SELECT id FROM `+table+` WHERE `+predicate+` ORDER BY id LIMIT 500)`, auditBefore)
			if err != nil {
				return err
			}
			if tag.RowsAffected() < 500 {
				break
			}
		}
	}
	for {
		tag, err := s.db.Q.Exec(ctx, `DELETE FROM deployments WHERE id IN (SELECT d.id FROM deployments d WHERE d.created_at<$1 AND d.id NOT IN (SELECT DISTINCT ON(repo_id,target) id FROM deployments ORDER BY repo_id,target,created_at DESC,id DESC) LIMIT 500)`, now.AddDate(0, 0, -p.Deployments))
		if err != nil {
			return err
		}
		if tag.RowsAffected() < 500 {
			break
		}
	}
	// Immutable metadata only needs to cover indexed heads. Retained history
	// reads Git directly and does not need a duplicate SQL cache.
	_, err := s.db.Q.Exec(ctx, `DELETE FROM commit_metadata m WHERE NOT EXISTS(SELECT 1 FROM refs WHERE repo_id=m.repo_id AND commit_hash=m.hash)`)
	return err
}
