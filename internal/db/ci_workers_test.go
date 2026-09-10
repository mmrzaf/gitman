package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/models"
)

func TestCIWorkerLifecycle(t *testing.T) {
	database, err := InitDB("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx := context.Background()
	started := time.Now().Add(-time.Minute).Truncate(time.Second)
	worker := models.CIWorker{
		ID:            "worker-1",
		Hostname:      "runner.example",
		PID:           42,
		Concurrency:   2,
		Healthy:       false,
		StatusMessage: "starting",
		StartedAt:     started,
		HeartbeatAt:   started,
	}
	if err := database.RegisterCIWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}
	if err := database.HeartbeatCIWorker(ctx, worker.ID, true, "ready", 1); err != nil {
		t.Fatal(err)
	}

	workers, err := database.ListCIWorkers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 {
		t.Fatalf("workers = %d, want 1", len(workers))
	}
	got := workers[0]
	if got.ID != worker.ID || got.Hostname != worker.Hostname || !got.Healthy || got.StatusMessage != "ready" || got.ActiveJobs != 1 {
		t.Fatalf("unexpected worker: %+v", got)
	}
	if got.StoppedAt != nil {
		t.Fatalf("running worker has stopped_at: %v", got.StoppedAt)
	}

	if err := database.StopCIWorker(ctx, worker.ID); err != nil {
		t.Fatal(err)
	}
	workers, err = database.ListCIWorkers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got = workers[0]
	if got.Healthy || got.StatusMessage != "stopped" || got.ActiveJobs != 0 || got.StoppedAt == nil {
		t.Fatalf("worker stop was not persisted: %+v", got)
	}
}

func TestHeartbeatCIWorkerRejectsUnknownWorker(t *testing.T) {
	database, err := InitDB("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.HeartbeatCIWorker(context.Background(), "missing", true, "ready", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRegisterCIWorkerBoundsRestartHistory(t *testing.T) {
	database, err := InitDB("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	now := time.Now()
	for i := 0; i < 1005; i++ {
		worker := models.CIWorker{
			ID: fmt.Sprintf("worker-%04d", i), Hostname: "runner", PID: i + 1,
			Concurrency: 1, Healthy: false, StatusMessage: "starting",
			StartedAt:   now.Add(time.Duration(i) * time.Second),
			HeartbeatAt: now.Add(time.Duration(i) * time.Second),
		}
		if err := database.RegisterCIWorker(ctx, worker); err != nil {
			t.Fatal(err)
		}
	}
	workers, err := database.ListCIWorkers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1000 {
		t.Fatalf("worker history length = %d, want 1000", len(workers))
	}
	if workers[0].ID != "worker-1004" {
		t.Fatalf("newest worker was not retained: first=%q", workers[0].ID)
	}
}
