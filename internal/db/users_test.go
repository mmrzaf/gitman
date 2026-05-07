package db

import (
	"context"
	"testing"
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
	if !VerifyPassword(user.PasswordHash, "StrongPass1") {
		t.Error("password verification failed")
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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u != nil {
		t.Error("expected nil for nonexistent user")
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
	u, _ := db.GetUserByUsername(ctx, "resetme")
	if !VerifyPassword(u.PasswordHash, "NewPass2") {
		t.Error("password not updated correctly")
	}
	if VerifyPassword(u.PasswordHash, "OldPass1") {
		t.Error("old password still valid")
	}
}

func TestDeleteUserByUsername(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	db.CreateUser(ctx, "delete_me", "Pass1")
	err := db.DeleteUserByUsername(ctx, "delete_me")
	if err != nil {
		t.Fatalf("DeleteUserByUsername failed: %v", err)
	}
	u, _ := db.GetUserByUsername(ctx, "delete_me")
	if u != nil {
		t.Error("user still exists after deletion")
	}

	err = db.DeleteUserByUsername(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent user")
	}
}
