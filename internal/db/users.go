package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/mmrzaf/gitman/internal/models"
	"golang.org/x/crypto/bcrypt"
)

// CreateUser hashes the password and saves the new user with a UUID primary key.
func (db *DB) CreateUser(ctx context.Context, username, password string) (*models.User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	id := uuid.New().String()
	_, err = db.sql.ExecContext(
		ctx,
		"INSERT INTO users (id, username, password_hash) VALUES (?, ?, ?)",
		id, username, string(hash),
	)
	if err != nil {
		if isSQLiteUniqueConstraint(err) {
			return nil, ErrAlreadyExists
		}
		return nil, err
	}

	return db.GetUserByID(ctx, id)
}

// GetUserByUsername fetches a user for login
func (db *DB) GetUserByUsername(ctx context.Context, username string) (*models.User, error) {
	var user models.User
	var createdAt, updatedAt int64
	err := db.sql.QueryRowContext(ctx,
		"SELECT id, username, password_hash, created_at, updated_at FROM users WHERE username = ?",
		username,
	).Scan(&user.ID, &user.Username, &user.PasswordHash, &createdAt, &updatedAt)
	if err != nil {
		return nil, normalizeNotFound(err)
	}
	user.CreatedAt = unixToTime(createdAt)
	user.UpdatedAt = unixToTime(updatedAt)
	return &user, nil
}

func (db *DB) GetUserByID(ctx context.Context, id string) (*models.User, error) {
	var user models.User
	var createdAt, updatedAt int64
	err := db.sql.QueryRowContext(ctx,
		"SELECT id, username, password_hash, created_at, updated_at FROM users WHERE id = ?",
		id,
	).Scan(&user.ID, &user.Username, &user.PasswordHash, &createdAt, &updatedAt)
	if err != nil {
		return nil, normalizeNotFound(err)
	}
	user.CreatedAt = unixToTime(createdAt)
	user.UpdatedAt = unixToTime(updatedAt)
	return &user, nil
}

// VerifyPassword distinguishes an ordinary credential mismatch from a corrupt
// or unsupported stored password hash. Authentication code must not turn
// persistence corruption into a fake "wrong password" response.
func VerifyPassword(hashedPassword, password string) (bool, error) {
	err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, bcrypt.ErrMismatchedHashAndPassword):
		return false, nil
	default:
		return false, err
	}
}

// UpdateUserPassword hashes a new password and updates it for the given username
func (db *DB) UpdateUserPassword(ctx context.Context, username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	tx, err := db.sql.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var userID string
	err = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE username = ?", username).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	res, err := tx.ExecContext(
		ctx,
		"UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?",
		string(hash), time.Now().Unix(), userID,
	)
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

	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM access_tokens WHERE user_id = ?", userID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteUserByID removes one exact logical user only when none of the user's
// repositories has CI work still owned by a worker. The active-run condition is
// part of the DELETE statement so a worker claim racing an earlier caller-side
// check cannot lose its durable run through the users -> repositories cascade.
//
// ErrNotFound means this exact immutable user no longer exists. ErrActiveCIRuns
// means the user still exists but deletion is currently unsafe.
func (db *DB) DeleteUserByID(ctx context.Context, userID string) error {
	res, err := db.sql.ExecContext(ctx, `
		DELETE FROM users
		WHERE id = ?
		  AND NOT EXISTS (
			SELECT 1
			FROM repositories r
			JOIN ci_runs c ON c.repo_id = r.id
			WHERE r.owner_id = users.id
			  AND (
				c.status IN ('pending', 'running')
				OR (c.status = 'cancelled' AND c.heartbeat_at IS NOT NULL)
			  )
		  )
	`, userID)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows > 0 {
		return nil
	}

	// Distinguish an already-deleted identity from an existing identity whose
	// active CI made the guarded DELETE a no-op. A concurrent successful delete
	// between the two statements correctly resolves to ErrNotFound.
	var one int
	err = db.sql.QueryRowContext(ctx, "SELECT 1 FROM users WHERE id = ?", userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return ErrActiveCIRuns
}
