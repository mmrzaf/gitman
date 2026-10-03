// Package repo manages repositories: the record that names one and the
// bare Git repository on disk that holds its content, who may read it,
// its ref rules and default push policy (together, the only permission
// system for writing to it), its ref index, its encrypted secrets, and
// the settings-change events those generate.
//
// service.go holds the package's rules and orchestration; store.go holds
// every SQL statement the package runs. Nothing outside this package
// queries these tables directly.
package repo

import (
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/git"
)

// MaxDescriptionLen is the longest repository description, in
// characters.
const MaxDescriptionLen = 500

// Visibility controls who may read a repository.
type Visibility string

const (
	// VisibilityEveryone lets every signed-in person read it.
	VisibilityEveryone Visibility = "everyone"
	// VisibilityRestricted limits reading to its explicit readers and
	// admins; to anyone else it does not exist.
	VisibilityRestricted Visibility = "restricted"
)

// ValidateVisibility checks a visibility value.
func ValidateVisibility(v Visibility) error {
	switch v {
	case VisibilityEveryone, VisibilityRestricted:
		return nil
	default:
		return apperr.New(apperr.KindInvalid, fmt.Sprintf("visibility must be %q or %q", VisibilityEveryone, VisibilityRestricted))
	}
}

// Repo is a repository record.
type Repo struct {
	ID            string
	Name          string
	Description   string
	DefaultBranch string
	// Visibility controls who may read this repository.
	Visibility Visibility
	// DefaultPushPolicy and DefaultPushPeople are who may push to a ref
	// no rule matches — the same shape, and the same meaning, as a
	// rule's own push policy.
	DefaultPushPolicy PushPolicy
	DefaultPushPeople []string
	CreatedBy         *string
	CreatedAt         time.Time
}

// IndexedRef is one row of the ref index: where a branch or tag points,
// and who last moved it.
type IndexedRef struct {
	UpdatedByUsername string
	Head              *git.Commit
	Kind              git.Kind
	Name              string
	Commit            string
	UpdatedAt         time.Time
	UpdatedBy         *string
}

// Secret is one repository secret's metadata: its key and who set it,
// never its value. Once set, a value can be replaced or deleted but
// never read back — the same shape as an access token.
type Secret struct {
	Key       string
	CreatedAt time.Time
	CreatedBy *string
}

// ValidateDescription checks a description's length in characters, so a
// limit shown in a form means the same thing for every script.
func ValidateDescription(description string) error {
	if utf8.RuneCountInString(description) > MaxDescriptionLen {
		return apperr.New(apperr.KindInvalid, fmt.Sprintf("description must be at most %d characters", MaxDescriptionLen))
	}
	return nil
}

// ValidateDefaultBranch checks a default branch name.
func ValidateDefaultBranch(branch string) error {
	if err := git.ValidateName(branch); err != nil {
		return apperr.New(apperr.KindInvalid, "default branch: "+apperr.PublicMessage(err))
	}
	if git.LooksLikeCommitHash(branch) {
		return apperr.New(apperr.KindInvalid, fmt.Sprintf("default branch %q looks like a commit hash", branch))
	}
	return nil
}
