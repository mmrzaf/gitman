package validate

import (
	"fmt"
	"regexp"
)

var (
	accountNameRE = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9_-]*[a-zA-Z0-9])?$`)
	simpleNameRE  = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
)

// Username validates the account-name contract shared by the web and admin UI.
func Username(username string) error {
	if len(username) < 3 || len(username) > 32 {
		return fmt.Errorf("username must be between 3 and 32 characters")
	}
	if !accountNameRE.MatchString(username) {
		return fmt.Errorf("username may only contain letters, numbers, dashes and underscores")
	}
	return nil
}

// RepositoryName validates the intentionally small repository naming grammar.
func RepositoryName(name string) error {
	if len(name) < 1 || len(name) > 100 {
		return fmt.Errorf("repository name must be between 1 and 100 characters")
	}
	if !simpleNameRE.MatchString(name) {
		return fmt.Errorf("repository name may only contain letters, numbers, dashes and underscores")
	}
	return nil
}

// StorageName validates an already-persisted owner/repository path component.
// It deliberately does not impose account-registration length/edge rules so
// existing repositories remain addressable if those product rules evolve.
func StorageName(name string) error {
	if name == "" || !simpleNameRE.MatchString(name) {
		return fmt.Errorf("invalid storage name")
	}
	return nil
}

func Password(password string) error {
	if len(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	if len([]byte(password)) > 72 {
		return fmt.Errorf("password must be at most 72 bytes")
	}
	hasLetter := false
	hasDigit := false
	for _, r := range password {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			hasLetter = true
		}
		if r >= '0' && r <= '9' {
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return fmt.Errorf("password must contain at least one letter and one digit")
	}
	return nil
}
