package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/repo"
	"strconv"
)

type backupManifest struct {
	Format     int               `json:"format"`
	Version    string            `json:"gitman_version"`
	InstanceID string            `json:"instance_id"`
	CreatedAt  time.Time         `json:"created_at"`
	KeySHA256  string            `json:"secret_key_sha256,omitempty"`
	Files      map[string]string `json:"sha256"`
}

func keyFingerprint(key string) (string, error) {
	if key == "" {
		return "", nil
	}
	b, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(b) != 32 {
		return "", errors.New("invalid secret key")
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Credentials stay in environment variables, never process arguments or logs.
func databaseTool(ctx context.Context, name, dsn string, args ...string) error {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return errors.New("backup and restore require a PostgreSQL URL connection string")
	}
	connection, err := pgx.ParseConfig(dsn)
	if err != nil {
		return errors.New("invalid backup database connection")
	}
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "PG") {
			env = append(env, v)
		}
	}
	env = append(env, "PGHOST="+connection.Host, "PGPORT="+strconv.Itoa(int(connection.Port)), "PGDATABASE="+connection.Database, "PGUSER="+connection.User, "PGPASSWORD="+connection.Password)
	for key, value := range map[string]string{"sslmode": "PGSSLMODE", "sslrootcert": "PGSSLROOTCERT", "sslcert": "PGSSLCERT", "sslkey": "PGSSLKEY"} {
		v := u.Query().Get(key)
		if v == "" {
			v = os.Getenv(value)
		}
		if v != "" {
			env = append(env, value+"="+v)
		}
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.WaitDelay = 10 * time.Second
	// Client diagnostics can echo connection strings; keep command errors generic.
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func archiveRepositories(ctx context.Context, root, destination string) error {
	f, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	walkErr := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported repository filesystem entry: %s", rel)
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, in)
		return errors.Join(copyErr, in.Close())
	})
	return errors.Join(walkErr, tw.Close(), gz.Close(), f.Sync(), f.Close())
}

func adminBackup(ctx context.Context, env *adminEnv, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: gitman admin maintenance backup <destination-directory>")
	}
	release, err := env.db.SnapshotLease(ctx)
	if err != nil {
		return err
	}
	defer release()
	dest, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	data, err := filepath.Abs(env.cfg.DataDir)
	if err != nil {
		return err
	}
	if dest == data || strings.HasPrefix(dest, data+string(os.PathSeparator)) {
		return errors.New("backup destination must be outside the data directory")
	}
	// Refuse replacement: a backup becomes valid only after its manifest is durable.
	if err := os.Mkdir(dest, 0700); err != nil {
		return err
	}
	m := backupManifest{Format: 1, Version: version, CreatedAt: time.Now().UTC(), Files: map[string]string{}}
	if err := env.db.Q.QueryRow(ctx, `SELECT id FROM instance`).Scan(&m.InstanceID); err != nil {
		return err
	}
	m.KeySHA256, err = keyFingerprint(env.cfg.SecretKey)
	if err != nil {
		return err
	}
	dump := filepath.Join(dest, "database.dump")
	if err := databaseTool(ctx, "pg_dump", env.cfg.DatabaseURL, "--format=custom", "--no-owner", "--file="+dump); err != nil {
		return err
	}
	if err := syncFile(dump); err != nil {
		return err
	}
	if err := os.Chmod(dump, 0600); err != nil {
		return err
	}
	if err := archiveRepositories(ctx, env.cfg.ReposPath(), filepath.Join(dest, "repos.tar.gz")); err != nil {
		return err
	}
	for _, name := range []string{"database.dump", "repos.tar.gz"} {
		m.Files[name], err = hashFile(filepath.Join(dest, name))
		if err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dest, "manifest.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(b, '\n'))
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if err := syncFile(dest); err != nil {
		return err
	}
	fmt.Fprintf(env.out, "Backup complete: %s\nKeep GITMAN_SECRET_KEY separately; the backup contains only its fingerprint.\nMaintenance remains enabled.\n", dest)
	return nil
}

func verifyBackup(path, key string) (*backupManifest, error) {
	b, err := os.ReadFile(filepath.Join(path, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m backupManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.Format != 1 || m.InstanceID == "" || len(m.Files) != 2 {
		return nil, errors.New("invalid backup manifest")
	}
	fingerprint, err := keyFingerprint(key)
	if err != nil {
		return nil, err
	}
	if fingerprint != m.KeySHA256 {
		return nil, errors.New("secret key does not match the backup fingerprint")
	}
	for _, name := range []string{"database.dump", "repos.tar.gz"} {
		got, err := hashFile(filepath.Join(path, name))
		if err != nil {
			return nil, err
		}
		if got != m.Files[name] {
			return nil, fmt.Errorf("backup checksum mismatch: %s", name)
		}
	}
	return &m, nil
}

func extractRepositories(ctx context.Context, source, root string) error {
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		clean := filepath.Clean(filepath.FromSlash(h.Name))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return errors.New("unsafe path in repository archive")
		}
		path := filepath.Join(root, clean)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0750); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
				return err
			}
			out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fs.FileMode(h.Mode)&0770)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			if err := errors.Join(copyErr, out.Close()); err != nil {
				return err
			}
		default:
			return errors.New("unsupported entry in repository archive")
		}
	}
}

// Restore only targets an empty database and data directory. The restored
// instance stays paused until repository integrity and secret decryption pass.
func runRestore(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: gitman restore <backup-directory>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	m, err := verifyBackup(args[0], cfg.SecretKey)
	if err != nil {
		return err
	}
	db, err := postgres.Connect(ctx, cfg.DatabaseURL, postgres.Options{MaxConns: 4})
	if err != nil {
		return err
	}
	defer db.Close()
	var populated bool
	if err := db.Q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema'))`).Scan(&populated); err != nil {
		return err
	}
	if populated {
		return errors.New("restore requires an empty database")
	}
	entries, err := os.ReadDir(cfg.DataDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(entries) != 0 {
		return errors.New("restore requires an empty data directory")
	}
	if err := os.MkdirAll(cfg.ReposPath(), 0750); err != nil {
		return err
	}
	if err := extractRepositories(ctx, filepath.Join(args[0], "repos.tar.gz"), cfg.ReposPath()); err != nil {
		return err
	}
	if err := databaseTool(ctx, "pg_restore", cfg.DatabaseURL, "--exit-on-error", "--single-transaction", "--no-owner", "--dbname="+envDatabaseName(cfg.DatabaseURL), filepath.Join(args[0], "database.dump")); err != nil {
		return err
	}
	if err := db.Migrate(ctx); err != nil {
		return err
	}
	root, err := filepath.Abs(cfg.ReposPath())
	if err != nil {
		return err
	}
	if err := db.BindRepositoryStorage(ctx, root); err != nil {
		return err
	}
	var instance string
	if err := db.Q.QueryRow(ctx, `SELECT id FROM instance`).Scan(&instance); err != nil {
		return err
	}
	if instance != m.InstanceID {
		return errors.New("restored instance does not match manifest")
	}
	if err := db.BackupReady(ctx); err != nil {
		return err
	}
	store := git.NewStore(cfg.ReposPath())
	defer store.Close()
	repos := repo.NewService(db, store, cfg.SecretKey)
	if err := repos.VerifySnapshot(ctx); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Restore verified. Maintenance remains enabled; inspect the restored installation before resuming.")
	return nil
}
func envDatabaseName(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Path, "/")
}

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
