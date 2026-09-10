package db

import (
	"context"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/models"
)

func TestAuditEventsRoundTripAndPreserveDeletedActorName(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()

	user, err := database.CreateUser(ctx, "auditor", "Pass1")
	if err != nil {
		t.Fatal(err)
	}
	event := models.AuditEvent{
		ActorUserID:   user.ID,
		ActorUsername: user.Username,
		Action:        models.AuditActionTokenCreated,
		TargetType:    "access_token",
		TargetID:      "token-1",
		SourceIP:      "192.0.2.10",
		RequestID:     "req-1",
		Metadata:      map[string]string{"name": "laptop"},
	}
	if err := database.RecordAuditEvent(ctx, event); err != nil {
		t.Fatal(err)
	}

	events, err := database.ListAuditEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	got := events[0]
	if got.ActorUserID != user.ID || got.ActorUsername != user.Username || got.Action != event.Action || got.Metadata["name"] != "laptop" {
		t.Fatalf("unexpected audit event: %+v", got)
	}

	if err := database.DeleteUserByID(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	events, err = database.ListAuditEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if events[0].ActorUserID != "" || events[0].ActorUsername != "auditor" {
		t.Fatalf("deleted actor snapshot was not preserved: %+v", events[0])
	}
}

func TestAuditMetadataSizeIsBounded(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	err := database.RecordAuditEvent(context.Background(), models.AuditEvent{
		Action:   models.AuditActionTokenCreated,
		Metadata: map[string]string{"oversized": strings.Repeat("x", 20*1024)},
	})
	if err == nil {
		t.Fatal("oversized audit metadata was accepted")
	}
}

func TestListAuditEventsForUserIncludesActorAndTargetEventsOnly(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()

	alice, err := database.CreateUser(ctx, "audit-alice", "Pass1234")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := database.CreateUser(ctx, "audit-bob", "Pass1234")
	if err != nil {
		t.Fatal(err)
	}
	events := []models.AuditEvent{
		{ActorUserID: alice.ID, ActorUsername: alice.Username, Action: models.AuditActionTokenCreated, TargetType: "access_token", TargetID: "alice-token"},
		{Action: models.AuditActionLoginFailed, TargetType: "user", TargetID: alice.ID, SourceIP: "192.0.2.40"},
		{ActorUsername: "admin-cli", Action: models.AuditActionPasswordReset, TargetType: "user", TargetID: alice.ID},
		{ActorUserID: bob.ID, ActorUsername: bob.Username, Action: models.AuditActionTokenCreated, TargetType: "access_token", TargetID: "bob-token"},
	}
	for _, event := range events {
		if err := database.RecordAuditEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	got, err := database.ListAuditEventsForUser(ctx, alice.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("alice security event count = %d, want 3: %+v", len(got), got)
	}
	for _, event := range got {
		if event.TargetID == "bob-token" || event.ActorUserID == bob.ID {
			t.Fatalf("other user's event leaked into account security history: %+v", event)
		}
	}
	if _, err := database.ListAuditEventsForUser(ctx, "", 10); err == nil {
		t.Fatal("empty user id was accepted")
	}
}
