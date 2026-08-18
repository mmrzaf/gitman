package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/db"
)

type repoInfo struct {
	id   string
	name string
}

func resolveRepo(ctx context.Context, database *db.DB, repoID string) (*repoInfo, string, error) {
	repo, err := database.GetRepositoryByID(ctx, repoID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, "", fmt.Errorf("repository %s: %w", repoID, db.ErrNotFound)
		}
		return nil, "", fmt.Errorf("load repository %s: %w", repoID, err)
	}
	owner, err := database.GetUserByID(ctx, repo.OwnerID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, "", fmt.Errorf("owner for repository %s: %w", repoID, db.ErrNotFound)
		}
		return nil, "", fmt.Errorf("load owner for repository %s: %w", repoID, err)
	}
	return &repoInfo{id: repo.ID, name: repo.Name}, owner.Username, nil
}

func selectRef(branch, tag string) string {
	if tag != "" {
		return tag
	}
	if branch != "" {
		return branch
	}
	return "HEAD"
}

func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func safeContainerID(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 48 {
		out = out[:48]
	}
	if out == "" {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return out
}
