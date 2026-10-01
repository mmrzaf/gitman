package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
)

func TestCreateAndGet(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	p, err := svc.Create(ctx, "darius", "correct-horse-battery", true, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.ID == "" {
		t.Fatal("expected a generated ID")
	}
	if !p.IsAdmin {
		t.Error("expected IsAdmin to be true")
	}

	byUsername, err := svc.GetByUsername(ctx, "darius")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if byUsername.ID != p.ID {
		t.Error("GetByUsername returned a different person")
	}

	byID, err := svc.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if byID.Username != "darius" {
		t.Errorf("GetByID.Username = %q", byID.Username)
	}
}

func TestCreateDuplicateUsername(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	if _, err := svc.Create(ctx, "darius", "correct-horse-battery", true, ""); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	for _, username := range []string{"darius", "Darius"} {
		_, err := svc.Create(ctx, username, "another-password1", false, "")
		if !errors.Is(err, postgres.ErrAlreadyExists) {
			t.Fatalf("Create(%q) = %v, want postgres.ErrAlreadyExists: usernames differ by more than case", username, err)
		}
	}
}

func TestCreateRejectsInvalidUsername(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	if _, err := svc.Create(ctx, "x", "correct-horse-battery", false, ""); err == nil {
		t.Fatal("expected an error for a too-short username")
	}
}

func TestCreateRejectsInvalidPassword(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	if _, err := svc.Create(ctx, "darius", "short", false, ""); err == nil {
		t.Fatal("expected an error for a too-short password")
	}
}

func TestGetByUsernameNotFound(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	if _, err := svc.GetByUsername(ctx, "nobody"); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("expected postgres.ErrNotFound, got %v", err)
	}
}

func TestList(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	if _, err := svc.Create(ctx, "alice", "correct-horse-battery", true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, "bob", "correct-horse-battery", false, ""); err != nil {
		t.Fatal(err)
	}

	all, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("List returned %d people, want 2", len(all))
	}
	if all[0].Username != "alice" || all[1].Username != "bob" {
		t.Fatalf("List not ordered by username: %v", all)
	}
}

func TestDisableRevokesSessionsAndBlocksLogin(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	p, err := svc.Create(ctx, "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := svc.CreateSession(ctx, p, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := svc.Disable(ctx, p.ID, ""); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	if _, err := svc.SessionPerson(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("SessionPerson after disable = %v, want ErrInvalidSession", err)
	}
	if _, err := svc.VerifyLogin(ctx, "darius", "correct-horse-battery"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("VerifyLogin after disable = %v, want ErrDisabled", err)
	}

	password, err := svc.Enable(ctx, p.ID, "")
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if _, err := svc.VerifyLogin(ctx, "darius", password); err != nil {
		t.Fatalf("VerifyLogin after enable: %v", err)
	}
}

func TestDisableIsIdempotent(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	p, err := svc.Create(ctx, "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Disable(ctx, p.ID, ""); err != nil {
		t.Fatalf("first Disable: %v", err)
	}
	if err := svc.Disable(ctx, p.ID, ""); err != nil {
		t.Fatalf("second Disable: %v", err)
	}
}

func TestSetAdmin(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	p, err := svc.Create(ctx, "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetAdmin(ctx, p.ID, true, ""); err != nil {
		t.Fatalf("SetAdmin: %v", err)
	}
	got, err := svc.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsAdmin {
		t.Error("expected IsAdmin to be true after SetAdmin")
	}
}

func TestResetPasswordRevokesSessions(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	p, err := svc.Create(ctx, "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := svc.CreateSession(ctx, p, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.ResetPassword(ctx, p.ID, "new-correct-password1", ""); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}

	if _, err := svc.SessionPerson(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("SessionPerson after reset = %v, want ErrInvalidSession", err)
	}
	if _, err := svc.VerifyLogin(ctx, "darius", "new-correct-password1"); err != nil {
		t.Fatalf("VerifyLogin with new password: %v", err)
	}
	if _, err := svc.VerifyLogin(ctx, "darius", "correct-horse-battery"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("VerifyLogin with old password = %v, want ErrInvalidCredentials", err)
	}
}

func TestVerifyLoginUnknownUsername(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	if _, err := svc.VerifyLogin(ctx, "nobody", "whatever1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("VerifyLogin(unknown) = %v, want ErrInvalidCredentials", err)
	}
}

func TestVerifyLoginWrongPassword(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	if _, err := svc.Create(ctx, "darius", "correct-horse-battery", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyLogin(ctx, "darius", "wrong-password1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("VerifyLogin(wrong password) = %v, want ErrInvalidCredentials", err)
	}
}

func TestLastAdminIsProtected(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	first, err := svc.Create(ctx, "darius", "correct-horse-battery", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Disable(ctx, first.ID, ""); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("Disable(only admin) = %v, want ErrLastAdmin", err)
	}
	if err := svc.SetAdmin(ctx, first.ID, false, ""); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("SetAdmin(only admin, false) = %v, want ErrLastAdmin", err)
	}

	second, err := svc.Create(ctx, "sara", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetAdmin(ctx, second.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetAdmin(ctx, first.ID, false, ""); err != nil {
		t.Fatalf("demoting one of two admins: %v", err)
	}
	if err := svc.Disable(ctx, second.ID, ""); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("Disable(new only admin) = %v, want ErrLastAdmin", err)
	}
}

func TestChangePassword(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)
	p, err := svc.Create(ctx, "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := svc.CreateSession(ctx, p, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.ChangePassword(ctx, p.ID, "wrong-password", "a-new-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("ChangePassword with a wrong current password = %v, want ErrInvalidCredentials", err)
	}
	if _, err := svc.ChangePassword(ctx, p.ID, "correct-horse-battery", "short"); err == nil {
		t.Fatal("expected a too-short new password to be rejected")
	}
	if _, err := svc.ChangePassword(ctx, p.ID, "correct-horse-battery", "a-new-password"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if _, err := svc.VerifyLogin(ctx, "darius", "a-new-password"); err != nil {
		t.Fatalf("VerifyLogin with the new password: %v", err)
	}
	if _, err := svc.SessionPerson(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("existing session after a password change = %v, want ErrInvalidSession", err)
	}
}

func TestChangesAreRecorded(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)
	admin, err := svc.Create(ctx, "lead", "correct-horse-battery", true, "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.Create(ctx, "darius", "correct-horse-battery", false, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Disable(ctx, p.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Disable(ctx, p.ID, admin.ID); err != nil {
		t.Fatal(err)
	}

	var added, disabled int
	if err := database.Pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE action = 'person.added' AND detail = 'darius' AND person_id = $1),
		       count(*) FILTER (WHERE action = 'person.disabled' AND detail = 'darius')
		FROM events`, admin.ID).Scan(&added, &disabled); err != nil {
		t.Fatal(err)
	}
	if added != 1 || disabled != 1 {
		t.Fatalf("events: added=%d disabled=%d, want 1 and 1 (a repeated disable changes nothing)", added, disabled)
	}
}
