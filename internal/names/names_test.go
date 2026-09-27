package names

import (
	"strings"
	"testing"
)

func TestValidateRepository(t *testing.T) {
	valid := []string{"a", "waiotech", "sms-gateway", "Cerv_2"}
	for _, name := range valid {
		if err := ValidateRepository(name); err != nil {
			t.Errorf("ValidateRepository(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"", "-leading-dash", "trailing-dash-", "has a space", "has@sign",
		"me", "People", "STATIC", strings.Repeat("a", 101),
	}
	for _, name := range invalid {
		if err := ValidateRepository(name); err == nil {
			t.Errorf("ValidateRepository(%q) = nil, want an error", name)
		}
	}
}

func TestValidateUsername(t *testing.T) {
	valid := []string{"abc", "darius", "d-a_r99"}
	for _, name := range valid {
		if err := ValidateUsername(name); err != nil {
			t.Errorf("ValidateUsername(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{"", "ab", "-abc", "abc-", "has space", "me", "Admin"}
	for _, name := range invalid {
		if err := ValidateUsername(name); err == nil {
			t.Errorf("ValidateUsername(%q) = nil, want an error", name)
		}
	}
}

func TestRunFetchUsernameIsReserved(t *testing.T) {
	if err := ValidateUsername("gitman-run"); err == nil {
		t.Fatal("expected the run fetch username to be reserved")
	}
}

// TestPageNamesAreReserved keeps a repository from taking the name of a
// page, which would make one of the two unreachable.
func TestPageNamesAreReserved(t *testing.T) {
	for _, name := range []string{"jump", "people", "events"} {
		if err := ValidateRepository(name); err == nil {
			t.Errorf("ValidateRepository(%q) = nil; it is a page's address", name)
		}
	}
}
