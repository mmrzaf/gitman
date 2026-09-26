// Package names enforces Gitman's naming rules for repositories and
// people: length, character set, and a reserved-word list shared by both,
// so a repository or a person can never collide with a route Gitman
// itself needs, such as /me or /people.
package names

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/mmrzaf/gitman/internal/apperr"
)

// reserved words cannot be used as a repository name or a username,
// compared case-insensitively.
var reserved = map[string]bool{
	"me":          true,
	"gitman-run":  true,
	"repos":       true,
	"people":      true,
	"login":       true,
	"logout":      true,
	"static":      true,
	"api":         true,
	"settings":    true,
	"runs":        true,
	"events":      true,
	"jump":        true,
	"hook":        true,
	"hooks":       true,
	"health":      true,
	"healthz":     true,
	"readyz":      true,
	"assets":      true,
	"admin":       true,
	"favicon.ico": true,
	"robots.txt":  true,
}

// nameCharsPattern accepts a single alphanumeric character, or a run of
// alphanumerics, dashes and underscores that starts and ends with an
// alphanumeric character. Length is checked separately by each caller.
var nameCharsPattern = regexp.MustCompile(`^[a-zA-Z0-9]$|^[a-zA-Z0-9][a-zA-Z0-9_-]*[a-zA-Z0-9]$`)

// ValidateRepository validates a repository name: the single path segment
// Gitman serves it at (e.g. "waiotech" in /waiotech).
func ValidateRepository(name string) error {
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 100 {
		return apperr.New(apperr.KindInvalid, "repository name must be between 1 and 100 characters")
	}
	if strings.Contains(name, "@") {
		return apperr.New(apperr.KindInvalid, "repository name must not contain '@', which separates a repository from a ref in Gitman's URLs")
	}
	if !nameCharsPattern.MatchString(name) {
		return apperr.New(apperr.KindInvalid, "repository name may only contain letters, numbers, dashes and underscores, and must start and end with a letter or number")
	}
	if reserved[strings.ToLower(name)] {
		return apperr.New(apperr.KindInvalid, fmt.Sprintf("%q is reserved and cannot be used as a repository name", name))
	}
	return nil
}

// ValidateUsername validates a person's username.
func ValidateUsername(name string) error {
	if utf8.RuneCountInString(name) < 3 || utf8.RuneCountInString(name) > 32 {
		return apperr.New(apperr.KindInvalid, "username must be between 3 and 32 characters")
	}
	if !nameCharsPattern.MatchString(name) {
		return apperr.New(apperr.KindInvalid, "username may only contain letters, numbers, dashes and underscores, and must start and end with a letter or number")
	}
	if reserved[strings.ToLower(name)] {
		return apperr.New(apperr.KindInvalid, fmt.Sprintf("%q is reserved and cannot be used as a username", name))
	}
	return nil
}
