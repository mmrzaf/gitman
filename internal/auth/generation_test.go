package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
)

func TestResetRejectsPreviouslyVerifiedLogin(t *testing.T) {
	ctx := t.Context()
	svc := NewService(pgtest.Open(t))
	p, err := svc.Create(ctx, "member", "old-password-value", false, "")
	if err != nil {
		t.Fatal(err)
	}
	verified, err := svc.VerifyLogin(ctx, p.Username, "old-password-value")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetPassword(ctx, p.ID, "new-password-value", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateSession(ctx, verified, time.Hour); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("stale login created a session: %v", err)
	}
}

func TestResetRejectsPasswordChangeSessionSnapshot(t *testing.T) {
	ctx := t.Context()
	svc := NewService(pgtest.Open(t))
	p, err := svc.Create(ctx, "member", "old-password-value", false, "")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := svc.ChangePassword(ctx, p.ID, "old-password-value", "changed-password-value")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetPassword(ctx, p.ID, "reset-password-value", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateSession(ctx, changed, time.Hour); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("stale changed password created a session: %v", err)
	}
}

func TestBootstrapExpiresAndCannotCreateTokens(t *testing.T) {
	ctx := t.Context()
	db := pgtest.Open(t)
	svc := NewService(db)
	p, err := svc.CreateBootstrap(ctx, "member", "temporary-password", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateToken(ctx, p.ID, "laptop", ScopeRead, nil, tokenTestRepos(t, db)); err == nil {
		t.Fatal("bootstrap created a token")
	}
	if _, err := db.Q.Exec(ctx, `UPDATE people SET bootstrap_expires_at = now() - interval '1 second' WHERE id = $1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyLogin(ctx, p.Username, "temporary-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expired bootstrap login: %v", err)
	}
	if _, _, err := svc.CreateSession(ctx, p, time.Hour); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expired bootstrap session: %v", err)
	}
}

func TestTokenScopeAndResetRevocation(t *testing.T) {
	ctx := t.Context()
	db := pgtest.Open(t)
	svc := NewService(db)
	p, err := svc.Create(ctx, "member", "old-password-value", false, "")
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := svc.CreateToken(ctx, p.ID, "laptop", ScopeRead, nil, tokenTestRepos(t, db))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Authenticate(ctx, plain, "another-repo"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("token escaped repository scope: %v", err)
	}
	if err := svc.ResetPassword(ctx, p.ID, "reset-password-value", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Authenticate(ctx, plain, "token-repo"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("reset left token valid: %v", err)
	}
}

func TestRevokeAllEndsSessionsTokensAndVerifiedSnapshots(t *testing.T) {
	ctx := t.Context()
	db := pgtest.Open(t)
	svc := NewService(db)
	p, err := svc.Create(ctx, "member", "password-value-long", false, "")
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := svc.CreateSession(ctx, p, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := svc.CreateToken(ctx, p.ID, "laptop", ScopeWrite, nil, tokenTestRepos(t, db))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeAll(ctx, p.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SessionPerson(ctx, session); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("session survived: %v", err)
	}
	if _, _, err := svc.Authenticate(ctx, plain, "token-repo"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("token survived: %v", err)
	}
	if _, _, err := svc.CreateSession(ctx, p, time.Hour); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("verified snapshot survived: %v", err)
	}
	if _, err := svc.VerifyLogin(ctx, p.Username, "password-value-long"); err != nil {
		t.Fatalf("password was changed: %v", err)
	}
}
