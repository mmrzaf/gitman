package worker

import (
	"context"
	"fmt"
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
	scriptName    = ".gitman-ci.sh"
	artifactsDir  = "artifacts"
	runTimeout    = 30 * time.Minute
	shutdownGrace = 60 * time.Second
)

// Run starts the worker pool and blocks until a shutdown signal is received.
func Run(cfg *config.Config, database *db.DB) error {
	slog.Info("starting gitman worker",
		"artifacts", cfg.ArtifactsPath,
		"workers", cfg.WorkerConcurrency,
	)

	if err := prepareDirectories(cfg.ArtifactsPath); err != nil {
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
		slog.Warn("shutdown grace period expired – some jobs may not have completed")
	}

	return nil
}

// worker is the polling loop for a single worker goroutine.
func worker(ctx context.Context, cfg *config.Config, database *db.DB, done <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if err := processNext(ctx, cfg, database); err != nil {
				slog.Error("job processing error", "error", err)
			}
		}
	}
}

// processNext claims a pending run and executes it, returning any booking error.
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
		_ = database.CompleteCIRun(ctx, run.ID, "failed")
		return nil
	}

	j := &job{
		cfg:      cfg,
		database: database,
		run:      run,
		repo:     repo,
		owner:    owner,
	}

	if err := j.execute(ctx); err != nil {
		slog.Error("CI run fatal error", "run_id", run.ID, "error", err)
		_ = database.CompleteCIRun(ctx, run.ID, "failed")
	}
	return nil
}

// job groups all runtime data for a single CI execution.
type job struct {
	cfg      *config.Config
	database *db.DB
	run      *models.CIRun
	repo     *repoInfo
	owner    string

	env []string

	logFile   *os.File
	workspace string
	checkout  string
}

// execute runs the full lifecycle: workspace → clone/checkout → script → artifacts.
// It updates the run status directly in the database (skipped / failed / success).
func (j *job) execute(ctx context.Context) error {
	slog.Info("processing CI run",
		"run_id", j.run.ID,
		"repo", j.repo.name,
		"commit", shortHash(j.run.CommitHash),
		"event", j.run.Event,
	)

	// ---------- 1. Workspace & log file ----------
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

	j.logFile, err = os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create log file: %w", err)
	}
	defer func() {
		if closeErr := j.logFile.Close(); closeErr != nil {
			slog.Warn("failed to close log file", "path", logPath, "error", closeErr)
		}
	}()

	j.logf("=== Gitman CI Run %s ===", j.run.ID)
	j.logf("Repository : %s/%s", j.owner, j.repo.name)
	j.logf("Commit     : %s", j.run.CommitHash)
	j.logf("Branch     : %s", j.run.Branch)
	j.logf("Tag        : %s", j.run.Tag)
	j.logf("Event      : %s", j.run.Event)
	j.logf("Workspace  : %s", j.workspace)
	j.logf("")

	// ---------- 2. Clone & checkout ----------
	j.checkout = filepath.Join(j.workspace, "src")
	if err := j.clone(ctx); err != nil {
		return j.database.CompleteCIRun(ctx, j.run.ID, "failed")
	}

	// ---------- 3. Script presence ----------
	scriptPath := filepath.Join(j.checkout, scriptName)
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		j.logf("No %s found — marking run as Skipped.", scriptName)
		return j.database.CompleteCIRun(ctx, j.run.ID, "skipped")
	}

	if err := os.Chmod(scriptPath, 0o755); err != nil {
		j.logf("WARN: could not chmod script: %v", err)
	}

	// ---------- 4. Execute script ----------
	j.env = j.buildEnv(ctx)
	scriptErr := j.runScript(ctx, scriptPath)

	// ---------- 5. Collect artifacts ----------
	artifactFailed := j.collectArtifacts()

	// ---------- 6. Final status ----------
	finalStatus := "success"
	if scriptErr != nil || artifactFailed {
		finalStatus = "failed"
	}

	j.logf("")
	j.logf("=== Run %s: %s ===", j.run.ID, strings.ToUpper(finalStatus))
	return j.database.CompleteCIRun(ctx, j.run.ID, finalStatus)
}

// clone clones the bare repository and checks out the exact commit.
// It returns nil only if the correct commit is present.
func (j *job) clone(ctx context.Context) error {
	bareRepo := filepath.Join(j.cfg.ReposPath, j.owner, fmt.Sprintf("%s.git", j.repo.name))

	j.logf("--- Cloning repository ---")

	cloneCmd := exec.CommandContext(ctx,
		"git", "clone", "--no-local", "--depth", "1",
		"--branch", selectRef(j.run.Branch, j.run.Tag),
		"file://"+bareRepo, j.checkout,
	)
	cloneCmd.Stdout = j.logFile
	cloneCmd.Stderr = j.logFile

	if err := cloneCmd.Run(); err != nil {
		j.logf("branch/tag clone failed, falling back to full clone")
		cloneFallback := exec.CommandContext(ctx,
			"git", "clone", "--no-local",
			"file://"+bareRepo, j.checkout,
		)
		cloneFallback.Stdout = j.logFile
		cloneFallback.Stderr = j.logFile
		if err2 := cloneFallback.Run(); err2 != nil {
			j.logf("ERROR: git clone failed: %v", err2)
			return fmt.Errorf("clone failed")
		}
	}

	if j.run.CommitHash != "" && len(j.run.CommitHash) >= 7 {
		cmd := exec.CommandContext(ctx, "git", "-C", j.checkout, "checkout", j.run.CommitHash)
		cmd.Stdout = j.logFile
		cmd.Stderr = j.logFile
		if err := cmd.Run(); err != nil {
			j.logf("ERROR: cannot checkout %s: %v – aborting run", j.run.CommitHash, err)
			return fmt.Errorf("checkout failed")
		}
	}
	return nil
}

// runScript executes the CI script and returns its exit error.
func (j *job) runScript(ctx context.Context, scriptPath string) error {
	j.logf("--- Executing %s ---", scriptName)

	runCtx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "/bin/bash", scriptPath)
	cmd.Dir = j.checkout
	cmd.Env = j.env
	cmd.Stdout = j.logFile
	cmd.Stderr = j.logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	err := cmd.Run()

	if runCtx.Err() != nil {
		// Timeout – kill the entire process group.
		if cmd.Process != nil {
			if killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); killErr != nil {
				j.logf("WARN: failed to kill process group: %v", killErr)
			}
		}
		j.logf("--- Script timed out and was killed ---")
		return fmt.Errorf("script timed out")
	}

	j.logf("")
	j.logf("--- Script finished ---")
	if err != nil {
		j.logf("Exit status: FAILED (%v)", err)
	} else {
		j.logf("Exit status: SUCCESS")
	}
	return err
}

// collectArtifacts moves files from the script's artifacts/ directory into
// permanent storage. It returns true if any artifact copy failed.
func (j *job) collectArtifacts() (failed bool) {
	srcDir := filepath.Join(j.checkout, artifactsDir)
	info, err := os.Stat(srcDir)
	if err != nil || !info.IsDir() {
		return false
	}

	dstDir := filepath.Join(j.cfg.ArtifactsPath, "files", j.owner, j.repo.name, j.run.ID)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		j.logf("WARN: failed to create artifact destination: %v", err)
		return true
	}

	entries, _ := os.ReadDir(srcDir)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		src := filepath.Join(srcDir, entry.Name())
		dst := filepath.Join(dstDir, entry.Name())
		if err := moveFile(src, dst); err != nil {
			j.logf("ERROR: failed to save artifact %s: %v", entry.Name(), err)
			failed = true
		} else {
			j.logf("Artifact saved: %s", entry.Name())
		}
	}
	return
}

// logf writes a timestamped line to the run’s log file and to the structured logger.
func (j *job) logf(format string, args ...any) {
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
	if _, err := j.logFile.WriteString(line); err != nil {
		slog.Warn("failed to write CI log", "run_id", j.run.ID, "error", err)
	}
	slog.Info("CI", "run_id", j.run.ID[:8], "msg", fmt.Sprintf(format, args...))
}

// buildEnv constructs the environment variable slice.
func (j *job) buildEnv(ctx context.Context) []string {
	env := []string{
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
		"TERM=xterm-256color",
		fmt.Sprintf("GITMAN_REPO=%s/%s", j.owner, j.repo.name),
		fmt.Sprintf("GITMAN_COMMIT=%s", j.run.CommitHash),
		fmt.Sprintf("GITMAN_BRANCH=%s", j.run.Branch),
		fmt.Sprintf("GITMAN_TAG=%s", j.run.Tag),
		fmt.Sprintf("GITMAN_EVENT=%s", j.run.Event),
		fmt.Sprintf("GITMAN_RUN_ID=%s", j.run.ID),
	}

	if j.cfg.SecretKey != "" {
		secrets, err := j.database.GetRepoSecrets(ctx, j.repo.id)
		if err != nil {
			slog.Warn("failed to fetch repo secrets", "repo", j.repo.id, "error", err)
		} else {
			for _, s := range secrets {
				val, err := db.DecryptSecret(j.cfg.SecretKey, s.EncryptedValue)
				if err != nil {
					slog.Warn("failed to decrypt secret", "key", s.Key, "error", err)
					continue
				}
				env = append(env, fmt.Sprintf("%s=%s", s.Key, val))
			}
		}
	}
	return env
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

// moveFile tries os.Rename first; falls back to copy+delete for cross‑fs moves.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(dst)
	if err != nil {
		_ = in.Close()
		return err
	}
	defer func() { _ = out.Close() }()

	buf := make([]byte, 64*1024)
	for {
		n, readErr := in.Read(buf)
		if n > 0 {
			if _, writeErr := out.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
		}
		if readErr != nil {
			if readErr.Error() == "EOF" {
				break
			}
			return readErr
		}
	}
	return os.Remove(src)
}

// prepareDirectories ensures required artifact directories exist.
func prepareDirectories(artifactsPath string) error {
	dirs := []string{
		artifactsPath,
		filepath.Join(artifactsPath, "logs"),
		filepath.Join(artifactsPath, "files"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", d, err)
		}
	}
	return nil
}
