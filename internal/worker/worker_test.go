package worker

import (
	"context"
	"os"
	"path/filepath"
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

func TestMoveFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	os.WriteFile(src, []byte("data"), 0644)
	err := moveFile(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("source not removed")
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "data" {
		t.Error("content mismatch")
	}
}

func TestPrepareDirectories(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "logs")
	err := prepareDirectories(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sub); os.IsNotExist(err) {
		t.Error("sub dir not created")
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
