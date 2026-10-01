package repo

import (
	"context"
	"github.com/mmrzaf/gitman/internal/postgres"
	"strings"
	"time"
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
		if _, err := tx.Exec(ctx, `INSERT INTO commit_metadata(repo_id,hash,subject,author_name,author_email,authored_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(repo_id,hash) DO NOTHING`, repoID, h, strings.ToValidUTF8(subject, "�"), strings.ToValidUTF8(name, "�"), strings.ToValidUTF8(email, "�"), validMetadataTime(at)); err != nil {
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
