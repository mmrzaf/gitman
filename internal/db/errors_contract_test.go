package db

import (
	"context"
	"errors"
	"testing"

	"github.com/mmrzaf/gitman/internal/models"
)

func TestSingleObjectLookupsUseErrNotFound(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()

	checks := []struct {
		name string
		fn   func() error
	}{
		{"user by username", func() error { _, err := database.GetUserByUsername(ctx, "missing"); return err }},
		{"user by id", func() error { _, err := database.GetUserByID(ctx, "missing"); return err }},
		{"session", func() error { _, err := database.GetUserBySession(ctx, "missing"); return err }},
		{"token", func() error { _, err := database.GetUserByTokenHash(ctx, "missing"); return err }},
		{"repository by id", func() error { _, err := database.GetRepositoryByID(ctx, "missing"); return err }},
		{"repository by owner/name", func() error { _, err := database.GetRepositoryByOwnerAndName(ctx, "missing", "repo"); return err }},
		{"repository access", func() error { _, err := database.GetRepoAccessLevel(ctx, "missing", "missing"); return err }},
		{"repository by webhook secret", func() error { _, err := database.GetRepositoryByWebhookSecret(ctx, "missing"); return err }},
		{"webhook secret", func() error { _, err := database.GetWebhookSecret(ctx, "missing"); return err }},
		{"ssh key", func() error { _, err := database.GetSSHKeyByID(ctx, "missing"); return err }},
		{"ci run", func() error { _, err := database.GetCIRunByID(ctx, "missing"); return err }},
		{"latest ci run", func() error { _, err := database.GetLatestCIRunForCommit(ctx, "missing", "deadbeef"); return err }},
		{"ci ref rule", func() error {
			_, err := database.GetRepoCIRefRule(ctx, "missing", models.CIRefBranch, "main")
			return err
		}},
	}

	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.fn(); !errors.Is(err, ErrNotFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestOwnedDeletesUseErrNotFound(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()

	checks := []struct {
		name string
		fn   func() error
	}{
		{"ssh key", func() error { return database.DeleteSSHKey(ctx, "missing", "missing") }},
		{"access token", func() error { return database.DeleteAccessToken(ctx, "missing", "missing") }},
		{"collaborator", func() error { return database.RemoveCollaborator(ctx, "missing", "missing") }},
		{"ci secret", func() error { return database.DeleteRepoSecret(ctx, "missing", "missing") }},
		{"ci ref rule", func() error {
			return database.DeleteRepoCIRefRule(ctx, "missing", models.CIRefBranch, "main")
		}},
		{"webhook secret update", func() error { return database.SetWebhookSecret(ctx, "missing", "secret") }},
	}

	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.fn(); !errors.Is(err, ErrNotFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
		})
	}
}
