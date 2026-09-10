package validate

import (
	"strings"
	"testing"
)

func TestUsername(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"alice", true}, {"a_b-1", true}, {"ab", false}, {"-alice", false}, {"alice-", false}, {"a b", false},
	} {
		if err := Username(tc.name); (err == nil) != tc.ok {
			t.Fatalf("Username(%q) error=%v ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestRepositoryName(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"repo", true}, {"repo_1-test", true}, {"", false}, {"a/b", false}, {"a.b", false}, {"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false},
	} {
		if err := RepositoryName(tc.name); (err == nil) != tc.ok {
			t.Fatalf("RepositoryName(%q) error=%v ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestPasswordBcryptByteLimit(t *testing.T) {
	valid72 := "A1" + strings.Repeat("a", 70)
	if got := len([]byte(valid72)); got != 72 {
		t.Fatalf("test setup: 72-byte password has %d bytes", got)
	}
	if err := Password(valid72); err != nil {
		t.Fatalf("72-byte password rejected: %v", err)
	}

	if err := Password(valid72 + "a"); err == nil {
		t.Fatal("73-byte password accepted")
	}

	multibyte := "A1" + strings.Repeat("é", 36)
	if got := len([]byte(multibyte)); got <= 72 {
		t.Fatalf("test setup expected more than 72 bytes, got %d", got)
	}
	if err := Password(multibyte); err == nil {
		t.Fatal("multibyte password over 72 bytes accepted")
	}
}
