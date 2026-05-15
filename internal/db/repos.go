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
		if closeErr := rows.Close(); closeErr != nil {
			slog.Warn("rows close error", "error", closeErr)
		}
	}()

	var repos []models.Repository
	for rows.Next() {
		var r models.Repository
		var createdAt, updatedAt int64
		if err := rows.Scan(
			&r.ID,
			&r.OwnerID,
			&r.Name,
			&r.Description,
			&r.IsPrivate,
			&createdAt,
			&updatedAt,
		); err != nil {
			return nil, err
		}
		r.CreatedAt = unixToTime(createdAt)
		r.UpdatedAt = unixToTime(updatedAt)
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
	var createdAt int64
	err := db.QueryRowContext(ctx, query, ownerID, name).
		Scan(&r.ID, &r.OwnerID, &r.Name, &r.Description, &r.IsPrivate, &createdAt)
	if err != nil {
		return nil, err
	}
	r.CreatedAt = unixToTime(createdAt)
	return &r, nil
}

// GetRepositoryByID fetches a single repo by its ID
func (db *DB) GetRepositoryByID(ctx context.Context, id string) (*models.Repository, error) {
	query := `SELECT id, owner_id, name, description, is_private, created_at, updated_at
			  FROM repositories WHERE id = ?`
	row := db.QueryRowContext(ctx, query, id)

	var r models.Repository
	var createdAt, updatedAt int64
	err := row.Scan(
		&r.ID,
		&r.OwnerID,
		&r.Name,
		&r.Description,
		&r.IsPrivate,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return nil, err
	}
	r.CreatedAt = unixToTime(createdAt)
	r.UpdatedAt = unixToTime(updatedAt)
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
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			slog.Warn("rows close error", "error", closeErr)
		}
	}()

	var collaborators []models.Collaborator
	for rows.Next() {
		var c models.Collaborator
		var u models.User
		var createdAt int64
		if err := rows.Scan(&u.ID, &u.Username, &c.AccessLevel, &createdAt); err != nil {
			return nil, err
		}
		c.CreatedAt = unixToTime(createdAt)
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

	switch requiredLevel {
	case "read":
		return level == "read" || level == "write", nil
	case "write":
		return level == "write", nil
	default:
		return false, nil
	}
}

func (db *DB) SetWebhookSecret(ctx context.Context, repoID, secret string) error {
	_, err := db.ExecContext(ctx, "UPDATE repositories SET webhook_secret = ? WHERE id = ?", secret, repoID)
	return err
}

// GetRepositoryByWebhookSecret fetches a single repo by its webhook secret
func (db *DB) GetRepositoryByWebhookSecret(ctx context.Context, secret string) (*models.Repository, error) {
	query := `SELECT id, owner_id, name, description, is_private, created_at, updated_at
			  FROM repositories WHERE webhook_secret = ?`
	row := db.QueryRowContext(ctx, query, secret)

	var r models.Repository
	var createdAt, updatedAt int64
	err := row.Scan(
		&r.ID,
		&r.OwnerID,
		&r.Name,
		&r.Description,
		&r.IsPrivate,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return nil, err
	}
	r.CreatedAt = unixToTime(createdAt)
	r.UpdatedAt = unixToTime(updatedAt)
	return &r, nil
}
