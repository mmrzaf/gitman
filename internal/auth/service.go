package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/id"
	"github.com/mmrzaf/gitman/internal/names"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/token"
)

// Service manages people, their sessions and their access tokens.
// Methods that change a person take actorID, the person making the
// change (empty for the command line), and record the change in the
// activity log in the same transaction.
type Service struct {
	db *postgres.DB
}

// NewService returns a Service backed by db.
func NewService(db *postgres.DB) *Service {
	return &Service{db: db}
}

// GeneratePassword returns a random one-time password for a new person
// or a password reset. The person changes it after signing in.
func GeneratePassword() (string, error) {
	return token.New(passwordBytes)
}

// Create adds a person.
func (s *Service) Create(ctx context.Context, username, password string, isAdmin bool, actorID string) (*Person, error) {
	return s.create(ctx, username, password, isAdmin, actorID, false)
}

// CreateBootstrap creates an account with a temporary password that must be changed.
func (s *Service) CreateBootstrap(ctx context.Context, username, password string, isAdmin bool, actorID string) (*Person, error) {
	return s.create(ctx, username, password, isAdmin, actorID, true)
}

func (s *Service) create(ctx context.Context, username, password string, isAdmin bool, actorID string, bootstrap bool) (*Person, error) {
	if err := names.ValidateUsername(username); err != nil {
		return nil, err
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	p := &Person{ID: id.New(), Username: strings.ToLower(username), PasswordHash: hash, IsAdmin: isAdmin}
	if bootstrap {
		expires := time.Now().Add(24 * time.Hour)
		p.BootstrapExpiresAt = &expires
	}
	err = s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := insertPerson(ctx, tx, p); err != nil {
			return err
		}
		return activity.Record(ctx, tx, "", actorID, activity.PersonAdded, p.Username)
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// GetByUsername looks up a person by username.
func (s *Service) GetByUsername(ctx context.Context, username string) (*Person, error) {
	return selectPersonByUsername(ctx, s.db.Q, username)
}

// GetByID looks up a person by ID.
func (s *Service) GetByID(ctx context.Context, personID string) (*Person, error) {
	return selectPersonByID(ctx, s.db.Q, personID)
}

// List returns every person, ordered by username.
func (s *Service) List(ctx context.Context) ([]Person, error) {
	return selectPeople(ctx, s.db.Q)
}

// isOnlyEnabledAdmin locks the enabled admins (see lockEnabledAdmins)
// and reports whether personID is the only one.
func isOnlyEnabledAdmin(ctx context.Context, tx postgres.Tx, personID string) (bool, error) {
	ids, err := lockEnabledAdmins(ctx, tx)
	if err != nil {
		return false, err
	}
	return len(ids) == 1 && ids[0] == personID, nil
}

// Disable revokes a person's credentials and ends every session of
// theirs, keeping the person row. Disabling an already-disabled person
// is not an error; disabling the only enabled admin is refused with
// ErrLastAdmin.
func (s *Service) Disable(ctx context.Context, personID, actorID string) error {
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		onlyAdmin, err := isOnlyEnabledAdmin(ctx, tx, personID)
		if err != nil {
			return err
		}
		p, err := selectPersonByID(ctx, tx, personID)
		if err != nil {
			return err
		}
		if p.Disabled() {
			return nil
		}
		if onlyAdmin {
			return ErrLastAdmin
		}
		if err := setDisabled(ctx, tx, personID, true); err != nil {
			return err
		}
		if err := revokeCredentials(ctx, tx, personID); err != nil {
			return err
		}
		return activity.Record(ctx, tx, "", actorID, activity.PersonDisabled, p.Username)
	})
}

// Enable replaces the disabled account's password and revokes credentials.
// The returned bootstrap password is shown once and expires in 24 hours.
func (s *Service) Enable(ctx context.Context, personID, actorID string) (string, error) {
	password, err := GeneratePassword()
	if err != nil {
		return "", err
	}
	hash, err := validatedHash(password)
	if err != nil {
		return "", err
	}
	err = s.db.Tx(ctx, func(tx postgres.Tx) error {
		p, err := selectPersonByID(ctx, tx, personID)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE people SET disabled_at = NULL, password_hash = $2, auth_generation = auth_generation + 1, bootstrap_expires_at = now() + interval '24 hours' WHERE id = $1 AND disabled_at IS NOT NULL`, personID, hash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.New(apperr.KindConflict, "person is already enabled")
		}
		if err := revokeCredentials(ctx, tx, personID); err != nil {
			return err
		}
		return activity.Record(ctx, tx, "", actorID, activity.PersonEnabled, p.Username)
	})
	if err != nil {
		return "", err
	}
	return password, nil
}

// SetAdmin changes a person's role. Removing the admin role from the
// only enabled admin is refused with ErrLastAdmin.
func (s *Service) SetAdmin(ctx context.Context, personID string, isAdmin bool, actorID string) error {
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		if !isAdmin {
			onlyAdmin, err := isOnlyEnabledAdmin(ctx, tx, personID)
			if err != nil {
				return err
			}
			if onlyAdmin {
				return ErrLastAdmin
			}
		}
		p, err := selectPersonByID(ctx, tx, personID)
		if err != nil {
			return err
		}
		if p.IsAdmin == isAdmin {
			return nil
		}
		if err := setAdminRow(ctx, tx, personID, isAdmin); err != nil {
			return err
		}
		role := "member"
		if isAdmin {
			role = "admin"
		}
		return activity.Record(ctx, tx, "", actorID, activity.PersonRole, p.Username+" "+role)
	})
}

// ResetPassword sets a new password for someone, as an admin action, and
// ends every session of theirs, so a compromised session cannot outlive
// the reset meant to end it.
func (s *Service) ResetPassword(ctx context.Context, personID, newPassword, actorID string) error {
	hash, err := validatedHash(newPassword)
	if err != nil {
		return err
	}
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		p, err := selectPersonByID(ctx, tx, personID)
		if err != nil {
			return err
		}
		if err := setPasswordHash(ctx, tx, personID, hash); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE people SET bootstrap_expires_at = now() + interval '24 hours' WHERE id = $1`, personID); err != nil {
			return err
		}
		if err := revokeCredentials(ctx, tx, personID); err != nil {
			return err
		}
		return activity.Record(ctx, tx, "", actorID, activity.PasswordReset, p.Username)
	})
}

// ChangePassword replaces a person's own password after checking their
// current one, returning ErrInvalidCredentials if it is wrong. Every
// session of theirs ends, including the one making the change; the
// caller issues a fresh session for it.
func (s *Service) ChangePassword(ctx context.Context, personID, currentPassword, newPassword string) (*Person, error) {
	p, err := s.GetByID(ctx, personID)
	if err != nil {
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(p.PasswordHash), []byte(currentPassword)); err != nil {
		return nil, ErrInvalidCredentials
	}
	hash, err := validatedHash(newPassword)
	if err != nil {
		return nil, err
	}
	err = s.db.Tx(ctx, func(tx postgres.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE people SET password_hash = $2, auth_generation = auth_generation + 1, bootstrap_expires_at = NULL WHERE id = $1 AND auth_generation = $3 AND disabled_at IS NULL`, personID, hash, p.Generation)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrInvalidCredentials
		}
		return revokeCredentials(ctx, tx, personID)
	})
	if err != nil {
		return nil, err
	}
	p.Generation++
	p.PasswordHash = hash
	p.BootstrapExpiresAt = nil
	return p, nil
}

func validatedHash(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	return hashPassword(password)
}

// VerifyLogin checks a username and password and returns the person on
// success.
func (s *Service) VerifyLogin(ctx context.Context, username, password string) (*Person, error) {
	p, err := s.GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			_ = bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	switch err := bcrypt.CompareHashAndPassword([]byte(p.PasswordHash), []byte(password)); {
	case err == nil:
	case errors.Is(err, bcrypt.ErrMismatchedHashAndPassword):
		return nil, ErrInvalidCredentials
	default:
		return nil, fmt.Errorf("verify password: %w", err)
	}
	if p.Disabled() {
		return nil, ErrDisabled
	}
	if p.BootstrapExpiresAt != nil && !p.BootstrapExpiresAt.After(time.Now()) {
		return nil, ErrInvalidCredentials
	}
	return p, nil
}

// CreateSession starts a session for the verified person snapshot and returns the plain
// token to set as a cookie. Only the token's hash is stored.
func (s *Service) CreateSession(ctx context.Context, person *Person, ttl time.Duration) (plain string, expiresAt time.Time, err error) {
	plain, err = token.New(sessionTokenBytes)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("generate session token: %w", err)
	}
	expiresAt = time.Now().Add(ttl)
	if err := insertSession(ctx, s.db.Q, token.Hash(plain), person, expiresAt); err != nil {
		return "", time.Time{}, err
	}
	return plain, expiresAt, nil
}

// SessionPerson resolves a session token to the person it belongs to. A
// session of a disabled person fails even though Disable already deletes
// their sessions, as defense against a session created concurrently
// with the disable.
func (s *Service) SessionPerson(ctx context.Context, sessionToken string) (*Person, error) {
	p, err := selectSessionPerson(ctx, s.db.Q, token.Hash(sessionToken))
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			return nil, ErrInvalidSession
		}
		return nil, fmt.Errorf("resolve session: %w", err)
	}
	if p.Disabled() {
		return nil, ErrInvalidSession
	}
	return p, nil
}

// DeleteSession ends one session. Deleting a session that does not exist
// is not an error: the end state the caller wants is already true.
func (s *Service) DeleteSession(ctx context.Context, sessionToken string) error {
	return deleteSession(ctx, s.db.Q, token.Hash(sessionToken))
}

// CreateToken generates an access token and returns its plain value,
// shown to the person exactly once. A nil ttl uses the 30-day default.
// An empty repository list grants access to all current and future repositories,
// subject to the owner's repository and ref permissions.
func (s *Service) CreateToken(ctx context.Context, personID, name string, scope Scope, ttl *time.Duration, repositories []string) (plain string, created *AccessToken, err error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return "", nil, apperr.New(apperr.KindInvalid, "token name must be between 1 and 100 characters")
	}
	if scope != ScopeRead && scope != ScopeWrite {
		return "", nil, apperr.New(apperr.KindInvalid, fmt.Sprintf("invalid token scope %q", scope))
	}
	plain, err = token.New(accessTokenBytes)
	if err != nil {
		return "", nil, fmt.Errorf("generate access token: %w", err)
	}
	created = &AccessToken{ID: id.New(), PersonID: personID, Name: name, Scope: scope}
	if ttl != nil {
		expiresAt := time.Now().Add(*ttl)
		created.ExpiresAt = &expiresAt
	}
	if err := insertToken(ctx, s.db.Q, created, token.Hash(plain)); err != nil {
		return "", nil, err
	}
	return plain, created, nil
}

// ListTokens returns a person's access tokens, oldest first.
func (s *Service) ListTokens(ctx context.Context, personID string) ([]AccessToken, error) {
	return selectTokens(ctx, s.db.Q, personID)
}

// RevokeToken deletes one of personID's own access tokens and returns
// it. It fails with postgres.ErrNotFound for a token that does not exist
// and ErrNotTokenOwner for one that belongs to someone else.
func (s *Service) RevokeToken(ctx context.Context, personID, tokenID string) (*AccessToken, error) {
	var revoked *AccessToken
	err := s.db.Tx(ctx, func(tx postgres.Tx) error {
		var err error
		if revoked, err = selectToken(ctx, tx, tokenID); err != nil {
			return err
		}
		if revoked.PersonID != personID {
			return ErrNotTokenOwner
		}
		return deleteTokenRow(ctx, tx, tokenID)
	})
	if err != nil {
		return nil, err
	}
	return revoked, nil
}

// Authenticate resolves a plain access token to its owner and the
// token's scope. Whether the scope is sufficient is the caller's
// decision, so it can say "this token can only read" instead of
// "authentication failed".
func (s *Service) Authenticate(ctx context.Context, plain, repositoryName string) (*Person, Scope, error) {
	if plain == "" {
		return nil, "", ErrInvalidToken
	}
	return useToken(ctx, s.db.Q, token.Hash(plain), repositoryName)
}

// PruneExpiredSessions deletes sessions past their expiry and returns
// how many. Expired sessions already fail to authenticate; this only
// keeps the table from growing.
func (s *Service) PruneExpiredSessions(ctx context.Context) (int64, error) {
	return deleteExpiredSessions(ctx, s.db.Q)
}

// RevokeAll invalidates every credential, including login snapshots verified
// before this transaction. The password is preserved; the caller must sign in.
func (s *Service) RevokeAll(ctx context.Context, personID, actorID string) error {
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		var username string
		if err := tx.QueryRow(ctx, `UPDATE people SET auth_generation = auth_generation + 1 WHERE id = $1 RETURNING username`, personID).Scan(&username); err != nil {
			return postgres.NormalizeNotFound(err)
		}
		if err := revokeCredentials(ctx, tx, personID); err != nil {
			return err
		}
		return activity.Record(ctx, tx, "", actorID, activity.CredentialsRevoked, username)
	})
}
