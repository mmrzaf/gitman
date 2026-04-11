package admin

import (
	"fmt"
	"regexp"
)

var usernameRegex = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9_-]*[a-zA-Z0-9])?$`)

func validateUser(username, password string) error {
	if len(username) < 3 || len(username) > 32 {
		return fmt.Errorf("username must be 3-32 characters")
	}

	if !usernameRegex.MatchString(username) {
		return fmt.Errorf("invalid username format")
	}

	if len(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}

	return nil
}
