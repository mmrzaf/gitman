package db

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/google/uuid"
	"github.com/mmrzaf/gitman/internal/models"
)

// CreateRepository inserts a new repo into the DB and returns its UUID.
func (db *DB) CreateRepository(ctx context.Context, ownerID, name, description string, isPrivate bool) (string, error) {
	id := uuid.New().String()
	query := `INSERT INTO repositories (id, owner_id, name, description, is_private) VALUES (?, ?, ?, ?, ?)`
	_, err := db.ExecContext(ctx, query, id, ownerID, name, description, isPrivate)
	if err != nil {
		return "", err
	}
	return id, nil
}

// GetUserRepositories returns all repos belonging to a specific user
func (db *DB) GetUserRepositories(ctx context.Context, ownerID string) ([]models.Repository, error) {
	query := `SELECT id, owner_id, name, description, is_private, created_at, updated_at
			  FROM repositories WHERE owner_id = ? ORDER BY created_at DESC`
	rows, err := db.QueryContext(ctx, query, ownerID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := rows.Close(); err != nil {
			slog.Info("rows close error: %v", err)
		}
	}()

	var repos []models.Repository
	for rows.Next() {
		var r models.Repository
		if err := rows.Scan(
			&r.ID,
			&r.OwnerID,
			&r.Name,
			&r.Description,
			&r.IsPrivate,
			&r.CreatedAt,
			&r.UpdatedAt,
		); err != nil {
			return nil, err
		}
		repos = append(repos, r)
	}
	return repos, nil
}

// GetRepositoryByOwnerAndName fetches a repository by its name, with given owner
func (db *DB) GetRepositoryByOwnerAndName(ctx context.Context, ownerID string, name string) (*models.Repository, error) {
	query := `SELECT id, owner_id, name, description, is_private, created_at
              FROM repositories
              WHERE owner_id = ? AND name = ? LIMIT 1`

	var r models.Repository
	err := db.QueryRowContext(ctx, query, ownerID, name).
		Scan(&r.ID, &r.OwnerID, &r.Name, &r.Description, &r.IsPrivate, &r.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// GetRepositoryByID fetches a single repo by its ID
func (db *DB) GetRepositoryByID(ctx context.Context, id string) (*models.Repository, error) {
	query := `SELECT id, owner_id, name, description, is_private, created_at, updated_at
			  FROM repositories WHERE id = ?`
	row := db.QueryRowContext(ctx, query, id)

	var r models.Repository
	err := row.Scan(
		&r.ID,
		&r.OwnerID,
		&r.Name,
		&r.Description,
		&r.IsPrivate,
		&r.CreatedAt,
		&r.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// DeleteRepository removes a repo from the DB
func (db *DB) DeleteRepository(ctx context.Context, id, ownerID string) error {
	query := `DELETE FROM repositories WHERE id = ? AND owner_id = ?`
	_, err := db.ExecContext(ctx, query, id, ownerID)
	return err
}

// AddCollaborator adds or updates a collaborator's access level.
func (db *DB) AddCollaborator(ctx context.Context, repoID, userID, accessLevel string) error {
	query := `
		INSERT INTO repo_collaborators (repo_id, user_id, access_level)
		VALUES (?, ?, ?)
		ON CONFLICT(repo_id, user_id)
		DO UPDATE SET access_level=excluded.access_level;
	`
	_, err := db.ExecContext(ctx, query, repoID, userID, accessLevel)
	return err
}

// RemoveCollaborator removes a user's access from a repository.
func (db *DB) RemoveCollaborator(ctx context.Context, repoID, userID string) error {
	query := `DELETE FROM repo_collaborators WHERE repo_id = ? AND user_id = ?`
	_, err := db.ExecContext(ctx, query, repoID, userID)
	return err
}

// GetCollaborators retrieves all collaborators for a given repository.
func (db *DB) GetCollaborators(ctx context.Context, repoID string) ([]models.Collaborator, error) {
	query := `
		SELECT u.id, u.username, rc.access_level, rc.created_at
		FROM repo_collaborators rc
		JOIN users u ON rc.user_id = u.id
		WHERE rc.repo_id = ?
		ORDER BY rc.created_at ASC
	`
	rows, err := db.QueryContext(ctx, query, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var collaborators []models.Collaborator
	for rows.Next() {
		var c models.Collaborator
		var u models.User
		if err := rows.Scan(&u.ID, &u.Username, &c.AccessLevel, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.User = u
		collaborators = append(collaborators, c)
	}
	return collaborators, nil
}

// HasRepoAccess checks if a user has the required access level for a repository.
func (db *DB) HasRepoAccess(ctx context.Context, repoID, userID, requiredLevel string) (bool, error) {
	var level string
	query := `SELECT access_level FROM repo_collaborators WHERE repo_id = ? AND user_id = ? LIMIT 1`
	err := db.QueryRowContext(ctx, query, repoID, userID).Scan(&level)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}

	if requiredLevel == "read" {
		return level == "read" || level == "write", nil
	} else if requiredLevel == "write" {
		return level == "write", nil
	}

	return false, nil
}
