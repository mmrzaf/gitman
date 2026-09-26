package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mmrzaf/gitman/internal/postgres"
)

const personColumns = `people.id, people.username, people.password_hash, people.is_admin, people.disabled_at, people.created_at`

func scanPerson(row interface{ Scan(...any) error }, extra ...any) (*Person, error) {
	p := &Person{}
	dest := append([]any{&p.ID, &p.Username, &p.PasswordHash, &p.IsAdmin, &p.DisabledAt, &p.CreatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	return p, nil
}

func insertPerson(ctx context.Context, q postgres.Querier, p *Person) error {
	err := q.QueryRow(ctx, `
		INSERT INTO people (id, username, password_hash, is_admin)
		VALUES ($1, $2, $3, $4)
		RETURNING created_at
	`, p.ID, p.Username, p.PasswordHash, p.IsAdmin).Scan(&p.CreatedAt)
	return postgres.NormalizeWrite(err)
}

func selectPersonByUsername(ctx context.Context, q postgres.Querier, username string) (*Person, error) {
	p, err := scanPerson(q.QueryRow(ctx, `SELECT `+personColumns+` FROM people WHERE username = $1`, username))
	return p, postgres.NormalizeNotFound(err)
}

func selectPersonByID(ctx context.Context, q postgres.Querier, personID string) (*Person, error) {
	p, err := scanPerson(q.QueryRow(ctx, `SELECT `+personColumns+` FROM people WHERE id = $1`, personID))
	return p, postgres.NormalizeNotFound(err)
}

func selectPeople(ctx context.Context, q postgres.Querier) ([]Person, error) {
	rows, err := q.Query(ctx, `SELECT `+personColumns+` FROM people ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("list people: %w", err)
	}
	defer rows.Close()
	var result []Person
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, fmt.Errorf("scan person: %w", err)
		}
		result = append(result, *p)
	}
	return result, rows.Err()
}

// lockEnabledAdmins locks every enabled admin's row for the rest of tx
// and returns their IDs. Holding those locks serializes changes that
// remove an admin, so two of them running at once cannot each see the
// other as the remaining admin.
func lockEnabledAdmins(ctx context.Context, tx postgres.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM people WHERE is_admin AND disabled_at IS NULL ORDER BY id FOR UPDATE`)
	if err != nil {
		return nil, fmt.Errorf("lock admins: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var personID string
		if err := rows.Scan(&personID); err != nil {
			return nil, fmt.Errorf("lock admins: %w", err)
		}
		ids = append(ids, personID)
	}
	return ids, rows.Err()
}

func setDisabled(ctx context.Context, q postgres.Querier, personID string, disabled bool) error {
	tag, err := q.Exec(ctx, `
		UPDATE people SET disabled_at = CASE WHEN $2 THEN COALESCE(disabled_at, now()) END
		WHERE id = $1
	`, personID, disabled)
	if err != nil {
		return fmt.Errorf("update disabled state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return postgres.ErrNotFound
	}
	return nil
}

func setAdminRow(ctx context.Context, q postgres.Querier, personID string, isAdmin bool) error {
	tag, err := q.Exec(ctx, `UPDATE people SET is_admin = $2 WHERE id = $1`, personID, isAdmin)
	if err != nil {
		return fmt.Errorf("set admin: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return postgres.ErrNotFound
	}
	return nil
}

func setPasswordHash(ctx context.Context, q postgres.Querier, personID, hash string) error {
	tag, err := q.Exec(ctx, `UPDATE people SET password_hash = $2 WHERE id = $1`, personID, hash)
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return postgres.ErrNotFound
	}
	return nil
}

func deleteSessionsOf(ctx context.Context, q postgres.Querier, personID string) error {
	if _, err := q.Exec(ctx, `DELETE FROM sessions WHERE person_id = $1`, personID); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	return nil
}

func insertSession(ctx context.Context, q postgres.Querier, tokenHash, personID string, expiresAt time.Time) error {
	if _, err := q.Exec(ctx, `
		INSERT INTO sessions (token_hash, person_id, expires_at) VALUES ($1, $2, $3)
	`, tokenHash, personID, expiresAt); err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func selectSessionPerson(ctx context.Context, q postgres.Querier, tokenHash string) (*Person, error) {
	p, err := scanPerson(q.QueryRow(ctx, `
		SELECT `+personColumns+`
		FROM sessions JOIN people ON people.id = sessions.person_id
		WHERE sessions.token_hash = $1 AND sessions.expires_at > now()
	`, tokenHash))
	return p, postgres.NormalizeNotFound(err)
}

// extendSessionRow moves a session's expiry to expiresAt, but only when
// it currently expires within extendWithin, and reports whether it did.
func extendSessionRow(ctx context.Context, q postgres.Querier, tokenHash string, expiresAt time.Time, extendWithin time.Duration) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE sessions SET expires_at = $2
		WHERE token_hash = $1 AND expires_at > now() AND expires_at < now() + $3
	`, tokenHash, expiresAt, extendWithin)
	if err != nil {
		return false, fmt.Errorf("extend session: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func deleteSession(ctx context.Context, q postgres.Querier, tokenHash string) error {
	if _, err := q.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

const tokenColumns = `id, person_id, name, scope, created_at, expires_at, last_used_at`

func scanToken(row interface{ Scan(...any) error }) (*AccessToken, error) {
	t := &AccessToken{}
	if err := row.Scan(&t.ID, &t.PersonID, &t.Name, &t.Scope, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt); err != nil {
		return nil, err
	}
	return t, nil
}

func insertToken(ctx context.Context, q postgres.Querier, t *AccessToken, tokenHash string) error {
	err := q.QueryRow(ctx, `
		INSERT INTO tokens (id, person_id, name, token_hash, scope, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at
	`, t.ID, t.PersonID, t.Name, tokenHash, t.Scope, t.ExpiresAt).Scan(&t.CreatedAt)
	return postgres.NormalizeWrite(err)
}

func selectTokens(ctx context.Context, q postgres.Querier, personID string) ([]AccessToken, error) {
	rows, err := q.Query(ctx, `SELECT `+tokenColumns+` FROM tokens WHERE person_id = $1 ORDER BY created_at`, personID)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	defer rows.Close()
	var tokens []AccessToken
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, fmt.Errorf("scan token: %w", err)
		}
		tokens = append(tokens, *t)
	}
	return tokens, rows.Err()
}

func selectToken(ctx context.Context, q postgres.Querier, tokenID string) (*AccessToken, error) {
	t, err := scanToken(q.QueryRow(ctx, `SELECT `+tokenColumns+` FROM tokens WHERE id = $1`, tokenID))
	return t, postgres.NormalizeNotFound(err)
}

func deleteTokenRow(ctx context.Context, q postgres.Querier, tokenID string) error {
	tag, err := q.Exec(ctx, `DELETE FROM tokens WHERE id = $1`, tokenID)
	if err != nil {
		return fmt.Errorf("delete token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return postgres.ErrNotFound
	}
	return nil
}

// useToken resolves a token hash to its owner and scope, recording the
// use. The expiry check, the disabled-person check and the last_used_at
// update are one statement, so a token revoked or a person disabled
// concurrently can never authenticate.
func useToken(ctx context.Context, q postgres.Querier, tokenHash string) (*Person, Scope, error) {
	var scope Scope
	p, err := scanPerson(q.QueryRow(ctx, `
		UPDATE tokens SET last_used_at = now()
		FROM people
		WHERE tokens.token_hash = $1
		  AND tokens.person_id = people.id
		  AND (tokens.expires_at IS NULL OR tokens.expires_at > now())
		  AND people.disabled_at IS NULL
		RETURNING `+personColumns+`, tokens.scope
	`, tokenHash), &scope)
	if err != nil {
		if errors.Is(postgres.NormalizeNotFound(err), postgres.ErrNotFound) {
			return nil, "", ErrInvalidToken
		}
		return nil, "", fmt.Errorf("authenticate token: %w", err)
	}
	return p, scope, nil
}

func deleteExpiredSessions(ctx context.Context, q postgres.Querier) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}
