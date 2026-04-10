package git

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

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
