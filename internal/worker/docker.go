package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
)

func (j *job) runDocker(ctx context.Context, cfg *CIConfig, envFile, runnerPath string) error {
	if err := ensureDockerImageAvailable(ctx, cfg.Image); err != nil {
		return err
	}
	cacheDir, releaseCache, err := j.lockCache(ctx)
	if err != nil {
		j.logf("WARN: CI cache is unavailable; running without cache")
		slog.Warn("CI cache unavailable", "run_id", j.run.ID, "repo", j.owner+"/"+j.repo.name, "error", err)
		cacheDir = ""
	} else {
		defer releaseCache()
	}

	containerName := "gitman-ci-" + safeContainerID(j.run.ID) + "-" + safeContainerID(j.run.AttemptID)
	hostCheckout, err := j.dockerHostPath(j.checkout)
	if err != nil {
		return fmt.Errorf("%w: checkout path: %w", errDockerHostPathMisconfigured, err)
	}
	hostArtifacts, err := j.dockerHostPath(j.artifactsStagingDir)
	if err != nil {
		return fmt.Errorf("%w: artifacts path: %w", errDockerHostPathMisconfigured, err)
	}
	hostRunner, err := j.dockerHostPath(runnerPath)
	if err != nil {
		return fmt.Errorf("%w: runner path: %w", errDockerHostPathMisconfigured, err)
	}
	hostCacheDir := ""
	if cacheDir != "" {
		hostCacheDir, err = j.dockerHostPath(cacheDir)
		if err != nil {
			return fmt.Errorf("%w: cache path: %w", errDockerHostPathMisconfigured, err)
		}
	}
	defer func() {
		if err := forceRemoveContainer(containerName); err != nil {
			slog.Warn("failed to remove CI container", "container", containerName, "error", err)
		}
	}()

	args := []string{
		"run", "--rm",
		"--pull", "never",
		"--log-driver", "none",
		"--name", containerName,
		"--label", "gitman.managed=true",
		"--label", "gitman.run_id=" + j.run.ID,
		"--label", "gitman.attempt_id=" + j.run.AttemptID,
		"--network", j.cfg.CINetwork,
		"--pids-limit", "256",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--user", j.cfg.CIContainerUser,
		"--tmpfs", "/tmp:rw,nosuid,nodev,exec,size=256m",
		"-v", fmt.Sprintf("%s:/workspace", hostCheckout),
		"-v", fmt.Sprintf("%s:/gitman/artifacts", hostArtifacts),
		"-v", fmt.Sprintf("%s:/gitman/runner.sh:ro", hostRunner),
		"-w", "/workspace",
		"--env-file", envFile,
	}

	if cacheDir != "" {
		args = append(args, "-v", fmt.Sprintf("%s:/gitman/cache", hostCacheDir))
	}
	args, err = appendDockerSocketArgs(args, j.cfg, cfg.Docker, j.refPolicy.AllowDockerSocket)
	if err != nil {
		return err
	}
	if j.cfg.MemoryLimit != "" {
		args = append(args, "--memory", j.cfg.MemoryLimit)
	}
	if j.cfg.CPULimit != "" {
		args = append(args, "--cpus", j.cfg.CPULimit)
	}
	args = append(args, cfg.Image, "/bin/sh", "/gitman/runner.sh")

	j.logSection("Starting container")
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = j.logWriter
	cmd.Stderr = j.logWriter

	limits := []diskLimit{
		{name: "workspace", path: j.checkout, maxBytes: j.cfg.CIWorkspaceMaxBytes},
		{name: "artifacts", path: j.artifactsStagingDir, maxBytes: j.cfg.CIArtifactMaxBytes},
		{name: "cache", path: cacheDir, maxBytes: j.cfg.CICacheMaxBytes},
	}
	if err := checkDiskLimits(limits); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: start Docker runner container: %v", errDockerUnavailable, err)
	}

	watchCtx, stopWatch := context.WithCancel(ctx)
	violations := make(chan error, 1)
	go watchDiskLimits(watchCtx, containerName, limits, violations)

	err = cmd.Wait()
	stopWatch()
	select {
	case violation := <-violations:
		if err == nil {
			err = violation
		} else {
			err = fmt.Errorf("%v: %w", violation, err)
		}
	default:
	}
	if cacheDir != "" {
		if pruneErr := resetDirectoryIfOverLimit(cacheDir, j.cfg.CICacheMaxBytes); pruneErr != nil {
			j.logf("WARN: CI cache cleanup failed; future runs may continue without cache")
			slog.Warn("failed to prune oversized CI cache", "run_id", j.run.ID, "repo", j.owner+"/"+j.repo.name, "error", pruneErr)
		}
	}
	if ctx.Err() != nil {
		if removeErr := forceRemoveContainer(containerName); removeErr != nil {
			slog.Warn("failed to remove cancelled CI container", "container", containerName, "error", removeErr)
		}
	}

	j.logSection("Container exited")
	if err != nil {
		j.logf("Exit status: FAILED (%v)", err)
	} else {
		j.logf("Exit status: SUCCESS")
	}
	return err
}

func ensureDockerImageAvailable(ctx context.Context, image string) error {
	inspectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(inspectCtx, "docker", "image", "inspect", "--format", "{{.Id}}", image)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if inspectCtx.Err() != nil {
		return inspectCtx.Err()
	}
	// A failed image lookup and an unavailable Docker daemon are different
	// operational states. Ask Docker itself whether the daemon is reachable
	// instead of interpreting its human-readable image-inspect error text.
	probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
	defer probeCancel()
	probe := exec.CommandContext(probeCtx, "docker", "info", "--format", "{{.ServerVersion}}")
	probeOutput, probeErr := probe.CombinedOutput()
	if probeErr != nil {
		if probeCtx.Err() != nil {
			return probeCtx.Err()
		}
		return fmt.Errorf("%w: docker info: %v%s", errDockerUnavailable, probeErr, commandOutputSuffix(probeOutput))
	}
	return fmt.Errorf("%w: %s%s", errDockerImageUnavailable, image, commandOutputSuffix(output))
}

func forceRemoveContainer(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "rm", "-f", name)
	if output, err := cmd.CombinedOutput(); err != nil {
		message := strings.TrimSpace(string(output))
		if strings.Contains(strings.ToLower(message), "no such container") {
			return nil
		}
		return fmt.Errorf("docker rm -f %s: %w (%s)", name, err, message)
	}
	return nil
}

func appendDockerSocketArgs(args []string, cfg *config.Config, enabled bool, refAllowed bool) ([]string, error) {
	if !enabled {
		return args, nil
	}
	if !cfg.CIAllowDockerSocket {
		return nil, fmt.Errorf("%w: pipeline requests docker socket access, but GITMAN_CI_ALLOW_DOCKER_SOCKET is disabled", errDockerSocketWorkerDisabled)
	}
	if !refAllowed {
		return nil, fmt.Errorf("%w: pipeline requests docker socket access, but the CI ref is not trusted for Docker socket access", errDockerSocketRefNotTrusted)
	}
	gid, err := dockerSocketGroupID(cfg.CIDockerSocketPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errDockerSocketUnavailable, err)
	}
	return append(args,
		"-v", fmt.Sprintf("%s:/var/run/docker.sock", cfg.CIDockerSocketPath),
		"--group-add", gid,
		"-e", "DOCKER_HOST=unix:///var/run/docker.sock",
	), nil
}

func dockerSocketGroupID(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect Docker socket %s: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return "", fmt.Errorf("docker socket path %s is not a Unix socket", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("inspect Docker socket ownership %s: unsupported platform", path)
	}
	return strconv.FormatUint(uint64(stat.Gid), 10), nil
}

func (j *job) dockerHostPath(path string) (string, error) {
	return translateDockerHostPath(j.cfg, path)
}

// translateDockerHostPath maps paths inside a Dockerized worker to paths as
// seen by the host Docker daemon. Without configured prefixes, local workers
// use absolute host paths directly.
func translateDockerHostPath(cfg *config.Config, path string) (string, error) {
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	workerPrefix := strings.TrimSpace(cfg.CIWorkerPathPrefix)
	hostPrefix := strings.TrimSpace(cfg.CIHostPathPrefix)
	if workerPrefix == "" && hostPrefix == "" {
		return pathAbs, nil
	}
	if workerPrefix == "" || hostPrefix == "" {
		return "", fmt.Errorf("both GITMAN_CI_WORKER_PATH_PREFIX and GITMAN_CI_HOST_PATH_PREFIX must be set together")
	}
	workerAbs, err := filepath.Abs(workerPrefix)
	if err != nil {
		return "", err
	}
	hostAbs, err := filepath.Abs(hostPrefix)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(workerAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("CI path %s is outside worker path prefix %s", pathAbs, workerAbs)
	}
	return filepath.Join(hostAbs, rel), nil
}

func (j *job) lockCache(ctx context.Context) (string, func(), error) {
	root := filepath.Join(j.cfg.CacheRoot, j.owner, j.repo.name)
	if err := ensurePrivateDir(root); err != nil {
		return "", nil, err
	}
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	lock, err := acquireFileLock(lockCtx, filepath.Join(root, ".lock"))
	if err != nil {
		return "", nil, err
	}
	release := func() {
		if unlockErr := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); unlockErr != nil {
			slog.Warn("failed to unlock CI cache", "path", root, "error", unlockErr)
		}
		if closeErr := lock.Close(); closeErr != nil {
			slog.Warn("failed to close CI cache lock", "path", root, "error", closeErr)
		}
	}
	cacheDir := filepath.Join(root, "current")
	if err := resetDirectoryIfOverLimit(cacheDir, j.cfg.CICacheMaxBytes); err != nil {
		release()
		return "", nil, err
	}
	if err := ensurePrivateDir(cacheDir); err != nil {
		release()
		return "", nil, err
	}
	return cacheDir, release, nil
}

func acquireFileLock(ctx context.Context, path string) (*os.File, error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return nil, errors.Join(err, lock.Close())
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), lock.Close())
		case <-time.After(250 * time.Millisecond):
		}
	}
}
