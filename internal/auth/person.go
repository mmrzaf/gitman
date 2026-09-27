// Package auth is Gitman's identity model: named individuals, not
// per-repository accounts. Everyone who exists can read every
// repository; a repository's ref rules (internal/repo) are the only
// permission system for writing to it.
//
// service.go holds the package's rules and orchestration; store.go
// holds every SQL statement it runs.
package auth

import (
	"errors"
	"time"

	"github.com/mmrzaf/gitman/internal/apperr"
)

// Person is a named individual who can sign in to Gitman.
type Person struct {
	ID           string
	Username     string
	PasswordHash string
	IsAdmin      bool
	DisabledAt   *time.Time
	CreatedAt    time.Time
}

// Disabled reports whether the person has been disabled. A disabled
// person's credentials stop working, but the person row is kept so their
// name still appears on the pushes and runs they made; people are
// disabled, never deleted.
func (p *Person) Disabled() bool {
	return p != nil && p.DisabledAt != nil
}

var (
	// ErrInvalidCredentials is returned for both an unknown username and
	// a wrong password, so a caller cannot distinguish "no such person"
	// from "wrong password" by the error alone.
	ErrInvalidCredentials = errors.New("invalid username or password")

	// ErrDisabled is returned when the username and password are correct
	// but the person has been disabled.
	ErrDisabled = errors.New("person is disabled")

	// ErrLastAdmin is returned when a change would leave the instance
	// with no enabled admin, and so with no one able to manage it from
	// the web.
	ErrLastAdmin = apperr.New(apperr.KindConflict, "This is the only enabled admin. Make someone else an admin first.")

	// ErrInvalidSession is returned when a session token does not
	// resolve to an active session: it does not exist, has expired, or
	// belongs to a disabled person.
	ErrInvalidSession = errors.New("invalid session")

	// ErrInvalidToken is returned when a plain access token does not
	// resolve to an active token of a person who is not disabled.
	ErrInvalidToken = errors.New("invalid access token")

	// ErrNotTokenOwner is returned when revoking an access token that
	// belongs to someone else.
	ErrNotTokenOwner = errors.New("that token belongs to someone else")
)
