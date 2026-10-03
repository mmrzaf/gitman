package git

import (
	"strings"
	"testing"
)

func TestValidateNameAccepts(t *testing.T) {
	valid := []string{"main", "develop", "release/1.2", "v1.4.2", "feature/login-fix"}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}
}

func TestValidateNameRejects(t *testing.T) {
	invalid := []string{
		"", ".hidden", "trailing.", "double..dot", "has space", "has~tilde",
		"has^caret", "has:colon", "has?question", "has[bracket",
		"has\\backslash", "ends.lock", "/leading-slash", "trailing-slash/",
		"double//slash", "@", "with@{at-brace", "feature/.hidden",
	}
	for _, name := range invalid {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) = nil, want an error", name)
		}
	}
}

// TestValidateNameRejectsShellMetacharacters covers ref names that Git
// itself would allow but that would let a pusher inject commands into
// any pipeline step that puts $GITMAN_REF into a nested shell call
// unquoted, since every ref name reaches every step verbatim.
func TestValidateNameRejectsShellMetacharacters(t *testing.T) {
	invalid := []string{
		"$(id)", "`id`", "a;touch pwned", "a|b", "a&b", "a>b", "a<b",
		"a(b)", "a{b}", `a"b`, "a'b",
	}
	for _, name := range invalid {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) = nil, want an error", name)
		}
	}
}

func TestValidateNameRejectsStar(t *testing.T) {
	if err := ValidateName("v*"); err == nil {
		t.Error(`ValidateName("v*") = nil, want an error (use ValidatePattern for wildcards)`)
	}
}

func TestValidatePattern(t *testing.T) {
	valid := []string{"develop", "main", "v*", "release/*", "*"}
	for _, pattern := range valid {
		if err := ValidatePattern(pattern); err != nil {
			t.Errorf("ValidatePattern(%q) = %v, want nil", pattern, err)
		}
	}

	invalid := []string{"", ".hidden*", "trailing*."}
	for _, pattern := range invalid {
		if err := ValidatePattern(pattern); err == nil {
			t.Errorf("ValidatePattern(%q) = nil, want an error", pattern)
		}
	}
}

func TestLooksLikeCommitHash(t *testing.T) {
	hashLike := []string{"ABCDEF1", strings.Repeat("a", 64), "3f2a91c", "a81c03e91be2d4fedcba98", "0123456789abcdef0123456789abcdef01234567"}
	for _, name := range hashLike {
		if !LooksLikeCommitHash(name) {
			t.Errorf("LooksLikeCommitHash(%q) = false, want true", name)
		}
	}

	notHashLike := []string{"develop", "main", "v1.4.2", "abc", "release/1.2", strings.Repeat("a", 65)}
	for _, name := range notHashLike {
		if LooksLikeCommitHash(name) {
			t.Errorf("LooksLikeCommitHash(%q) = true, want false", name)
		}
	}
}

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"develop", "develop", true},
		{"develop", "developer", false},
		{"v*", "v1.4.2", true},
		{"v*", "1.4.2", false},
		{"*", "anything/at/all", true},
		{"release/*", "release/1.2", true},
		{"release/*", "other/1.2", false},
	}
	for _, c := range cases {
		if got := MatchPattern(c.pattern, c.name); got != c.want {
			t.Errorf("MatchPattern(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestSelectPatternExactBeatsWildcard(t *testing.T) {
	patterns := []string{"*", "main"}
	if got := SelectPattern(patterns, "main"); got != 1 {
		t.Errorf("SelectPattern = %d, want 1 (the exact pattern)", got)
	}
}

func TestSelectPatternMostSpecificWildcardWins(t *testing.T) {
	patterns := []string{"*", "v*"}
	if got := SelectPattern(patterns, "v1.4.2"); got != 1 {
		t.Errorf("SelectPattern = %d, want 1 (the more specific pattern)", got)
	}
}

func TestSelectPatternNoMatch(t *testing.T) {
	patterns := []string{"v*", "release/*"}
	if got := SelectPattern(patterns, "develop"); got != -1 {
		t.Errorf("SelectPattern = %d, want -1 (no match)", got)
	}
}

func TestFullNameRoundTrip(t *testing.T) {
	cases := []struct {
		kind Kind
		name string
		full string
	}{
		{KindBranch, "develop", "refs/heads/develop"},
		{KindBranch, "release/1.2", "refs/heads/release/1.2"},
		{KindTag, "v1.4.2", "refs/tags/v1.4.2"},
	}
	for _, c := range cases {
		kind, name, ok := SplitFullName(c.full)
		if !ok || kind != c.kind || name != c.name {
			t.Errorf("SplitFullName(%q) = %s, %q, %v, want %s, %q, true", c.full, kind, name, ok, c.kind, c.name)
		}
		if full := FullName(c.kind, c.name); full != c.full {
			t.Errorf("FullName(%s, %q) = %q, want %q", c.kind, c.name, full, c.full)
		}
	}
}

func TestSplitFullNameRejectsOtherNamespaces(t *testing.T) {
	for _, full := range []string{"refs/notes/commits", "refs/pull/1/head", "HEAD", "refs/heads/", "refs/tags/"} {
		if _, _, ok := SplitFullName(full); ok {
			t.Errorf("SplitFullName(%q) ok = true, want false", full)
		}
	}
}

func TestVersionLessReadsNumbersAsNumbers(t *testing.T) {
	ordered := []string{
		"beta", "v1", "v1.0.0-beta.2", "v1.0.0-beta.9", "v1.0.0-beta.10", "v1.0.0-beta.21", "v1.0.0-rc1", "v1.2", "v1.10", "v2",
	}
	for i, a := range ordered {
		for j, b := range ordered {
			if got, want := VersionLess(a, b), i < j; got != want {
				t.Errorf("VersionLess(%q, %q) = %v, want %v", a, b, got, want)
			}
		}
	}
	// Leading zeros do not change a number, and never make the order unstable.
	if VersionLess("v01", "v1") == VersionLess("v1", "v01") {
		t.Error("v01 and v1 have no order between them")
	}
	if VersionLess("v1", "v1") {
		t.Error("a name is less than itself")
	}
}
