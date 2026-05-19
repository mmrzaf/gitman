package admin

import (
	"strings"
	"testing"
)

func TestValidateUsername(t *testing.T) {
	tests := []struct {
		name    string
		user    string
		wantErr bool
		errMsg  string
	}{
		{"valid simple", "john", false, ""},
		{"valid with dash", "john-doe", false, ""},
		{"valid with underscore", "john_doe", false, ""},
		{"valid alphanumeric", "user123", false, ""},
		{"too short", "ab", true, "username must be between 3 and 32 characters"},
		{"too long", "a23456789012345678901234567890123", true, "username must be between 3 and 32 characters"},
		{"starts with dash", "-john", true, "username may only contain letters, numbers, dashes and underscores"},
		{"ends with dash", "john-", true, "username may only contain letters, numbers, dashes and underscores"},
		{"invalid chars", "john@doe", true, "username may only contain letters, numbers, dashes and underscores"},
		{"empty", "", true, "username must be between 3 and 32 characters"},
		{"exactly 3 chars", "abc", false, ""},
		{"exactly 32 chars", strings.Repeat("a", 32), false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUsername(tt.user)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateUsername(%q) error = %v, wantErr %v", tt.user, err, tt.wantErr)
			}
			if tt.wantErr && err != nil && err.Error() != tt.errMsg {
				t.Errorf("expected error %q, got %q", tt.errMsg, err.Error())
			}
		})
	}
}

func TestIsPasswordStrong(t *testing.T) {
	tests := []struct {
		name    string
		pass    string
		wantErr bool
		errMsg  string
	}{
		{"strong enough", "abcdefghij1", false, ""},
		{"strong mixed", "Abcdef12345", false, ""},
		{"too short", "Abc123", true, "password must be at least 8 characters"},
		{"no digit", "abcdefghijklm", true, "password must contain at least one letter and one digit"},
		{"no letter", "1234567890", true, "password must contain at least one letter and one digit"},
		{"exactly 8 with both", "abcfgh1j", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := IsPasswordStrong(tt.pass)
			if (err != nil) != tt.wantErr {
				t.Errorf("IsPasswordStrong(%q) error = %v, wantErr %v", tt.pass, err, tt.wantErr)
			}
			if tt.wantErr && err != nil && err.Error() != tt.errMsg {
				t.Errorf("expected error %q, got %q", tt.errMsg, err.Error())
			}
		})
	}
}
