package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	cipolicy "github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/models"
)

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
	j.logf("Timeout    : %s", j.cfg.CIJobTimeout)
	j.logf("")

	j.checkout = filepath.Join(j.workspace, "src")
	if err := j.clone(ctx); err != nil {
		j.logRunnerFailure(ctx, err, nil)
		return j.complete(models.CIStatusFailed, "Repository checkout failed")
	}

	configPath := filepath.Join(j.checkout, ciConfigFile)
	if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
		j.logf("No %s found; marking run as skipped.", ciConfigFile)
		return j.complete(models.CIStatusSkipped, "No .gitman-ci.yml found in this commit")
	}

	ciCfg, err := parseCIConfig(configPath)
	if err != nil {
		j.logf("ERROR: failed to parse %s: %v", ciConfigFile, err)
		return j.complete(models.CIStatusFailed, "Invalid .gitman-ci.yml")
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
		j.logError("Gitman could not resolve CI policy for this run.")
		slog.Error("CI ref policy resolution failed", "run_id", j.run.ID, "attempt_id", j.run.AttemptID, "repo_id", j.repo.id, "error", err)
		return j.complete(models.CIStatusFailed, "CI policy is temporarily unavailable")
	}
	j.refPolicy = policy
	j.logf("Ref policy : %s", j.refPolicySummary())
	j.logf("")

	envFile, err := j.resolveEnvFile(ctx, ciCfg)
	if err != nil {
		j.logRunnerFailure(ctx, err, ciCfg)
		return j.complete(models.CIStatusFailed, ciFailureSummary(ctx, err))
	}
	j.enableSecretMasking()
	defer j.flushSecretMasking()
	defer func() {
		if removeErr := os.Remove(envFile); removeErr != nil && !os.IsNotExist(removeErr) {
			slog.Warn("failed to remove CI environment file", "run_id", j.run.ID, "attempt_id", j.run.AttemptID, "error", removeErr)
		}
	}()

	// Keep generated control files outside the repository checkout. A repository
	// may contain symlinks, so writing the runner inside the checkout could
	// overwrite an arbitrary host file before the container starts.
	runnerPath := filepath.Join(j.workspace, ".gitman-runner.sh")
	if err := writeNewFile(runnerPath, []byte(j.generateRunnerScript(ciCfg)), 0o600); err != nil {
		j.logf("ERROR: failed to write runner script: %v", err)
		return j.complete(models.CIStatusFailed, "Runner setup failed")
	}

	j.artifactsStagingDir = filepath.Join(j.workspace, "gitman-artifacts")
	if err := os.MkdirAll(j.artifactsStagingDir, 0o700); err != nil {
		j.logf("ERROR: failed to create artifacts staging dir: %v", err)
		return j.complete(models.CIStatusFailed, "Artifact staging could not be prepared")
	}

	dockerErr := j.runDocker(ctx, ciCfg, envFile, runnerPath)
	if dockerErr == nil && ctx.Err() != nil {
		dockerErr = ctx.Err()
	}
	artifactErr := j.collectArtifacts(ctx)
	if dockerErr == nil && ctx.Err() != nil {
		dockerErr = ctx.Err()
	}

	finalStatus := models.CIStatusSuccess
	statusReason := ""
	if dockerErr != nil {
		finalStatus = models.CIStatusFailed
		statusReason = ciFailureSummary(ctx, dockerErr)
		j.logRunnerFailure(ctx, dockerErr, ciCfg)
	}
	if artifactErr != nil {
		j.logError("Gitman could not publish CI artifacts; check the server logs.")
		slog.Error("CI artifact publication failed", "run_id", j.run.ID, "attempt_id", j.run.AttemptID, "error", artifactErr)
		if dockerErr == nil {
			finalStatus = models.CIStatusFailed
			statusReason = "Artifact publication failed"
		}
	}

	j.logf("")
	j.logf("=== Run %s: %s ===", j.run.ID, strings.ToUpper(string(finalStatus)))
	return j.complete(finalStatus, statusReason)
}

func (j *job) logSection(name string) {
	j.logf("--- %s ---", name)
}

func (j *job) logError(format string, args ...any) {
	j.logf("ERROR: "+format, args...)
}

func (j *job) refPolicySummary() string {
	if j.refPolicy.Source == cipolicy.PolicySourceDetached {
		return "detached commit (auto_run=false secrets=false docker_socket=false)"
	}
	if j.refPolicy.RefType == "" && j.refPolicy.RefName == "" {
		return "not resolved"
	}
	matched := ""
	if j.refPolicy.RuleRefName != "" && j.refPolicy.RuleRefName != j.refPolicy.RefName {
		matched = fmt.Sprintf(", matched=%q", j.refPolicy.RuleRefName)
	}
	return fmt.Sprintf("%s %q from %s%s (auto_run=%t secrets=%t docker_socket=%t)",
		j.refPolicy.RefType,
		j.refPolicy.RefName,
		j.refPolicy.Source,
		matched,
		j.refPolicy.AutoRun,
		j.refPolicy.AllowSecrets,
		j.refPolicy.AllowDockerSocket,
	)
}

func (j *job) refPolicySubject() string {
	if j.refPolicy.Source == cipolicy.PolicySourceDetached {
		return "detached commit"
	}
	return fmt.Sprintf("%s %q", j.refPolicy.RefType, j.refPolicy.RefName)
}

func (j *job) logRunnerFailure(ctx context.Context, err error, cfg *CIConfig) {
	if err == nil {
		return
	}
	if isOperatorFailure(err) {
		slog.Error("CI runner infrastructure failure", "run_id", j.run.ID, "attempt_id", j.run.AttemptID, "error", err)
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		j.logError("CI job timed out after %s.", j.cfg.CIJobTimeout)
	case errors.Is(err, errDockerSocketWorkerDisabled):
		j.logError("Docker socket access was requested by .gitman-ci.yml, but the worker does not allow Docker socket passthrough.")
		j.logf("Details    : %v", err)
		j.logf("Fix        : set GITMAN_CI_ALLOW_DOCKER_SOCKET=true, mount the Docker socket into the worker, and recreate the worker container.")
	case errors.Is(err, errDockerSocketRefNotTrusted):
		j.logError("Docker socket access was requested by .gitman-ci.yml, but this %s is not trusted for Docker socket access.", j.refPolicySubject())
		j.logf("Ref policy : %s", j.refPolicySummary())
		j.logf("Details    : %v", err)
		j.logf("Fix        : add or update a matching CI ref rule and enable Docker socket access. For release tags, use a rule like: tag v*")
	case errors.Is(err, errDockerImageUnavailable):
		image := "the configured runner image"
		if cfg != nil && cfg.Image != "" {
			image = fmt.Sprintf("runner image %q", cfg.Image)
		}
		j.logError("%s is unavailable or invalid on this worker; Gitman does not pull images during a run.", image)
		j.logf("Fix        : verify the image reference and pre-pull it on the worker host.")
	case errors.Is(err, errDockerUnavailable):
		j.logError("The Gitman Docker runner is temporarily unavailable.")
		j.logf("Fix        : ask the Gitman operator to check the worker and Docker daemon.")
	case errors.Is(err, errDockerSocketUnavailable):
		j.logError("Docker socket access was requested, but the worker Docker socket is unavailable or invalid.")
		j.logf("Fix        : ask the Gitman operator to verify the worker Docker socket configuration.")
	case errors.Is(err, errCISecretStoreUnavailable):
		j.logError("Gitman could not read the repository's configured CI secrets.")
		j.logf("Fix        : check GITMAN_SECRET_KEY and the Gitman server logs.")
	case errors.Is(err, errCISecretsRefNotTrusted):
		j.logError("CI secrets were requested by .gitman-ci.yml, but this %s is not trusted for secrets.", j.refPolicySubject())
		j.logf("Ref policy : %s", j.refPolicySummary())
		j.logf("Details    : %v", err)
		j.logf("Fix        : add or update a matching CI ref rule and enable secrets.")
	case errors.Is(err, errDockerHostPathMisconfigured):
		j.logError("The Gitman worker path mapping is misconfigured.")
		j.logf("Fix        : ask the Gitman operator to verify the worker Docker path mapping.")
	case errors.Is(err, errDiskLimitExceeded):
		j.logError("CI disk limit exceeded.")
		j.logf("Details    : %v", err)
		j.logf("Fix        : reduce workspace/cache/artifact output or increase the CI size limits.")
	default:
		j.logError("CI runner failed before or during execution.")
		j.logf("Details    : %v", err)
	}
}

func isOperatorFailure(err error) bool {
	return errors.Is(err, errDockerUnavailable) ||
		errors.Is(err, errDockerSocketUnavailable) ||
		errors.Is(err, errDockerHostPathMisconfigured) ||
		errors.Is(err, errCISecretStoreUnavailable)
}

func ciFailureSummary(ctx context.Context, err error) string {
	var exitErr *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "Run exceeded its time limit"
	case errors.Is(ctx.Err(), context.Canceled):
		return "Run was interrupted while stopping"
	case errors.Is(err, errDockerSocketWorkerDisabled):
		return "Docker socket access is disabled on the worker"
	case errors.Is(err, errDockerSocketRefNotTrusted):
		return "This revision is not trusted for Docker socket access"
	case errors.Is(err, errDockerImageUnavailable):
		return "Runner image is unavailable or invalid on the worker"
	case errors.Is(err, errDockerUnavailable):
		return "Docker runner is temporarily unavailable"
	case errors.Is(err, errDockerSocketUnavailable):
		return "Docker socket is unavailable"
	case errors.Is(err, errCISecretStoreUnavailable):
		return "Gitman could not read the configured CI secrets"
	case errors.Is(err, errCISecretsRefNotTrusted):
		return "This revision is not trusted to use CI secrets"
	case errors.Is(err, errDockerHostPathMisconfigured):
		return "Worker path mapping is misconfigured"
	case errors.Is(err, errDiskLimitExceeded):
		return "CI storage limit exceeded"
	case errors.As(err, &exitErr):
		return fmt.Sprintf("Pipeline exited with code %d", exitErr.ExitCode())
	default:
		return "Runner execution failed; open the build log for details"
	}
}

func (j *job) complete(status models.CIStatus, reason string) error {
	return completeCIRunReliably(j.database, j.run.ID, j.run.AttemptID, status, reason)
}

func (j *job) clone(ctx context.Context) error {
	bareRepo, err := filepath.Abs(filepath.Join(j.cfg.ReposPath, j.owner, fmt.Sprintf("%s.git", j.repo.name)))
	if err != nil {
		return fmt.Errorf("resolve bare repository path: %w", err)
	}
	repoURL := "file://" + bareRepo
	cloneLimits := []diskLimit{{name: "workspace", path: j.workspace, maxBytes: j.cfg.CIWorkspaceMaxBytes}}

	j.logSection("Cloning repository")
	usedFullClone := false
	cloneCmd := exec.CommandContext(ctx,
		"git", "-c", "advice.detachedHead=false", "clone", "--no-local", "--depth", "1",
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
			if errors.Is(err, errDiskLimitExceeded) {
				return err
			}
			j.logf("full clone could not resolve the repository default ref; fetching the exact run commit")
			if exactErr := j.exactCommitClone(ctx, repoURL, cloneLimits); exactErr != nil {
				return errors.Join(err, exactErr)
			}
			return nil
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
			j.logf("full clone did not advertise the exact run commit; fetching it directly")
			if fetchErr := j.fetchExactCommit(ctx, repoURL, cloneLimits); fetchErr != nil {
				return errors.Join(err, fetchErr)
			}
			if err := j.checkoutCommit(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func (j *job) fullClone(ctx context.Context, repoURL string, limits []diskLimit) error {
	if err := os.RemoveAll(j.checkout); err != nil {
		return fmt.Errorf("reset checkout before full clone: %w", err)
	}
	fallback := exec.CommandContext(ctx, "git", "-c", "advice.detachedHead=false", "clone", "--no-local", repoURL, j.checkout)
	fallback.Stdout = j.logWriter
	fallback.Stderr = j.logWriter
	if err := runCommandWithDiskLimits(ctx, fallback, limits); err != nil {
		return fmt.Errorf("clone failed: %w", err)
	}
	return nil
}

func (j *job) fetchExactCommit(ctx context.Context, repoURL string, limits []diskLimit) error {
	cmd := exec.CommandContext(ctx, "git", "-C", j.checkout, "fetch", "--no-tags", repoURL, j.run.CommitHash)
	cmd.Stdout = j.logWriter
	cmd.Stderr = j.logWriter
	if err := runCommandWithDiskLimits(ctx, cmd, limits); err != nil {
		return fmt.Errorf("fetch exact run commit: %w", err)
	}
	return nil
}

func (j *job) exactCommitClone(ctx context.Context, repoURL string, limits []diskLimit) error {
	if err := os.RemoveAll(j.checkout); err != nil {
		return fmt.Errorf("reset checkout before exact commit fetch: %w", err)
	}
	initCmd := exec.CommandContext(ctx, "git", "init", j.checkout)
	initCmd.Stdout = j.logWriter
	initCmd.Stderr = j.logWriter
	if err := runCommandWithDiskLimits(ctx, initCmd, limits); err != nil {
		return fmt.Errorf("initialize exact commit checkout: %w", err)
	}
	remoteCmd := exec.CommandContext(ctx, "git", "-C", j.checkout, "remote", "add", "origin", repoURL)
	remoteCmd.Stdout = j.logWriter
	remoteCmd.Stderr = j.logWriter
	if err := remoteCmd.Run(); err != nil {
		return fmt.Errorf("configure exact commit checkout: %w", err)
	}
	if err := j.fetchExactCommit(ctx, repoURL, limits); err != nil {
		return err
	}
	return j.checkoutCommit(ctx)
}

func (j *job) checkoutCommit(ctx context.Context) error {
	if len(j.run.CommitHash) < 7 {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", j.checkout, "-c", "advice.detachedHead=false", "checkout", "--detach", j.run.CommitHash)
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
			return "", fmt.Errorf("%w: pipeline requests CI secrets, but %s is not trusted for secrets", errCISecretsRefNotTrusted, j.refPolicySubject())
		}
		secrets, err := j.database.GetRepoSecrets(ctx, j.repo.id)
		if err != nil {
			return "", fmt.Errorf("%w: fetch repository secrets: %w", errCISecretStoreUnavailable, err)
		}
		for _, s := range secrets {
			val, err := db.DecryptSecret(j.cfg.SecretKey, s.EncryptedValue)
			if err != nil {
				slog.Error("failed to decrypt CI secret", "run_id", j.run.ID, "repo_id", j.repo.id, "secret", s.Key, "error", err)
				return "", fmt.Errorf("%w: decrypt secret %q: %w", errCISecretStoreUnavailable, s.Key, err)
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
		"GITMAN_EVENT":  string(j.run.Event),
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
	sb.WriteString("export HOME=/tmp/gitman-home\n")
	sb.WriteString("gitman_current_step=\"\"\n")
	sb.WriteString("gitman_on_exit() {\n")
	sb.WriteString("  status=$?\n")
	sb.WriteString("  if [ \"$status\" -ne 0 ] && [ -n \"$gitman_current_step\" ]; then\n")
	sb.WriteString("    printf '%s --- Step: %s: FAILED (exit %s) ---\\n' \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\" \"$gitman_current_step\" \"$status\"\n")
	sb.WriteString("  fi\n")
	sb.WriteString("  exit \"$status\"\n")
	sb.WriteString("}\n")
	sb.WriteString("trap gitman_on_exit EXIT\n\n")

	for _, step := range cfg.Steps {
		quotedName := shellSingleQuote(step.Name)
		fmt.Fprintf(&sb, "gitman_current_step=%s\n", quotedName)
		fmt.Fprintf(&sb, "printf '%%s --- Step: %%s ---\\n' \"$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)\" %s\n", quotedName)
		sb.WriteString(step.Run)
		if !strings.HasSuffix(step.Run, "\n") {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "printf '%%s --- Step: %%s: SUCCESS ---\\n' \"$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)\" %s\n", quotedName)
		sb.WriteString("gitman_current_step=\"\"\n\n")
	}

	return sb.String()
}
