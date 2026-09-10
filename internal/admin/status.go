package admin

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
	"golang.org/x/sys/unix"
)

type PathStatus struct {
	Path  string
	Ready bool
	Error string
}

type WorkerStatus struct {
	Recent     int
	Healthy    int
	ActiveJobs int
}

type QueueStatus struct {
	Pending         int
	OldestPendingAt *time.Time
}

type SystemStatus struct {
	DatabaseReady bool
	DatabaseError string
	SchemaVersion int
	Repositories  PathStatus
	Artifacts     PathStatus
	Workers       WorkerStatus
	WorkerError   string
	Queue         QueueStatus
	QueueError    string
	Warnings      []string
}

func CollectStatus(ctx context.Context, cfg *config.Config, database *db.DB, now time.Time) SystemStatus {
	status := SystemStatus{
		Repositories: pathStatus(cfg.ReposPath),
		Artifacts:    pathStatus(cfg.ArtifactsPath),
		Warnings:     cfg.ProductionWarnings(),
	}
	if err := database.PingContext(ctx); err != nil {
		status.DatabaseError = err.Error()
	} else {
		status.DatabaseReady = true
		if version, err := database.SchemaVersion(ctx); err != nil {
			status.DatabaseReady = false
			status.DatabaseError = fmt.Sprintf("read schema version: %v", err)
		} else {
			status.SchemaVersion = version
		}
	}

	workers, err := database.ListCIWorkers(ctx)
	if err != nil {
		status.WorkerError = err.Error()
	} else {
		staleBefore := now.Add(-models.CIWorkerStaleAfter)
		for _, worker := range workers {
			if worker.StoppedAt != nil || worker.HeartbeatAt.Before(staleBefore) {
				continue
			}
			status.Workers.Recent++
			status.Workers.ActiveJobs += worker.ActiveJobs
			if worker.Healthy {
				status.Workers.Healthy++
			}
		}
	}
	queue, err := database.GetCIQueueSummary(ctx)
	if err != nil {
		status.QueueError = err.Error()
	} else {
		status.Queue.Pending = queue.Pending
		status.Queue.OldestPendingAt = queue.OldestPendingAt
	}
	return status
}

func (s SystemStatus) CoreReady() bool {
	return s.DatabaseReady && s.Repositories.Ready && s.Artifacts.Ready
}

// CIReady treats CI as optional while the queue is empty. Once work is queued,
// at least one recently healthy worker must be available to service it.
func (s SystemStatus) CIReady() bool {
	if s.WorkerError != "" || s.QueueError != "" {
		return false
	}
	return s.Queue.Pending == 0 || s.Workers.Healthy > 0
}

func pathStatus(path string) PathStatus {
	result := PathStatus{Path: path}
	info, err := os.Stat(path)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if !info.IsDir() {
		result.Error = "not a directory"
		return result
	}
	if err := unix.Access(path, unix.W_OK); err != nil {
		result.Error = "not writable: " + err.Error()
		return result
	}
	result.Ready = true
	return result
}
