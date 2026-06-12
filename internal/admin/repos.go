package admin

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
)

// BackupRepos creates an atomic filesystem backup of the repositories tree.
func BackupRepos(reposPath, destination string) error {
	if err := rejectNestedDestination(destination, reposPath); err != nil {
		return err
	}
	return withAtomicDestination(destination, func(staging string) error {
		return copyDir(reposPath, staging)
	})
}

// BackupAll creates a coherent SQLite snapshot and filesystem copy. Repository
// and artifact files are copied live; operators should use a maintenance window
// or filesystem snapshot when strict point-in-time consistency is required.
func BackupAll(ctx context.Context, database *db.DB, cfg *config.Config, destination string) error {
	if err := rejectNestedDestination(destination, cfg.ReposPath, cfg.ArtifactsPath); err != nil {
		return err
	}
	return withAtomicDestination(destination, func(staging string) error {
		dbDst := filepath.Join(staging, "db", filepath.Base(cfg.DBPath))
		if err := os.MkdirAll(filepath.Dir(dbDst), 0o700); err != nil {
			return err
		}
		if err := vacuumDatabase(ctx, database, dbDst); err != nil {
			return fmt.Errorf("backup db: %w", err)
		}
		if err := os.Chmod(dbDst, 0o600); err != nil {
			return fmt.Errorf("secure db backup: %w", err)
		}

		if err := copyDir(cfg.ReposPath, filepath.Join(staging, "repos")); err != nil {
			return fmt.Errorf("backup repos: %w", err)
		}
		if err := copyDir(cfg.ArtifactsPath, filepath.Join(staging, "artifacts")); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("backup artifacts: %w", err)
		}
		if err := copyFile(cfg.AuthKeysPath, filepath.Join(staging, "authorized_keys")); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("backup authorized_keys: %w", err)
		}
		return nil
	})
}

func ConfigureAllRepos(ctx context.Context, database *db.DB, cfg *config.Config) error {
	if cfg.GitReceiveMaxBytes <= 0 {
		return fmt.Errorf("GITMAN_GIT_RECEIVE_MAX_BYTES must be positive")
	}
	base, err := filepath.Abs(cfg.ReposPath)
	if err != nil {
		return err
	}
	rows, err := database.QueryContext(ctx, `
		SELECT u.username, r.name
		FROM repositories r
		JOIN users u ON u.id = r.owner_id
		ORDER BY u.username, r.name
	`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var owner, repoName string
		if err := rows.Scan(&owner, &repoName); err != nil {
			return err
		}
		repoPath, err := git.SecureRepoPath(cfg.ReposPath, owner, repoName)
		if err != nil {
			return err
		}
		repoAbs, err := filepath.Abs(repoPath)
		if err != nil {
			return err
		}
		if !pathWithin(base, repoAbs) {
			return fmt.Errorf("repository path escaped root: %s", repoAbs)
		}
		if err := rejectSymlinkPath(base, repoAbs); err != nil {
			return err
		}
		info, err := os.Lstat(repoAbs)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("repository path is not a real directory: %s", repoAbs)
		}
		if err := git.ConfigureReceiveMaxInputSize(ctx, repoAbs, cfg.GitReceiveMaxBytes); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	return rows.Close()
}

func rejectSymlinkPath(base, candidate string) error {
	rel, err := filepath.Rel(base, candidate)
	if err != nil {
		return err
	}
	current := base
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink in repository path: %s", current)
		}
	}
	return nil
}

func withAtomicDestination(destination string, populate func(staging string) error) error {
	destination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(destination); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("destination already exists: %s", destination)
		}
		entries, readErr := os.ReadDir(destination)
		if readErr != nil {
			return readErr
		}
		if len(entries) != 0 {
			return fmt.Errorf("destination already exists and is not empty: %s", destination)
		}
		if err := os.Remove(destination); err != nil {
			return fmt.Errorf("remove empty destination: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(destination)+".partial-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if err := os.Chmod(staging, 0o700); err != nil {
		return err
	}
	if err := populate(staging); err != nil {
		return err
	}
	if err := os.Rename(staging, destination); err != nil {
		return fmt.Errorf("publish backup: %w", err)
	}
	return nil
}

func rejectNestedDestination(destination string, sources ...string) error {
	destResolved, err := resolvePathForContainment(destination)
	if err != nil {
		return err
	}
	for _, source := range sources {
		if source == "" {
			continue
		}
		srcResolved, err := resolvePathForContainment(source)
		if err != nil {
			return err
		}
		if pathWithin(srcResolved, destResolved) {
			return fmt.Errorf("backup destination must not be inside source path %s", srcResolved)
		}
	}
	return nil
}

// resolvePathForContainment resolves symlinks in the nearest existing parent
// and then reattaches non-existing tail components. This closes containment
// bypasses such as /tmp/repos-link/backup where repos-link points into repos.
func resolvePathForContainment(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(abs)
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			parts := append([]string{resolved}, tail...)
			return filepath.Clean(filepath.Join(parts...)), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		tail = append([]string{filepath.Base(current)}, tail...)
		current = parent
	}
}

func pathWithin(base, candidate string) bool {
	rel, err := filepath.Rel(base, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		info, err := os.Lstat(srcPath)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if info.Mode().IsRegular() {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dst string) (err error) {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to copy symlink: %s", src)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to copy non-regular file: %s", src)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := in.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := out.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func vacuumDatabase(ctx context.Context, database *db.DB, destination string) error {
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("destination database already exists: %s", destination)
	} else if !os.IsNotExist(err) {
		return err
	}
	_, err := database.ExecContext(ctx, "VACUUM INTO "+sqliteStringLiteral(destination))
	return err
}

func sqliteStringLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
