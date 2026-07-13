package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	cipolicy "github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

const (
	ciHookQueueDirName  = "gitman-ci-queue"
	ciQueuePollInterval = 2 * time.Second
	ciQueueBatchPerRepo = 100
)

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
	entries, err := os.ReadDir(queueDir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		slog.Warn("failed to read CI trigger queue", "repo", repo.ID, "error", err)
		return
	}

	names := make([]string, 0, len(entries))
	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".processing-event-") {
			info, infoErr := entry.Info()
			if infoErr == nil && now.Sub(info.ModTime()) > time.Minute {
				recovered := strings.TrimPrefix(name, ".processing-")
				_ = os.Rename(filepath.Join(queueDir, name), filepath.Join(queueDir, recovered))
				name = recovered
			}
		}
		if strings.HasPrefix(name, "event-") {
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
			slog.Warn("failed to process queued CI trigger", "repo", repo.ID, "event", name, "error", err)
			_ = os.Rename(claimed, source)
			continue
		}
		if remove {
			if err := os.Remove(claimed); err != nil && !os.IsNotExist(err) {
				slog.Warn("failed to remove delivered CI trigger", "repo", repo.ID, "event", name, "error", err)
			}
		}
	}
}

func (app *App) processQueuedCITrigger(ctx context.Context, owner *models.User, repo *models.Repository, eventName, eventPath string) (bool, error) {
	event, err := readQueuedCITrigger(eventPath)
	if err != nil {
		slog.Error("discarding malformed CI trigger event", "repo", repo.ID, "event", eventName, "error", err)
		return true, nil
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
		return true, nil
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
	triggerKey := repo.ID + ":hook:" + eventName
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
