package admin

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mmrzaf/gitman/internal/config"
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

		if entry.IsDir() {
			if err := os.MkdirAll(dstPath, 0o755); err != nil {
				return err
			}
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := in.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	out, err := os.Create(dst)
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

	info, err := os.Stat(src)
	if err != nil {
		return err
	}

	return os.Chmod(dst, info.Mode())
}

func BackupAll(cfg *config.Config, destination string) error {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return fmt.Errorf("create dest: %w", err)
	}
	dbSrc := cfg.DBPath
	dbDst := filepath.Join(destination, "db", filepath.Base(cfg.DBPath))
	if err := os.MkdirAll(filepath.Dir(dbDst), 0o755); err != nil {
		return err
	}
	if err := copyFile(dbSrc, dbDst); err != nil {
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
