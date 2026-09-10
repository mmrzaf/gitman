package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/models"
)

func TestAccessTokenLifecycle(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()

	user, err := database.CreateUser(ctx, "tokuser", "Pass1")
	if err != nil {
		t.Fatal(err)
	}
	plain := "gm_secrettokenvalue"
	hash := sha256.Sum256([]byte(plain))
	tokenHash := hex.EncodeToString(hash[:])
	expiresAt := time.Now().Add(24 * time.Hour).Truncate(time.Second)

	tokenID, err := database.CreateAccessToken(ctx, user.ID, "my token", tokenHash, &expiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if tokenID == "" {
		t.Fatal("token ID is empty")
	}

	tokens, err := database.GetUserAccessTokens(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tokens))
	}
	if tokens[0].Name != "my token" || tokens[0].Scope != models.AccessTokenScopeRepoRead || tokens[0].ExpiresAt == nil || !tokens[0].ExpiresAt.Equal(expiresAt) {
		t.Fatalf("unexpected token metadata: %+v", tokens[0])
	}
	if tokens[0].LastUsedAt != nil {
		t.Fatalf("new token unexpectedly has last_used_at: %v", tokens[0].LastUsedAt)
	}
	if _, err := database.AuthenticateAccessTokenForUser(ctx, tokenHash, "wrong-user"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("token accepted for wrong username: %v", err)
	}
	tokens, err = database.GetUserAccessTokens(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tokens[0].LastUsedAt != nil {
		t.Fatal("rejected username/token pair updated last_used_at")
	}

	u, err := database.AuthenticateAccessToken(ctx, tokenHash)
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "tokuser" {
		t.Fatalf("expected tokuser, got %s", u.Username)
	}
	tokens, err = database.GetUserAccessTokens(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tokens[0].LastUsedAt == nil {
		t.Fatal("successful authentication did not update last_used_at")
	}
	firstUsedAt := *tokens[0].LastUsedAt
	if _, err := database.AuthenticateAccessToken(ctx, tokenHash); err != nil {
		t.Fatal(err)
	}
	tokens, err = database.GetUserAccessTokens(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tokens[0].LastUsedAt == nil || !tokens[0].LastUsedAt.Equal(firstUsedAt) {
		t.Fatalf("last_used_at was rewritten inside the five-minute coalescing window: first=%v now=%v", firstUsedAt, tokens[0].LastUsedAt)
	}

	if err := database.DeleteAccessToken(ctx, tokenID, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AuthenticateAccessToken(ctx, tokenHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token error = %v, want ErrNotFound", err)
	}
}

func TestExpiredAccessTokenIsRejected(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()
	user, err := database.CreateUser(ctx, "expireduser", "Pass1")
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("gm_expired"))
	expiresAt := time.Now().Add(-time.Minute)
	if _, err := database.CreateAccessToken(ctx, user.ID, "expired", hex.EncodeToString(hash[:]), &expiresAt); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AuthenticateAccessToken(ctx, hex.EncodeToString(hash[:])); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired token error = %v, want ErrNotFound", err)
	}
}

func TestAccessTokenCreationRequiresExpiration(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()
	user, err := database.CreateUser(ctx, "requiredexpiry", "Pass1")
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("gm_no_expiry"))
	if _, err := database.CreateAccessToken(ctx, user.ID, "no expiry", hex.EncodeToString(hash[:]), nil); err == nil {
		t.Fatal("access token without expiration was accepted")
	}
}

func TestAccessTokenScopes(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()
	user, err := database.CreateUser(ctx, "scoped-user", "Pass1234")
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().Add(24 * time.Hour)
	readHash := sha256.Sum256([]byte("gm_read_only"))
	readTokenHash := hex.EncodeToString(readHash[:])
	if _, err := database.CreateAccessTokenWithScope(ctx, user.ID, "read", readTokenHash, models.AccessTokenScopeRepoRead, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AuthenticateAccessTokenWithScope(ctx, readTokenHash, models.AccessTokenScopeRepoRead); err != nil {
		t.Fatalf("read-only token rejected for read: %v", err)
	}
	if _, err := database.AuthenticateAccessTokenWithScope(ctx, readTokenHash, models.AccessTokenScopeRepoWrite); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read-only token write auth error = %v, want ErrNotFound", err)
	}

	writeHash := sha256.Sum256([]byte("gm_read_write"))
	writeTokenHash := hex.EncodeToString(writeHash[:])
	if _, err := database.CreateAccessTokenWithScope(ctx, user.ID, "write", writeTokenHash, models.AccessTokenScopeRepoWrite, &expiresAt); err != nil {
		t.Fatal(err)
	}
	for _, required := range []models.AccessTokenScope{models.AccessTokenScopeRepoRead, models.AccessTokenScopeRepoWrite} {
		if _, err := database.AuthenticateAccessTokenWithScope(ctx, writeTokenHash, required); err != nil {
			t.Fatalf("read/write token rejected for %s: %v", required, err)
		}
	}
	if _, err := database.CreateAccessTokenWithScope(ctx, user.ID, "bad", "hash", models.AccessTokenScope("admin"), &expiresAt); err == nil {
		t.Fatal("invalid token scope was accepted")
	}
}
