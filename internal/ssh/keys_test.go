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
