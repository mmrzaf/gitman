package auth

import "time"

// Amount of randomness behind each kind of secret token: 32 bytes (256
// bits) is far beyond what resisting guessing needs, and an access token
// leaking is at least as damaging as a session cookie leaking.
const (
	sessionTokenBytes = 32
	accessTokenBytes  = 32
	// passwordBytes backs the one-time passwords issued for a new person
	// or a password reset: 18 random bytes, 24 characters.
	passwordBytes = 18
)

// Scope limits what an access token can do. There is no third scope,
// because there is no third thing an access token is for.
type Scope string

const (
	ScopeRead  Scope = "read"
	ScopeWrite Scope = "write"
)

// Satisfies reports whether a token with this scope satisfies a request
// that needs at least required. A write-scoped token satisfies a
// read-scoped request; the reverse is not true.
func (s Scope) Satisfies(required Scope) bool {
	return s == ScopeWrite || s == required
}

// AccessToken is a credential for Git over HTTP. Its plain value is
// shown once, when it is created; only its hash is stored.
type AccessToken struct {
	AllRepositories bool
	Repositories    []string
	ID              string
	PersonID        string
	Name            string
	Scope           Scope
	CreatedAt       time.Time
	ExpiresAt       *time.Time
	LastUsedAt      *time.Time
}
