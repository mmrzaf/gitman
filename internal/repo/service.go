package repo

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/id"
	"github.com/mmrzaf/gitman/internal/names"
	"github.com/mmrzaf/gitman/internal/postgres"
)

// Service manages repositories, their rules, ref index and secrets.
// Methods that change something take actorID, the person making the
// change (empty for the command line), and record the change in the
// activity log in the same transaction.
type Service struct {
	db  *postgres.DB
	git *git.Store
	// secretKey is the instance's GITMAN_SECRET_KEY; empty means secret
	// storage is unavailable.
	secretKey string
}

// NewService returns a Service backed by db and the bare repositories in
// store. secretKey encrypts repository secrets; it may be empty.
func NewService(db *postgres.DB, store *git.Store, secretKey string) *Service {
	return &Service{db: db, git: store, secretKey: secretKey}
}

// Create adds a repository record and initializes its bare repository.
// The row and the directory are created together: if either fails,
// neither is left behind.
func (s *Service) Create(ctx context.Context, name, description, defaultBranch, actorID string) (*Repo, error) {
	if err := names.ValidateRepository(name); err != nil {
		return nil, err
	}
	description = strings.TrimSpace(description)
	if err := ValidateDescription(description); err != nil {
		return nil, err
	}
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	if err := ValidateDefaultBranch(defaultBranch); err != nil {
		return nil, err
	}

	var r *Repo
	created := false
	err := s.db.Tx(ctx, func(tx postgres.Tx) error {
		var err error
		if r, err = insertRepo(ctx, tx, id.New(), name, description, defaultBranch, actorID); err != nil {
			return err
		}
		if err := activity.Record(ctx, tx, r.ID, actorID, activity.RepoCreated, r.Name); err != nil {
			return err
		}
		// The directory is created last inside the transaction, so a
		// failed insert never leaves a directory behind; if the commit
		// itself fails, the directory is removed below.
		if err := s.git.Create(ctx, r.ID, defaultBranch); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		if created {
			if delErr := s.git.Delete(r.ID); delErr != nil && !errors.Is(delErr, git.ErrNotFound) {
				return nil, fmt.Errorf("%w (and its half-created directory could not be removed either: %v)", err, delErr)
			}
		}
		return nil, err
	}
	return r, nil
}

// GetByName looks up a repository by name.
func (s *Service) GetByName(ctx context.Context, name string) (*Repo, error) {
	return selectRepoByName(ctx, s.db.Q, name)
}

// GetByID looks up a repository by ID.
func (s *Service) GetByID(ctx context.Context, repoID string) (*Repo, error) {
	return selectRepoByID(ctx, s.db.Q, repoID)
}

// List returns every repository, ordered by name, regardless of
// visibility. It is for the command line, which acts with full
// authority and has no person to check readability against.
func (s *Service) List(ctx context.Context) ([]*Repo, error) {
	return selectRepos(ctx, s.db.Q)
}

// ListReadable returns, ordered by name, every repository personID may
// read: every repository for an admin, otherwise those visible to
// everyone plus those personID is an explicit reader of. It filters in
// SQL, not by loading every repository and discarding some.
func (s *Service) ListReadable(ctx context.Context, personID string, isAdmin bool) ([]*Repo, error) {
	if isAdmin {
		return s.List(ctx)
	}
	return selectReadableRepos(ctx, s.db.Q, personID)
}

// CanRead reports whether personID may read r: always for an admin or a
// repository visible to everyone, otherwise only if personID is one of
// its explicit readers.
func (s *Service) CanRead(ctx context.Context, r *Repo, personID string, isAdmin bool) (bool, error) {
	if isAdmin || r.Visibility == VisibilityEveryone {
		return true, nil
	}
	if personID == "" {
		return false, nil
	}
	return selectIsReader(ctx, s.db.Q, r.ID, personID)
}

// CanReadID is CanRead for a caller that has a repository's ID but has
// not loaded the repository itself, such as an event naming it.
func (s *Service) CanReadID(ctx context.Context, repoID, personID string, isAdmin bool) (bool, error) {
	r, err := s.GetByID(ctx, repoID)
	if err != nil {
		return false, err
	}
	return s.CanRead(ctx, r, personID, isAdmin)
}

// Open returns the bare Git repository behind a repository record. A
// record whose directory is missing is a damaged installation, not an
// empty repository — an empty repository still has its directory — so
// that is reported as an error naming the repository.
func (s *Service) Open(r *Repo) (*git.Repo, error) {
	gitRepo, err := s.git.Open(r.ID)
	if err != nil {
		return nil, fmt.Errorf("open repository %s: %w", r.Name, err)
	}
	return gitRepo, nil
}

// Delete removes a repository's record — and with it, through foreign
// keys, its refs, rules, pushes and runs — and then its directory. If
// removing the directory fails after the record is gone, the repository
// is already unreachable, and the returned error names the files an
// operator must remove.
func (s *Service) Delete(ctx context.Context, repoID, actorID string) error {
	var name string
	err := s.db.Tx(ctx, func(tx postgres.Tx) error {
		// A push landing now finishes recording itself first, or finds
		// the repository gone; never half of each.
		if err := lockRefIndex(ctx, tx, repoID); err != nil {
			return err
		}
		var err error
		if name, err = deleteRepoRow(ctx, tx, repoID); err != nil {
			return err
		}
		return activity.Record(ctx, tx, "", actorID, activity.RepoDeleted, name)
	})
	if err != nil {
		return err
	}
	if err := s.git.Delete(repoID); err != nil && !errors.Is(err, git.ErrNotFound) {
		return fmt.Errorf("repository %s was removed but its files were not: %w", name, err)
	}
	return nil
}

// SetDescription updates a repository's description.
func (s *Service) SetDescription(ctx context.Context, repoID, description, actorID string) error {
	description = strings.TrimSpace(description)
	if err := ValidateDescription(description); err != nil {
		return err
	}
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := updateDescriptionRow(ctx, tx, repoID, description); err != nil {
			return err
		}
		return activity.Record(ctx, tx, repoID, actorID, activity.RepoDescribed, "")
	})
}

// SetDefaultBranch makes branch the repository's default branch: the one
// a clone checks out, the Files page opens and comparisons are made
// against. It must be a branch the repository has. Gitman never changes
// the default branch on its own; this is the only way it changes.
//
// The record and Git's HEAD change together: HEAD is moved last inside
// the transaction, and moved back if the transaction then fails to
// commit.
func (s *Service) SetDefaultBranch(ctx context.Context, r *Repo, branch, actorID string) error {
	if err := ValidateDefaultBranch(branch); err != nil {
		return err
	}
	gitRepo, err := s.Open(r)
	if err != nil {
		return err
	}
	var previous string
	headMoved := false
	err = s.db.Tx(ctx, func(tx postgres.Tx) error {
		// Holding the ref index lock keeps a push from being recorded
		// between checking that the branch exists and choosing it.
		if err := lockRefIndex(ctx, tx, r.ID); err != nil {
			return err
		}
		refs, err := gitRepo.Refs(ctx)
		if err != nil {
			return fmt.Errorf("list refs: %w", err)
		}
		if !slices.ContainsFunc(refs, func(ref git.Ref) bool { return ref.Kind == git.KindBranch && ref.Name == branch }) {
			return apperr.New(apperr.KindInvalid, fmt.Sprintf("%s has no branch named %q", r.Name, branch))
		}
		if previous, err = updateDefaultBranchRow(ctx, tx, r.ID, branch); err != nil {
			return err
		}
		if previous == branch {
			return nil
		}
		if err := activity.Record(ctx, tx, r.ID, actorID, activity.RepoDefaultBranchChanged, branch); err != nil {
			return err
		}
		if err := gitRepo.SetHead(ctx, branch); err != nil {
			return err
		}
		headMoved = true
		return nil
	})
	if err != nil && headMoved {
		if restoreErr := gitRepo.SetHead(context.WithoutCancel(ctx), previous); restoreErr != nil {
			return fmt.Errorf("%w (and HEAD could not be moved back to %s either: %v)", err, previous, restoreErr)
		}
	}
	return err
}

// SetVisibility changes who may read a repository.
func (s *Service) SetVisibility(ctx context.Context, repoID string, v Visibility, actorID string) error {
	if err := ValidateVisibility(v); err != nil {
		return err
	}
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := updateVisibilityRow(ctx, tx, repoID, v); err != nil {
			return err
		}
		return activity.Record(ctx, tx, repoID, actorID, activity.RepoVisibilityChanged, string(v))
	})
}

// AddReader grants personID read access to a repository, regardless of
// its current visibility. readerName is recorded in the activity log,
// the same way every other action naming a person records their
// username rather than their ID; the grant itself is keyed by personID.
// repo has no dependency on the auth package to look readerName up
// itself, so every caller passes it in already knowing it.
func (s *Service) AddReader(ctx context.Context, repoID, personID, readerName, actorID string) error {
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := insertReaderRow(ctx, tx, repoID, personID); err != nil {
			return err
		}
		return activity.Record(ctx, tx, repoID, actorID, activity.RepoReaderAdded, readerName)
	})
}

// RemoveReader revokes personID's explicit read access to a repository.
// See AddReader on readerName.
func (s *Service) RemoveReader(ctx context.Context, repoID, personID, readerName, actorID string) error {
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := deleteReaderRow(ctx, tx, repoID, personID); err != nil {
			return err
		}
		return activity.Record(ctx, tx, repoID, actorID, activity.RepoReaderRemoved, readerName)
	})
}

// ListReaders returns the person IDs of a repository's explicit readers,
// in the order they were added.
func (s *Service) ListReaders(ctx context.Context, repoID string) ([]string, error) {
	return selectReaderIDs(ctx, s.db.Q, repoID)
}

// SetDefaultPush changes who may push to a ref no rule matches.
func (s *Service) SetDefaultPush(ctx context.Context, repoID string, policy PushPolicy, people []string, actorID string) error {
	if err := ValidateDefaultPush(policy, people); err != nil {
		return err
	}
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := updateDefaultPushRow(ctx, tx, repoID, policy, people); err != nil {
			return err
		}
		return activity.Record(ctx, tx, repoID, actorID, activity.RepoDefaultPushChanged, string(policy))
	})
}

// ListRules returns a repository's ref rules.
func (s *Service) ListRules(ctx context.Context, repoID string) ([]Rule, error) {
	return selectRules(ctx, s.db.Q, repoID)
}

// SaveRule creates or replaces the rule for (kind, pattern).
func (s *Service) SaveRule(ctx context.Context, repoID string, r Rule, actorID string) error {
	if err := ValidateRule(r); err != nil {
		return err
	}
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := upsertRule(ctx, tx, id.New(), repoID, r, actorID); err != nil {
			return err
		}
		return activity.Record(ctx, tx, repoID, actorID, activity.RuleSaved, string(r.Kind)+" "+r.Pattern)
	})
}

// DeleteRule removes the rule for (kind, pattern).
func (s *Service) DeleteRule(ctx context.Context, repoID string, kind git.Kind, pattern, actorID string) error {
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := deleteRuleRow(ctx, tx, repoID, kind, pattern); err != nil {
			return err
		}
		return activity.Record(ctx, tx, repoID, actorID, activity.RuleDeleted, string(kind)+" "+pattern)
	})
}

// ListRefs returns a repository's indexed refs, most recently updated
// first.
func (s *Service) ListRefs(ctx context.Context, repoID string) ([]IndexedRef, error) {
	return selectRefs(ctx, s.db.Q, repoID)
}

// SyncRefs rebuilds a repository's ref index from what Git has, for a
// repository whose files were restored or changed outside Gitman, and
// returns how many refs it has.
func (s *Service) SyncRefs(ctx context.Context, r *Repo) (int, error) {
	gitRepo, err := s.Open(r)
	if err != nil {
		return 0, err
	}
	var n int
	err = s.db.Tx(ctx, func(tx postgres.Tx) error {
		refs, err := s.SyncRefsTx(ctx, tx, r.ID, gitRepo, "")
		n = len(refs)
		return err
	})
	return n, err
}

// SyncRefsTx makes a repository's ref index match Git exactly, inside
// the caller's transaction, and returns how many refs Git has: rows are
// inserted and updated for every ref Git has and deleted for refs it no
// longer has. Refs whose commit changed are attributed to personID.
//
// It first takes a per-repository lock held until the transaction ends,
// and only then reads Git. Two pushes finishing at once therefore sync
// one after the other, and the later one always reads the later state:
// reading first and locking second would let an older snapshot commit
// last and delete a branch the other push had just created.
//
// Syncing the whole index, rather than applying only the refs one push
// named, also means a push that was never recorded — because its hook
// failed after Git had already accepted it — is corrected by the next.
//
// It returns the refs Git has, as read under that lock: what a push's
// post-receive must go by, since another push may have moved a ref it
// named since Git accepted it.
func (s *Service) SyncRefsTx(ctx context.Context, tx postgres.Tx, repoID string, gitRepo *git.Repo, personID string) ([]git.Ref, error) {
	if err := lockRefIndex(ctx, tx, repoID); err != nil {
		return nil, err
	}
	actual, err := gitRepo.Refs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list refs: %w", err)
	}
	kinds := make([]string, len(actual))
	refNames := make([]string, len(actual))
	commits := make([]string, len(actual))
	for i, r := range actual {
		kinds[i], refNames[i], commits[i] = string(r.Kind), r.Name, r.Commit
	}
	return actual, syncRefsRows(ctx, tx, repoID, kinds, refNames, commits, personID)
}

// MaxSecretValueLen bounds a secret's plaintext value.
const MaxSecretValueLen = 8 << 10

var secretKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// ValidateSecretKey checks a secret's name against the same shape as a
// pipeline environment variable, since that's what it becomes: injected
// into a run as $KEY.
func ValidateSecretKey(key string) error {
	if !secretKeyPattern.MatchString(key) {
		return apperr.New(apperr.KindInvalid, "secret name must be uppercase letters, digits and underscores, starting with a letter, up to 64 characters")
	}
	if strings.HasPrefix(key, "GITMAN_") {
		return apperr.New(apperr.KindInvalid, fmt.Sprintf("%q starts with the \"GITMAN_\" prefix, which is reserved for Gitman's own variables", key))
	}
	// Secret values reach a step through the Docker client's own
	// environment, so a secret must not replace a variable the client
	// reads for itself.
	if key == "PATH" || key == "HOME" || strings.HasPrefix(key, "DOCKER_") {
		return apperr.New(apperr.KindInvalid, fmt.Sprintf("%q is used by the Docker client that runs steps, so it cannot be a secret's name", key))
	}
	return nil
}

// ErrSecretsUnavailable is returned when storing a secret on an instance
// without GITMAN_SECRET_KEY.
var ErrSecretsUnavailable = errors.New("GITMAN_SECRET_KEY is not configured on this instance, so secrets can't be stored")

// errSecretsUnavailable carries the public-facing form of
// ErrSecretsUnavailable, preserving the sentinel underneath it so
// errors.Is(err, ErrSecretsUnavailable) still matches.
var errSecretsUnavailable = apperr.Wrap(apperr.KindInvalid, ErrSecretsUnavailable.Error(), ErrSecretsUnavailable)

// SecretsAvailable reports whether the instance can store secrets.
func (s *Service) SecretsAvailable() bool {
	return s.secretKey != ""
}

// ListSecrets returns a repository's secrets, without their values.
func (s *Service) ListSecrets(ctx context.Context, repoID string) ([]Secret, error) {
	return selectSecrets(ctx, s.db.Q, repoID)
}

// SetSecret encrypts value and creates or replaces the repository secret
// named key.
func (s *Service) SetSecret(ctx context.Context, repoID, key, value, actorID string) error {
	if !s.SecretsAvailable() {
		return errSecretsUnavailable
	}
	if err := ValidateSecretKey(key); err != nil {
		return err
	}
	if value == "" {
		return apperr.New(apperr.KindInvalid, "secret value must not be empty")
	}
	if utf8.RuneCountInString(value) > MaxSecretValueLen {
		return apperr.New(apperr.KindInvalid, fmt.Sprintf("secret value must be at most %d characters", MaxSecretValueLen))
	}
	ciphertext, nonce, err := encryptSecret(s.secretKey, value, secretContext(repoID, key))
	if err != nil {
		return err
	}
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := upsertSecretRow(ctx, tx, id.New(), repoID, key, ciphertext, nonce, actorID); err != nil {
			return err
		}
		return activity.Record(ctx, tx, repoID, actorID, activity.SecretSet, key)
	})
}

// DeleteSecret removes a repository secret.
func (s *Service) DeleteSecret(ctx context.Context, repoID, key, actorID string) error {
	return s.db.Tx(ctx, func(tx postgres.Tx) error {
		if err := deleteSecretRow(ctx, tx, repoID, key); err != nil {
			return err
		}
		return activity.Record(ctx, tx, repoID, actorID, activity.SecretDeleted, key)
	})
}

// RunSecrets decrypts every secret of a repository, for injecting into a
// run whose rule allows secrets. It fails if any secret cannot be
// decrypted — typically because GITMAN_SECRET_KEY changed — rather than
// running with some secrets silently missing.
func (s *Service) RunSecrets(ctx context.Context, repoID string) (map[string]string, error) {
	sealed, err := selectSecretValues(ctx, s.db.Q, repoID)
	if err != nil {
		return nil, err
	}
	if len(sealed) > 0 && !s.SecretsAvailable() {
		return nil, ErrSecretsUnavailable
	}
	values := make(map[string]string, len(sealed))
	for key, v := range sealed {
		plain, err := decryptSecret(s.secretKey, v.ciphertext, v.nonce, secretContext(repoID, key))
		if err != nil {
			return nil, fmt.Errorf("secret %s: %w", key, err)
		}
		values[key] = plain
	}
	return values, nil
}
