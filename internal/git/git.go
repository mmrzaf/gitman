package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ErrRepoEmpty is returned when trying to browse a newly initialized repository.
var ErrRepoEmpty = errors.New("repository is empty")

// Commit represents a parsed Git commit.
type Commit struct {
	Hash    string
	Author  string
	Email   string
	Date    time.Time
	Message string
}

// TreeEntry represents a file or directory in a Git tree.
type TreeEntry struct {
	Mode string
	Type string // "blob" or "tree"
	Hash string
	Size int64 // 0 for trees
	Name string
}

// run is the central, exclusive wrapper for executing git commands.
// All code in Gitman MUST use this to interact with git repositories.
func run(ctx context.Context, repoPath string, args ...string) ([]byte, error) {
	// -C ensures git operates ONLY inside the specified secure repository path
	cmdArgs := append([]string{"-C", repoPath}, args...)

	cmd := exec.CommandContext(ctx, "git", cmdArgs...)

	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Include stderr in the error for easier debugging
			return nil, fmt.Errorf("git error: %w (stderr: %s)", err, bytes.TrimSpace(exitErr.Stderr))
		}
		return nil, err
	}

	return out, nil
}

// IsEmpty checks if a repository has any commits.
func IsEmpty(ctx context.Context, repoPath string) bool {
	// rev-parse HEAD fails if there are no commits in the repository
	_, err := run(ctx, repoPath, "rev-parse", "HEAD")
	return err != nil
}

// GetDefaultBranch returns the branch HEAD is currently pointing to (usually main or master).
func GetDefaultBranch(ctx context.Context, repoPath string) (string, error) {
	out, err := run(ctx, repoPath, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(out)), nil
}

// GetBranches returns a list of all branch names.
func GetBranches(ctx context.Context, repoPath string) ([]string, error) {
	out, err := run(ctx, repoPath, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil, err
	}

	var branches []string
	lines := bytes.Split(bytes.TrimSpace(out), []byte{'\n'})
	for _, line := range lines {
		if len(line) > 0 {
			branches = append(branches, string(line))
		}
	}
	return branches, nil
}

// GetCommits fetches a paginated list of commits.
func GetCommits(ctx context.Context, repoPath, ref string, skip, limit int) ([]Commit, error) {
	// %x00 inserts a null byte, making it perfectly safe to parse regardless of characters in the commit message
	format := "%H%x00%an%x00%ae%x00%cI%x00%s"
	args := []string{"log", ref, fmt.Sprintf("--format=%s", format)}

	if skip > 0 {
		args = append(args, fmt.Sprintf("--skip=%d", skip))
	}
	if limit > 0 {
		args = append(args, fmt.Sprintf("-n=%d", limit))
	}

	out, err := run(ctx, repoPath, args...)
	if err != nil {
		return nil, err
	}

	var commits []Commit
	lines := bytes.Split(bytes.TrimSpace(out), []byte{'\n'})
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		parts := bytes.SplitN(line, []byte{0}, 5)
		if len(parts) != 5 {
			continue
		}

		date, _ := time.Parse(time.RFC3339, string(parts[3]))
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

// GetTree returns the directory listing for a specific path at a specific commit/branch.
func GetTree(ctx context.Context, repoPath, ref, path string) ([]TreeEntry, error) {
	treeish := ref
	if path != "" {
		// Clean the path to prevent escaping the tree view
		path = strings.TrimPrefix(path, "/")
		treeish = fmt.Sprintf("%s:%s", ref, path)
	}

	// -l includes file sizes, -z uses null bytes instead of newlines for safe parsing
	out, err := run(ctx, repoPath, "ls-tree", "-l", "-z", treeish)
	if err != nil {
		return nil, err
	}

	var entries []TreeEntry
	if len(out) == 0 {
		return entries, nil
	}

	records := bytes.Split(out, []byte{0})
	for _, record := range records {
		if len(record) == 0 {
			continue
		}

		// Format: <mode> SP <type> SP <hash> SP <size> TAB <name>
		tabIdx := bytes.IndexByte(record, '\t')
		if tabIdx == -1 {
			continue
		}
		meta := record[:tabIdx]
		name := record[tabIdx+1:]

		parts := bytes.SplitN(meta, []byte{' '}, 4)
		if len(parts) != 4 {
			continue
		}

		sizeStr := string(bytes.TrimSpace(parts[3]))
		var size int64
		if sizeStr != "-" { // directories have a size of "-"
			size, _ = strconv.ParseInt(sizeStr, 10, 64)
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

// GetBlob returns the raw file contents.
func GetBlob(ctx context.Context, repoPath, ref, path string) ([]byte, error) {
	treeish := fmt.Sprintf("%s:%s", ref, strings.TrimPrefix(path, "/"))
	return run(ctx, repoPath, "cat-file", "-p", treeish)
}

// GetBlobSize is useful for checking a file size before loading it into memory.
func GetBlobSize(ctx context.Context, repoPath, ref, path string) (int64, error) {
	treeish := fmt.Sprintf("%s:%s", ref, strings.TrimPrefix(path, "/"))
	out, err := run(ctx, repoPath, "cat-file", "-s", treeish)
	if err != nil {
		return 0, err
	}
	sizeStr := string(bytes.TrimSpace(out))
	return strconv.ParseInt(sizeStr, 10, 64)
}

