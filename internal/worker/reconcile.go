package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
)

func validateWorkerConfig(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("worker config is required")
	}
	if cfg.CILeaseTimeout <= 0 {
		return fmt.Errorf("GITMAN_CI_LEASE_TIMEOUT must be positive")
	}
	if cfg.CIHeartbeatInterval <= 0 {
		return fmt.Errorf("GITMAN_CI_HEARTBEAT_INTERVAL must be positive")
	}
	if cfg.CIHeartbeatInterval*3 > cfg.CILeaseTimeout {
		return fmt.Errorf("GITMAN_CI_HEARTBEAT_INTERVAL must be at most one third of GITMAN_CI_LEASE_TIMEOUT")
	}
	for name, value := range map[string]string{
		"GITMAN_REPOS":             cfg.ReposPath,
		"GITMAN_ARTIFACTS":         cfg.ArtifactsPath,
		"GITMAN_CACHE_ROOT":        cfg.CacheRoot,
		"GITMAN_CI_WORKSPACE_ROOT": cfg.CIWorkspaceRoot,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must not be empty", name)
		}
	}
	uid, gid, err := config.ParseCIContainerUser(cfg.CIContainerUser)
	if err != nil || uid == 0 || gid == 0 {
		return fmt.Errorf("GITMAN_CI_CONTAINER_USER must be a numeric non-root UID:GID")
	}
	workerPrefix := strings.TrimSpace(cfg.CIWorkerPathPrefix)
	hostPrefix := strings.TrimSpace(cfg.CIHostPathPrefix)
	if (workerPrefix == "") != (hostPrefix == "") {
		return fmt.Errorf("GITMAN_CI_WORKER_PATH_PREFIX and GITMAN_CI_HOST_PATH_PREFIX must be set together")
	}
	if workerPrefix != "" && (!filepath.IsAbs(workerPrefix) || !filepath.IsAbs(hostPrefix)) {
		return fmt.Errorf("CI Docker path prefixes must be absolute")
	}
	if cfg.CIAllowDockerSocket {
		if !filepath.IsAbs(cfg.CIDockerSocketPath) {
			return fmt.Errorf("GITMAN_CI_DOCKER_SOCKET_PATH must be absolute")
		}
		if _, err := dockerSocketGroupID(cfg.CIDockerSocketPath); err != nil {
			return err
		}
	}
	if workerPrefix != "" {
		for name, path := range map[string]string{
			"GITMAN_ARTIFACTS":         cfg.ArtifactsPath,
			"GITMAN_CACHE_ROOT":        cfg.CacheRoot,
			"GITMAN_CI_WORKSPACE_ROOT": cfg.CIWorkspaceRoot,
		} {
			if _, err := translateDockerHostPath(cfg, path); err != nil {
				return fmt.Errorf("%s is not Docker-visible: %w", name, err)
			}
		}
	}
	return nil
}

func reconcileAndRequeue(ctx context.Context, cfg *config.Config, database *db.DB) (int64, error) {
	staleBefore := time.Now().Add(-cfg.CILeaseTimeout)
	if err := reconcileManagedContainers(ctx, database, staleBefore); err != nil {
		return 0, err
	}
	cleanupStaleWorkspaces(ctx, cfg.CIWorkspaceRoot, database, staleBefore)
	requeued, err := database.RequeueStaleCIRuns(ctx, staleBefore)
	if err != nil {
		return 0, err
	}
	released, err := database.ReleaseStaleCancelledCIRuns(ctx, staleBefore)
	if err != nil {
		return requeued, err
	}
	if released > 0 {
		slog.Warn("released stale cancelled CI attempts", "count", released)
	}
	return requeued, nil
}

// reconcileManagedContainers removes Docker containers whose attempt lease is
// no longer fresh. Job containers are labeled so this works after SIGKILL or a
// worker restart, before a stale run is made claimable again.
func reconcileManagedContainers(ctx context.Context, database *db.DB, staleBefore time.Time) error {
	dockerCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(dockerCtx, "docker", "ps", "-a",
		"--filter", "label=gitman.managed=true",
		"--format", `{{.ID}}|{{.Label "gitman.run_id"}}|{{.Label "gitman.attempt_id"}}`,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("list managed Docker containers: %w%s", err, commandOutputSuffix(out))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			slog.Warn("removing malformed Gitman CI container", "record", line)
			if err := forceRemoveContainer(parts[0]); err != nil {
				return fmt.Errorf("remove malformed managed container %s: %w", parts[0], err)
			}
			continue
		}
		containerID, runID, attemptID := parts[0], parts[1], parts[2]
		fresh, err := database.IsCIRunAttemptFresh(ctx, runID, attemptID, staleBefore)
		if err != nil {
			return fmt.Errorf("check container lease %s: %w", containerID, err)
		}
		if fresh {
			continue
		}
		slog.Warn("removing stale Gitman CI container", "container", containerID, "run_id", runID, "attempt_id", attemptID)
		if err := forceRemoveContainer(containerID); err != nil {
			return fmt.Errorf("remove stale managed container %s: %w", containerID, err)
		}
	}
	return nil
}

func commandOutputSuffix(output []byte) string {
	message := strings.TrimSpace(string(output))
	if message == "" {
		return ""
	}
	return ": " + message
}

const attemptMetadataFile = ".gitman-attempt"

func writeAttemptMetadata(workspace, runID, attemptID string) error {
	if runID == "" || attemptID == "" || strings.ContainsAny(runID+attemptID, "\r\n") {
		return fmt.Errorf("invalid attempt metadata")
	}
	return os.WriteFile(filepath.Join(workspace, attemptMetadataFile), []byte(runID+"\n"+attemptID+"\n"), 0o600)
}

func cleanupStaleWorkspaces(ctx context.Context, workspaceRoot string, database *db.DB, staleBefore time.Time) {
	paths, err := filepath.Glob(filepath.Join(workspaceRoot, "gitman-run-*"))
	if err != nil {
		slog.Warn("failed to list stale CI workspaces", "error", err)
		return
	}
	for _, workspace := range paths {
		data, err := os.ReadFile(filepath.Join(workspace, attemptMetadataFile))
		if err != nil {
			// A worker can die after MkdirTemp and before writing metadata. Remove
			// only old unmarked directories so an active creator is never raced.
			if workspaceOlderThan(workspace, staleBefore) {
				removeStaleWorkspace(workspace)
			}
			continue
		}
		parts := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			if workspaceOlderThan(workspace, staleBefore) {
				removeStaleWorkspace(workspace)
			}
			continue
		}
		fresh, err := database.IsCIRunAttemptFresh(ctx, parts[0], parts[1], staleBefore)
		if err != nil {
			slog.Warn("failed to inspect stale CI workspace", "workspace", workspace, "error", err)
			continue
		}
		if fresh {
			continue
		}
		removeStaleWorkspace(workspace)
	}
}

func workspaceOlderThan(workspace string, cutoff time.Time) bool {
	info, err := os.Stat(workspace)
	return err == nil && info.ModTime().Before(cutoff)
}

func removeStaleWorkspace(workspace string) {
	if err := os.RemoveAll(workspace); err != nil {
		slog.Warn("failed to remove stale CI workspace", "workspace", workspace, "error", err)
	}
}

func ensurePrivateDir(path string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func prepareDirectories(artifactsPath, cacheRoot, workspaceRoot string) error {
	dirs := []string{
		artifactsPath,
		filepath.Join(artifactsPath, "logs"),
		filepath.Join(artifactsPath, "files"),
		cacheRoot,
		workspaceRoot,
	}
	for _, d := range dirs {
		if err := ensurePrivateDir(d); err != nil {
			return fmt.Errorf("create directory %s: %w", d, err)
		}
	}
	return nil
}
