package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrRepoEmpty is returned when an operation needs commits but the repo has none.
	ErrRepoEmpty = errors.New("repository is empty")

	// ErrRefNotFound is returned when a requested ref cannot be resolved.
	ErrRefNotFound = errors.New("ref not found")
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
	safeRefRegex    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9/_.-]*$`)
	commitHashRegex = regexp.MustCompile(`^[A-Fa-f0-9]{7,40}$`)
)

func ValidateRefName(ref string) error {
	if ref == "" {
		return nil
	}
	if len(ref) > 255 {
		return fmt.Errorf("ref name too long")
	}
	if !safeRefRegex.MatchString(ref) {
		return fmt.Errorf("invalid ref name: contains illegal characters")
	}
	if strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") {
		return fmt.Errorf("invalid ref name: cannot end with slash or dot")
	}
	if strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.Contains(ref, "@{") || strings.Contains(ref, "\\") {
		return fmt.Errorf("invalid ref name")
	}
	if strings.ContainsAny(ref, " ~^:?*[") {
		return fmt.Errorf("invalid ref name: contains reserved git characters")
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return fmt.Errorf("invalid ref name component")
		}
	}
	return nil
}

var SafeNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// SecureRepoPath guarantees the resulting path is safely inside the base directory
func SecureRepoPath(basePath, username, repoName string) (string, error) {
	if !SafeNameRegex.MatchString(username) {
		return "", fmt.Errorf("invalid username format")
	}
	if !SafeNameRegex.MatchString(repoName) {
		return "", fmt.Errorf("invalid repository name format")
	}

	fullPath := filepath.Join(basePath, username, fmt.Sprintf("%s.git", repoName))

	// Double-check against path traversal
	cleanBase := filepath.Clean(basePath)
	cleanPath := filepath.Clean(fullPath)

	if !strings.HasPrefix(cleanPath, cleanBase+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal attempt detected")
	}

	return cleanPath, nil
}

// run executes a git command inside a repository directory.
func run(ctx context.Context, repoPath string, args ...string) ([]byte, error) {
	start := time.Now()
	cmdArgs := append([]string{"-C", repoPath}, args...)

	slog.Debug("git command start",
		"repo", repoPath,
		"args", args,
	)

	cmd := exec.CommandContext(ctx, "git", cmdArgs...)

	out, err := cmd.Output()
	duration := time.Since(start)

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := strings.TrimSpace(string(exitErr.Stderr))

			slog.Error("git command failed",
				"repo", repoPath,
				"args", args,
				"duration", duration,
				"stderr", stderr,
			)

			return nil, fmt.Errorf("git command failed: %w (%s)", err, stderr)
		}

		slog.Error("git command execution error",
			"repo", repoPath,
			"args", args,
			"duration", duration,
			"error", err,
		)

		return nil, fmt.Errorf("git execution error: %w", err)
	}

	slog.Debug("git command success",
		"repo", repoPath,
		"args", args,
		"duration", duration,
		"bytes", len(out),
	)

	return out, nil
}

// InitBareRepo initializes a new bare repository at fullPath.
func InitBareRepo(ctx context.Context, fullPath string) error {
	if err := os.MkdirAll(fullPath, 0o700); err != nil {
		slog.Error("failed to create repo directory",
			"repo", fullPath,
			"error", err,
		)
		return fmt.Errorf("failed to create repo directory: %w", err)
	}

	if _, err := run(ctx, fullPath, "init", "--bare", "--initial-branch=main", "."); err != nil {
		slog.Error("failed to initialize bare repo, cleaning up",
			"repo", fullPath,
			"error", err,
		)

		if rmErr := os.RemoveAll(fullPath); rmErr != nil {
			slog.Error("failed to clean up partial repo",
				"repo", fullPath,
				"error", rmErr,
			)
			return fmt.Errorf("cleanup failed after init error: %v (cleanup error: %v)", err, rmErr)
		}

		return fmt.Errorf("git init --bare failed: %w", err)
	}

	slog.Info("bare repository created successfully",
		"repo", fullPath,
	)

	return nil
}

// DeleteRepo deletes the repository directory at fullPath.
func DeleteRepo(fullPath string) error {
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(fullPath)
}

// IsEmpty returns true if the repo has no commits yet.
// Works correctly for bare repositories.
func IsEmpty(ctx context.Context, repoPath string) bool {
	out, err := run(ctx, repoPath, "rev-list", "--all", "--max-count=1")
	if err != nil {
		slog.Debug("repository has no commits",
			"repo", repoPath,
			"error", err,
		)
		return true
	}
	return len(bytes.TrimSpace(out)) == 0
}

// ensureNotEmpty returns ErrRepoEmpty if the repo has no commits.
func ensureNotEmpty(ctx context.Context, repoPath string) error {
	if IsEmpty(ctx, repoPath) {
		return ErrRepoEmpty
	}
	return nil
}

// GetDefaultBranch reads the HEAD file directly from the bare repository.
// This avoids the `git symbolic-ref` failure on repos where HEAD points
// to a branch that has not yet been pushed (common in fresh bare repos).
//
// Returns "" if HEAD is detached (points to a commit hash) or unreadable.
func GetDefaultBranch(ctx context.Context, repoPath string) (string, error) {
	headPath := filepath.Join(repoPath, "HEAD")
	data, err := os.ReadFile(headPath)
	if err != nil {
		slog.Warn("unable to read HEAD file",
			"repo", repoPath,
			"error", err,
		)
		return "", nil
	}

	line := strings.TrimSpace(string(data))
	const prefix = "ref: refs/heads/"
	if !strings.HasPrefix(line, prefix) {
		// Detached HEAD – contains a raw commit hash.
		slog.Debug("HEAD is detached, no default branch",
			"repo", repoPath,
			"head", line,
		)
		return "", nil
	}

	branch := strings.TrimPrefix(line, prefix)
	slog.Debug("resolved default branch from HEAD file",
		"repo", repoPath,
		"branch", branch,
	)
	return branch, nil
}

// GetBranches lists local branches in the repo (bare or non-bare).
func GetBranches(ctx context.Context, repoPath string) ([]string, error) {
	out, err := run(ctx, repoPath,
		"for-each-ref",
		"--format=%(refname:short)",
		"refs/heads/",
	)
	if err != nil {
		slog.Error("failed to list branches",
			"repo", repoPath,
			"error", err,
		)
		return nil, err
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

	slog.Debug("branches loaded",
		"repo", repoPath,
		"count", len(branches),
	)

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
		// Tags may simply be absent; don't treat this as a hard error.
		slog.Debug("no tags or failed to list tags",
			"repo", repoPath,
			"error", err,
		)
		return []string{}, nil
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

	slog.Debug("tags loaded",
		"repo", repoPath,
		"count", len(tags),
	)

	return tags, nil
}

// refExists checks whether a fully-qualified git ref (e.g. refs/heads/main,
// refs/tags/v1.0) exists in the repository.
func refExists(ctx context.Context, repoPath, fullRef string) bool {
	_, err := run(ctx, repoPath, "show-ref", "--verify", "--quiet", fullRef)
	return err == nil
}

// isCommitHash returns true when s looks like a full or abbreviated commit SHA
// that git can resolve.
func isCommitHash(ctx context.Context, repoPath, s string) bool {
	if !commitHashRegex.MatchString(s) {
		return false
	}
	// git rev-parse --verify <sha>^{commit} succeeds only for valid commits.
	_, err := run(ctx, repoPath, "rev-parse", "--verify", s+"^{commit}")
	return err == nil
}

// ResolveRef returns a concrete git ref to use for logs/tree/blob operations.
//
// Resolution order:
//  1. Empty requestedRef → use default branch (from HEAD file), first branch, or HEAD.
//  2. Exact branch match (refs/heads/<ref>).
//  3. Exact tag match (refs/tags/<ref>).
//  4. Valid commit hash / abbreviation.
//
// An explicit but missing ref returns ErrRefNotFound. It must never silently
// fall back to another branch.
func ResolveRef(ctx context.Context, repoPath, requestedRef string) (string, error) {
	if err := ensureNotEmpty(ctx, repoPath); err != nil {
		return "", err
	}
	if err := ValidateRefName(requestedRef); err != nil {
		return "", err
	}
	if requestedRef == "" {
		if def, _ := GetDefaultBranch(ctx, repoPath); def != "" {
			if refExists(ctx, repoPath, "refs/heads/"+def) {
				slog.Debug("resolved ref to default branch", "repo", repoPath, "branch", def)
				return def, nil
			}
		}
		if branches, _ := GetBranches(ctx, repoPath); len(branches) > 0 {
			slog.Debug("resolved ref to first branch (no default)", "repo", repoPath, "branch", branches[0])
			return branches[0], nil
		}
		return "HEAD", nil
	}

	if refExists(ctx, repoPath, "refs/heads/"+requestedRef) {
		slog.Debug("resolved ref as branch", "repo", repoPath, "ref", requestedRef)
		return requestedRef, nil
	}
	if refExists(ctx, repoPath, "refs/tags/"+requestedRef) {
		slog.Debug("resolved ref as tag", "repo", repoPath, "ref", requestedRef)
		return requestedRef, nil
	}
	if isCommitHash(ctx, repoPath, requestedRef) {
		slog.Debug("resolved ref as commit hash", "repo", repoPath, "ref", requestedRef)
		return requestedRef, nil
	}

	return "", ErrRefNotFound
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
	return sb.String()
}

// GetCommits returns commits for the given ref (branch name or HEAD), with pagination.
func GetCommits(ctx context.Context, repoPath, ref string, skip, limit int) ([]Commit, error) {
	if err := ensureNotEmpty(ctx, repoPath); err != nil {
		return nil, err
	}

	resolvedRef, err := ResolveRef(ctx, repoPath, ref)
	if err != nil {
		return nil, err
	}

	format := "%H%x00%an%x00%ae%x00%cI%x00%s"
	args := []string{
		"log",
		resolvedRef,
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
		slog.Error("failed to read commits",
			"repo", repoPath,
			"ref", resolvedRef,
			"error", err,
		)
		return nil, fmt.Errorf("failed to read commits for ref %q: %w", resolvedRef, err)
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
			slog.Warn("malformed commit entry skipped",
				"repo", repoPath,
			)
			continue
		}

		date, err := time.Parse(time.RFC3339, string(parts[3]))
		if err != nil {
			slog.Warn("failed to parse commit date",
				"repo", repoPath,
				"value", string(parts[3]),
				"error", err,
			)
		}

		commits = append(commits, Commit{
			Hash:    string(parts[0]),
			Author:  string(parts[1]),
			Email:   string(parts[2]),
			Date:    date,
			Message: string(parts[4]),
		})
	}

	slog.Debug("commits loaded",
		"repo", repoPath,
		"ref", resolvedRef,
		"count", len(commits),
	)

	return commits, nil
}

// GetTree returns the tree entries for a given ref and path (directory inside repo).
// If path is empty, it returns the root tree for that ref.
func GetTree(ctx context.Context, repoPath, ref, path string) ([]TreeEntry, error) {
	if err := ensureNotEmpty(ctx, repoPath); err != nil {
		return nil, err
	}

	resolvedRef, err := ResolveRef(ctx, repoPath, ref)
	if err != nil {
		return nil, err
	}

	treeish := resolvedRef
	if path != "" {
		path = strings.TrimPrefix(path, "/")
		treeish = fmt.Sprintf("%s:%s", resolvedRef, path)
	}

	out, err := run(ctx, repoPath, "ls-tree", "-l", "-z", treeish)
	if err != nil {
		slog.Error("failed to read tree",
			"repo", repoPath,
			"ref", resolvedRef,
			"path", path,
			"error", err,
		)
		return nil, err
	}

	if len(out) == 0 {
		slog.Debug("empty tree result",
			"repo", repoPath,
			"ref", resolvedRef,
			"path", path,
		)
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
			slog.Warn("invalid ls-tree record skipped",
				"repo", repoPath,
			)
			continue
		}

		meta := record[:tabIdx]
		name := record[tabIdx+1:]

		parts := bytes.SplitN(meta, []byte{' '}, 4)
		if len(parts) != 4 {
			slog.Warn("invalid ls-tree metadata",
				"repo", repoPath,
			)
			continue
		}

		sizeStr := strings.TrimSpace(string(parts[3]))
		var size int64
		if sizeStr != "-" {
			v, err := strconv.ParseInt(sizeStr, 10, 64)
			if err != nil {
				slog.Warn("failed to parse blob size",
					"value", sizeStr,
					"repo", repoPath,
				)
			} else {
				size = v
			}
		}

		entries = append(entries, TreeEntry{
			Mode: string(parts[0]),
			Type: string(parts[1]),
			Hash: string(parts[2]),
			Size: size,
			Name: string(name),
		})
	}

	slog.Debug("tree loaded",
		"repo", repoPath,
		"ref", resolvedRef,
		"path", path,
		"entries", len(entries),
	)

	return entries, nil
}

// GetBlob returns the content of a file (blob) at path for the given ref.
func GetBlob(ctx context.Context, repoPath, ref, path string) ([]byte, error) {
	if err := ensureNotEmpty(ctx, repoPath); err != nil {
		return nil, err
	}

	resolvedRef, err := ResolveRef(ctx, repoPath, ref)
	if err != nil {
		return nil, err
	}

	treeish := fmt.Sprintf("%s:%s", resolvedRef, strings.TrimPrefix(path, "/"))

	out, err := run(ctx, repoPath, "cat-file", "-p", treeish)
	if err != nil {
		slog.Error("failed to read blob",
			"repo", repoPath,
			"ref", resolvedRef,
			"path", path,
			"error", err,
		)
		return nil, err
	}

	return out, nil
}

// GetBlobSize returns the size of a file (blob) at path for the given ref.
func GetBlobSize(ctx context.Context, repoPath, ref, path string) (int64, error) {
	if err := ensureNotEmpty(ctx, repoPath); err != nil {
		return 0, err
	}

	resolvedRef, err := ResolveRef(ctx, repoPath, ref)
	if err != nil {
		return 0, err
	}

	treeish := fmt.Sprintf("%s:%s", resolvedRef, strings.TrimPrefix(path, "/"))

	out, err := run(ctx, repoPath, "cat-file", "-s", treeish)
	if err != nil {
		slog.Error("failed to read blob size",
			"repo", repoPath,
			"ref", resolvedRef,
			"path", path,
			"error", err,
		)
		return 0, err
	}

	sizeStr := strings.TrimSpace(string(out))
	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		slog.Error("failed to parse blob size",
			"value", sizeStr,
			"repo", repoPath,
			"error", err,
		)
		return 0, err
	}

	return size, nil
}

// StreamArchive writes the repository archive for the given ref and format to the provided writer.
func StreamArchive(ctx context.Context, repoPath, ref, format string, w io.Writer) error {
	if err := ensureNotEmpty(ctx, repoPath); err != nil {
		return err
	}

	resolvedRef, err := ResolveRef(ctx, repoPath, ref)
	if err != nil {
		return err
	}

	// Git natively supports 'zip', 'tar', and 'tgz' (which outputs tar.gz)
	gitFormat := format
	if format == "tar.gz" {
		gitFormat = "tgz"
	}

	args := []string{"archive", fmt.Sprintf("--format=%s", gitFormat), resolvedRef}

	slog.Debug("git archive start", "repo", repoPath, "args", args)

	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoPath}, args...)...)

	// Stream directly to the provided writer (the HTTP ResponseWriter)
	cmd.Stdout = w

	stderr := new(bytes.Buffer)
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		slog.Error("git archive failed",
			"repo", repoPath,
			"ref", resolvedRef,
			"format", gitFormat,
			"error", err,
			"stderr", stderr.String(),
		)
		return fmt.Errorf("git archive failed: %w", err)
	}

	return nil
}
