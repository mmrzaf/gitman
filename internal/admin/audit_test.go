package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
)

func TestWriteAuditEventsHumanAndJSON(t *testing.T) {
	database, err := db.InitDB(filepath.Join(t.TempDir(), "audit-output.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	event := models.AuditEvent{
		ActorUsername: "alice",
		Action:        models.AuditActionCISecretUpserted,
		TargetType:    "repository",
		TargetID:      "repo-1",
		SourceIP:      "192.0.2.10",
		Metadata:      map[string]string{"key": "DEPLOY_TOKEN"},
	}
	if err := database.RecordAuditEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}

	var human bytes.Buffer
	if err := WriteAuditEvents(context.Background(), database, &human, 10, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{models.AuditActionCISecretUpserted, "actor=alice", "target=repository:repo-1", "DEPLOY_TOKEN"} {
		if !strings.Contains(human.String(), want) {
			t.Fatalf("human audit output missing %q: %s", want, human.String())
		}
	}

	var jsonOut bytes.Buffer
	if err := WriteAuditEvents(context.Background(), database, &jsonOut, 10, true); err != nil {
		t.Fatal(err)
	}
	var decoded models.AuditEvent
	if err := json.Unmarshal(bytes.TrimSpace(jsonOut.Bytes()), &decoded); err != nil {
		t.Fatalf("decode JSON audit output: %v\n%s", err, jsonOut.String())
	}
	if decoded.Action != event.Action || decoded.Metadata["key"] != "DEPLOY_TOKEN" {
		t.Fatalf("unexpected JSON audit event: %+v", decoded)
	}
}

func TestWriteAuditEventsHumanEscapesControlCharacters(t *testing.T) {
	database, err := db.InitDB(filepath.Join(t.TempDir(), "audit-escape.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.RecordAuditEvent(context.Background(), models.AuditEvent{
		ActorUsername: "alice\ninjected=1",
		Action:        models.AuditActionLoginSucceeded,
	}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := WriteAuditEvents(context.Background(), database, &out, 10, false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Count(text, "\n") != 1 {
		t.Fatalf("audit event injected extra output lines: %q", text)
	}
	if !strings.Contains(text, `actor="alice\ninjected=1"`) {
		t.Fatalf("control characters were not escaped: %q", text)
	}
}
