package db

import (
	"context"
	"testing"
)

func TestCreateRepository(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	user, _ := db.CreateUser(ctx, "owner", "OwnerPass1")
	repoID, err := db.CreateRepository(ctx, user.ID, "myrepo", "test desc", true)
	if err != nil {
		t.Fatalf("CreateRepository failed: %v", err)
	}
	repo, err := db.GetRepositoryByID(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Name != "myrepo" {
		t.Errorf("expected name myrepo, got %s", repo.Name)
	}
	if !repo.IsPrivate {
		t.Error("expected private repo")
	}
}

func TestGetUserRepositories(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	user, _ := db.CreateUser(ctx, "dev", "DevPass1")
	db.CreateRepository(ctx, user.ID, "repo1", "", false)
	db.CreateRepository(ctx, user.ID, "repo2", "", true)
	repos, err := db.GetUserRepositories(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Errorf("expected 2 repos, got %d", len(repos))
	}
}

func TestGetRepositoryByOwnerAndName(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	user, _ := db.CreateUser(ctx, "owner2", "OwnerPass1")
	db.CreateRepository(ctx, user.ID, "unique", "", false)
	repo, err := db.GetRepositoryByOwnerAndName(ctx, user.ID, "unique")
	if err != nil {
		t.Fatal(err)
	}
	if repo == nil || repo.Name != "unique" {
		t.Errorf("repo not found correctly")
	}

	_, err = db.GetRepositoryByOwnerAndName(ctx, user.ID, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent")
	}
}

func TestCollaborators(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	owner, _ := db.CreateUser(ctx, "owner", "OwnerPass1")
	collab, _ := db.CreateUser(ctx, "collab", "CollabPass1")
	repoID, _ := db.CreateRepository(ctx, owner.ID, "shared", "", false)

	err := db.AddCollaborator(ctx, repoID, collab.ID, "write")
	if err != nil {
		t.Fatal(err)
	}

	has, err := db.HasRepoAccess(ctx, repoID, collab.ID, "write")
	if err != nil || !has {
		t.Errorf("expected write access")
	}
	has, err = db.HasRepoAccess(ctx, repoID, collab.ID, "read")
	if err != nil || !has {
		t.Errorf("expected read access (write implies read)")
	}

	colls, err := db.GetCollaborators(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(colls) != 1 {
		t.Errorf("expected 1 collaborator, got %d", len(colls))
	}

	err = db.RemoveCollaborator(ctx, repoID, collab.ID)
	if err != nil {
		t.Fatal(err)
	}
	has, _ = db.HasRepoAccess(ctx, repoID, collab.ID, "read")
	if has {
		t.Error("access not removed")
	}
}

func TestDeleteRepository(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	user, _ := db.CreateUser(ctx, "owner", "OwnerPass1")
	id, _ := db.CreateRepository(ctx, user.ID, "todelete", "", false)
	err := db.DeleteRepository(ctx, id, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.GetRepositoryByID(ctx, id)
	if err == nil {
		t.Error("expected error fetching deleted repo")
	}
}
