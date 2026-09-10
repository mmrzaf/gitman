package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mmrzaf/gitman/internal/models"
)

func (db *DB) CreateAccessToken(ctx context.Context, userID, name, tokenHash string, expiresAt *time.Time) (string, error) {
	return db.CreateAccessTokenWithScope(ctx, userID, name, tokenHash, models.AccessTokenScopeRepoRead, expiresAt)
}

func (db *DB) CreateAccessTokenWithScope(ctx context.Context, userID, name, tokenHash string, scope models.AccessTokenScope, expiresAt *time.Time) (string, error) {
	if expiresAt == nil {
		return "", errors.New("access token expiration is required")
	}
	if !scope.Valid() {
		return "", fmt.Errorf("invalid access token scope %q", scope)
	}
	id := uuid.New().String()
	expiresAtUnix := expiresAt.Unix()
	_, err := db.sql.ExecContext(ctx,
		"INSERT INTO access_tokens (id, user_id, name, token_hash, scope, expires_at) VALUES (?, ?, ?, ?, ?, ?)",
		id, userID, name, tokenHash, scope, expiresAtUnix,
	)
	if err != nil {
		return "", err
	}
	return id, nil
}

func (db *DB) GetUserAccessTokens(ctx context.Context, userID string) (tokens []models.AccessToken, err error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, user_id, name, scope, created_at, expires_at, last_used_at
		FROM access_tokens
		WHERE user_id = ?
		ORDER BY created_at DESC, rowid DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, rows.Close())
	}()

	for rows.Next() {
		var t models.AccessToken
		var scope string
		var createdAt int64
		var expiresAt, lastUsedAt sql.NullInt64
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &scope, &createdAt, &expiresAt, &lastUsedAt); err != nil {
			return nil, err
		}
		t.Scope = models.AccessTokenScope(scope)
		if !t.Scope.Valid() {
			return nil, fmt.Errorf("access token %s has invalid scope %q", t.ID, scope)
		}
		t.CreatedAt = unixToTime(createdAt)
		if expiresAt.Valid {
			value := unixToTime(expiresAt.Int64)
			t.ExpiresAt = &value
		}
		if lastUsedAt.Valid {
			value := unixToTime(lastUsedAt.Int64)
			t.LastUsedAt = &value
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

func (db *DB) DeleteAccessToken(ctx context.Context, id, userID string) error {
	res, err := db.sql.ExecContext(ctx,
		"DELETE FROM access_tokens WHERE id = ? AND user_id = ?",
		id, userID,
	)
	if err != nil {
		return err
	}
	return requireAffectedRow(res, ErrNotFound)
}

// AuthenticateAccessToken resolves any currently-valid personal access token.
// Callers serving an external capability should prefer the scope-aware variant.
func (db *DB) AuthenticateAccessToken(ctx context.Context, tokenHash string) (*models.User, error) {
	required := models.AccessTokenScopeRepoRead
	return db.authenticateAccessToken(ctx, tokenHash, nil, &required)
}

func (db *DB) AuthenticateAccessTokenWithScope(ctx context.Context, tokenHash string, requiredScope models.AccessTokenScope) (*models.User, error) {
	if !requiredScope.Valid() {
		return nil, fmt.Errorf("invalid required access token scope %q", requiredScope)
	}
	return db.authenticateAccessToken(ctx, tokenHash, nil, &requiredScope)
}

func (db *DB) AuthenticateAccessTokenForUser(ctx context.Context, tokenHash, username string) (*models.User, error) {
	required := models.AccessTokenScopeRepoRead
	return db.authenticateAccessToken(ctx, tokenHash, &username, &required)
}

func (db *DB) AuthenticateAccessTokenForUserWithScope(ctx context.Context, tokenHash, username string, requiredScope models.AccessTokenScope) (*models.User, error) {
	if !requiredScope.Valid() {
		return nil, fmt.Errorf("invalid required access token scope %q", requiredScope)
	}
	return db.authenticateAccessToken(ctx, tokenHash, &username, &requiredScope)
}

func (db *DB) authenticateAccessToken(ctx context.Context, tokenHash string, expectedUsername *string, requiredScope *models.AccessTokenScope) (*models.User, error) {
	now := time.Now().Unix()
	tx, err := db.sql.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var user models.User
	var createdAt, updatedAt int64
	var lastUsedAt sql.NullInt64
	var scopeRaw string
	query := `
		SELECT u.id, u.username, u.password_hash, u.created_at, u.updated_at, t.last_used_at, t.scope
		FROM users u
		INNER JOIN access_tokens t ON u.id = t.user_id
		WHERE t.token_hash = ?
		  AND t.expires_at > ?`
	args := []any{tokenHash, now}
	if expectedUsername != nil {
		query += " AND u.username = ?"
		args = append(args, *expectedUsername)
	}
	err = tx.QueryRowContext(ctx, query, args...).Scan(&user.ID, &user.Username, &user.PasswordHash, &createdAt, &updatedAt, &lastUsedAt, &scopeRaw)
	if err != nil {
		return nil, normalizeNotFound(err)
	}
	scope := models.AccessTokenScope(scopeRaw)
	if !scope.Valid() {
		return nil, fmt.Errorf("access token has invalid scope %q", scopeRaw)
	}
	if requiredScope != nil && !models.AccessTokenScopeAllows(scope, *requiredScope) {
		return nil, ErrNotFound
	}

	// Keep last-used information useful without turning every authenticated Git
	// HTTP request into a SQLite write. Five-minute precision is enough for the
	// credential-management UI.
	if !lastUsedAt.Valid || lastUsedAt.Int64 < now-300 {
		res, err := tx.ExecContext(ctx, `
			UPDATE access_tokens
			SET last_used_at = ?
			WHERE token_hash = ?
			  AND expires_at > ?
		`, now, tokenHash, now)
		if err != nil {
			return nil, err
		}
		if err := requireAffectedRow(res, ErrNotFound); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	user.CreatedAt = unixToTime(createdAt)
	user.UpdatedAt = unixToTime(updatedAt)
	return &user, nil
}
