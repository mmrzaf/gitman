package db

import (
	"context"
	"database/sql"
	"errors"

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
	_, err = db.ExecContext(
		ctx,
		"INSERT INTO users (id, username, password_hash) VALUES (?, ?, ?)",
		id, username, string(hash),
	)
	if err != nil {
		return nil, err
	}

	return db.GetUserByID(ctx, id)
}

// GetUserByUsername fetches a user for login
func (db *DB) GetUserByUsername(ctx context.Context, username string) (*models.User, error) {
	var user models.User
	err := db.QueryRowContext(ctx,
		"SELECT id, username, password_hash, created_at, updated_at FROM users WHERE username = ?",
		username,
	).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // User not found
		}
		return nil, err
	}
	return &user, nil
}

func (db *DB) GetUserByID(ctx context.Context, id string) (*models.User, error) {
	var user models.User
	err := db.QueryRowContext(ctx,
		"SELECT id, username, password_hash, created_at, updated_at FROM users WHERE id = ?",
		id,
	).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// VerifyPassword checks if the provided password matches the hash
func VerifyPassword(hashedPassword, password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
	return err == nil
}
