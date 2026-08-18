package validate

import "testing"

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
