package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
)

func TestResetPasswordRevokesCredentialsAndWritesAuditEvent(t *testing.T) {
	database, err := db.InitDB(filepath.Join(t.TempDir(), "admin.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	user, err := database.CreateUser(ctx, "reset-user", "OldPass123")
	if err != nil {
		t.Fatal(err)
	}
	session, err := database.CreateSession(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("gm_reset_token"))
	expiresAt := time.Now().Add(24 * time.Hour)
	if _, err := database.CreateAccessToken(ctx, user.ID, "reset token", hex.EncodeToString(hash[:]), &expiresAt); err != nil {
		t.Fatal(err)
	}

	const newPassword = "NewPass456"
	if err := ResetPassword(ctx, database, user.Username, newPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetUserBySession(ctx, session); err == nil {
		t.Fatal("password reset left an existing session valid")
	}
	if _, err := database.AuthenticateAccessToken(ctx, hex.EncodeToString(hash[:])); err == nil {
		t.Fatal("password reset left an access token valid")
	}

	events, err := database.ListAuditEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != models.AuditActionPasswordReset || events[0].TargetID != user.ID {
		t.Fatalf("unexpected password reset audit events: %+v", events)
	}
	for key, value := range events[0].Metadata {
		if key == "password" || value == newPassword {
			t.Fatalf("password leaked into audit metadata: %+v", events[0].Metadata)
		}
	}
}

func TestAdminCreateAndDeleteUserWriteAuditEvents(t *testing.T) {
	database, err := db.InitDB(filepath.Join(t.TempDir(), "admin-lifecycle.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	root := t.TempDir()
	cfg := &config.Config{
		ReposPath:     filepath.Join(root, "repos"),
		ArtifactsPath: filepath.Join(root, "artifacts"),
		CacheRoot:     filepath.Join(root, "cache"),
		AuthKeysPath:  filepath.Join(root, "ssh", "authorized_keys"),
		BinaryPath:    "/usr/local/bin/gitman",
	}
	ctx := context.Background()
	if err := CreateUser(ctx, cfg, database, "audit-admin-user", "CreatePass123"); err != nil {
		t.Fatal(err)
	}
	user, err := database.GetUserByUsername(ctx, "audit-admin-user")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteUser(ctx, cfg, database, user.Username); err != nil {
		t.Fatal(err)
	}
	events, err := database.ListAuditEvents(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, event := range events {
		counts[event.Action]++
		if event.Action == models.AuditActionUserCreated || event.Action == models.AuditActionUserDeleted {
			if event.ActorUsername != "admin-cli" || event.TargetID != user.ID {
				t.Fatalf("unexpected admin audit event: %+v", event)
			}
		}
	}
	if counts[models.AuditActionUserCreated] != 1 || counts[models.AuditActionUserDeleted] != 1 {
		t.Fatalf("admin lifecycle audit counts = %+v", counts)
	}
}
