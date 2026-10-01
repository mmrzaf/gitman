package auth

import (
	"fmt"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/mmrzaf/gitman/internal/apperr"
)

const bcryptCost = 12

// ValidatePassword enforces Gitman's only password rule: length. bcrypt
// itself limits input to 72 bytes; complexity rules beyond length add
// little defensible security for a self-hosted tool with no public
// registration, so none are enforced.
func ValidatePassword(password string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 12 {
		return apperr.New(apperr.KindInvalid, "password must be at least 12 characters")
	}
	if len(password) > 72 {
		return apperr.New(apperr.KindInvalid, "password must be at most 72 bytes")
	}
	return nil
}

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// dummyHash is compared against when a login's username does not exist,
// so an attempt against an unknown username costs as much as one against
// a known username with a wrong password, and the two cannot be told
// apart by response time. It is computed on first use rather than at
// package load, so processes that never verify a login (the Git hooks,
// the CLI) do not pay for a bcrypt hash on every start.
var dummyHash = sync.OnceValue(func() []byte {
	hash, err := bcrypt.GenerateFromPassword([]byte("gitman-timing-equalizer"), bcryptCost)
	if err != nil {
		// bcrypt only fails for a cost out of range or an input over 72
		// bytes, neither of which can happen with these constants.
		panic("auth: precompute dummy password hash: " + err.Error())
	}
	return hash
})
