package worker

import (
	"context"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoveryFencesForeignExecutionAndWaitsForHealthyHeartbeats(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	svc := ci.NewService(db)
	queueRun(t, db, svc)
	root := t.TempDir()
	if err := svc.RegisterWorker(ctx, "old", "host"); err != nil {
		t.Fatal(err)
	}
	if err := svc.BindWorkerEngine(ctx, "old", "fake-engine", root); err != nil {
		t.Fatal(err)
	}
	claim, err := svc.ClaimNext(ctx, "old", time.Hour, []string{"alpine:3.20"})
	if err != nil || claim == nil {
		t.Fatalf("claim=%v err=%v", claim, err)
	}
	if _, err := db.Q.Exec(ctx, `UPDATE workers SET heartbeat_at=now()-interval '5 minutes' WHERE id='old'`); err != nil {
		t.Fatal(err)
	}
	fake := newFakeDocker(t, "alpine:3.20")
	w := New(Config{WorkspaceRoot: root}, db, svc, nil, fake.docker, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := svc.RegisterWorker(ctx, w.id, "host"); err != nil {
		t.Fatal(err)
	}
	if err := svc.BindWorkerEngine(ctx, w.id, "fake-engine", root); err != nil {
		t.Fatal(err)
	}
	w.engineID = "fake-engine"
	w.healthySince.Store(time.Now().UnixNano())
	if w.recoverExecutions(ctx) {
		t.Fatal("recovery ignored database-outage grace period")
	}
	if runStatus(t, db) != "running" {
		t.Fatal("grace period ended the run")
	}
	w.healthySince.Store(time.Now().Add(-ci.WorkerLostAfter - time.Second).UnixNano())
	unlock, locked, err := executionLock(root, claim.RunID)
	if err != nil || !locked {
		t.Fatal("execution lock unavailable", err)
	}
	if w.recoverExecutions(ctx) {
		t.Fatal("recovery touched a live execution")
	}
	unlock()
	ws := filepath.Join(root, claim.RunID)
	if err := os.Mkdir(ws, 0700); err != nil {
		t.Fatal(err)
	}
	if !w.recoverExecutions(ctx) {
		t.Fatal("unlocked lost execution did not recover")
	}
	if runStatus(t, db) != "failed" {
		t.Fatal("interrupted execution was not failed")
	}
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Fatal("confirmed recovery retained workspace", err)
	}
	if len(fake.CallsTo(t, "create")) != 0 {
		t.Fatal("recovery reran a script")
	}
}

func TestWorkersOnOneEngineRequireOneWorkspaceRoot(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	svc := ci.NewService(db)
	for _, id := range []string{"one", "two"} {
		if err := svc.RegisterWorker(ctx, id, "host"); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.BindWorkerEngine(ctx, "one", "engine", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := svc.BindWorkerEngine(ctx, "two", "engine", t.TempDir()); err == nil {
		t.Fatal("same engine accepted incompatible workspace paths")
	}
}
