package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mmrzaf/gitman/internal/postgres"
)

const personColumns = `people.id, people.username, people.password_hash, people.is_admin, people.disabled_at, people.created_at, people.auth_generation, people.bootstrap_expires_at`

func scanPerson(row interface{ Scan(...any) error }, extra ...any) (*Person, error) {
	p := &Person{}
	dest := append([]any{&p.ID, &p.Username, &p.PasswordHash, &p.IsAdmin, &p.DisabledAt, &p.CreatedAt, &p.Generation, &p.BootstrapExpiresAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	return p, nil
}

func insertPerson(ctx context.Context, q postgres.Querier, p *Person) error {
	err := q.QueryRow(ctx, `
		INSERT INTO people (id, username, password_hash, is_admin, bootstrap_expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at, auth_generation
	`, p.ID, p.Username, p.PasswordHash, p.IsAdmin, p.BootstrapExpiresAt).Scan(&p.CreatedAt, &p.Generation)
	return postgres.NormalizeWrite(err)
}

func selectPersonByUsername(ctx context.Context, q postgres.Querier, username string) (*Person, error) {
	p, err := scanPerson(q.QueryRow(ctx, `SELECT `+personColumns+` FROM people WHERE username = lower($1)`, username))
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
		UPDATE people SET auth_generation = auth_generation + 1, disabled_at = CASE WHEN $2 THEN COALESCE(disabled_at, now()) END
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
	tag, err := q.Exec(ctx, `UPDATE people SET password_hash = $2, auth_generation = auth_generation + 1, bootstrap_expires_at = NULL WHERE id = $1`, personID, hash)
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

func insertSession(ctx context.Context, q postgres.Querier, tokenHash string, person *Person, expiresAt time.Time, ip, userAgent string) error {
	tag, err := q.Exec(ctx, `
		INSERT INTO sessions (token_hash, person_id, expires_at, auth_generation, ip, user_agent)
        SELECT $1, id, $3, auth_generation, $5, $6 FROM people
        WHERE id = $2 AND auth_generation = $4 AND disabled_at IS NULL AND (bootstrap_expires_at IS NULL OR bootstrap_expires_at > now())
	`, tokenHash, person.ID, expiresAt, person.Generation, ip, userAgent)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrInvalidCredentials
	}
	return nil
}

func selectSessionIdentity(ctx context.Context, q postgres.Querier, tokenHash string) (*Person, string, error) {
	var sessionID string
	p, err := scanPerson(q.QueryRow(ctx, `
		SELECT `+personColumns+`, sessions.id
		FROM sessions JOIN people ON people.id = sessions.person_id
		WHERE sessions.token_hash = $1 AND sessions.expires_at > now() AND sessions.auth_generation = people.auth_generation AND (people.bootstrap_expires_at IS NULL OR people.bootstrap_expires_at > now())
	`, tokenHash), &sessionID)
	return p, sessionID, postgres.NormalizeNotFound(err)
}

func touchSession(ctx context.Context, q postgres.Querier, tokenHash string) error {
	_, err := q.Exec(ctx, `
		UPDATE sessions SET last_seen_at = now()
		WHERE token_hash = $1 AND last_seen_at < now() - interval '1 minute'
	`, tokenHash)
	return err
}

const sessionColumns = `sessions.id, sessions.person_id, people.username, sessions.created_at, sessions.expires_at, sessions.last_seen_at, sessions.ip, sessions.user_agent`

func scanSession(row interface{ Scan(...any) error }) (Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.PersonID, &s.Username, &s.CreatedAt, &s.ExpiresAt, &s.LastSeenAt, &s.IP, &s.UserAgent)
	return s, err
}

func selectSessions(ctx context.Context, q postgres.Querier, personID string) ([]Session, error) {
	rows, err := q.Query(ctx, `
		SELECT `+sessionColumns+`
		FROM sessions JOIN people ON people.id = sessions.person_id
		WHERE sessions.expires_at > now() AND sessions.auth_generation = people.auth_generation
		  AND people.disabled_at IS NULL AND ($1 = '' OR sessions.person_id = $1)
		ORDER BY sessions.last_seen_at DESC, sessions.id
		LIMIT 500
	`, personID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	var sessions []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

func pruneSessions(ctx context.Context, q postgres.Querier, personID string) error {
	_, err := q.Exec(ctx, `
		DELETE FROM sessions
		WHERE person_id = $1 AND id NOT IN (
			SELECT id FROM sessions WHERE person_id = $1
			ORDER BY last_seen_at DESC, id DESC LIMIT $2
		)
	`, personID, maxSessionsPerPerson)
	if err != nil {
		return fmt.Errorf("prune sessions: %w", err)
	}
	return nil
}

func selectSessionByID(ctx context.Context, q postgres.Querier, sessionID string) (*Session, error) {
	s, err := scanSession(q.QueryRow(ctx, `
		SELECT `+sessionColumns+`
		FROM sessions JOIN people ON people.id = sessions.person_id
		WHERE sessions.id = $1
	`, sessionID))
	if err != nil {
		return nil, postgres.NormalizeNotFound(err)
	}
	return &s, nil
}

func deleteSessionByID(ctx context.Context, q postgres.Querier, sessionID string) error {
	tag, err := q.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, sessionID)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return postgres.ErrNotFound
	}
	return nil
}

func deleteSession(ctx context.Context, q postgres.Querier, tokenHash string) error {
	if _, err := q.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

const tokenColumns = `id, person_id, name, scope, created_at, expires_at, last_used_at, all_repositories, ARRAY(SELECT r.name FROM token_repos tr JOIN repos r ON r.id = tr.repo_id WHERE tr.token_id = tokens.id ORDER BY r.name)`

func scanToken(row interface{ Scan(...any) error }) (*AccessToken, error) {
	t := &AccessToken{}
	if err := row.Scan(&t.ID, &t.PersonID, &t.Name, &t.Scope, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &t.AllRepositories, &t.Repositories); err != nil {
		return nil, err
	}
	return t, nil
}

func insertToken(ctx context.Context, q postgres.Querier, t *AccessToken, tokenHash string, generation int64) error {
	err := q.QueryRow(ctx, `
		INSERT INTO tokens (id, person_id, name, token_hash, scope, expires_at, auth_generation, all_repositories)
		SELECT $1, id, $3, $4, $5, $6, auth_generation, $8 FROM people WHERE id = $2 AND auth_generation = $7 AND disabled_at IS NULL AND bootstrap_expires_at IS NULL
		RETURNING created_at
	`, t.ID, t.PersonID, t.Name, tokenHash, t.Scope, t.ExpiresAt, generation, t.AllRepositories).Scan(&t.CreatedAt)
	return postgres.NormalizeWrite(err)
}

func pruneTokens(ctx context.Context, q postgres.Querier, personID string) error {
	_, err := q.Exec(ctx, `
		DELETE FROM tokens
		WHERE person_id = $1 AND id NOT IN (
			SELECT id FROM tokens WHERE person_id = $1
			ORDER BY created_at DESC, id DESC LIMIT $2
		)
	`, personID, maxTokensPerPerson)
	if err != nil {
		return fmt.Errorf("prune tokens: %w", err)
	}
	return nil
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

// useToken resolves a token hash to its owner and scope, then records
// the use at most once a minute. The validating statement keeps every
// expiry, revocation and repository check. A token revoked between that
// statement and the guarded update may still finish the request in
// flight; every later request fails, as before.
func useToken(ctx context.Context, q postgres.Querier, tokenHash, repositoryName string) (*Person, Scope, error) {
	var scope Scope
	var tokenID string
	p, err := scanPerson(q.QueryRow(ctx, `
		SELECT `+personColumns+`, tokens.scope, tokens.id
		FROM tokens
		JOIN people ON tokens.person_id = people.id
		WHERE tokens.token_hash = $1
		  AND tokens.expires_at > now()
		  AND tokens.auth_generation = people.auth_generation
		  AND people.bootstrap_expires_at IS NULL
		  AND (tokens.all_repositories OR EXISTS (SELECT 1 FROM token_repos tr JOIN repos r ON r.id = tr.repo_id WHERE tr.token_id = tokens.id AND r.name = lower($2)))
		  AND people.disabled_at IS NULL
	`, tokenHash, repositoryName), &scope, &tokenID)
	if err != nil {
		if errors.Is(postgres.NormalizeNotFound(err), postgres.ErrNotFound) {
			return nil, "", ErrInvalidToken
		}
		return nil, "", fmt.Errorf("authenticate token: %w", err)
	}
	_, _ = q.Exec(ctx, `
		UPDATE tokens SET last_used_at = now()
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')
	`, tokenID)
	return p, scope, nil
}

func deleteExpiredSessions(ctx context.Context, q postgres.Querier) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

func deleteExpiredTokens(ctx context.Context, q postgres.Querier) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM tokens WHERE expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("delete expired access tokens: %w", err)
	}
	return tag.RowsAffected(), nil
}

func revokeCredentials(ctx context.Context, q postgres.Querier, personID string) error {
	if err := deleteSessionsOf(ctx, q, personID); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `DELETE FROM tokens WHERE person_id = $1`, personID)
	return err
}
