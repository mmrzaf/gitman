package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
)

func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	p, err := svc.Create(ctx, "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}

	token, expiresAt, err := svc.CreateSession(ctx, p, time.Hour, "", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if token == "" {
		t.Fatal("expected a non-empty token")
	}
	if time.Until(expiresAt) <= 0 {
		t.Fatal("expected expiresAt to be in the future")
	}

	got, err := svc.SessionPerson(ctx, token)
	if err != nil {
		t.Fatalf("SessionPerson: %v", err)
	}
	if got.ID != p.ID {
		t.Error("SessionPerson returned a different person")
	}

	if err := svc.DeleteSession(ctx, token); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := svc.SessionPerson(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("SessionPerson after delete = %v, want ErrInvalidSession", err)
	}
}

func TestDeleteSessionOfUnknownTokenIsNotAnError(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	if err := svc.DeleteSession(ctx, "not-a-real-token"); err != nil {
		t.Fatalf("DeleteSession(unknown token) = %v, want nil", err)
	}
}

func TestSessionPersonUnknownToken(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	if _, err := svc.SessionPerson(ctx, "not-a-real-token"); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("SessionPerson(unknown) = %v, want ErrInvalidSession", err)
	}
}

func TestSessionDetailsAndThrottledLastSeen(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)
	p, err := svc.Create(ctx, "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := svc.CreateSession(ctx, p, time.Hour, "192.0.2.10", "Test Browser")
	if err != nil {
		t.Fatal(err)
	}
	person, sessionID, err := svc.SessionIdentity(ctx, token)
	if err != nil || person.ID != p.ID || sessionID == "" {
		t.Fatalf("SessionIdentity = %+v, %q, %v", person, sessionID, err)
	}
	sessions, err := svc.ListSessions(ctx, p.ID)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("ListSessions = %+v, %v", sessions, err)
	}
	if sessions[0].ID != sessionID || sessions[0].IP != "192.0.2.10" || sessions[0].UserAgent != "Test Browser" {
		t.Fatalf("session details = %+v", sessions[0])
	}

	// A first use may record last seen; an immediate second one must not.
	if _, err := database.Q.Exec(ctx, `UPDATE sessions SET last_seen_at = now() - interval '2 minutes' WHERE id = $1`, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.SessionIdentity(ctx, token); err != nil {
		t.Fatal(err)
	}
	var first time.Time
	if err := database.Q.QueryRow(ctx, `SELECT last_seen_at FROM sessions WHERE id = $1`, sessionID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.SessionIdentity(ctx, token); err != nil {
		t.Fatal(err)
	}
	var second time.Time
	if err := database.Q.QueryRow(ctx, `SELECT last_seen_at FROM sessions WHERE id = $1`, sessionID).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if !second.Equal(first) {
		t.Fatalf("last seen was rewritten within a minute: %v then %v", first, second)
	}
}

func TestRevokeSessionOwnershipAndAdmin(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)
	owner, err := svc.Create(ctx, "owner", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.Create(ctx, "other", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := svc.Create(ctx, "boss", "correct-horse-battery", true, "")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := svc.CreateSession(ctx, owner, time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, sessionID, err := svc.SessionIdentity(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RevokeSession(ctx, sessionID, other.ID); !errors.Is(err, ErrNotSessionOwner) {
		t.Fatalf("non-owner revoked a session: %v", err)
	}
	if _, err := svc.RevokeSession(ctx, sessionID, admin.ID); err != nil {
		t.Fatalf("admin revoke: %v", err)
	}
	if _, err := svc.SessionPerson(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("revoked session still works: %v", err)
	}
	entries, err := activity.NewService(database).Recent(ctx, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Action == activity.SessionRevoked {
			found = true
		}
	}
	if !found {
		t.Fatal("admin session revocation was not recorded")
	}
}

func TestSessionExpires(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	p, err := svc.Create(ctx, "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := svc.CreateSession(ctx, p, -time.Second, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SessionPerson(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("SessionPerson(already expired) = %v, want ErrInvalidSession", err)
	}
}

func TestSessionsAreCappedAndUserAgentIsTruncated(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)
	p, err := svc.Create(ctx, "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	longAgent := strings.Repeat("x", 1000)
	var last string
	for i := 0; i < maxSessionsPerPerson+5; i++ {
		token, _, err := svc.CreateSession(ctx, p, time.Hour, "", longAgent)
		if err != nil {
			t.Fatal(err)
		}
		last = token
	}
	sessions, err := svc.ListSessions(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != maxSessionsPerPerson {
		t.Fatalf("sessions = %d, want %d", len(sessions), maxSessionsPerPerson)
	}
	for _, session := range sessions {
		if len(session.UserAgent) > maxUserAgentBytes {
			t.Fatalf("user agent length = %d, want at most %d", len(session.UserAgent), maxUserAgentBytes)
		}
	}
	if _, err := svc.SessionPerson(ctx, last); err != nil {
		t.Fatalf("the newest session was pruned: %v", err)
	}
}
