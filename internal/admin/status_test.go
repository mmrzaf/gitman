package admin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
)

func TestCollectStatusReportsCoreAndRecentWorkers(t *testing.T) {
	database, err := db.InitDB(filepath.Join(t.TempDir(), "status.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	root := t.TempDir()
	cfg := &config.Config{
		ReposPath:          filepath.Join(root, "repos"),
		ArtifactsPath:      filepath.Join(root, "artifacts"),
		PublicURL:          "https://git.example",
		ForceSecureCookies: true,
		SecretKey:          "configured",
	}
	for _, path := range []string{cfg.ReposPath, cfg.ArtifactsPath} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	owner, err := database.CreateUser(context.Background(), "status-owner", "OwnerPass1")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := database.CreateRepository(context.Background(), owner.ID, "status-repo", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateCIRun(context.Background(), repoID, strings.Repeat("a", 40), "main", "", models.CIEventManual); err != nil {
		t.Fatal(err)
	}
	if err := database.RegisterCIWorker(context.Background(), models.CIWorker{
		ID: "healthy", Hostname: "runner", PID: 1, Concurrency: 2,
		Healthy: true, ActiveJobs: 1, StartedAt: now, HeartbeatAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.RegisterCIWorker(context.Background(), models.CIWorker{
		ID: "stale", Hostname: "old-runner", PID: 2, Concurrency: 1,
		Healthy: true, StartedAt: now.Add(-time.Hour), HeartbeatAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	status := CollectStatus(context.Background(), cfg, database, now)
	if !status.CoreReady() || !status.DatabaseReady {
		t.Fatalf("core status not ready: %+v", status)
	}
	if status.SchemaVersion < 12 {
		t.Fatalf("schema version = %d, want at least 12", status.SchemaVersion)
	}
	if status.Workers.Recent != 1 || status.Workers.Healthy != 1 || status.Workers.ActiveJobs != 1 {
		t.Fatalf("unexpected worker summary: %+v", status.Workers)
	}
	if status.Queue.Pending != 1 || status.Queue.OldestPendingAt == nil || !status.CIReady() {
		t.Fatalf("unexpected CI queue readiness: queue=%+v ready=%v", status.Queue, status.CIReady())
	}
	if len(status.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", status.Warnings)
	}
}

func TestCollectStatusReportsMissingStorage(t *testing.T) {
	database, err := db.InitDB(filepath.Join(t.TempDir(), "status-missing.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cfg := &config.Config{
		ReposPath:          filepath.Join(t.TempDir(), "missing-repos"),
		ArtifactsPath:      filepath.Join(t.TempDir(), "missing-artifacts"),
		PublicURL:          "https://git.example",
		ForceSecureCookies: true,
		SecretKey:          "configured",
	}
	status := CollectStatus(context.Background(), cfg, database, time.Now())
	if status.CoreReady() {
		t.Fatalf("missing storage reported ready: %+v", status)
	}
	if status.Repositories.Ready || status.Artifacts.Ready {
		t.Fatalf("missing storage marked ready: repos=%+v artifacts=%+v", status.Repositories, status.Artifacts)
	}
}

func TestSystemStatusCIReady(t *testing.T) {
	tests := []struct {
		name   string
		status SystemStatus
		want   bool
	}{
		{name: "optional empty queue", status: SystemStatus{}, want: true},
		{name: "queued with healthy worker", status: SystemStatus{Queue: QueueStatus{Pending: 1}, Workers: WorkerStatus{Healthy: 1}}, want: true},
		{name: "queued without worker", status: SystemStatus{Queue: QueueStatus{Pending: 1}}, want: false},
		{name: "worker query error", status: SystemStatus{WorkerError: "db"}, want: false},
		{name: "queue query error", status: SystemStatus{QueueError: "db"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.status.CIReady(); got != tt.want {
				t.Fatalf("CIReady() = %v, want %v", got, tt.want)
			}
		})
	}
}
