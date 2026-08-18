package admin

import "github.com/mmrzaf/gitman/internal/validate"

// ValidateUsername is retained as the admin package's public command helper;
// the naming rule itself lives in one shared package.
func ValidateUsername(username string) error { return validate.Username(username) }

func IsPasswordStrong(password string) error { return validate.Password(password) }
