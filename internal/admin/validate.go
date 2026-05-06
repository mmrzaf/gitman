package admin

import (
	"context"
	"fmt"
	"regexp"
)

var usernameRegex = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9_-]*[a-zA-Z0-9])?$`)

// var strengthRegex = regexp.MustCompile(`^[[:ascii:]]+$`) // only ASCII; length + digit+letter

func ValidateUsername(username string) error {
	if len(username) < 3 || len(username) > 32 {
		return fmt.Errorf("username must be between 3 and 32 characters")
	}
	if !usernameRegex.MatchString(username) {
		return fmt.Errorf("username may only contain letters, numbers, dashes and underscores")
	}
	return nil
}

func contextBackground() context.Context {
	return context.Background()
}

func IsPasswordStrong(password string) error {
	if len(password) < 10 {
		return fmt.Errorf("password must be at least 10 characters")
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
