package worker

import (
	"context"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

func TestCleanupRecordsFullContainerIDBeforeRemoval(t *testing.T) {
	ctx := t.Context()
	db := pgtest.Open(t)
	svc := ci.NewService(db)
	queueRun(t, db, svc)
	if err := svc.RegisterWorker(ctx, "ended-worker", "host"); err != nil {
		t.Fatal(err)
	}
	claim, err := svc.ClaimNext(ctx, "ended-worker", time.Minute, []string{"alpine:3.20"})
	if err != nil || claim == nil {
		t.Fatalf("claim=%v err=%v", claim, err)
	}
	stepID := claim.Steps[0].ID
	fullID := strings.Repeat("a", 64)
	if _, err := db.Q.Exec(ctx, `UPDATE steps SET status='running',started_at=now(),container_name='step-container',container_id=$2 WHERE id=$1`, stepID, fullID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Finish(ctx, claim.RunID, ci.Outcome{Status: ci.StatusFailed}); err != nil {
		t.Fatal(err)
	}
	fake := newFakeDocker(t)
	t.Setenv("FAKE_DOCKER_PS", fullID+" "+claim.RunID+"\n")
	// The daemon retains an exit receipt after its attached client has gone away.
	if err := os.WriteFile(filepath.Join(filepath.Dir(fake.Binary), "exit-"+fullID), []byte("0"), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, claim.RunID), 0700); err != nil {
		t.Fatal(err)
	}
	containers, workspaces, err := RemoveLeftovers(ctx, fake.docker, root, svc.RunningRunIDs, svc.RecoverStepExit, svc.ConfirmContainersRemoved)
	if err != nil || containers != 1 || workspaces != 1 {
		t.Fatalf("containers=%d workspaces=%d err=%v", containers, workspaces, err)
	}
	var code int
	var removed bool
	if err := db.Q.QueryRow(ctx, `SELECT exit_code,container_removed_at IS NOT NULL FROM steps WHERE id=$1`, stepID).Scan(&code, &removed); err != nil || code != 0 || !removed {
		t.Fatalf("code=%d removed=%v err=%v", code, removed, err)
	}
}
