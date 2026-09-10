package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSessionLifecycle(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "sess_user", "Pass1")
	if err != nil {
		t.Fatal(err)
	}
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

	extended, err := db.ExtendSessionIfExpiring(ctx, token, 24*time.Hour, 25*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !extended {
		t.Fatal("expected session extension")
	}

	err = db.DeleteSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	u, err = db.GetUserBySession(ctx, token)
	if !errors.Is(err, ErrNotFound) || u != nil {
		t.Fatalf("deleted session lookup = user=%v err=%v, want ErrNotFound", u, err)
	}
}

func TestExpiredSession(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "exp", "Pass1")
	if err != nil {
		t.Fatal(err)
	}
	id := "expired-token"
	_, err = db.sql.ExecContext(ctx,
		"INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)",
		hashSessionToken(id), user.ID, time.Now().Add(-1*time.Hour).Unix())
	if err != nil {
		t.Fatal(err)
	}
	u, err := db.GetUserBySession(ctx, id)
	if !errors.Is(err, ErrNotFound) || u != nil {
		t.Fatalf("expired session lookup = user=%v err=%v, want ErrNotFound", u, err)
	}
}

func TestPlaintextStoredSessionTokenIsRejected(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()

	user, err := database.CreateUser(ctx, "plaintext_session", "Pass1")
	if err != nil {
		t.Fatal(err)
	}
	token := "plaintext-stored-token"
	if _, err := database.sql.ExecContext(ctx,
		"INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)",
		token, user.ID, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	got, err := database.GetUserBySession(ctx, token)
	if !errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("plaintext session lookup = user=%v err=%v, want ErrNotFound", got, err)
	}
}
