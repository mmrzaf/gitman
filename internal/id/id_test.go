package id

import (
	"regexp"
	"testing"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewLooksLikeUUIDv4(t *testing.T) {
	for i := 0; i < 100; i++ {
		got := New()
		if !uuidPattern.MatchString(got) {
			t.Fatalf("New() = %q, does not look like a UUID v4", got)
		}
	}
}

func TestNewIsUnique(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		got := New()
		if seen[got] {
			t.Fatalf("New() produced a duplicate: %q", got)
		}
		seen[got] = true
	}
}
