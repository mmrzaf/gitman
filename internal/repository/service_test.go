package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	citrigger "github.com/mmrzaf/gitman/internal/ci/trigger"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

func setupRepositoryManagerTest(t *testing.T) (*Manager, context.Context, *models.User) {
	t.Helper()
	database, err := db.InitDB(filepath.Join(t.TempDir(), "gitman.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	owner, err := database.CreateUser(ctx, "owner", "Password1")
	if err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		DB:                 database,
		ReposPath:          t.TempDir(),
		ArtifactsPath:      t.TempDir(),
		CacheRoot:          t.TempDir(),
		GitReceiveMaxBytes: 512 * 1024 * 1024,
	}
	return manager, ctx, owner
}

func TestManagerCreateInstallsManagedCIHook(t *testing.T) {
	manager, ctx, owner := setupRepositoryManagerTest(t)
	id, err := manager.Create(ctx, owner, "project", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.DB.GetRepositoryByID(ctx, id); err != nil {
		t.Fatal(err)
	}
	repoPath, err := git.SecureRepoPath(manager.ReposPath, owner.Username, "project")
	if err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(repoPath, "hooks", "post-receive")
	hook, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hook), "# gitman-managed-post-receive:") {
		t.Fatalf("post-receive hook is not Gitman-managed: %q", hook)
	}
	info, err := os.Stat(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("post-receive hook is not executable: mode=%o", info.Mode().Perm())
	}
}

func TestManagerDeleteRejectsActiveCI(t *testing.T) {
	manager, ctx, owner := setupRepositoryManagerTest(t)
	id, err := manager.Create(ctx, owner, "active", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.DB.CreateCIRun(ctx, id, strings.Repeat("a", 40), "main", "", models.CIEventManual); err != nil {
		t.Fatal(err)
	}
	if err := manager.Delete(ctx, owner, id); !errors.Is(err, ErrActiveCI) {
		t.Fatalf("Delete error=%v, want ErrActiveCI", err)
	}
	if _, err := manager.DB.GetRepositoryByID(ctx, id); err != nil {
		t.Fatal("repository disappeared despite active CI")
	}
}

func TestManagerDeleteRejectsUndeliveredPushEvent(t *testing.T) {
	manager, ctx, owner := setupRepositoryManagerTest(t)
	id, err := manager.Create(ctx, owner, "queued", "", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := manager.DB.GetRepositoryByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	repoPath, err := git.SecureRepoPath(manager.ReposPath, owner.Username, repo.Name)
	if err != nil {
		t.Fatal(err)
	}
	queueDir := filepath.Join(repoPath, "hooks", citrigger.QueueDirName)
	if err := os.MkdirAll(queueDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A not-yet-published temporary event must fail deletion closed.
	if err := os.WriteFile(filepath.Join(queueDir, ".event.incomplete"), []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Delete(ctx, owner, id); !errors.Is(err, ErrQueuedTriggers) {
		t.Fatalf("Delete error=%v, want ErrQueuedTriggers", err)
	}
}

func TestManagerDeleteRemovesRepositoryAndDerivedStorage(t *testing.T) {
	manager, ctx, owner := setupRepositoryManagerTest(t)
	id, err := manager.Create(ctx, owner, "delete-me", "", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{manager.ArtifactsPath, manager.CacheRoot} {
		path := filepath.Join(root, "logs", owner.Username, "delete-me")
		if root == manager.CacheRoot {
			path = filepath.Join(root, owner.Username, "delete-me")
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Delete(ctx, owner, id); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.DB.GetRepositoryByID(ctx, id); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("repository lookup error=%v, want ErrNotFound", err)
	}
	repoPath, _ := git.SecureRepoPath(manager.ReposPath, owner.Username, "delete-me")
	if _, err := os.Stat(repoPath); !os.IsNotExist(err) {
		t.Fatalf("repository storage still exists: %v", err)
	}
}
