package db

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/mmrzaf/gitman/internal/models"
)

func (db *DB) CreateAccessToken(ctx context.Context, userID, name, tokenHash string) error {
	id := uuid.New().String()
	_, err := db.ExecContext(ctx,
		"INSERT INTO access_tokens (id, user_id, name, token_hash) VALUES (?, ?, ?, ?)",
		id, userID, name, tokenHash,
	)
	return err
}

func (db *DB) GetUserAccessTokens(ctx context.Context, userID string) ([]models.AccessToken, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT id, user_id, name, created_at FROM access_tokens WHERE user_id = ? ORDER BY created_at DESC",
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := rows.Close(); err != nil {
			slog.Info("rows close error: %v", err)
		}
	}()

	var tokens []models.AccessToken
	for rows.Next() {
		var t models.AccessToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.CreatedAt); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, nil
}

func (db *DB) DeleteAccessToken(ctx context.Context, id, userID string) error {
	_, err := db.ExecContext(ctx,
		"DELETE FROM access_tokens WHERE id = ? AND user_id = ?",
		id, userID,
	)
	return err
}

// GetUserByTokenHash looks up the user associated with a SHA256 hashed token
func (db *DB) GetUserByTokenHash(ctx context.Context, tokenHash string) (*models.User, error) {
	var user models.User
	err := db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.password_hash, u.created_at, u.updated_at
		FROM users u
		INNER JOIN access_tokens t ON u.id = t.user_id
		WHERE t.token_hash = ?
	`, tokenHash).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &user, nil
}
