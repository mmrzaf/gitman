// refs.go validates Git ref names and ref_rules patterns, and decides
// which of several patterns matches a name most specifically — the one
// rule ref rules and pipeline targets share.

package git

import (
	"regexp"
	"strings"

	"github.com/mmrzaf/gitman/internal/apperr"
)

// Kind distinguishes a branch from a tag. It is the vocabulary used
// throughout Gitman: in ref_rules, in runs, and in "@ref" URLs.
type Kind string

const (
	KindBranch Kind = "branch"
	KindTag    Kind = "tag"
)

// Other returns the kind that is not k.
func (k Kind) Other() Kind {
	if k == KindTag {
		return KindBranch
	}
	return KindTag
}

// controlOrSpecial rejects Git's own reserved ref characters, plus every
// character with a special meaning to a POSIX shell. Git would accept
// several of the shell ones, but a ref name reaches every pipeline step
// verbatim as $GITMAN_REF, and a pipeline author who writes a step like
// `docker build -t app:$GITMAN_REF .` is not expecting a branch name to
// be able to run its own commands there.
var controlOrSpecial = regexp.MustCompile("[\x00-\x20\x7f~^:?*\\[\\\\$`(){}<>;|&'\"]")

// ValidateName reports whether name is a syntactically valid Git ref
// component (the part after "refs/heads/" or "refs/tags/"), following the
// structural rules `git check-ref-format` enforces, tightened to also
// reject shell metacharacters. It rejects '*': for a ref_rules pattern,
// which may contain '*' as a wildcard, use ValidatePattern instead.
func ValidateName(name string) error {
	if name == "" {
		return apperr.New(apperr.KindInvalid, "ref name must not be empty")
	}
	if controlOrSpecial.MatchString(name) {
		return apperr.New(apperr.KindInvalid, "ref name must not contain control characters, shell metacharacters, or any of: space ~ ^ : ? * [ \\")
	}
	if strings.Contains(name, "..") {
		return apperr.New(apperr.KindInvalid, "ref name must not contain '..'")
	}
	if strings.Contains(name, "@{") {
		return apperr.New(apperr.KindInvalid, "ref name must not contain '@{'")
	}
	if name == "@" {
		return apperr.New(apperr.KindInvalid, "ref name must not be exactly '@'")
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.Contains(name, "//") {
		return apperr.New(apperr.KindInvalid, "ref name must not start or end with '/' or contain '//'")
	}
	if strings.HasSuffix(name, ".") {
		return apperr.New(apperr.KindInvalid, "ref name must not end with '.'")
	}
	if strings.HasSuffix(name, ".lock") {
		return apperr.New(apperr.KindInvalid, "ref name must not end with '.lock'")
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" {
			return apperr.New(apperr.KindInvalid, "ref name must not have an empty path component")
		}
		if strings.HasSuffix(component, ".lock") {
			return apperr.New(apperr.KindInvalid, "ref name components must not end with .lock")
		}
		if strings.HasPrefix(component, ".") {
			return apperr.New(apperr.KindInvalid, "ref name components must not start with '.'")
		}
	}
	return nil
}

// ValidatePattern validates a ref_rules pattern: the same structural rules
// as ValidateName, except that '*' is allowed as a wildcard.
func ValidatePattern(pattern string) error {
	if pattern == "" {
		return apperr.New(apperr.KindInvalid, "pattern must not be empty")
	}
	probe := strings.ReplaceAll(pattern, "*", "x")
	if err := ValidateName(probe); err != nil {
		return apperr.New(apperr.KindInvalid, "invalid pattern: "+apperr.PublicMessage(err))
	}
	return nil
}

var hashLikePattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// LooksLikeCommitHash reports whether name has the shape of an abbreviated
// or full Git commit hash. Gitman resolves "@<7-64 hex characters>" as a
// commit, so a branch or tag with the same shape would be permanently
// unreachable by name; ref creation rejects it instead of resolving it
// ambiguously.
func LooksLikeCommitHash(name string) bool {
	return hashLikePattern.MatchString(strings.ToLower(name))
}

// FullName is the full ref name of a branch or tag: the inverse of
// SplitFullName.
func FullName(kind Kind, name string) string {
	if kind == KindTag {
		return "refs/tags/" + name
	}
	return "refs/heads/" + name
}

// SplitFullName splits a full ref name into its kind and short name. It
// reports ok == false for anything that is not a branch or a tag, such as
// refs/notes/* or refs/pull/*, which Gitman does not accept.
func SplitFullName(full string) (kind Kind, name string, ok bool) {
	switch {
	case strings.HasPrefix(full, "refs/heads/") && len(full) > len("refs/heads/"):
		return KindBranch, strings.TrimPrefix(full, "refs/heads/"), true
	case strings.HasPrefix(full, "refs/tags/") && len(full) > len("refs/tags/"):
		return KindTag, strings.TrimPrefix(full, "refs/tags/"), true
	}
	return "", "", false
}

// MatchPattern reports whether name matches pattern, where '*' matches
// any sequence of characters, including '/'. This is deliberately simpler
// than glob or Git pathspec matching: Gitman's own patterns only ever
// need "exact name" or "prefix/suffix around one or more wildcard
// regions" — "develop", "v*", "release/*".
func MatchPattern(pattern, name string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == name
	}
	parts := strings.Split(pattern, "*")

	if !strings.HasPrefix(name, parts[0]) {
		return false
	}
	name = name[len(parts[0]):]

	last := parts[len(parts)-1]
	if !strings.HasSuffix(name, last) {
		return false
	}
	name = name[:len(name)-len(last)]

	for _, part := range parts[1 : len(parts)-1] {
		idx := strings.Index(name, part)
		if idx < 0 {
			return false
		}
		name = name[idx+len(part):]
	}
	return true
}

// Specificity is a pattern's count of literal, non-'*' characters. Used to
// compare two patterns that both match the same name.
func Specificity(pattern string) int {
	return len(pattern) - strings.Count(pattern, "*")
}

// MoreSpecific reports whether pattern a should be preferred over pattern
// b when both match the same name: more literal characters wins, then
// the shorter pattern, then lexicographic order — so the result never
// depends on which pattern was declared first.
func MoreSpecific(a, b string) bool {
	sa, sb := Specificity(a), Specificity(b)
	if sa != sb {
		return sa > sb
	}
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// SelectPattern returns the index into patterns of the single most
// specific pattern matching name, or -1 if none matches. An exact
// (wildcard-free) pattern always wins over one with a wildcard — even a
// wildcard pattern with many literal characters is still less specific
// than an exact match — and MoreSpecific breaks ties among the rest. This
// is the one rule Gitman uses everywhere it must pick a single match from
// several candidate patterns: ref rules and pipeline targets both call it
// rather than each inventing its own precedence.
func SelectPattern(patterns []string, name string) int {
	best := -1
	for i, pattern := range patterns {
		if !MatchPattern(pattern, name) {
			continue
		}
		if !strings.Contains(pattern, "*") {
			return i
		}
		if best == -1 || MoreSpecific(pattern, patterns[best]) {
			best = i
		}
	}
	return best
}

// VersionLess orders names the way a person reads versions: runs of digits
// compare as numbers, so "v1.0.0-beta.9" comes before "v1.0.0-beta.10", and
// everything else compares as text. Names that read the same, such as "v01"
// and "v1", fall back to plain text order, so the order is always total.
func VersionLess(a, b string) bool {
	if c := versionCompare(a, b); c != 0 {
		return c < 0
	}
	return a < b
}

// versionCompare is -1, 0 or 1 for a before, the same as, or after b.
func versionCompare(a, b string) int {
	for a != "" && b != "" {
		da, db := leadingDigits(a), leadingDigits(b)
		if da != "" && db != "" {
			na, nb := strings.TrimLeft(da, "0"), strings.TrimLeft(db, "0")
			switch {
			case len(na) != len(nb):
				return cmpInt(len(na), len(nb))
			case na != nb:
				return strings.Compare(na, nb)
			}
			a, b = a[len(da):], b[len(db):]
			continue
		}
		if a[0] != b[0] {
			return cmpInt(int(a[0]), int(b[0]))
		}
		a, b = a[1:], b[1:]
	}
	return cmpInt(len(a), len(b))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// leadingDigits is the run of ASCII digits s starts with.
func leadingDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}
