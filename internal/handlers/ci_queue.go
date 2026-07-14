package handlers

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	cipolicy "github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
	"golang.org/x/sys/unix"
)

const (
	ciHookQueueDirName  = "gitman-ci-queue"
	ciQueuePollInterval = 2 * time.Second
	ciQueueBatchPerRepo = 100
	ciQueueDrainLock    = ".drain.lock"
)

var errMalformedQueuedCITrigger = errors.New("malformed queued CI trigger")

type queuedCITrigger struct {
	oldCommit string
	newCommit string
	ref       string
}

// RunCITriggerQueue drains durable post-receive events stored beside each
// managed repository hook. Delivery remains safe across web restarts because
// each event has a stable database idempotency key.
func (app *App) RunCITriggerQueue(ctx context.Context) {
	app.drainCITriggerQueues(ctx)
	ticker := time.NewTicker(ciQueuePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			app.drainCITriggerQueues(ctx)
		}
	}
}

func (app *App) drainCITriggerQueues(ctx context.Context) {
	repos, err := app.DB.GetAllRepositories(ctx)
	if err != nil {
		slog.Warn("failed to list repositories for CI trigger queue", "error", err)
		return
	}
	for i := range repos {
		if ctx.Err() != nil {
			return
		}
		owner, err := app.DB.GetUserByID(ctx, repos[i].OwnerID)
		if err != nil || owner == nil {
			slog.Warn("failed to resolve CI queue repository owner", "repo", repos[i].ID, "error", err)
			continue
		}
		app.drainRepoCITriggerQueue(ctx, owner, &repos[i])
	}
}

func (app *App) drainRepoCITriggerQueue(ctx context.Context, owner *models.User, repo *models.Repository) {
	repoPath, err := git.SecureRepoPath(app.Config.ReposPath, owner.Username, repo.Name)
	if err != nil {
		return
	}
	queueDir := filepath.Join(repoPath, "hooks", ciHookQueueDirName)
	unlock, acquired, err := tryLockCITriggerQueue(queueDir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		slog.Warn("failed to lock CI trigger queue", "repo", repo.ID, "error", err)
		return
	}
	if !acquired {
		return
	}
	defer unlock()

	sequenceUnlock, sequenceAcquired, err := tryLockCITriggerQueueFile(filepath.Join(queueDir, ".sequence.lock"))
	if err != nil {
		slog.Warn("failed to lock CI trigger sequence", "repo", repo.ID, "error", err)
		return
	}
	if !sequenceAcquired {
		return
	}

	entries, err := os.ReadDir(queueDir)
	if os.IsNotExist(err) {
		sequenceUnlock()
		return
	}
	if err != nil {
		sequenceUnlock()
		slog.Warn("failed to read CI trigger queue", "repo", repo.ID, "error", err)
		return
	}
	if err := recoverPendingCITriggers(queueDir, entries); err != nil {
		sequenceUnlock()
		slog.Warn("failed to recover pending CI trigger publication", "repo", repo.ID, "error", err)
		return
	}
	sequenceUnlock()
	entries, err = os.ReadDir(queueDir)
	if err != nil {
		slog.Warn("failed to reread CI trigger queue", "repo", repo.ID, "error", err)
		return
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".processing-event-") {
			recovered := strings.TrimPrefix(name, ".processing-")
			source := filepath.Join(queueDir, name)
			destination := filepath.Join(queueDir, recovered)
			if _, statErr := os.Lstat(destination); statErr == nil {
				conflict := filepath.Join(queueDir, ".conflict-"+name)
				if recoverErr := os.Rename(source, conflict); recoverErr != nil {
					slog.Warn("failed to isolate conflicting claimed CI trigger", "repo", repo.ID, "event", name, "error", recoverErr)
				}
				continue
			} else if !os.IsNotExist(statErr) {
				slog.Warn("failed to inspect claimed CI trigger destination", "repo", repo.ID, "event", name, "error", statErr)
				continue
			}
			if recoverErr := os.Rename(source, destination); recoverErr != nil {
				slog.Warn("failed to recover claimed CI trigger", "repo", repo.ID, "event", name, "error", recoverErr)
				continue
			}
			name = recovered
		}
		if strings.HasPrefix(name, "event-") {
			if _, ok := orderedCITriggerSequence(name); !ok {
				source := filepath.Join(queueDir, name)
				quarantined := filepath.Join(queueDir, ".malformed-"+name)
				if quarantineErr := os.Rename(source, quarantined); quarantineErr != nil {
					slog.Error("failed to isolate CI trigger with invalid filename", "repo", repo.ID, "event", name, "error", quarantineErr)
				} else {
					slog.Error("isolated CI trigger with invalid filename", "repo", repo.ID, "event", name)
				}
				continue
			}
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > ciQueueBatchPerRepo {
		names = names[:ciQueueBatchPerRepo]
	}

	for _, name := range names {
		if ctx.Err() != nil {
			return
		}
		source := filepath.Join(queueDir, name)
		claimed := filepath.Join(queueDir, ".processing-"+name)
		if err := os.Rename(source, claimed); err != nil {
			continue
		}
		remove, err := app.processQueuedCITrigger(ctx, owner, repo, name, claimed)
		if err != nil {
			if errors.Is(err, errMalformedQueuedCITrigger) {
				quarantined := filepath.Join(queueDir, ".malformed-"+name)
				if quarantineErr := os.Rename(claimed, quarantined); quarantineErr != nil {
					slog.Error("failed to isolate malformed CI trigger", "repo", repo.ID, "event", name, "error", quarantineErr)
				} else {
					slog.Error("isolated malformed CI trigger", "repo", repo.ID, "event", name, "error", err)
				}
				continue
			}
			slog.Warn("failed to process queued CI trigger", "repo", repo.ID, "event", name, "error", err)
			if restoreErr := os.Rename(claimed, source); restoreErr != nil {
				slog.Warn("failed to restore queued CI trigger", "repo", repo.ID, "event", name, "error", restoreErr)
			}
			// Later events must not overtake an older event that could not be
			// committed. Retry the exact same prefix on the next drain.
			return
		}
		if remove {
			if err := os.Remove(claimed); err != nil && !os.IsNotExist(err) {
				slog.Warn("failed to remove delivered CI trigger", "repo", repo.ID, "event", name, "error", err)
			}
		}
	}
}

func recoverPendingCITriggers(queueDir string, entries []os.DirEntry) error {
	sequencePath := filepath.Join(queueDir, ".sequence")
	sequence, sequenceErr := readCITriggerSequence(sequencePath)
	maxSequence := sequence
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if value, ok := orderedCITriggerSequence(entry.Name()); ok && value > maxSequence {
			maxSequence = value
		}
	}
	if sequenceErr != nil || maxSequence > sequence {
		if err := writeCITriggerSequence(queueDir, maxSequence); err != nil {
			return err
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, ".pending-event-") {
			continue
		}
		recovered := strings.TrimPrefix(name, ".pending-")
		source := filepath.Join(queueDir, name)
		destination := filepath.Join(queueDir, recovered)
		if _, err := os.Lstat(destination); err == nil {
			conflict := filepath.Join(queueDir, ".conflict-"+recovered)
			if renameErr := os.Rename(source, conflict); renameErr != nil {
				return renameErr
			}
			slog.Error("isolated conflicting pending CI trigger", "event", name)
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(source, destination); err != nil {
			return err
		}
	}
	return nil
}

func readCITriggerSequence(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid CI trigger sequence: %w", err)
	}
	return value, nil
}

func writeCITriggerSequence(queueDir string, sequence uint64) (err error) {
	tmp, err := os.CreateTemp(queueDir, ".sequence.recovery-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := fmt.Fprintf(tmp, "%d\n", sequence); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, filepath.Join(queueDir, ".sequence")); err != nil {
		return err
	}
	dir, err := os.Open(queueDir)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := dir.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	return dir.Sync()
}

func orderedCITriggerSequence(name string) (uint64, bool) {
	for _, prefix := range []string{".pending-", ".processing-"} {
		name = strings.TrimPrefix(name, prefix)
	}
	const eventPrefix = "event-"
	if !strings.HasPrefix(name, eventPrefix) || len(name) != len(eventPrefix)+20 {
		return 0, false
	}
	value, err := strconv.ParseUint(strings.TrimPrefix(name, eventPrefix), 10, 64)
	return value, err == nil
}

func hasQueuedCITriggerFiles(queueDir string) (bool, error) {
	entries, err := os.ReadDir(queueDir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "event-") ||
			strings.HasPrefix(name, ".pending-event-") ||
			strings.HasPrefix(name, ".processing-event-") ||
			strings.HasPrefix(name, ".event.") {
			return true, nil
		}
	}
	return false, nil
}

// tryLockCITriggerQueue serializes drains for one repository across multiple
// web processes. Advisory locks are released by the kernel on process exit,
// which lets a restarted web process immediately recover a claimed event
// without waiting for a stale-file timeout.
func tryLockCITriggerQueue(queueDir string) (unlock func(), acquired bool, err error) {
	return tryLockCITriggerQueueFile(filepath.Join(queueDir, ciQueueDrainLock))
}

func tryLockCITriggerQueueFile(path string) (unlock func(), acquired bool, err error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() {
		if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
			slog.Warn("failed to unlock CI trigger queue", "error", err)
		}
		if err := lock.Close(); err != nil {
			slog.Warn("failed to close CI trigger queue lock", "error", err)
		}
	}, true, nil
}

func (app *App) processQueuedCITrigger(ctx context.Context, owner *models.User, repo *models.Repository, eventName, eventPath string) (bool, error) {
	event, err := readQueuedCITrigger(eventPath)
	if err != nil {
		return false, fmt.Errorf("%w: %v", errMalformedQueuedCITrigger, err)
	}
	if isZeroGitObjectID(event.newCommit) {
		return true, nil
	}

	req := triggerRequest{CommitHash: event.newCommit, Event: "push"}
	switch {
	case strings.HasPrefix(event.ref, "refs/heads/"):
		req.Branch = strings.TrimPrefix(event.ref, "refs/heads/")
	case strings.HasPrefix(event.ref, "refs/tags/"):
		req.Tag = strings.TrimPrefix(event.ref, "refs/tags/")
	default:
		return false, fmt.Errorf("%w: unsupported ref namespace", errMalformedQueuedCITrigger)
	}
	normalized, err := normalizeQueuedCITrigger(ctx, app.Config.ReposPath, owner, repo, req)
	if err != nil {
		return false, fmt.Errorf("normalize queued push: %w", err)
	}
	policy, err := (cipolicy.Resolver{DB: app.DB, ReposPath: app.Config.ReposPath}).Resolve(
		ctx, owner, repo, normalized.Branch, normalized.Tag,
	)
	if err != nil {
		return false, fmt.Errorf("resolve ref policy: %w", err)
	}
	if !policy.AutoRun {
		return true, nil
	}
	eventDigest := sha256.Sum256([]byte(event.oldCommit + "\n" + event.newCommit + "\n" + event.ref + "\n"))
	triggerKey := fmt.Sprintf("%s:hook:%s:%x", repo.ID, eventName, eventDigest)
	runID, err := app.DB.CreatePushCIRunWithTriggerKey(
		ctx, repo.ID, normalized.CommitHash, normalized.Branch, normalized.Tag, triggerKey,
	)
	if err != nil {
		return false, err
	}
	slog.Info("queued CI trigger delivered", "repo", repo.ID, "event", eventName, "run_id", runID)
	return true, nil
}

// normalizeQueuedCITrigger validates a locally queued post-receive event
// without requiring the ref to still point at the same object. A later push or
// deletion must not erase an earlier durable event before it reaches the
// database. Resolving the object through ^{commit} also handles annotated tags.
func normalizeQueuedCITrigger(ctx context.Context, reposPath string, owner *models.User, repo *models.Repository, req triggerRequest) (triggerRequest, error) {
	req.CommitHash = strings.TrimSpace(req.CommitHash)
	req.Branch = strings.TrimSpace(req.Branch)
	req.Tag = strings.TrimSpace(req.Tag)
	req.Event = "push"
	if req.Branch == "" && req.Tag == "" {
		return req, fmt.Errorf("queued push requires a branch or tag")
	}
	if req.Branch != "" && req.Tag != "" {
		return req, fmt.Errorf("queued push cannot target both branch and tag")
	}
	refName := req.Branch
	if refName == "" {
		refName = req.Tag
	}
	if err := git.ValidateRefName(refName); err != nil {
		return req, fmt.Errorf("invalid queued ref: %w", err)
	}
	repoPath, err := git.SecureRepoPath(reposPath, owner.Username, repo.Name)
	if err != nil {
		return req, fmt.Errorf("resolve repository path: %w", err)
	}
	commitHash, err := git.ResolveCommitHash(ctx, repoPath, req.CommitHash)
	if err != nil {
		return req, fmt.Errorf("resolve queued commit: %w", err)
	}
	req.CommitHash = commitHash
	return req, nil
}

func readQueuedCITrigger(path string) (queuedCITrigger, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return queuedCITrigger{}, err
	}
	if len(data) > 1024 {
		return queuedCITrigger{}, fmt.Errorf("event exceeds size limit")
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		return queuedCITrigger{}, fmt.Errorf("expected three event fields")
	}
	event := queuedCITrigger{
		oldCommit: strings.TrimSpace(lines[0]),
		newCommit: strings.TrimSpace(lines[1]),
		ref:       strings.TrimSpace(lines[2]),
	}
	if !validGitObjectID(event.oldCommit) || !validGitObjectID(event.newCommit) {
		return queuedCITrigger{}, fmt.Errorf("invalid object id")
	}
	if event.ref == "" || strings.ContainsAny(event.ref, "\x00\r\n") {
		return queuedCITrigger{}, fmt.Errorf("invalid ref")
	}
	return event, nil
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func isZeroGitObjectID(value string) bool {
	return strings.Trim(value, "0") == ""
}
