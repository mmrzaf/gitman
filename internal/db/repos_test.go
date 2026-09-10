package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/models"
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

	level, err := db.GetRepoAccessLevel(ctx, repoID, collab.ID)
	if err != nil || level != models.AccessWrite {
		t.Fatalf("expected write access, got %q, %v", level, err)
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
	if _, err := db.GetRepoAccessLevel(ctx, repoID, collab.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected collaborator access to be removed, got %v", err)
	}
}

func TestDeleteRepository(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	user, _ := db.CreateUser(ctx, "owner", "OwnerPass1")
	id, _ := db.CreateRepository(ctx, user.ID, "todelete", "", false)
	deleted, err := db.DeleteRepository(ctx, id, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("expected repository to be deleted")
	}
	_, err = db.GetRepositoryByID(ctx, id)
	if err == nil {
		t.Error("expected error fetching deleted repo")
	}
}

func TestDeleteRepositoryRejectsActiveCIAtomically(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()

	owner, _ := database.CreateUser(ctx, "delete-owner", "OwnerPass1")
	repoID, _ := database.CreateRepository(ctx, owner.ID, "busy", "", false)
	if _, err := database.CreateCIRun(ctx, repoID, strings.Repeat("a", 40), "main", "", "manual"); err != nil {
		t.Fatal(err)
	}

	deleted, err := database.DeleteRepository(ctx, repoID, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted {
		t.Fatal("active repository was deleted")
	}
	if _, err := database.GetRepositoryByID(ctx, repoID); err != nil {
		t.Fatalf("repository should remain: %v", err)
	}
}

func TestUpdateRepositorySettings(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()

	owner, _ := database.CreateUser(ctx, "settings-owner", "OwnerPass1")
	other, _ := database.CreateUser(ctx, "settings-other", "OtherPass1")
	repoID, _ := database.CreateRepository(ctx, owner.ID, "settings", "before", false)

	if err := database.UpdateRepositorySettings(ctx, repoID, other.ID, "forbidden", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-owner update error = %v, want ErrNotFound", err)
	}
	if err := database.UpdateRepositorySettings(ctx, repoID, owner.ID, "after", true); err != nil {
		t.Fatalf("UpdateRepositorySettings: %v", err)
	}
	repo, err := database.GetRepositoryByID(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Description != "after" || !repo.IsPrivate {
		t.Fatalf("settings = (%q, %t), want (%q, true)", repo.Description, repo.IsPrivate, "after")
	}
}

func TestListRepositoryLocationsIncludesRepositoryID(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()

	owner, err := database.CreateUser(ctx, "location-owner", "OwnerPass1")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := database.CreateRepository(ctx, owner.ID, "location-repo", "", false)
	if err != nil {
		t.Fatal(err)
	}

	locations, err := database.ListRepositoryLocations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, location := range locations {
		if location.ID == repoID {
			if location.Owner != owner.Username || location.Name != "location-repo" {
				t.Fatalf("unexpected location: %+v", location)
			}
			return
		}
	}
	t.Fatalf("repository %s missing from locations: %+v", repoID, locations)
}
