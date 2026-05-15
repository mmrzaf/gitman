package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/mmrzaf/gitman/internal/models"
)

func GenerateSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (db *DB) CreateSession(ctx context.Context, userID string) (string, error) {
	token, err := GenerateSessionToken()
	if err != nil {
		return "", err
	}
	expiresAt := time.Now().Add(24 * time.Hour).Unix()

	_, err = db.ExecContext(ctx,
		"INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)",
		token, userID, expiresAt,
	)
	return token, err
}

func (db *DB) GetUserBySession(ctx context.Context, token string) (*models.User, error) {
	var user models.User
	var createdAt, updatedAt int64

	query := `
		SELECT u.id, u.username, u.created_at, u.updated_at
		FROM users u
		JOIN sessions s ON u.id = s.user_id
		WHERE s.token = ? AND s.expires_at > ?
	`

	if err := db.QueryRowContext(ctx, query, token, time.Now().Unix()).
		Scan(&user.ID, &user.Username, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	user.CreatedAt = unixToTime(createdAt)
	user.UpdatedAt = unixToTime(updatedAt)

	return &user, nil
}

func (db *DB) DeleteSession(ctx context.Context, token string) error {
	_, err := db.ExecContext(ctx, "DELETE FROM sessions WHERE token = ?", token)
	return err
}

func (db *DB) ExtendSession(ctx context.Context, token string, duration time.Duration) error {
	newExpires := time.Now().Add(duration).Unix()
	_, err := db.ExecContext(ctx, "UPDATE sessions SET expires_at = ? WHERE token = ?", newExpires, token)
	return err
}
