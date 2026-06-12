package ci

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

func setupPolicyTest(t *testing.T) (*db.DB, string, *models.User, *models.Repository) {
	t.Helper()
	database, err := db.InitDB(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	user, err := database.CreateUser(context.Background(), "owner", "OwnerPass1")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := database.CreateRepository(context.Background(), user.ID, "repo", "", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := database.GetRepositoryByID(context.Background(), repoID)
	if err != nil {
		t.Fatal(err)
	}
	reposPath := t.TempDir()
	repoPath, err := git.SecureRepoPath(reposPath, user.Username, repo.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := git.InitBareRepo(context.Background(), repoPath, 512*1024*1024); err != nil {
		t.Fatal(err)
	}
	return database, reposPath, user, repo
}

func TestResolverDefaultPolicies(t *testing.T) {
	database, reposPath, owner, repo := setupPolicyTest(t)
	resolver := Resolver{DB: database, ReposPath: reposPath}
	mainPolicy, err := resolver.Resolve(context.Background(), owner, repo, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if !mainPolicy.AutoRun || !mainPolicy.AllowSecrets || mainPolicy.AllowDockerSocket {
		t.Fatalf("unexpected default branch policy: %+v", mainPolicy)
	}
	devPolicy, err := resolver.Resolve(context.Background(), owner, repo, "development", "")
	if err != nil {
		t.Fatal(err)
	}
	if devPolicy.AutoRun || devPolicy.AllowSecrets || devPolicy.AllowDockerSocket {
		t.Fatalf("unexpected non-default branch policy: %+v", devPolicy)
	}
	tagPolicy, err := resolver.Resolve(context.Background(), owner, repo, "", "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if tagPolicy.AutoRun || tagPolicy.AllowSecrets || tagPolicy.AllowDockerSocket {
		t.Fatalf("unexpected tag policy: %+v", tagPolicy)
	}
}

func TestResolverExplicitRuleOverridesDefault(t *testing.T) {
	database, reposPath, owner, repo := setupPolicyTest(t)
	if err := database.UpsertRepoCIRefRule(context.Background(), models.RepoCIRefRule{
		RepoID:            repo.ID,
		RefType:           "branch",
		RefName:           "development",
		AutoRun:           true,
		AllowSecrets:      true,
		AllowDockerSocket: true,
	}); err != nil {
		t.Fatal(err)
	}
	policy, err := (Resolver{DB: database, ReposPath: reposPath}).Resolve(context.Background(), owner, repo, "development", "")
	if err != nil {
		t.Fatal(err)
	}
	if policy.Source != PolicySourceRule || !policy.AutoRun || !policy.AllowSecrets || !policy.AllowDockerSocket {
		t.Fatalf("explicit rule was not applied: %+v", policy)
	}
}
