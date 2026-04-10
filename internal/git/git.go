package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
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

	// Note: using "git -C fullPath init --bare fullPath" is okay but redundant;
	// "git -C fullPath init --bare ." would also work. We'll stick to your existing style.
	if _, err := run(ctx, fullPath, "init", "--bare", "."); err != nil {
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
	_, err := run(ctx, repoPath, "rev-parse", "--verify", "HEAD")
	if err != nil {
		slog.Debug("repository has no commits",
			"repo", repoPath,
			"error", err,
		)
		return true
	}
	return false
}

// ensureNotEmpty returns ErrRepoEmpty if the repo has no commits.
func ensureNotEmpty(ctx context.Context, repoPath string) error {
	if IsEmpty(ctx, repoPath) {
		return ErrRepoEmpty
	}
	return nil
}

// GetDefaultBranch attempts to resolve HEAD to a short branch name.
// Returns "" if HEAD is not a valid symbolic ref yet.
func GetDefaultBranch(ctx context.Context, repoPath string) (string, error) {
	out, err := run(ctx, repoPath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		// In a bare, empty repo, this will usually fail until the first push.
		slog.Warn("unable to determine default branch",
			"repo", repoPath,
			"error", err,
		)
		return "", nil
	}

	branch := string(bytes.TrimSpace(out))

	slog.Debug("resolved default branch",
		"repo", repoPath,
		"branch", branch,
	)

	return branch, nil
}

// GetBranches lists local branches in the repo (bare or non‑bare).
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

// ResolveRef returns a safe ref to use for logs/tree/blob.
//
// Priority:
//  1. If requestedRef is non‑empty and exists as a local branch, return it.
//  2. Else if default branch exists and is a local branch, return it.
//  3. Else if any branch exists, return the first one.
//  4. Else fall back to "HEAD" (only useful if repo is not empty).
//
// If the repo is empty, ErrRepoEmpty is returned.
func ResolveRef(ctx context.Context, repoPath, requestedRef string) (string, error) {
	if err := ensureNotEmpty(ctx, repoPath); err != nil {
		return "", err
	}

	branches, _ := GetBranches(ctx, repoPath)

	branchExists := func(name string) bool {
		for _, b := range branches {
			if b == name {
				return true
			}
		}
		return false
	}

	// 1. directly requested branch exists?
	if requestedRef != "" && branchExists(requestedRef) {
		return requestedRef, nil
	}

	// 2. default branch
	if def, _ := GetDefaultBranch(ctx, repoPath); def != "" && branchExists(def) {
		return def, nil
	}

	// 3. any branch
	if len(branches) > 0 {
		return branches[0], nil
	}

	// 4. fallback
	// At this point we know ensureNotEmpty passed, so HEAD *should* be valid.
	return "HEAD", nil
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
		return nil, err
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
