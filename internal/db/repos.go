package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/mmrzaf/gitman/internal/models"
)

// CreateRepository inserts a new repo into the DB and returns its UUID.
func (db *DB) CreateRepository(ctx context.Context, ownerID, name, description string, isPrivate bool) (string, error) {
	id := uuid.New().String()
	query := `INSERT INTO repositories (id, owner_id, name, description, is_private) VALUES (?, ?, ?, ?, ?)`
	_, err := db.sql.ExecContext(ctx, query, id, ownerID, name, description, isPrivate)
	if err != nil {
		if isSQLiteUniqueConstraint(err) {
			return "", ErrAlreadyExists
		}
		return "", err
	}
	return id, nil
}

// GetUserRepositories returns all repos belonging to a specific user
func (db *DB) GetUserRepositories(ctx context.Context, ownerID string) (repos []models.Repository, err error) {
	query := `SELECT id, owner_id, name, COALESCE(description, ''), is_private, created_at, updated_at
			  FROM repositories WHERE owner_id = ? ORDER BY created_at DESC, rowid DESC`
	rows, err := db.sql.QueryContext(ctx, query, ownerID)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, rows.Close())
	}()

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
	return repos, rows.Err()
}

func (db *DB) GetAllRepositories(ctx context.Context) (repos []models.Repository, err error) {
	query := `SELECT id, owner_id, name, COALESCE(description, ''), is_private, created_at, updated_at
		FROM repositories ORDER BY owner_id ASC, name ASC`
	rows, err := db.sql.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	for rows.Next() {
		var repo models.Repository
		var createdAt, updatedAt int64
		if err := rows.Scan(&repo.ID, &repo.OwnerID, &repo.Name, &repo.Description, &repo.IsPrivate, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		repo.CreatedAt = unixToTime(createdAt)
		repo.UpdatedAt = unixToTime(updatedAt)
		repos = append(repos, repo)
	}
	return repos, rows.Err()
}

// GetRepositoryByOwnerAndName fetches a repository by its name, with given owner
func (db *DB) GetRepositoryByOwnerAndName(ctx context.Context, ownerID string, name string) (*models.Repository, error) {
	query := `SELECT id, owner_id, name, COALESCE(description, ''), is_private, created_at, updated_at
              FROM repositories
              WHERE owner_id = ? AND name = ? LIMIT 1`

	var r models.Repository
	var createdAt, updatedAt int64
	err := db.sql.QueryRowContext(ctx, query, ownerID, name).
		Scan(&r.ID, &r.OwnerID, &r.Name, &r.Description, &r.IsPrivate, &createdAt, &updatedAt)
	if err != nil {
		return nil, normalizeNotFound(err)
	}
	r.CreatedAt = unixToTime(createdAt)
	r.UpdatedAt = unixToTime(updatedAt)
	return &r, nil
}

// GetRepositoryByID fetches a single repo by its ID
func (db *DB) GetRepositoryByID(ctx context.Context, id string) (*models.Repository, error) {
	query := `SELECT id, owner_id, name, COALESCE(description, ''), is_private, created_at, updated_at
			  FROM repositories WHERE id = ?`
	row := db.sql.QueryRowContext(ctx, query, id)

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
		return nil, normalizeNotFound(err)
	}
	r.CreatedAt = unixToTime(createdAt)
	r.UpdatedAt = unixToTime(updatedAt)
	return &r, nil
}

// DeleteRepository removes an idle repository from the database. The active
// CI condition is part of the DELETE statement so a worker claim racing the
// caller's earlier safety check cannot orphan attempt-scoped files.
func (db *DB) DeleteRepository(ctx context.Context, id, ownerID string) (bool, error) {
	res, err := db.sql.ExecContext(ctx, `
		DELETE FROM repositories
		WHERE id = ? AND owner_id = ?
		  AND NOT EXISTS (
			SELECT 1 FROM ci_runs
			WHERE repo_id = repositories.id
			  AND (
				status IN ('pending', 'running')
				OR (status = 'cancelled' AND heartbeat_at IS NOT NULL)
			  )
		  )
	`, id, ownerID)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	return rows > 0, err
}

func (db *DB) UpdateRepositorySettings(ctx context.Context, id, ownerID, description string, isPrivate bool) error {
	res, err := db.sql.ExecContext(ctx, `
		UPDATE repositories
		SET description = ?, is_private = ?, updated_at = strftime('%s', 'now')
		WHERE id = ? AND owner_id = ?
	`, description, isPrivate, id, ownerID)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// AddCollaborator adds or updates a collaborator's access level.
func (db *DB) AddCollaborator(ctx context.Context, repoID, userID string, accessLevel models.AccessLevel) error {
	if !accessLevel.Valid() {
		return fmt.Errorf("invalid repository access level %q", accessLevel)
	}
	query := `
		INSERT INTO repo_collaborators (repo_id, user_id, access_level)
		VALUES (?, ?, ?)
		ON CONFLICT(repo_id, user_id)
		DO UPDATE SET access_level=excluded.access_level;
	`
	_, err := db.sql.ExecContext(ctx, query, repoID, userID, accessLevel)
	return err
}

// RemoveCollaborator removes a user's access from a repository.
func (db *DB) RemoveCollaborator(ctx context.Context, repoID, userID string) error {
	query := `DELETE FROM repo_collaborators WHERE repo_id = ? AND user_id = ?`
	res, err := db.sql.ExecContext(ctx, query, repoID, userID)
	if err != nil {
		return err
	}
	return requireAffectedRow(res, ErrNotFound)
}

// GetCollaborators retrieves all collaborators for a given repository.
func (db *DB) GetCollaborators(ctx context.Context, repoID string) (collaborators []models.Collaborator, err error) {
	query := `
		SELECT u.id, u.username, rc.access_level, rc.created_at
		FROM repo_collaborators rc
		JOIN users u ON rc.user_id = u.id
		WHERE rc.repo_id = ?
		ORDER BY rc.created_at ASC
	`
	rows, err := db.sql.QueryContext(ctx, query, repoID)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, rows.Close())
	}()

	for rows.Next() {
		var c models.Collaborator
		var u models.User
		var createdAt int64
		if err := rows.Scan(&u.ID, &u.Username, &c.AccessLevel, &createdAt); err != nil {
			return nil, err
		}
		if !c.AccessLevel.Valid() {
			return nil, fmt.Errorf("invalid stored repository access level %q", c.AccessLevel)
		}
		c.CreatedAt = unixToTime(createdAt)
		c.User = u
		collaborators = append(collaborators, c)
	}
	return collaborators, rows.Err()
}

// GetRepoAccessLevel returns the explicit collaborator access level for a user.
// Repository ownership and public visibility are authorization concerns and are
// deliberately handled outside persistence. Absence is reported as ErrNotFound.
func (db *DB) GetRepoAccessLevel(ctx context.Context, repoID, userID string) (models.AccessLevel, error) {
	var level models.AccessLevel
	query := `SELECT access_level FROM repo_collaborators WHERE repo_id = ? AND user_id = ? LIMIT 1`
	if err := db.sql.QueryRowContext(ctx, query, repoID, userID).Scan(&level); err != nil {
		return "", normalizeNotFound(err)
	}
	if !level.Valid() {
		return "", fmt.Errorf("invalid stored repository access level %q", level)
	}
	return level, nil
}

// RepositoryLocation is the minimal repository identity needed by operator
// maintenance tasks that walk repositories on disk.
type RepositoryLocation struct {
	Owner string
	Name  string
}

// ListRepositoryLocations returns every repository with its owning username.
func (db *DB) ListRepositoryLocations(ctx context.Context) (locations []RepositoryLocation, err error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT u.username, r.name
		FROM repositories r
		JOIN users u ON u.id = r.owner_id
		ORDER BY u.username, r.name
	`)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	for rows.Next() {
		var location RepositoryLocation
		if err := rows.Scan(&location.Owner, &location.Name); err != nil {
			return nil, err
		}
		locations = append(locations, location)
	}
	return locations, rows.Err()
}
