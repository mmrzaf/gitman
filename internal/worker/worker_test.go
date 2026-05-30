package worker

import (
	"context"
	"testing"

	"github.com/mmrzaf/gitman/internal/db"
)

func TestShortHash(t *testing.T) {
	if s := shortHash("1234567890"); s != "1234567" {
		t.Errorf("expected 1234567, got %s", s)
	}
	if s := shortHash("abc"); s != "abc" {
		t.Errorf("expected abc, got %s", s)
	}
}

func TestSelectRef(t *testing.T) {
	if ref := selectRef("", ""); ref != "HEAD" {
		t.Errorf("expected HEAD, got %s", ref)
	}
	if ref := selectRef("main", ""); ref != "main" {
		t.Errorf("expected main, got %s", ref)
	}
	if ref := selectRef("main", "v1.0"); ref != "v1.0" {
		t.Errorf("expected v1.0, got %s", ref)
	}
}

func TestResolveRepo(t *testing.T) {
	database, err := db.InitDB("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	// Create user and repo
	user, _ := database.CreateUser(ctx, "owner", "OwnerPass1")
	repoID, _ := database.CreateRepository(ctx, user.ID, "testrepo", "", false)

	info, ownerName, err := resolveRepo(ctx, database, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if ownerName != "owner" {
		t.Errorf("expected owner, got %s", ownerName)
	}
	if info.name != "testrepo" || info.id != repoID {
		t.Error("repo info mismatch")
	}
}

func TestResolveRepoNotFound(t *testing.T) {
	database, err := db.InitDB("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	_, _, err = resolveRepo(ctx, database, "nonexistent")
	if err == nil {
		t.Error("expected error")
	}
}
