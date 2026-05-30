package admin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
)

func TestBackupRepos(t *testing.T) {
	srcDir := t.TempDir()
	subDir := filepath.Join(srcDir, "sub")
	err := os.MkdirAll(subDir, 0755)
	if err != nil {
		t.Fatal(err)
	}
	// Write files whose content matches their name (without extension)
	writeFile := func(name, content string) {
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("root.txt", "root")
	writeFile("sub/sub.txt", "sub/sub") // content must be "sub/sub" to match assertion logic

	destDir := t.TempDir()
	err = BackupRepos(srcDir, destDir)
	if err != nil {
		t.Fatalf("BackupRepos failed: %v", err)
	}

	// Verify files content
	for _, f := range []string{"root.txt", "sub/sub.txt"} {
		data, err := os.ReadFile(filepath.Join(destDir, f))
		if err != nil {
			t.Errorf("missing file %s: %v", f, err)
		} else if expected := f[:len(f)-4]; string(data) != expected {
			t.Errorf("content mismatch in %s: got %q want %q", f, data, expected)
		}
	}
}

func TestBackupAll(t *testing.T) {
	baseDir := t.TempDir()
	dbPath := filepath.Join(baseDir, "db", "gitman.sqlite")
	os.MkdirAll(filepath.Dir(dbPath), 0755)
	reposPath := filepath.Join(baseDir, "repos")
	os.MkdirAll(filepath.Join(reposPath, "owner", "repo.git"), 0755)
	os.WriteFile(filepath.Join(reposPath, "owner", "repo.git", "HEAD"), []byte("ref: refs/heads/main"), 0644)

	artifactsPath := filepath.Join(baseDir, "artifacts")
	os.MkdirAll(filepath.Join(artifactsPath, "logs"), 0755)
	os.WriteFile(filepath.Join(artifactsPath, "logs", "run.log"), []byte("log"), 0644)

	authKeysPath := filepath.Join(baseDir, "authorized_keys")
	os.WriteFile(authKeysPath, []byte("ssh-rsa AAA..."), 0600)

	cfg := &config.Config{
		DBPath:        dbPath,
		ReposPath:     reposPath,
		ArtifactsPath: artifactsPath,
		AuthKeysPath:  authKeysPath,
	}

	database, err := db.InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer database.Close()

	destDir := t.TempDir()
	err = BackupAll(context.Background(), database, cfg, destDir)
	if err != nil {
		t.Fatalf("BackupAll failed: %v", err)
	}

	// Check DB copy
	dbCopy := filepath.Join(destDir, "db", "gitman.sqlite")
	data, err := os.ReadFile(dbCopy)
	if err != nil {
		t.Error("db not copied")
	} else if len(data) == 0 {
		t.Error("db backup is empty")
	}

	// Repos copy
	repoFile := filepath.Join(destDir, "repos", "owner", "repo.git", "HEAD")
	if _, err := os.Stat(repoFile); os.IsNotExist(err) {
		t.Error("repo not copied")
	}

	// Artifacts copy
	artFile := filepath.Join(destDir, "artifacts", "logs", "run.log")
	if _, err := os.Stat(artFile); os.IsNotExist(err) {
		t.Error("artifacts not copied")
	}

	// Auth keys copy
	authFile := filepath.Join(destDir, "authorized_keys")
	data, err = os.ReadFile(authFile)
	if err != nil {
		t.Error("authorized_keys not copied")
	} else if string(data) != "ssh-rsa AAA..." {
		t.Error("auth content mismatch")
	}
}

func TestCopyFileError(t *testing.T) {
	err := copyFile("/nonexistent/src", t.TempDir()+"/dst")
	if err == nil {
		t.Error("expected error")
	}
}

func TestCopyDirError(t *testing.T) {
	err := copyDir("/nonexistent", t.TempDir()+"/dst")
	if err == nil {
		t.Error("expected error")
	}
}
