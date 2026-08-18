package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/validate"
)

var (
	// ErrRepoEmpty is returned when an operation needs commits but the repo has none.
	ErrRepoEmpty = errors.New("repository is empty")

	// ErrRepoPathExists prevents orphaned repository contents from being adopted.
	ErrRepoPathExists = errors.New("repository path already exists")

	// ErrInvalidRef is returned when a requested ref is syntactically invalid.
	ErrInvalidRef = errors.New("invalid ref")

	// ErrInvalidCommit is returned when a requested commit identifier is not a
	// supported full or abbreviated object ID.
	ErrInvalidCommit = errors.New("invalid commit")

	// ErrRefNotFound is returned when a requested ref cannot be resolved.
	ErrRefNotFound = errors.New("ref not found")

	// ErrPathNotFound is returned when a requested path does not exist at a
	// valid repository revision.
	ErrPathNotFound = errors.New("path not found")
)

type Commit struct {
	Hash    string
	Author  string
	Email   string
	Date    time.Time
	Message string
}

type TreeEntry struct {
	Mode string
	Type string
	Hash string
	Size int64
	Name string
}

var (
	commitHashRegex       = regexp.MustCompile(`^[A-Fa-f0-9]{7,64}$`)
	canonicalGitHashRegex = regexp.MustCompile(`^(?:[A-Fa-f0-9]{40}|[A-Fa-f0-9]{64})$`)
)

// ValidateRefNameContext asks Git to validate the ref grammar Gitman accepts.
// Gitman adds only a small length/control-character bound of its own. The
// validation process inherits caller cancellation and is also bounded so a
// request cannot hang indefinitely on what should be a cheap syntax check.
func ValidateRefNameContext(ctx context.Context, ref string) error {
	if ref == "" {
		return nil
	}
	if len(ref) > 255 {
		return ErrInvalidRef
	}
	if strings.ContainsAny(ref, "\x00\r\n") {
		return ErrInvalidRef
	}
	validateCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := runGit(validateCtx, "check-ref-format", "refs/heads/"+ref)
	if err == nil {
		return nil
	}
	if ctxErr := validateCtx.Err(); ctxErr != nil {
		return fmt.Errorf("validate ref name: %w", ctxErr)
	}
	if _, ok := commandExitCode(err); ok {
		return ErrInvalidRef
	}
	return fmt.Errorf("validate ref name: %w", err)
}

// ValidateRefName is retained for callers without a request/job context.
func ValidateRefName(ref string) error {
	return ValidateRefNameContext(context.Background(), ref)
}

// SecureRepoPath guarantees the resulting path is safely inside the base directory
func SecureRepoPath(basePath, username, repoName string) (string, error) {
	if err := validate.StorageName(username); err != nil {
		return "", fmt.Errorf("invalid username: %w", err)
	}
	if err := validate.StorageName(repoName); err != nil {
		return "", fmt.Errorf("invalid repository name: %w", err)
	}

	fullPath := filepath.Join(basePath, username, fmt.Sprintf("%s.git", repoName))

	// Double-check containment with filepath.Rel rather than a string prefix.
	// Prefix comparisons reject valid roots such as "." after cleaning and are
	// easy to get wrong for similarly named sibling directories.
	cleanBase := filepath.Clean(basePath)
	cleanPath := filepath.Clean(fullPath)
	rel, err := filepath.Rel(cleanBase, cleanPath)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal attempt detected")
	}

	return cleanPath, nil
}

// InitBareRepo initializes a new bare repository at fullPath. Existing paths
// are rejected so orphaned repository contents can never be adopted by a new
// database record.
func InitBareRepo(ctx context.Context, fullPath string, receiveMaxBytes int64) error {
	if receiveMaxBytes <= 0 {
		return fmt.Errorf("receive.maxInputSize must be positive")
	}
	if _, err := os.Lstat(fullPath); err == nil {
		return ErrRepoPathExists
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect repo path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		return fmt.Errorf("create repo parent directory: %w", err)
	}
	if err := os.Mkdir(fullPath, 0o700); err != nil {
		return fmt.Errorf("create repo directory: %w", err)
	}

	if _, err := run(ctx, fullPath, "init", "--bare", "--initial-branch=main", "."); err != nil {
		primary := fmt.Errorf("git init --bare failed: %w", err)
		if rmErr := os.RemoveAll(fullPath); rmErr != nil {
			return errors.Join(primary, fmt.Errorf("cleanup failed repository: %w", rmErr))
		}
		return primary
	}
	if err := ConfigureReceiveMaxInputSize(ctx, fullPath, receiveMaxBytes); err != nil {
		if rmErr := os.RemoveAll(fullPath); rmErr != nil {
			return errors.Join(err, fmt.Errorf("cleanup failed repository: %w", rmErr))
		}
		return err
	}

	return nil
}

func ConfigureReceiveMaxInputSize(ctx context.Context, repoPath string, maxBytes int64) error {
	if maxBytes <= 0 {
		return fmt.Errorf("receive.maxInputSize must be positive")
	}
	_, err := run(ctx, repoPath, "config", "receive.maxInputSize", strconv.FormatInt(maxBytes, 10))
	if err != nil {
		return fmt.Errorf("configure receive.maxInputSize: %w", err)
	}
	return nil
}

// QuarantineRepo atomically moves a repository out of its active namespace.
// The returned path can be restored when the following database mutation fails.
func QuarantineRepo(fullPath string) (string, error) {
	if _, err := os.Lstat(fullPath); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", err
	}

	base := filepath.Dir(filepath.Dir(fullPath))
	trashRoot := filepath.Join(base, ".trash", "repos")
	if err := os.MkdirAll(trashRoot, 0o700); err != nil {
		return "", err
	}
	placeholder, err := os.MkdirTemp(trashRoot, filepath.Base(fullPath)+"-")
	if err != nil {
		return "", err
	}
	if err := os.Remove(placeholder); err != nil {
		return "", err
	}
	if err := os.Rename(fullPath, placeholder); err != nil {
		return "", err
	}
	return placeholder, nil
}

func RestoreQuarantinedRepo(quarantinePath, fullPath string) error {
	if quarantinePath == "" {
		return nil
	}
	return os.Rename(quarantinePath, fullPath)
}

func DeleteRepo(fullPath string) error {
	if fullPath == "" {
		return nil
	}
	return os.RemoveAll(fullPath)
}

// CheckBareRepository verifies that repoPath is an accessible bare Git
// repository. Transports use this before handing a request to Git so a missing
// or corrupt repository record is treated as infrastructure failure rather than
// being misreported to clients as a nonexistent database repository.
func CheckBareRepository(ctx context.Context, repoPath string) error {
	out, err := run(ctx, repoPath, "rev-parse", "--is-bare-repository")
	if err != nil {
		return fmt.Errorf("verify bare repository: %w", err)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return fmt.Errorf("path is not a bare Git repository")
	}
	return nil
}

// IsEmpty reports whether the repository has no commits. Operational Git
// failures are returned to the caller rather than being mistaken for emptiness.
func IsEmpty(ctx context.Context, repoPath string) (bool, error) {
	out, err := run(ctx, repoPath, "rev-list", "--all", "--max-count=1")
	if err != nil {
		return false, fmt.Errorf("check repository history: %w", err)
	}
	if len(bytes.TrimSpace(out)) != 0 {
		return false, nil
	}

	// --all enumerates refs, but a bare repository may intentionally have a
	// detached HEAD pointing at an otherwise-unreferenced commit. That is still
	// a real, browsable repository state and must not be reported as empty.
	_, err = run(ctx, repoPath, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err == nil {
		return false, nil
	}
	if code, ok := commandExitCode(err); ok && code == 1 {
		return true, nil
	}
	return false, fmt.Errorf("check repository HEAD: %w", err)
}

// ensureNotEmpty returns ErrRepoEmpty if the repo has no commits.
func ensureNotEmpty(ctx context.Context, repoPath string) error {
	empty, err := IsEmpty(ctx, repoPath)
	if err != nil {
		return err
	}
	if empty {
		return ErrRepoEmpty
	}
	return nil
}

// GetDefaultBranch returns the branch named by repository HEAD, including for
// unborn bare repositories. Detached HEAD returns an empty branch name.
func GetDefaultBranch(ctx context.Context, repoPath string) (string, error) {
	out, err := run(ctx, repoPath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	if code, ok := commandExitCode(err); ok && code == 1 {
		return "", nil
	}
	return "", fmt.Errorf("resolve repository HEAD: %w", err)
}

// GetBranches lists local branches in the repo (bare or non-bare).
func GetBranches(ctx context.Context, repoPath string) ([]string, error) {
	out, err := run(ctx, repoPath,
		"for-each-ref",
		"--format=%(refname:short)",
		"refs/heads/",
	)
	if err != nil {
		return nil, fmt.Errorf("list branches: %w", err)
	}

	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return []string{}, nil
	}

	var branches []string
	lines := bytes.Split(out, []byte{'\n'})
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		branches = append(branches, string(line))
	}

	return branches, nil
}

// GetTags lists all tags in the repo, sorted by version/refname.
func GetTags(ctx context.Context, repoPath string) ([]string, error) {
	out, err := run(ctx, repoPath,
		"for-each-ref",
		"--format=%(refname:short)",
		"--sort=version:refname",
		"refs/tags/",
	)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}

	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return []string{}, nil
	}

	var tags []string
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		if len(line) > 0 {
			tags = append(tags, string(line))
		}
	}

	return tags, nil
}

// refExists checks whether a fully-qualified git ref (e.g. refs/heads/main,
// refs/tags/v1.0) exists in the repository.
func refExists(ctx context.Context, repoPath, fullRef string) (bool, error) {
	_, err := run(ctx, repoPath, "show-ref", "--verify", "--quiet", fullRef)
	if err == nil {
		return true, nil
	}
	if code, ok := commandExitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check ref %q: %w", fullRef, err)
}

// isCommitHash returns true when s looks like a full or abbreviated commit SHA
// that git can resolve.
func isCommitHash(ctx context.Context, repoPath, s string) (bool, error) {
	if !commitHashRegex.MatchString(s) {
		return false, nil
	}
	_, err := run(ctx, repoPath, "rev-parse", "--verify", "--quiet", s+"^{commit}")
	if err == nil {
		return true, nil
	}
	if code, ok := commandExitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, fmt.Errorf("resolve commit %q: %w", s, err)
}

type RefKind uint8

const (
	RefKindBranch RefKind = iota + 1
	RefKindTag
	RefKindCommit
	RefKindHEAD
)

// RefResolution describes the revision Gitman selected without exposing the
// option-safe command argument used internally. Kind is important when HEAD is
// detached: the display name alone is not enough to distinguish detached HEAD
// from a branch literally named "HEAD".
type RefResolution struct {
	Name string
	Kind RefKind
}

// resolvedRef separates the human-facing ref name from the revision argument
// passed to Git. Branches and tags use their fully-qualified refs so a valid
// Git ref such as "-release" can never be reinterpreted as a command option.
type resolvedRef struct {
	display string
	spec    string
	kind    RefKind
}

// resolveRef resolves a user-facing ref to both its display name and an
// option-safe Git revision spec.
func resolveRef(ctx context.Context, repoPath, requestedRef string) (resolvedRef, error) {
	if err := ValidateRefNameContext(ctx, requestedRef); err != nil {
		return resolvedRef{}, err
	}
	if requestedRef == "" {
		if err := ensureNotEmpty(ctx, repoPath); err != nil {
			return resolvedRef{}, err
		}
		def, err := GetDefaultBranch(ctx, repoPath)
		if err != nil {
			return resolvedRef{}, err
		}
		if def == "" {
			// A non-empty repository with a non-symbolic HEAD is detached. Resolve
			// it to the immutable commit hash so the selected revision cannot later
			// be confused with a real branch or tag named HEAD.
			out, err := run(ctx, repoPath, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
			if err != nil {
				return resolvedRef{}, fmt.Errorf("resolve detached HEAD: %w", err)
			}
			hash := strings.TrimSpace(string(out))
			if !canonicalGitHashRegex.MatchString(hash) {
				return resolvedRef{}, fmt.Errorf("resolve detached HEAD: unexpected commit hash")
			}
			return resolvedRef{display: hash, spec: hash, kind: RefKindCommit}, nil
		}
		exists, err := refExists(ctx, repoPath, "refs/heads/"+def)
		if err != nil {
			return resolvedRef{}, err
		}
		if exists {
			return resolvedRef{display: def, spec: "refs/heads/" + def, kind: RefKindBranch}, nil
		}
		// HEAD is the repository's configured default revision. If it names an
		// unborn or deleted branch, do not silently substitute some other branch:
		// callers need to see the broken/default-ref state and can explicitly
		// select another existing ref.
		return resolvedRef{}, ErrRefNotFound
	}

	branchExists, err := refExists(ctx, repoPath, "refs/heads/"+requestedRef)
	if err != nil {
		return resolvedRef{}, err
	}
	if branchExists {
		return resolvedRef{display: requestedRef, spec: "refs/heads/" + requestedRef, kind: RefKindBranch}, nil
	}
	tagExists, err := refExists(ctx, repoPath, "refs/tags/"+requestedRef)
	if err != nil {
		return resolvedRef{}, err
	}
	if tagExists {
		return resolvedRef{display: requestedRef, spec: "refs/tags/" + requestedRef, kind: RefKindTag}, nil
	}
	commit, err := isCommitHash(ctx, repoPath, requestedRef)
	if err != nil {
		return resolvedRef{}, err
	}
	if commit {
		return resolvedRef{display: requestedRef, spec: requestedRef, kind: RefKindCommit}, nil
	}
	if requestedRef == "HEAD" {
		out, err := run(ctx, repoPath, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
		if err != nil {
			if code, ok := commandExitCode(err); ok && code == 1 {
				return resolvedRef{}, ErrRefNotFound
			}
			return resolvedRef{}, fmt.Errorf("resolve HEAD: %w", err)
		}
		hash := strings.TrimSpace(string(out))
		if !canonicalGitHashRegex.MatchString(hash) {
			return resolvedRef{}, fmt.Errorf("resolve HEAD: unexpected commit hash")
		}
		return resolvedRef{display: hash, spec: hash, kind: RefKindHEAD}, nil
	}

	return resolvedRef{}, ErrRefNotFound
}

// ResolveRefInfo returns the selected revision name together with its semantic
// kind. Callers that make trust or policy decisions must use the kind rather
// than guessing from the display string.
func ResolveRefInfo(ctx context.Context, repoPath, requestedRef string) (RefResolution, error) {
	resolved, err := resolveRef(ctx, repoPath, requestedRef)
	if err != nil {
		return RefResolution{}, err
	}
	return RefResolution{Name: resolved.display, Kind: resolved.kind}, nil
}

// ResolveRef returns the human-facing ref selected for logs/tree/blob
// operations. Explicit missing refs never silently fall back to another ref.
func ResolveRef(ctx context.Context, repoPath, requestedRef string) (string, error) {
	resolved, err := resolveRef(ctx, repoPath, requestedRef)
	if err != nil {
		return "", err
	}
	return resolved.display, nil
}

// ResolveCommitHash verifies that a full or abbreviated hash resolves to a
// commit inside repoPath and returns the canonical full hash.
func ResolveCommitHash(ctx context.Context, repoPath, hash string) (string, error) {
	if !commitHashRegex.MatchString(hash) {
		return "", ErrInvalidCommit
	}
	out, err := run(ctx, repoPath, "rev-parse", "--verify", "--quiet", hash+"^{commit}")
	if err != nil {
		if code, ok := commandExitCode(err); ok && code == 1 {
			return "", ErrRefNotFound
		}
		return "", fmt.Errorf("resolve commit hash: %w", err)
	}
	resolved := strings.TrimSpace(string(out))
	if !canonicalGitHashRegex.MatchString(resolved) {
		return "", fmt.Errorf("unexpected resolved commit hash")
	}
	return resolved, nil
}

// ResolveBranchCommitHash returns the canonical commit currently at a branch.
func ResolveBranchCommitHash(ctx context.Context, repoPath, branch string) (string, error) {
	if err := ValidateRefNameContext(ctx, branch); err != nil {
		return "", err
	}
	if branch == "" {
		return "", ErrInvalidRef
	}
	return resolveFullRefCommitHash(ctx, repoPath, "refs/heads/"+branch)
}

// ResolveTagCommitHash returns the canonical commit currently referenced by a tag.
func ResolveTagCommitHash(ctx context.Context, repoPath, tag string) (string, error) {
	if err := ValidateRefNameContext(ctx, tag); err != nil {
		return "", err
	}
	if tag == "" {
		return "", ErrInvalidRef
	}
	return resolveFullRefCommitHash(ctx, repoPath, "refs/tags/"+tag)
}

func resolveFullRefCommitHash(ctx context.Context, repoPath, fullRef string) (string, error) {
	out, err := run(ctx, repoPath, "rev-parse", "--verify", "--quiet", fullRef+"^{commit}")
	if err != nil {
		if code, ok := commandExitCode(err); ok && code == 1 {
			return "", ErrRefNotFound
		}
		return "", fmt.Errorf("resolve ref %q: %w", fullRef, err)
	}
	resolved := strings.TrimSpace(string(out))
	if !canonicalGitHashRegex.MatchString(resolved) {
		return "", fmt.Errorf("unexpected resolved commit hash")
	}
	return resolved, nil
}

// IsCommitReachableFromBranch reports whether commitHash is an ancestor of the
// current branch tip. Manual historical runs may target reachable commits;
// push-triggered runs require the exact tip in the HTTP handler.
func IsCommitReachableFromBranch(ctx context.Context, repoPath, commitHash, branch string) (bool, error) {
	if _, err := ResolveCommitHash(ctx, repoPath, commitHash); err != nil {
		return false, err
	}
	if err := ValidateRefNameContext(ctx, branch); err != nil {
		return false, err
	}
	if branch == "" {
		return false, ErrInvalidRef
	}
	_, err := run(ctx, repoPath, "merge-base", "--is-ancestor", commitHash, "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	if code, ok := commandExitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check branch reachability: %w", err)
}

// SanitizeRefForFilename converts a ref name to a safe filename component.
// Slashes are replaced with underscores; any character that is not
// alphanumeric, a hyphen, underscore, or period is dropped.
func SanitizeRefForFilename(ref string) string {
	ref = strings.ReplaceAll(ref, "/", "_")
	var sb strings.Builder
	for _, c := range ref {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			sb.WriteRune(c)
		}
	}
	if sb.Len() == 0 {
		return "revision"
	}
	return sb.String()
}

// GetCommitsByHashes loads commit metadata for a set of canonical commit hashes
// in one git invocation. Unknown or malformed hashes are ignored so callers can
// enrich best-effort presentation data without turning a missing commit into a
// repository-wide failure.
func GetCommitsByHashes(ctx context.Context, repoPath string, hashes []string) (map[string]Commit, error) {
	seen := make(map[string]struct{}, len(hashes))
	args := []string{"log", "--ignore-missing", "--no-walk=unsorted", "--format=%H%x00%an%x00%ae%x00%cI%x00%s"}
	for _, hash := range hashes {
		hash = strings.TrimSpace(hash)
		if !canonicalGitHashRegex.MatchString(hash) {
			continue
		}
		if _, ok := seen[hash]; ok {
			continue
		}
		seen[hash] = struct{}{}
		args = append(args, hash)
	}
	if len(seen) == 0 {
		return map[string]Commit{}, nil
	}

	out, err := run(ctx, repoPath, args...)
	if err != nil {
		return nil, fmt.Errorf("load commit metadata: %w", err)
	}
	result := make(map[string]Commit, len(seen))
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		parts := bytes.SplitN(line, []byte{0}, 5)
		if len(parts) != 5 {
			return nil, fmt.Errorf("load commit metadata: malformed git log output")
		}
		date, err := time.Parse(time.RFC3339, string(parts[3]))
		if err != nil {
			return nil, fmt.Errorf("load commit metadata: parse commit date %q: %w", string(parts[3]), err)
		}
		commit := Commit{
			Hash:    string(parts[0]),
			Author:  string(parts[1]),
			Email:   string(parts[2]),
			Date:    date,
			Message: string(parts[4]),
		}
		result[commit.Hash] = commit
	}
	return result, nil
}

// GetCommits returns commits for the given ref (branch name or HEAD), with pagination.
func GetCommits(ctx context.Context, repoPath, ref string, skip, limit int) ([]Commit, error) {
	resolvedRef, err := resolveRef(ctx, repoPath, ref)
	if err != nil {
		return nil, err
	}

	format := "%H%x00%an%x00%ae%x00%cI%x00%s"
	args := []string{
		"log",
		resolvedRef.spec,
		fmt.Sprintf("--format=%s", format),
	}
	if skip > 0 {
		args = append(args, "--skip", strconv.Itoa(skip))
	}
	if limit > 0 {
		args = append(args, "-n", strconv.Itoa(limit))
	}
	out, err := run(ctx, repoPath, args...)
	if err != nil {
		return nil, fmt.Errorf("read commits for ref %q: %w", resolvedRef.display, err)
	}

	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return []Commit{}, nil
	}

	var commits []Commit
	lines := bytes.Split(out, []byte{'\n'})

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		parts := bytes.SplitN(line, []byte{0}, 5)
		if len(parts) != 5 {
			return nil, fmt.Errorf("parse commits for ref %q: malformed git log output", resolvedRef.display)
		}

		date, err := time.Parse(time.RFC3339, string(parts[3]))
		if err != nil {
			return nil, fmt.Errorf("parse commit date %q: %w", string(parts[3]), err)
		}

		commits = append(commits, Commit{
			Hash:    string(parts[0]),
			Author:  string(parts[1]),
			Email:   string(parts[2]),
			Date:    date,
			Message: string(parts[4]),
		})
	}

	return commits, nil
}

// GetTree returns the tree entries for a given ref and path (directory inside repo).
// If path is empty, it returns the root tree for that ref.
func GetTree(ctx context.Context, repoPath, ref, path string) ([]TreeEntry, error) {
	resolvedRef, err := resolveRef(ctx, repoPath, ref)
	if err != nil {
		return nil, err
	}

	treeish := resolvedRef.spec
	if path != "" {
		path = strings.TrimPrefix(path, "/")
		if path == "" || strings.ContainsRune(path, '\x00') {
			return nil, ErrPathNotFound
		}
		probe, err := run(ctx, repoPath, "ls-tree", "-d", "-z", resolvedRef.spec, "--", ":(literal)"+path)
		if err != nil {
			return nil, fmt.Errorf("inspect tree %q at %q: %w", path, resolvedRef.display, err)
		}
		if len(probe) == 0 {
			return nil, ErrPathNotFound
		}
		treeish = fmt.Sprintf("%s:%s", resolvedRef.spec, path)
	}

	out, err := run(ctx, repoPath, "ls-tree", "-l", "-z", treeish)
	if err != nil {
		return nil, fmt.Errorf("read tree %q at %q: %w", path, resolvedRef.display, err)
	}

	if len(out) == 0 {
		return []TreeEntry{}, nil
	}

	var entries []TreeEntry
	records := bytes.Split(out, []byte{0})

	for _, record := range records {
		if len(record) == 0 {
			continue
		}

		tabIdx := bytes.IndexByte(record, '\t')
		if tabIdx == -1 {
			return nil, fmt.Errorf("parse tree %q at %q: malformed ls-tree record", path, resolvedRef.display)
		}

		meta := record[:tabIdx]
		name := record[tabIdx+1:]

		parts := bytes.SplitN(meta, []byte{' '}, 4)
		if len(parts) != 4 {
			return nil, fmt.Errorf("parse tree %q at %q: malformed ls-tree metadata", path, resolvedRef.display)
		}

		sizeStr := strings.TrimSpace(string(parts[3]))
		var size int64
		if sizeStr != "-" {
			v, err := strconv.ParseInt(sizeStr, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse tree size %q: %w", sizeStr, err)
			}
			size = v
		}

		entries = append(entries, TreeEntry{
			Mode: string(parts[0]),
			Type: string(parts[1]),
			Hash: string(parts[2]),
			Size: size,
			Name: string(name),
		})
	}

	return entries, nil
}

// BlobExists reports whether path resolves to a blob at ref without logging a
// missing file as an operational error. It is useful for optional repository
// metadata such as .gitman-ci.yml and README files.
func BlobExists(ctx context.Context, repoPath, ref, path string) (bool, error) {
	resolvedRef, err := resolveRef(ctx, repoPath, ref)
	if err != nil {
		return false, err
	}
	return blobExistsAtRef(ctx, repoPath, resolvedRef.spec, path)
}

func blobExistsAtRef(ctx context.Context, repoPath, resolvedRef, path string) (bool, error) {
	path = strings.TrimPrefix(path, "/")
	if path == "" || strings.ContainsRune(path, '\x00') {
		return false, nil
	}

	// ls-tree reports a missing path as a successful empty result, while real
	// repository/Git failures still propagate. This makes optional-blob probes
	// truthful without interpreting every non-zero Git exit as "not found".
	out, err := run(ctx, repoPath, "ls-tree", "-z", resolvedRef, "--", ":(literal)"+path)
	if err != nil {
		return false, fmt.Errorf("inspect blob %q: %w", path, err)
	}
	for _, record := range bytes.Split(out, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		tab := bytes.IndexByte(record, '\t')
		if tab < 0 {
			return false, fmt.Errorf("inspect blob %q: malformed ls-tree output", path)
		}
		meta := bytes.Fields(record[:tab])
		if len(meta) < 2 {
			return false, fmt.Errorf("inspect blob %q: malformed ls-tree metadata", path)
		}
		if string(record[tab+1:]) == path {
			return string(meta[1]) == "blob", nil
		}
	}
	return false, nil
}

func resolveBlobSpec(ctx context.Context, repoPath, ref, path string) (string, error) {
	resolvedRef, err := resolveRef(ctx, repoPath, ref)
	if err != nil {
		return "", err
	}
	path = strings.TrimPrefix(path, "/")
	if path == "" || strings.ContainsRune(path, '\x00') {
		return "", ErrPathNotFound
	}
	exists, err := blobExistsAtRef(ctx, repoPath, resolvedRef.spec, path)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", ErrPathNotFound
	}
	return fmt.Sprintf("%s:%s", resolvedRef.spec, path), nil
}

// GetBlob returns the content of a file (blob) at path for the given ref.
func GetBlob(ctx context.Context, repoPath, ref, path string) ([]byte, error) {
	treeish, err := resolveBlobSpec(ctx, repoPath, ref, path)
	if err != nil {
		return nil, err
	}

	out, err := run(ctx, repoPath, "cat-file", "-p", treeish)
	if err != nil {
		return nil, fmt.Errorf("read blob %q: %w", path, err)
	}

	return out, nil
}

// GetBlobSize returns the size of a file (blob) at path for the given ref.
func GetBlobSize(ctx context.Context, repoPath, ref, path string) (int64, error) {
	treeish, err := resolveBlobSpec(ctx, repoPath, ref, path)
	if err != nil {
		return 0, err
	}

	out, err := run(ctx, repoPath, "cat-file", "-s", treeish)
	if err != nil {
		return 0, fmt.Errorf("read blob size %q: %w", path, err)
	}

	sizeStr := strings.TrimSpace(string(out))
	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse blob size %q: %w", sizeStr, err)
	}

	return size, nil
}

// StreamArchive writes the repository archive for the given ref and format to the provided writer.
func StreamArchive(ctx context.Context, repoPath, ref, format string, w io.Writer) error {
	resolvedRef, err := resolveRef(ctx, repoPath, ref)
	if err != nil {
		return err
	}

	gitFormat := format
	if format == "tar.gz" {
		gitFormat = "tgz"
	}
	if err := runTo(ctx, repoPath, w, "archive", fmt.Sprintf("--format=%s", gitFormat), resolvedRef.spec); err != nil {
		return fmt.Errorf("stream repository archive: %w", err)
	}
	return nil
}
