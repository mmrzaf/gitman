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
)

// BackupRepos copies the entire repos directory to a destination path.
func BackupRepos(reposPath, destination string) error {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}
	return copyDir(reposPath, destination)
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
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
			if err := os.MkdirAll(dstPath, info.Mode().Perm()); err != nil {
				return err
			}
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else if info.Mode().IsRegular() {
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

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := in.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
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

func BackupAll(ctx context.Context, database *db.DB, cfg *config.Config, destination string) error {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return fmt.Errorf("create dest: %w", err)
	}

	dbDst := filepath.Join(destination, "db", filepath.Base(cfg.DBPath))
	if err := os.MkdirAll(filepath.Dir(dbDst), 0o755); err != nil {
		return err
	}
	if err := vacuumDatabase(ctx, database, dbDst); err != nil {
		return fmt.Errorf("backup db: %w", err)
	}

	reposDst := filepath.Join(destination, "repos")
	if err := copyDir(cfg.ReposPath, reposDst); err != nil {
		return fmt.Errorf("backup repos: %w", err)
	}

	artifactsDst := filepath.Join(destination, "artifacts")
	if err := copyDir(cfg.ArtifactsPath, artifactsDst); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("backup artifacts: %w", err)
		}
	}

	authKeysDst := filepath.Join(destination, "authorized_keys")
	if err := copyFile(cfg.AuthKeysPath, authKeysDst); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("backup authorized_keys: %w", err)
		}
	}

	return nil
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
