package ssh

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
)

func setupTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.InitDB("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	user, _ := database.CreateUser(ctx, "gituser", "Pass1")
	_ = database.AddSSHKey(ctx, user.ID, "key1", "ssh-rsa AAAAB3...")
	_ = database.AddSSHKey(ctx, user.ID, "key2", "ecdsa-sha2-nistp256 AAAAE2V...")
	return database
}

func TestSyncAuthorizedKeys(t *testing.T) {
	database := setupTestDB(t)
	dir := t.TempDir()
	authFile := filepath.Join(dir, "authorized_keys")
	cfg := &config.Config{
		AuthKeysPath: authFile,
		BinaryPath:   "/usr/local/bin/gitman",
	}
	err := SyncAuthorizedKeys(context.Background(), database, cfg)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(authFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	// header + 2 keys = 3 lines
	if len(lines) != 3 {
		t.Errorf("expected 3 lines (header + 2 keys), got %d", len(lines))
	}
	if !strings.Contains(string(data), `command="/usr/local/bin/gitman serve`) {
		t.Error("forced command not found")
	}
}

func TestSyncAuthorizedKeysUsesRestrictivePermissions(t *testing.T) {
	database := setupTestDB(t)
	dir := t.TempDir()
	authFile := filepath.Join(dir, "authorized_keys")
	cfg := &config.Config{AuthKeysPath: authFile, BinaryPath: "/usr/local/bin/gitman"}
	if err := SyncAuthorizedKeys(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(authFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 authorized_keys, got %o", info.Mode().Perm())
	}
}

func TestSyncAuthorizedKeysBasenameDoesNotChmodWorkingDirectory(t *testing.T) {
	database := setupTestDB(t)
	cwd, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	cfg := &config.Config{AuthKeysPath: "authorized_keys", BinaryPath: "/usr/local/bin/gitman"}
	if err := SyncAuthorizedKeys(context.Background(), database, cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("working directory mode changed to %o", got)
	}
}
