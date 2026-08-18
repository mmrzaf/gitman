package db

import (
	"context"
	"errors"
	"testing"

	"github.com/mmrzaf/gitman/internal/models"
)

func TestCreateUser(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "alice", "StrongPass1")
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	if user.ID == "" {
		t.Error("expected user ID")
	}
	if user.Username != "alice" {
		t.Errorf("expected username alice, got %s", user.Username)
	}
	if user.PasswordHash == "" {
		t.Error("expected password hash")
	}
	ok, err := VerifyPassword(user.PasswordHash, "StrongPass1")
	if err != nil || !ok {
		t.Fatalf("password verification = %v, %v; want true, nil", ok, err)
	}
}

func TestCreateUserDuplicate(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	_, err := db.CreateUser(ctx, "bob", "ValidPass1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.CreateUser(ctx, "bob", "OtherPass1")
	if err == nil {
		t.Error("expected duplicate username error")
	}
}

func TestGetUserByUsername(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	db.CreateUser(ctx, "eve", "Pass12345")
	u, err := db.GetUserByUsername(ctx, "eve")
	if err != nil {
		t.Fatalf("GetUserByUsername failed: %v", err)
	}
	if u.Username != "eve" {
		t.Errorf("expected eve, got %s", u.Username)
	}

	u, err = db.GetUserByUsername(ctx, "nonexistent")
	if !errors.Is(err, ErrNotFound) || u != nil {
		t.Fatalf("missing user = %+v, %v; want ErrNotFound", u, err)
	}
}

func TestUpdateUserPassword(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	db.CreateUser(ctx, "resetme", "OldPass1")
	err := db.UpdateUserPassword(ctx, "resetme", "NewPass2")
	if err != nil {
		t.Fatalf("UpdateUserPassword failed: %v", err)
	}
	u, err := db.GetUserByUsername(ctx, "resetme")
	if err != nil {
		t.Fatalf("GetUserByUsername after password update: %v", err)
	}
	ok, err := VerifyPassword(u.PasswordHash, "NewPass2")
	if err != nil || !ok {
		t.Fatalf("new password verification = %v, %v; want true, nil", ok, err)
	}
	ok, err = VerifyPassword(u.PasswordHash, "OldPass1")
	if err != nil {
		t.Fatalf("old password verification returned error: %v", err)
	}
	if ok {
		t.Error("old password still valid")
	}
}

func TestDeleteUserByID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "delete_me", "Pass1")
	if err != nil {
		t.Fatal(err)
	}
	err = db.DeleteUserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("DeleteUserByID failed: %v", err)
	}
	u, _ := db.GetUserByUsername(ctx, "delete_me")
	if u != nil {
		t.Error("user still exists after deletion")
	}

	err = db.DeleteUserByID(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent user")
	}
}

func TestDeleteUserByIDRefusesActiveCI(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()

	user, err := database.CreateUser(ctx, "busy_delete", "Pass1")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := database.CreateRepository(ctx, user.ID, "busy", "", false)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := database.CreateCIRun(ctx, repoID, "abcdef", "main", "", models.CIEventManual)
	if err != nil {
		t.Fatal(err)
	}

	if err := database.DeleteUserByID(ctx, user.ID); !errors.Is(err, ErrActiveCIRuns) {
		t.Fatalf("DeleteUserByID with pending CI error = %v, want ErrActiveCIRuns", err)
	}
	if _, err := database.GetUserByID(ctx, user.ID); err != nil {
		t.Fatalf("user disappeared after refused delete: %v", err)
	}
	if cancelled, err := database.CancelCIRun(ctx, repoID, runID, "test cleanup"); err != nil || !cancelled {
		t.Fatalf("cancel run = %v, %v; want true, nil", cancelled, err)
	}
	if err := database.DeleteUserByID(ctx, user.ID); err != nil {
		t.Fatalf("DeleteUserByID after CI became inactive: %v", err)
	}
}

func TestVerifyPasswordRejectsCorruptHashAsError(t *testing.T) {
	ok, err := VerifyPassword("not-a-bcrypt-hash", "Password1")
	if err == nil || ok {
		t.Fatalf("VerifyPassword corrupt hash = %v, %v; want false, error", ok, err)
	}
}
