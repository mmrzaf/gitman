package db

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/mmrzaf/gitman/internal/models"
)

var ErrSSHKeyExists = errors.New("SSH key already exists")

// AddSSHKey inserts a new SSH key for a user
func (db *DB) AddSSHKey(ctx context.Context, userID, name, publicKey string) error {
	id := uuid.New().String()
	res, err := db.ExecContext(ctx, `
		INSERT INTO ssh_keys (id, user_id, name, public_key)
		SELECT ?, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM ssh_keys WHERE public_key = ?)
	`, id, userID, name, publicKey, publicKey,
	)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrSSHKeyExists
	}
	return nil
}

func (db *DB) GetSSHKeyByID(ctx context.Context, id string) (*models.SSHKey, error) {
	var k models.SSHKey
	var createdAt, updatedAt int64
	err := db.QueryRowContext(ctx,
		"SELECT id, user_id, name, public_key, created_at, updated_at FROM ssh_keys WHERE id = ?",
		id,
	).Scan(&k.ID, &k.UserID, &k.Name, &k.PublicKey, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	k.CreatedAt = unixToTime(createdAt)
	k.UpdatedAt = unixToTime(updatedAt)
	return &k, nil
}

// GetUserSSHKeys returns all keys for a specific user
func (db *DB) GetUserSSHKeys(ctx context.Context, userID string) ([]models.SSHKey, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT id, user_id, name, public_key, created_at, updated_at FROM ssh_keys WHERE user_id = ? ORDER BY created_at DESC, id ASC",
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			slog.Warn("rows close error", "error", closeErr)
		}
	}()

	var keys []models.SSHKey
	for rows.Next() {
		var k models.SSHKey
		var createdAt, updatedAt int64
		if err := rows.Scan(&k.ID, &k.UserID, &k.Name, &k.PublicKey, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		k.CreatedAt = unixToTime(createdAt)
		k.UpdatedAt = unixToTime(updatedAt)
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// GetAllSSHKeys returns all keys in the system (useful for writing the authorized_keys file)
func (db *DB) GetAllSSHKeys(ctx context.Context) ([]models.SSHKey, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT id, user_id, name, public_key, created_at, updated_at FROM ssh_keys ORDER BY user_id ASC, id ASC",
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			slog.Warn("rows close error", "error", closeErr)
		}
	}()

	var keys []models.SSHKey
	for rows.Next() {
		var k models.SSHKey
		var createdAt, updatedAt int64
		if err := rows.Scan(&k.ID, &k.UserID, &k.Name, &k.PublicKey, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		k.CreatedAt = unixToTime(createdAt)
		k.UpdatedAt = unixToTime(updatedAt)
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (db *DB) DeleteSSHKey(ctx context.Context, id, userID string) error {
	_, err := db.ExecContext(ctx,
		"DELETE FROM ssh_keys WHERE id = ? AND user_id = ?",
		id, userID,
	)
	return err
}

func (db *DB) DeleteSSHKeyByPublicKey(ctx context.Context, userID, publicKey string) error {
	res, err := db.ExecContext(ctx,
		"DELETE FROM ssh_keys WHERE user_id = ? AND public_key = ?",
		userID, publicKey,
	)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}
