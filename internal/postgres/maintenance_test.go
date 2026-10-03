package postgres

import (
	"context"
	"github.com/mmrzaf/gitman/internal/postgres/testschema"
	"testing"
	"time"
)

func TestMaintenanceDrainsAdmissionAndKeepsPause(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	admitted, release, err := db.AdmitMutation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, nested, err := db.AdmitMutation(admitted)
	if err != nil {
		t.Fatal(err)
	}
	nested()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() { close(started); done <- db.SetMaintenance(ctx, true) }()
	<-started
	select {
	case err := <-done:
		t.Fatalf("maintenance did not drain admission: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, release, err := db.AdmitMutation(ctx); err == nil {
		release()
		t.Fatal("paused instance admitted a mutation")
	}
	if err := db.BackupReady(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.SnapshotLease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	go func() { done <- db.SetMaintenance(ctx, false) }()
	select {
	case err := <-done:
		t.Fatalf("resume interrupted a snapshot: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	snapshot()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_, release, err = db.AdmitMutation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestAdmissionDoesNotExhaustDomainPool(t *testing.T) {
	db, err := Connect(t.Context(), testschema.DSN(t), Options{MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	ctx := t.Context()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		_, release, err := db.AdmitMutation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	work, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var one int
	if err := db.Q.QueryRow(work, "SELECT 1").Scan(&one); err != nil {
		t.Fatalf("admitted operations blocked domain query: %v", err)
	}
}
