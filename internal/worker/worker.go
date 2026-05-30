package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
)

const (
	pollInterval  = 3 * time.Second
	shutdownGrace = 60 * time.Second
)

func Run(cfg *config.Config, database *db.DB) error {
	slog.Info("starting gitman worker",
		"artifacts", cfg.ArtifactsPath,
		"cache", cfg.CacheRoot,
		"workers", cfg.WorkerConcurrency,
		"timeout", cfg.CIJobTimeout,
		"network", cfg.CINetwork,
	)

	if err := prepareDirectories(cfg.ArtifactsPath, cfg.CacheRoot); err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

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

	repo, owner, err := resolveRepo(ctx, database, run.RepoID)
	if err != nil {
		slog.Error("cannot resolve repo for run", "run_id", run.ID, "error", err)
		_ = database.CompleteCIRun(context.Background(), run.ID, "failed")
		return nil
	}

	j := &job{
		cfg:      cfg,
		database: database,
		run:      run,
		repo:     repo,
		owner:    owner,
	}

	jobCtx := ctx
	cancel := func() {}
	if cfg.CIJobTimeout > 0 {
		jobCtx, cancel = context.WithTimeout(ctx, cfg.CIJobTimeout)
	}
	defer cancel()

	if err := j.execute(jobCtx); err != nil {
		if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
			slog.Error("CI run timed out", "run_id", run.ID, "timeout", cfg.CIJobTimeout)
		} else {
			slog.Error("CI run fatal error", "run_id", run.ID, "error", err)
		}
		_ = database.CompleteCIRun(context.Background(), run.ID, "failed")
	}
	return nil
}

type job struct {
	cfg      *config.Config
	database *db.DB
	run      *models.CIRun
	repo     *repoInfo
	owner    string

	logFile             *os.File
	logWriter           io.Writer
	workspace           string
	checkout            string
	artifactsStagingDir string
}

func (j *job) execute(ctx context.Context) error {
	slog.Info("processing CI run",
		"run_id", j.run.ID,
		"repo", j.repo.name,
		"commit", shortHash(j.run.CommitHash),
		"event", j.run.Event,
	)

	var err error
	j.workspace, err = os.MkdirTemp("", fmt.Sprintf("gitman-run-%s-", j.run.ID))
	if err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	defer func() {
		if rmErr := os.RemoveAll(j.workspace); rmErr != nil {
			slog.Warn("failed to remove workspace", "path", j.workspace, "error", rmErr)
		}
	}()

	logDir := filepath.Join(j.cfg.ArtifactsPath, "logs", j.owner, j.repo.name)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("%s.log", j.run.ID))
	if err := j.database.UpdateCIRunLogFile(ctx, j.run.ID, logPath); err != nil {
		slog.Warn("failed to record log file path", "run_id", j.run.ID, "error", err)
	}

	j.logFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
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

	envFile, err := j.resolveEnvFile(ctx, ciCfg)
	if err != nil {
		j.logf("ERROR: %v", err)
		return j.complete(ctx, "failed")
	}
	defer func() { _ = os.Remove(envFile) }()

	runnerPath := filepath.Join(j.checkout, ".gitman-runner.sh")
	if err := os.WriteFile(runnerPath, []byte(j.generateRunnerScript(ciCfg)), 0o700); err != nil {
		j.logf("ERROR: failed to write runner script: %v", err)
		return j.complete(ctx, "failed")
	}
	defer func() { _ = os.Remove(runnerPath) }()

	j.artifactsStagingDir = filepath.Join(j.workspace, "gitman-artifacts")
	if err := os.MkdirAll(j.artifactsStagingDir, 0o755); err != nil {
		j.logf("ERROR: failed to create artifacts staging dir: %v", err)
		return j.complete(ctx, "failed")
	}

	dockerErr := j.runDocker(ctx, ciCfg, envFile)
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
	if err := j.database.CompleteCIRun(ctx, j.run.ID, status); err != nil {
		return err
	}
	return nil
}

func (j *job) clone(ctx context.Context) error {
	bareRepo := filepath.Join(j.cfg.ReposPath, j.owner, fmt.Sprintf("%s.git", j.repo.name))

	j.logf("--- Cloning repository ---")

	cloneCmd := exec.CommandContext(ctx,
		"git", "clone", "--no-local", "--depth", "1",
		"--branch", selectRef(j.run.Branch, j.run.Tag),
		bareRepo, j.checkout,
	)
	cloneCmd.Stdout = j.logWriter
	cloneCmd.Stderr = j.logWriter

	if err := cloneCmd.Run(); err != nil {
		j.logf("branch/tag clone failed, falling back to full clone")
		fallback := exec.CommandContext(ctx,
			"git", "clone", "--no-local",
			"file://"+bareRepo, j.checkout,
		)
		fallback.Stdout = j.logWriter
		fallback.Stderr = j.logWriter
		if err2 := fallback.Run(); err2 != nil {
			j.logf("ERROR: git clone failed: %v", err2)
			return fmt.Errorf("clone failed")
		}
	}

	if len(j.run.CommitHash) >= 7 {
		cmd := exec.CommandContext(ctx, "git", "-C", j.checkout, "checkout", j.run.CommitHash)
		cmd.Stdout = j.logWriter
		cmd.Stderr = j.logWriter
		if err := cmd.Run(); err != nil {
			j.logf("ERROR: cannot checkout %s: %v", j.run.CommitHash, err)
			return fmt.Errorf("checkout failed")
		}
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
		}
	}

	lines := []string{
		fmt.Sprintf("GITMAN_REPO=%s/%s", j.owner, j.repo.name),
		fmt.Sprintf("GITMAN_COMMIT=%s", j.run.CommitHash),
		fmt.Sprintf("GITMAN_BRANCH=%s", j.run.Branch),
		fmt.Sprintf("GITMAN_TAG=%s", j.run.Tag),
		fmt.Sprintf("GITMAN_EVENT=%s", j.run.Event),
		fmt.Sprintf("GITMAN_RUN_ID=%s", j.run.ID),
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

func (j *job) runDocker(ctx context.Context, cfg *CIConfig, envFile string) error {
	cacheDir := filepath.Join(j.cfg.CacheRoot, j.owner, j.repo.name)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		j.logf("WARN: could not create cache dir %s: %v; running without cache mount", cacheDir, err)
		cacheDir = ""
	}

	containerName := "gitman-ci-" + safeContainerID(j.run.ID)
	cidFile := filepath.Join(j.workspace, "container.cid")
	defer forceRemoveContainer(containerName)

	args := []string{
		"run", "--rm",
		"--name", containerName,
		"--cidfile", cidFile,
		"--network", j.cfg.CINetwork,
		"--pids-limit", "256",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=256m",
		"-v", fmt.Sprintf("%s:/workspace", j.checkout),
		"-v", fmt.Sprintf("%s:/gitman/artifacts", j.artifactsStagingDir),
		"-w", "/workspace",
		"--env-file", envFile,
	}

	if cacheDir != "" {
		args = append(args, "-v", fmt.Sprintf("%s:/gitman/cache", cacheDir))
	}
	if j.cfg.MemoryLimit != "" {
		args = append(args, "--memory", j.cfg.MemoryLimit)
	}
	if j.cfg.CPULimit != "" {
		args = append(args, "--cpus", j.cfg.CPULimit)
	}

	args = append(args, cfg.Image, "/bin/sh", "/workspace/.gitman-runner.sh")

	j.logf("--- Starting container ---")
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = j.logWriter
	cmd.Stderr = j.logWriter

	err := cmd.Run()

	if ctx.Err() != nil {
		forceRemoveContainer(containerName)
	}

	j.logf("--- Container exited ---")
	if err != nil {
		j.logf("Exit status: FAILED (%v)", err)
	} else {
		j.logf("Exit status: SUCCESS")
	}

	return err
}

func (j *job) collectArtifacts() {
	info, err := os.Lstat(j.artifactsStagingDir)
	if err != nil || !info.IsDir() {
		return
	}

	dstDir := filepath.Join(j.cfg.ArtifactsPath, "files", j.owner, j.repo.name, j.run.ID)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
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
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
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
	msg := fmt.Sprintf(format, args...)
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

func forceRemoveContainer(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "rm", "-f", name)
	_ = cmd.Run()
}

func copyRegularFileNoFollow(src, dst string, expectedSize int64) error {
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
	defer in.Close()

	openedInfo, err := in.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.Mode().IsRegular() {
		return fmt.Errorf("non-regular artifact opened")
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()

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

func prepareDirectories(artifactsPath, cacheRoot string) error {
	dirs := []string{
		artifactsPath,
		filepath.Join(artifactsPath, "logs"),
		filepath.Join(artifactsPath, "files"),
		cacheRoot,
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", d, err)
		}
	}
	return nil
}
