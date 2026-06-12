package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	cipolicy "github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
)

const (
	pollInterval  = 3 * time.Second
	shutdownGrace = 60 * time.Second
)

var errDiskLimitExceeded = errors.New("disk limit exceeded")

func Run(cfg *config.Config, database *db.DB) error {
	if err := validateWorkerConfig(cfg); err != nil {
		return err
	}
	slog.Info("starting gitman worker",
		"artifacts", cfg.ArtifactsPath,
		"cache", cfg.CacheRoot,
		"workers", cfg.WorkerConcurrency,
		"timeout", cfg.CIJobTimeout,
		"network", cfg.CINetwork,
	)

	if err := prepareDirectories(cfg.ArtifactsPath, cfg.CacheRoot, cfg.CIWorkspaceRoot); err != nil {
		return err
	}
	requeued, err := reconcileAndRequeue(context.Background(), cfg, database)
	if err != nil {
		return fmt.Errorf("reconcile CI state: %w", err)
	}
	if requeued > 0 {
		slog.Warn("requeued stale CI runs", "count", requeued)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go reapStaleRuns(ctx, cfg, database)

	done := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < cfg.WorkerConcurrency; i++ {
		wg.Add(1)
		go worker(ctx, cfg, database, done, &wg)
	}

	slog.Info("worker pool ready")
	<-ctx.Done()
	slog.Info("shutdown signal received, stopping polling", "signal", ctx.Err())

	close(done)

	drainCtx, drainCancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer drainCancel()

	drainDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(drainDone)
	}()

	select {
	case <-drainDone:
		slog.Info("all workers finished gracefully")
	case <-drainCtx.Done():
		slog.Warn("shutdown grace period expired; some jobs may still be stopping")
	}

	return nil
}

func reapStaleRuns(ctx context.Context, cfg *config.Config, database *db.DB) {
	interval := cfg.CILeaseTimeout / 2
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			requeued, err := reconcileAndRequeue(context.Background(), cfg, database)
			if err != nil {
				slog.Warn("failed to reconcile stale CI runs", "error", err)
				continue
			}
			if requeued > 0 {
				slog.Warn("requeued stale CI runs", "count", requeued)
			}
		}
	}
}

func worker(ctx context.Context, cfg *config.Config, database *db.DB, done <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := processNext(ctx, cfg, database); err != nil {
				slog.Error("job processing error", "error", err)
			}
		}
	}
}

func processNext(ctx context.Context, cfg *config.Config, database *db.DB) error {
	run, err := database.ClaimNextPendingRun(ctx)
	if err != nil {
		return fmt.Errorf("claim run: %w", err)
	}
	if run == nil {
		return nil
	}

	jobCtx, cancelJob := context.WithCancel(ctx)
	if cfg.CIJobTimeout > 0 {
		jobCtx, cancelJob = context.WithTimeout(ctx, cfg.CIJobTimeout)
	}
	defer cancelJob()

	heartbeatCtx, stopHeartbeat := context.WithCancel(context.Background())
	defer stopHeartbeat()
	leaseLost := make(chan error, 1)
	go heartbeatRun(heartbeatCtx, database, run.ID, run.AttemptID, cfg.CIHeartbeatInterval, cancelJob, leaseLost)

	repo, owner, err := resolveRepo(jobCtx, database, run.RepoID)
	if err != nil {
		slog.Error("cannot resolve repo for run", "run_id", run.ID, "attempt_id", run.AttemptID, "error", err)
		_ = database.CompleteCIRun(context.Background(), run.ID, run.AttemptID, "failed")
		return nil
	}

	j := &job{
		cfg:      cfg,
		database: database,
		run:      run,
		repo:     repo,
		owner:    owner,
	}

	if err := j.execute(jobCtx); err != nil {
		select {
		case leaseErr := <-leaseLost:
			slog.Error("CI run cancelled after lease loss", "run_id", run.ID, "attempt_id", run.AttemptID, "error", leaseErr)
		default:
			if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
				slog.Error("CI run timed out", "run_id", run.ID, "attempt_id", run.AttemptID, "timeout", cfg.CIJobTimeout)
			} else {
				slog.Error("CI run fatal error", "run_id", run.ID, "attempt_id", run.AttemptID, "error", err)
			}
		}
		_ = database.CompleteCIRun(context.Background(), run.ID, run.AttemptID, "failed")
	}
	return nil
}

func heartbeatRun(ctx context.Context, database *db.DB, runID, attemptID string, interval time.Duration, cancelJob context.CancelFunc, leaseLost chan<- error) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			heartbeatCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := database.HeartbeatCIRun(heartbeatCtx, runID, attemptID)
			cancel()
			if err != nil {
				slog.Warn("failed to heartbeat CI run; cancelling attempt", "run_id", runID, "attempt_id", attemptID, "error", err)
				select {
				case leaseLost <- err:
				default:
				}
				cancelJob()
				return
			}
		}
	}
}

type job struct {
	cfg      *config.Config
	database *db.DB
	run      *models.CIRun
	repo     *repoInfo
	owner    string

	logFile             *os.File
	logWriter           io.Writer
	redactor            *redactingWriter
	secretValues        []string
	workspace           string
	checkout            string
	artifactsStagingDir string
	refPolicy           cipolicy.RefPolicy
}

func (j *job) execute(ctx context.Context) error {
	slog.Info("processing CI run",
		"run_id", j.run.ID,
		"repo", j.repo.name,
		"commit", shortHash(j.run.CommitHash),
		"event", j.run.Event,
	)

	var err error
	j.workspace, err = os.MkdirTemp(j.cfg.CIWorkspaceRoot, fmt.Sprintf("gitman-run-%s-", j.run.ID))
	if err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	defer func() {
		if rmErr := os.RemoveAll(j.workspace); rmErr != nil {
			slog.Warn("failed to remove workspace", "path", j.workspace, "error", rmErr)
		}
	}()
	if err := writeAttemptMetadata(j.workspace, j.run.ID, j.run.AttemptID); err != nil {
		return fmt.Errorf("write workspace attempt metadata: %w", err)
	}

	logDir := filepath.Join(j.cfg.ArtifactsPath, "logs", j.owner, j.repo.name, j.run.ID)
	if err := ensurePrivateDir(logDir); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("%s.log", j.run.AttemptID))
	if err := j.database.UpdateCIRunLogFile(ctx, j.run.ID, j.run.AttemptID, logPath); err != nil {
		return fmt.Errorf("record log file path: %w", err)
	}

	j.logFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create log file: %w", err)
	}
	defer func() {
		if closeErr := j.logFile.Close(); closeErr != nil {
			slog.Warn("failed to close log file", "path", logPath, "error", closeErr)
		}
	}()
	j.logWriter = &limitedWriter{w: j.logFile, max: j.cfg.CILogMaxBytes}

	j.logf("=== Gitman CI Run %s ===", j.run.ID)
	j.logf("Repository : %s/%s", j.owner, j.repo.name)
	j.logf("Commit     : %s", j.run.CommitHash)
	j.logf("Branch     : %s", j.run.Branch)
	j.logf("Tag        : %s", j.run.Tag)
	j.logf("Event      : %s", j.run.Event)
	j.logf("Workspace  : %s", j.workspace)
	j.logf("Timeout    : %s", j.cfg.CIJobTimeout)
	j.logf("")

	j.checkout = filepath.Join(j.workspace, "src")
	if err := j.clone(ctx); err != nil {
		return j.complete(ctx, "failed")
	}

	configPath := filepath.Join(j.checkout, ciConfigFile)
	if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
		j.logf("No %s found; marking run as skipped.", ciConfigFile)
		return j.complete(ctx, "skipped")
	}

	ciCfg, err := parseCIConfig(configPath)
	if err != nil {
		j.logf("ERROR: failed to parse %s: %v", ciConfigFile, err)
		return j.complete(ctx, "failed")
	}

	j.logf("Image  : %s", ciCfg.Image)
	j.logf("Steps  : %d", len(ciCfg.Steps))
	j.logf("")

	policy, err := (cipolicy.Resolver{DB: j.database, ReposPath: j.cfg.ReposPath}).Resolve(
		ctx,
		&models.User{Username: j.owner},
		&models.Repository{ID: j.repo.id, Name: j.repo.name},
		j.run.Branch,
		j.run.Tag,
	)
	if err != nil {
		j.logf("ERROR: CI ref policy is unavailable for this run; failing closed: %v", err)
		return j.complete(ctx, "failed")
	}
	j.refPolicy = policy

	envFile, err := j.resolveEnvFile(ctx, ciCfg)
	if err != nil {
		j.logf("ERROR: %v", err)
		return j.complete(ctx, "failed")
	}
	j.enableSecretMasking()
	defer j.flushSecretMasking()
	defer func() { _ = os.Remove(envFile) }()

	// Keep generated control files outside the repository checkout. A repository
	// may contain symlinks, so writing the runner inside the checkout could
	// overwrite an arbitrary host file before the container starts.
	runnerPath := filepath.Join(j.workspace, ".gitman-runner.sh")
	if err := writeNewFile(runnerPath, []byte(j.generateRunnerScript(ciCfg)), 0o600); err != nil {
		j.logf("ERROR: failed to write runner script: %v", err)
		return j.complete(ctx, "failed")
	}

	j.artifactsStagingDir = filepath.Join(j.workspace, "gitman-artifacts")
	if err := os.MkdirAll(j.artifactsStagingDir, 0o700); err != nil {
		j.logf("ERROR: failed to create artifacts staging dir: %v", err)
		return j.complete(ctx, "failed")
	}

	dockerErr := j.runDocker(ctx, ciCfg, envFile, runnerPath)
	j.collectArtifacts()

	finalStatus := "success"
	if dockerErr != nil {
		finalStatus = "failed"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			j.logf("ERROR: CI job timed out after %s", j.cfg.CIJobTimeout)
		}
	}

	j.logf("")
	j.logf("=== Run %s: %s ===", j.run.ID, strings.ToUpper(finalStatus))
	return j.complete(ctx, finalStatus)
}

func (j *job) complete(ctx context.Context, status string) error {
	if err := j.database.CompleteCIRun(ctx, j.run.ID, j.run.AttemptID, status); err != nil {
		return err
	}
	return nil
}

func (j *job) clone(ctx context.Context) error {
	bareRepo, err := filepath.Abs(filepath.Join(j.cfg.ReposPath, j.owner, fmt.Sprintf("%s.git", j.repo.name)))
	if err != nil {
		return fmt.Errorf("resolve bare repository path: %w", err)
	}
	repoURL := "file://" + bareRepo
	cloneLimits := []diskLimit{{name: "workspace", path: j.workspace, maxBytes: j.cfg.CIWorkspaceMaxBytes}}

	j.logf("--- Cloning repository ---")
	usedFullClone := false
	cloneCmd := exec.CommandContext(ctx,
		"git", "clone", "--no-local", "--depth", "1",
		"--branch", selectRef(j.run.Branch, j.run.Tag),
		repoURL, j.checkout,
	)
	cloneCmd.Stdout = j.logWriter
	cloneCmd.Stderr = j.logWriter
	if err := runCommandWithDiskLimits(ctx, cloneCmd, cloneLimits); err != nil {
		if errors.Is(err, errDiskLimitExceeded) {
			j.logf("ERROR: git clone exceeded workspace limit: %v", err)
			return err
		}
		j.logf("branch/tag clone failed, falling back to full clone")
		if err := j.fullClone(ctx, repoURL, cloneLimits); err != nil {
			return err
		}
		usedFullClone = true
	}

	if err := j.checkoutCommit(ctx); err != nil && !usedFullClone {
		// A valid manual run may target a historical commit reachable from the
		// selected branch. A depth-1 clone cannot check it out, so retry with the
		// complete repository before failing the job.
		j.logf("shallow checkout did not contain requested commit; retrying full clone")
		if err := j.fullClone(ctx, repoURL, cloneLimits); err != nil {
			return err
		}
		usedFullClone = true
	}
	if usedFullClone {
		if err := j.checkoutCommit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (j *job) fullClone(ctx context.Context, repoURL string, limits []diskLimit) error {
	if err := os.RemoveAll(j.checkout); err != nil {
		return fmt.Errorf("reset checkout before full clone: %w", err)
	}
	fallback := exec.CommandContext(ctx, "git", "clone", "--no-local", repoURL, j.checkout)
	fallback.Stdout = j.logWriter
	fallback.Stderr = j.logWriter
	if err := runCommandWithDiskLimits(ctx, fallback, limits); err != nil {
		j.logf("ERROR: git clone failed: %v", err)
		return fmt.Errorf("clone failed: %w", err)
	}
	return nil
}

func (j *job) checkoutCommit(ctx context.Context) error {
	if len(j.run.CommitHash) < 7 {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", j.checkout, "checkout", j.run.CommitHash)
	cmd.Stdout = j.logWriter
	cmd.Stderr = j.logWriter
	if err := cmd.Run(); err != nil {
		j.logf("ERROR: cannot checkout %s: %v", j.run.CommitHash, err)
		return fmt.Errorf("checkout failed: %w", err)
	}
	return nil
}

func (j *job) resolveEnvFile(ctx context.Context, cfg *CIConfig) (string, error) {
	secretsMap := make(map[string]string)
	hasSecrets := false
	for _, e := range cfg.Env {
		if e.Secret != "" {
			hasSecrets = true
			break
		}
	}

	if hasSecrets {
		if !j.refPolicy.AllowSecrets {
			return "", fmt.Errorf("pipeline requests CI secrets, but ref %s %q is not trusted for secrets", j.refPolicy.RefType, j.refPolicy.RefName)
		}
		secrets, err := j.database.GetRepoSecrets(ctx, j.repo.id)
		if err != nil {
			return "", fmt.Errorf("fetch repo secrets: %w", err)
		}
		for _, s := range secrets {
			val, err := db.DecryptSecret(j.cfg.SecretKey, s.EncryptedValue)
			if err != nil {
				slog.Warn("failed to decrypt secret", "key", s.Key, "error", err)
				continue
			}
			secretsMap[s.Key] = val
			if val != "" {
				j.secretValues = append(j.secretValues, val)
			}
		}
	}
	sort.Slice(j.secretValues, func(i, k int) bool { return len(j.secretValues[i]) > len(j.secretValues[k]) })

	baseValues := map[string]string{
		"GITMAN_REPO":   j.owner + "/" + j.repo.name,
		"GITMAN_COMMIT": j.run.CommitHash,
		"GITMAN_BRANCH": j.run.Branch,
		"GITMAN_TAG":    j.run.Tag,
		"GITMAN_EVENT":  j.run.Event,
		"GITMAN_RUN_ID": j.run.ID,
	}
	lines := make([]string, 0, len(baseValues)+len(cfg.Env))
	for _, key := range []string{"GITMAN_REPO", "GITMAN_COMMIT", "GITMAN_BRANCH", "GITMAN_TAG", "GITMAN_EVENT", "GITMAN_RUN_ID"} {
		value := baseValues[key]
		if strings.ContainsAny(value, "\x00\r\n") {
			return "", fmt.Errorf("env value for %q contains unsupported control characters", key)
		}
		lines = append(lines, fmt.Sprintf("%s=%s", key, value))
	}

	var missing []string
	for _, entry := range cfg.Env {
		value := entry.Value
		if entry.Secret != "" {
			val, ok := secretsMap[entry.Secret]
			if !ok {
				missing = append(missing, entry.Secret)
				continue
			}
			value = val
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return "", fmt.Errorf("env value for %q contains unsupported control characters", entry.Key)
		}
		lines = append(lines, fmt.Sprintf("%s=%s", entry.Key, value))
	}

	if len(missing) > 0 {
		return "", fmt.Errorf("missing secret(s): %s", strings.Join(missing, ", "))
	}

	envPath := filepath.Join(j.workspace, "ci.env")
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("write env file: %w", err)
	}

	return envPath, nil
}

func writeNewFile(path string, content []byte, mode os.FileMode) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if _, err := f.Write(content); err != nil {
		return err
	}
	return f.Sync()
}

func (j *job) generateRunnerScript(cfg *CIConfig) string {
	var sb strings.Builder

	sb.WriteString("#!/bin/sh\n")
	sb.WriteString("set -eu\n\n")
	sb.WriteString("mkdir -p /gitman/artifacts /tmp/gitman-home\n")
	sb.WriteString("export HOME=/tmp/gitman-home\n\n")

	for _, step := range cfg.Steps {
		quotedName := shellSingleQuote(step.Name)
		fmt.Fprintf(&sb, "printf '%%s --- Step: %%s ---\\n' \"$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)\" %s\n", quotedName)
		sb.WriteString(step.Run)
		if !strings.HasSuffix(step.Run, "\n") {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "printf '%%s --- Step: %%s: SUCCESS ---\\n' \"$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)\" %s\n\n", quotedName)
	}

	return sb.String()
}

func (j *job) runDocker(ctx context.Context, cfg *CIConfig, envFile, runnerPath string) error {
	cacheDir, releaseCache, err := j.lockCache(ctx)
	if err != nil {
		j.logf("WARN: could not prepare cache: %v; running without cache mount", err)
		cacheDir = ""
	} else {
		defer releaseCache()
	}

	containerName := "gitman-ci-" + safeContainerID(j.run.ID) + "-" + safeContainerID(j.run.AttemptID)
	cidFile := filepath.Join(j.workspace, "container.cid")
	hostCheckout, err := j.dockerHostPath(j.checkout)
	if err != nil {
		return err
	}
	hostArtifacts, err := j.dockerHostPath(j.artifactsStagingDir)
	if err != nil {
		return err
	}
	hostRunner, err := j.dockerHostPath(runnerPath)
	if err != nil {
		return err
	}
	hostCacheDir := ""
	if cacheDir != "" {
		hostCacheDir, err = j.dockerHostPath(cacheDir)
		if err != nil {
			return err
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
		"--cidfile", cidFile,
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

	j.logf("--- Starting container ---")
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
		return err
	}

	watchCtx, stopWatch := context.WithCancel(context.Background())
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
			j.logf("WARN: failed to prune oversized cache: %v", pruneErr)
		}
	}
	if ctx.Err() != nil {
		if removeErr := forceRemoveContainer(containerName); removeErr != nil {
			slog.Warn("failed to remove cancelled CI container", "container", containerName, "error", removeErr)
		}
	}

	j.logf("--- Container exited ---")
	if err != nil {
		j.logf("Exit status: FAILED (%v)", err)
	} else {
		j.logf("Exit status: SUCCESS")
	}
	return err
}

func appendDockerSocketArgs(args []string, cfg *config.Config, enabled bool, refAllowed bool) ([]string, error) {
	if !enabled {
		return args, nil
	}
	if !cfg.CIAllowDockerSocket {
		return nil, fmt.Errorf("pipeline requests docker socket access, but GITMAN_CI_ALLOW_DOCKER_SOCKET is disabled")
	}
	if !refAllowed {
		return nil, fmt.Errorf("pipeline requests docker socket access, but the exact CI ref is not trusted for Docker socket access")
	}
	gid, err := dockerSocketGroupID(cfg.CIDockerSocketPath)
	if err != nil {
		return nil, err
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
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
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
			_ = lock.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = lock.Close()
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (j *job) collectArtifacts() {
	active, err := j.database.IsCIRunAttemptActive(context.Background(), j.run.ID, j.run.AttemptID)
	if err != nil {
		j.logf("WARN: failed to verify artifact publication lease: %v", err)
		return
	}
	if !active {
		j.logf("WARN: skipping artifact publication after lease loss")
		return
	}

	info, err := os.Lstat(j.artifactsStagingDir)
	if err != nil || !info.IsDir() {
		return
	}

	dstDir := filepath.Join(j.cfg.ArtifactsPath, "files", j.owner, j.repo.name, j.run.ID, j.run.AttemptID)
	if err := ensurePrivateDir(dstDir); err != nil {
		j.logf("WARN: failed to create artifact destination: %v", err)
		return
	}

	var totalBytes int64
	var fileCount int

	err = filepath.WalkDir(j.artifactsStagingDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == j.artifactsStagingDir {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			j.logf("WARN: skipping symlink artifact: %s", path)
			return nil
		}
		if entry.IsDir() {
			return nil
		}

		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}

		if j.cfg.CIArtifactMaxFiles > 0 && fileCount >= j.cfg.CIArtifactMaxFiles {
			j.logf("WARN: artifact file limit reached; skipping remaining files")
			return filepath.SkipAll
		}
		if j.cfg.CIArtifactMaxBytes > 0 && totalBytes+info.Size() > j.cfg.CIArtifactMaxBytes {
			j.logf("WARN: artifact byte limit reached; skipping %s", path)
			return nil
		}

		rel, err := filepath.Rel(j.artifactsStagingDir, path)
		if err != nil {
			return nil
		}
		rel = filepath.Clean(rel)
		if rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			j.logf("WARN: skipping unsafe artifact path: %s", rel)
			return nil
		}

		dst := filepath.Join(dstDir, rel)
		if !pathInside(dstDir, dst) {
			j.logf("WARN: skipping artifact outside destination: %s", rel)
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			j.logf("WARN: failed to create artifact subdir for %s: %v", rel, err)
			return nil
		}

		if err := copyRegularFileNoFollow(path, dst, info.Size()); err != nil {
			j.logf("WARN: failed to save artifact %s: %v", rel, err)
		} else {
			fileCount++
			totalBytes += info.Size()
			j.logf("Artifact saved: %s", rel)
		}

		return nil
	})

	if err != nil {
		j.logf("WARN: artifact collection walk error: %v", err)
	}
}

func (j *job) logf(format string, args ...any) {
	msg := j.maskString(fmt.Sprintf(format, args...))
	line := fmt.Sprintf("[%s] %s\n", time.Now().UTC().Format(time.RFC3339), msg)
	if j.logWriter != nil {
		if _, err := j.logWriter.Write([]byte(line)); err != nil {
			slog.Warn("failed to write CI log", "run_id", j.run.ID, "error", err)
		}
	}
	slog.Info("CI", "run_id", shortHash(j.run.ID), "msg", msg)
}

type repoInfo struct {
	id   string
	name string
}

func resolveRepo(ctx context.Context, database *db.DB, repoID string) (*repoInfo, string, error) {
	repo, err := database.GetRepositoryByID(ctx, repoID)
	if err != nil || repo == nil {
		return nil, "", fmt.Errorf("repo %s not found", repoID)
	}
	owner, err := database.GetUserByID(ctx, repo.OwnerID)
	if err != nil || owner == nil {
		return nil, "", fmt.Errorf("owner for repo %s not found", repoID)
	}
	return &repoInfo{id: repo.ID, name: repo.Name}, owner.Username, nil
}

func selectRef(branch, tag string) string {
	if tag != "" {
		return tag
	}
	if branch != "" {
		return branch
	}
	return "HEAD"
}

func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func safeContainerID(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 48 {
		out = out[:48]
	}
	if out == "" {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return out
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

func copyRegularFileNoFollow(src, dst string, expectedSize int64) (err error) {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink artifacts are not allowed")
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("non-regular artifacts are not allowed")
	}
	if expectedSize >= 0 && info.Size() != expectedSize {
		return fmt.Errorf("artifact changed during collection")
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := in.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	openedInfo, err := in.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.Mode().IsRegular() {
		return fmt.Errorf("non-regular artifact opened")
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := out.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func pathInside(base, candidate string) bool {
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(baseAbs, candidateAbs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func (j *job) enableSecretMasking() {
	if len(j.secretValues) == 0 || j.redactor != nil {
		return
	}
	j.redactor = newRedactingWriter(j.logWriter, j.secretValues)
	j.logWriter = j.redactor
}

func (j *job) flushSecretMasking() {
	if j.redactor != nil {
		_ = j.redactor.Flush()
	}
}

func (j *job) maskString(value string) string {
	for _, secret := range j.secretValues {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "***")
		}
	}
	return value
}

type redactingWriter struct {
	mu      sync.Mutex
	w       io.Writer
	secrets [][]byte
	pending []byte
	maxLen  int
}

func newRedactingWriter(w io.Writer, secrets []string) *redactingWriter {
	rw := &redactingWriter{w: w}
	seen := make(map[string]bool)
	for _, secret := range secrets {
		if secret == "" || seen[secret] {
			continue
		}
		seen[secret] = true
		rw.secrets = append(rw.secrets, []byte(secret))
		if len(secret) > rw.maxLen {
			rw.maxLen = len(secret)
		}
	}
	return rw
}

func (rw *redactingWriter) Write(p []byte) (int, error) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if rw.maxLen == 0 {
		_, err := rw.w.Write(p)
		return len(p), err
	}

	data := append(append([]byte(nil), rw.pending...), p...)
	emitEnd := len(data) - (rw.maxLen - 1)
	if emitEnd < 0 {
		emitEnd = 0
	}
	for {
		original := emitEnd
		for _, secret := range rw.secrets {
			searchFrom := emitEnd - len(secret) + 1
			if searchFrom < 0 {
				searchFrom = 0
			}
			for searchFrom < len(data) {
				idx := bytes.Index(data[searchFrom:], secret)
				if idx < 0 {
					break
				}
				idx += searchFrom
				if idx < emitEnd && idx+len(secret) > emitEnd {
					emitEnd = idx
					break
				}
				searchFrom = idx + 1
			}
		}
		if emitEnd == original {
			break
		}
	}

	out := redactBytes(data[:emitEnd], rw.secrets)
	rw.pending = append(rw.pending[:0], data[emitEnd:]...)
	_, err := rw.w.Write(out)
	return len(p), err
}

func (rw *redactingWriter) Flush() error {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if len(rw.pending) == 0 {
		return nil
	}
	_, err := rw.w.Write(redactBytes(rw.pending, rw.secrets))
	rw.pending = nil
	return err
}

func redactBytes(data []byte, secrets [][]byte) []byte {
	out := append([]byte(nil), data...)
	for _, secret := range secrets {
		out = bytes.ReplaceAll(out, secret, []byte("***"))
	}
	return out
}

type diskLimit struct {
	name     string
	path     string
	maxBytes int64
}

func checkDiskLimits(limits []diskLimit) error {
	for _, limit := range limits {
		if limit.path == "" || limit.maxBytes <= 0 {
			continue
		}
		exceeded, err := directoryUsageExceeds(limit.path, limit.maxBytes)
		if err != nil {
			return fmt.Errorf("check %s disk usage: %w", limit.name, err)
		}
		if exceeded {
			return fmt.Errorf("%s: %w (%d bytes)", limit.name, errDiskLimitExceeded, limit.maxBytes)
		}
	}
	return nil
}

func runCommandWithDiskLimits(ctx context.Context, cmd *exec.Cmd, limits []diskLimit) error {
	if err := checkDiskLimits(limits); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan struct{})
	violations := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := checkDiskLimits(limits); err != nil {
					select {
					case violations <- err:
					default:
					}
					if cmd.Process != nil {
						_ = cmd.Process.Kill()
					}
					return
				}
			}
		}
	}()

	err := cmd.Wait()
	close(done)
	select {
	case violation := <-violations:
		return violation
	default:
		return err
	}
}

func watchDiskLimits(ctx context.Context, containerName string, limits []diskLimit, violations chan<- error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	check := func() bool {
		err := checkDiskLimits(limits)
		if err == nil {
			return false
		}
		select {
		case violations <- err:
		default:
		}
		if removeErr := forceRemoveContainer(containerName); removeErr != nil {
			slog.Warn("failed to remove disk-limit CI container", "container", containerName, "error", removeErr)
		}
		return true
	}
	if check() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if check() {
				return
			}
		}
	}
}

func directoryUsageExceeds(root string, maxBytes int64) (bool, error) {
	if maxBytes <= 0 {
		return false, nil
	}
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		total += info.Size()
		if total > maxBytes {
			return filepath.SkipAll
		}
		return nil
	})
	return total > maxBytes, err
}

func resetDirectoryIfOverLimit(path string, maxBytes int64) error {
	if path == "" || maxBytes <= 0 {
		return nil
	}
	exceeded, err := directoryUsageExceeds(path, maxBytes)
	if err != nil {
		return err
	}
	if !exceeded {
		return nil
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return os.MkdirAll(path, 0o700)
}

type limitedWriter struct {
	w       io.Writer
	max     int64
	written int64
	noticed bool
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	if lw.max <= 0 {
		return lw.w.Write(p)
	}
	remaining := lw.max - lw.written
	if remaining <= 0 {
		if !lw.noticed {
			lw.noticed = true
			_, _ = lw.w.Write([]byte("\n[gitman] log limit reached; further output suppressed\n"))
		}
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		_, err := lw.w.Write(p[:remaining])
		lw.written = lw.max
		if !lw.noticed {
			lw.noticed = true
			_, _ = lw.w.Write([]byte("\n[gitman] log limit reached; further output suppressed\n"))
		}
		return len(p), err
	}
	n, err := lw.w.Write(p)
	lw.written += int64(n)
	return n, err
}

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
	userParts := strings.Split(strings.TrimSpace(cfg.CIContainerUser), ":")
	if len(userParts) != 2 {
		return fmt.Errorf("GITMAN_CI_CONTAINER_USER must be a numeric non-root UID:GID")
	}
	uid, uidErr := strconv.Atoi(userParts[0])
	gid, gidErr := strconv.Atoi(userParts[1])
	if uidErr != nil || gidErr != nil || uid <= 0 || gid <= 0 {
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
	return database.RequeueStaleCIRuns(ctx, staleBefore)
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
