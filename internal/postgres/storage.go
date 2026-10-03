package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BindRepositoryStorage pairs a database with its repository storage by instance
// identity. The marker travels with the files when an operator moves storage.
func (d *DB) BindRepositoryStorage(ctx context.Context, root string) (result error) {
	var instance string
	if err := d.Q.QueryRow(ctx, `SELECT id FROM instance`).Scan(&instance); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0750); err != nil {
		return err
	}
	marker := filepath.Join(root, ".gitman-instance")
	check := func() error {
		info, err := os.Lstat(marker)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("repository storage identity must be a regular file")
		}
		value, err := os.ReadFile(marker)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(value)) != instance {
			return errors.New("repository storage belongs to another Gitman instance")
		}
		return nil
	}
	if err := check(); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// An existing database must not silently attach to an empty or partial store.
	rows, err := d.Q.Query(ctx, `SELECT id FROM repos`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		info, err := os.Stat(filepath.Join(root, id+".git"))
		if err != nil {
			return fmt.Errorf("repository %s is missing from storage: %w", id, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("repository %s storage is not a directory", id)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Publish a complete, synced marker atomically without replacing another one.
	file, err := os.CreateTemp(root, ".instance-*")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, os.Remove(file.Name())) }()
	if _, err := file.WriteString(instance + "\n"); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Link(file.Name(), marker); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, dir.Close()) }()
	if err := dir.Sync(); err != nil {
		return err
	}
	return check()
}
