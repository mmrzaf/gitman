package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestAccessTokens(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	user, _ := db.CreateUser(ctx, "tokuser", "Pass1")
	plain := "gm_secrettokenvalue"
	hash := sha256.Sum256([]byte(plain))
	tokenHash := hex.EncodeToString(hash[:])

	err := db.CreateAccessToken(ctx, user.ID, "my token", tokenHash)
	if err != nil {
		t.Fatal(err)
	}

	tokens, err := db.GetUserAccessTokens(ctx, user.ID)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tokens))
	}
	if tokens[0].Name != "my token" {
		t.Errorf("token name mismatch")
	}

	u, err := db.GetUserByTokenHash(ctx, tokenHash)
	if err != nil || u == nil {
		t.Fatal("token hash lookup failed")
	}
	if u.Username != "tokuser" {
		t.Errorf("expected tokuser, got %s", u.Username)
	}

	err = db.DeleteAccessToken(ctx, tokens[0].ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	u, err = db.GetUserByTokenHash(ctx, tokenHash)
	if err == nil || u != nil {
		t.Error("token hash still valid after deletion")
	}
}
