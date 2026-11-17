package repo

import (
	"context"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/postgres"
)

// ListReadablePage applies access and keyset pagination before reading metadata.
func (s *Service) ListReadablePage(ctx context.Context, personID string, admin bool, after string, limit int) ([]*Repo, error) {
	limit = max(1, min(limit, 100))
	rows, err := s.db.Q.Query(ctx, `SELECT `+repoColumns+` FROM repos WHERE name>$3 AND ($2 OR visibility='everyone' OR EXISTS(SELECT 1 FROM repo_readers WHERE repo_id=repos.id AND person_id=$1)) ORDER BY name LIMIT $4`, personID, admin, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// LastActivityForRepos reports the most recent meaningful work for each
// repository. Home uses this to show active projects first, without a
// configurable sort order or a new database column.
func (s *Service) LastActivityForRepos(ctx context.Context, repoIDs []string) (map[string]time.Time, error) {
	rows, err := s.db.Q.Query(ctx, `
        SELECT repos.id, GREATEST(
            repos.created_at,
            COALESCE((SELECT MAX(updated_at) FROM refs WHERE repo_id = repos.id), repos.created_at),
            COALESCE((SELECT MAX(created_at) FROM pushes WHERE repo_id = repos.id), repos.created_at),
            COALESCE((SELECT MAX(queued_at) FROM runs WHERE repo_id = repos.id), repos.created_at),
            COALESCE((SELECT MAX(created_at) FROM deployments WHERE repo_id = repos.id), repos.created_at)
        )
        FROM repos WHERE id = ANY($1)
    `, repoIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	activity := make(map[string]time.Time, len(repoIDs))
	for rows.Next() {
		var id string
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		activity[id] = at
	}
	return activity, rows.Err()
}

func storeCommitMetadata(ctx context.Context, tx postgres.Tx, repoID string, hashes []string, read func(string) (string, string, string, time.Time, error)) error {
	rows, err := tx.Query(ctx, `SELECT hash FROM commit_metadata WHERE repo_id=$1 AND hash=ANY($2)`, repoID, hashes)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return err
		}
		known[h] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, h := range hashes {
		if known[h] {
			continue
		}
		subject, name, email, at, err := read(h)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO commit_metadata(repo_id,hash,subject,author_name,author_email,authored_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(repo_id,hash) DO NOTHING`, repoID, h, metadataText(subject), metadataText(name), metadataText(email), validMetadataTime(at)); err != nil {
			return err
		}
		known[h] = true
	}
	return nil
}

func validMetadataTime(at time.Time) *time.Time {
	if at.Year() < 1 || at.Year() > 9999 {
		return nil
	}
	return &at
}

// PostgreSQL text excludes NUL and invalid UTF-8, both permitted in raw Git data.
func metadataText(value string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(value, "�"), "\x00", "�")
}
