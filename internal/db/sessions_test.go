package db

import (
	"context"
	"testing"
	"time"
)

func TestSessionLifecycle(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	user, _ := db.CreateUser(ctx, "sess_user", "Pass1")
	token, err := db.CreateSession(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}

	u, err := db.GetUserBySession(ctx, token)
	if err != nil || u == nil {
		t.Fatal("session not valid")
	}
	if u.ID != user.ID {
		t.Error("user mismatch")
	}

	err = db.ExtendSession(ctx, token, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	err = db.DeleteSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	u, _ = db.GetUserBySession(ctx, token)
	if u != nil {
		t.Error("session still valid after deletion")
	}
}

func TestExpiredSession(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	user, _ := db.CreateUser(ctx, "exp", "Pass1")
	id := "expired-token"
	_, err := db.ExecContext(ctx,
		"INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)",
		id, user.ID, time.Now().Add(-1*time.Hour).Unix())
	if err != nil {
		t.Fatal(err)
	}
	u, err := db.GetUserBySession(ctx, id)
	if err == nil || u != nil {
		t.Error("expected expired session to be invalid")
	}
}
