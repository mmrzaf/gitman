package db

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/mmrzaf/gitman/internal/models"
)

var ErrSSHKeyExists = errors.New("SSH key already exists")

// AddSSHKey inserts a new SSH key for a user
func (db *DB) AddSSHKey(ctx context.Context, userID, name, publicKey string) error {
	id := uuid.New().String()
	_, err := db.sql.ExecContext(ctx,
		"INSERT INTO ssh_keys (id, user_id, name, public_key) VALUES (?, ?, ?, ?)",
		id, userID, name, publicKey,
	)
	if err == nil {
		return nil
	}
	// Keep the database constraint as the source of truth without coupling the
	// persistence package to a SQLite-driver-specific error type.
	var one int
	if lookupErr := db.sql.QueryRowContext(ctx, "SELECT 1 FROM ssh_keys WHERE public_key = ?", publicKey).Scan(&one); lookupErr == nil {
		return ErrSSHKeyExists
	}
	return err
}

func (db *DB) GetSSHKeyByID(ctx context.Context, id string) (*models.SSHKey, error) {
	var k models.SSHKey
	var createdAt, updatedAt int64
	err := db.sql.QueryRowContext(ctx,
		"SELECT id, user_id, name, public_key, created_at, updated_at FROM ssh_keys WHERE id = ?",
		id,
	).Scan(&k.ID, &k.UserID, &k.Name, &k.PublicKey, &createdAt, &updatedAt)
	if err != nil {
		return nil, normalizeNotFound(err)
	}
	k.CreatedAt = unixToTime(createdAt)
	k.UpdatedAt = unixToTime(updatedAt)
	return &k, nil
}

// GetUserSSHKeys returns all keys for a specific user
func (db *DB) GetUserSSHKeys(ctx context.Context, userID string) (keys []models.SSHKey, err error) {
	rows, err := db.sql.QueryContext(ctx,
		"SELECT id, user_id, name, public_key, created_at, updated_at FROM ssh_keys WHERE user_id = ? ORDER BY created_at DESC, id ASC",
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, rows.Close())
	}()

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
func (db *DB) GetAllSSHKeys(ctx context.Context) (keys []models.SSHKey, err error) {
	rows, err := db.sql.QueryContext(ctx,
		"SELECT id, user_id, name, public_key, created_at, updated_at FROM ssh_keys ORDER BY user_id ASC, id ASC",
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, rows.Close())
	}()

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
	res, err := db.sql.ExecContext(ctx,
		"DELETE FROM ssh_keys WHERE id = ? AND user_id = ?",
		id, userID,
	)
	if err != nil {
		return err
	}
	return requireAffectedRow(res, ErrNotFound)
}

func (db *DB) DeleteSSHKeyByPublicKey(ctx context.Context, userID, publicKey string) error {
	res, err := db.sql.ExecContext(ctx,
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
		return ErrNotFound
	}
	return nil
}

// RestoreSSHKey reinstates an exact previously-read key row during
// authorized_keys publication rollback. It is intentionally narrow: normal key
// creation must use AddSSHKey so callers cannot choose persistent IDs.
func (db *DB) RestoreSSHKey(ctx context.Context, key *models.SSHKey) error {
	if key == nil || key.ID == "" || key.UserID == "" {
		return errors.New("SSH key snapshot is incomplete")
	}
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO ssh_keys (id, user_id, name, public_key, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, key.ID, key.UserID, key.Name, key.PublicKey, key.CreatedAt.Unix(), key.UpdatedAt.Unix())
	return err
}
