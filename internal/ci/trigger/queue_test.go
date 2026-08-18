package trigger

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

func setupTriggerTest(t *testing.T) (*Manager, context.Context, *models.User) {
	t.Helper()
	database, err := db.InitDB(filepath.Join(t.TempDir(), "gitman.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	owner, err := database.CreateUser(ctx, "testuser", "TestPass123")
	if err != nil {
		t.Fatal(err)
	}
	return &Manager{DB: database, ReposPath: t.TempDir()}, ctx, owner
}

func setupCIRefRepo(t *testing.T, reposPath string, owner *models.User, repo *models.Repository) (mainCommit, devCommit, tagCommit string) {
	t.Helper()
	repoPath, err := git.SecureRepoPath(reposPath, owner.Username, repo.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := git.InitBareRepo(context.Background(), repoPath, 512*1024*1024); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", work}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	clone := exec.Command("git", "clone", repoPath, work)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone failed: %v\n%s", err, out)
	}
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(work, ".gitman-ci.yml"), []byte("image: alpine\nsteps:\n- name: main\n  run: echo main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("checkout", "-b", "main")
	runGit("add", ".gitman-ci.yml")
	runGit("commit", "-m", "main")
	mainCommit = runGit("rev-parse", "HEAD")
	runGit("tag", "v1.0.0")
	tagCommit = mainCommit
	runGit("checkout", "-b", "development")
	if err := os.WriteFile(filepath.Join(work, ".gitman-ci.yml"), []byte("image: alpine\nsteps:\n- name: dev\n  run: echo dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".gitman-ci.yml")
	runGit("commit", "-m", "development")
	devCommit = runGit("rev-parse", "HEAD")
	runGit("push", "origin", "main", "development", "v1.0.0")
	return mainCommit, devCommit, tagCommit
}

func setupQueuedPushTest(t *testing.T, repoName string) (*Manager, context.Context, *models.User, *models.Repository, string, string, string) {
	t.Helper()
	manager, ctx, owner := setupTriggerTest(t)
	repoID, err := manager.DB.CreateRepository(ctx, owner.ID, repoName, "", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := manager.DB.GetRepositoryByID(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	first, second, _ := setupCIRefRepo(t, manager.ReposPath, owner, repo)
	repoPath, err := git.SecureRepoPath(manager.ReposPath, owner.Username, repo.Name)
	if err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repoPath}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	tree := runGit("rev-parse", second+"^{tree}")
	third := runGit("-c", "user.name=Gitman Test", "-c", "user.email=test@example.com", "commit-tree", tree, "-p", second, "-m", "third")
	if err := manager.DB.UpsertRepoCIRefRule(ctx, models.RepoCIRefRule{RepoID: repo.ID, RefType: models.CIRefBranch, RefName: "main", AutoRun: true}); err != nil {
		t.Fatal(err)
	}
	return manager, ctx, owner, repo, first, second, third
}

func queuedEventBody(oldCommit, newCommit string) string {
	return oldCommit + "\n" + newCommit + "\nrefs/heads/main\n"
}

func writeQueuedEvent(t *testing.T, queueDir, name, oldCommit, newCommit string) {
	t.Helper()
	if err := os.MkdirAll(queueDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queueDir, name), []byte(queuedEventBody(oldCommit, newCommit)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func queueDirForRepo(t *testing.T, manager *Manager, owner *models.User, repo *models.Repository) string {
	t.Helper()
	repoPath, err := git.SecureRepoPath(manager.ReposPath, owner.Username, repo.Name)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(repoPath, "hooks", QueueDirName)
}

func assertQueuedPushStates(t *testing.T, manager *Manager, repoID string, commits []string) {
	t.Helper()
	runs, err := manager.DB.GetCIRunsByRepo(context.Background(), repoID, len(commits)+10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != len(commits) {
		t.Fatalf("run count = %d, want %d: %+v", len(runs), len(commits), runs)
	}
	// Durable events must be inserted in queue order. Push superseding is a
	// separate CI policy: once a newer push for the same ref is inserted, older
	// still-pending runs become cancelled rather than wasting worker time.
	for i, commit := range commits {
		run := runs[len(runs)-1-i] // DB returns newest first.
		if run.CommitHash != commit {
			t.Fatalf("run %d commit = %s, want %s", i, run.CommitHash, commit)
		}
		wantStatus := models.CIStatusPending
		if i < len(commits)-1 {
			wantStatus = models.CIStatusCancelled
		}
		if run.Status != wantStatus {
			t.Fatalf("run %d status = %s, want %s: %+v", i, run.Status, wantStatus, run)
		}
		if wantStatus == models.CIStatusCancelled {
			if run.StatusReason == "" || run.CancelReason != run.StatusReason {
				t.Fatalf("superseded run %d has inconsistent reason: %+v", i, run)
			}
		} else if run.StatusReason != "" || run.CancelReason != "" {
			t.Fatalf("latest pending run has cancellation reason: %+v", run)
		}
	}
}

func TestDurableQueueReplaysOfflinePushesChronologically(t *testing.T) {
	manager, ctx, owner, repo, first, second, third := setupQueuedPushTest(t, "offline-order")
	queueDir := queueDirForRepo(t, manager, owner, repo)
	hook := filepath.Join(filepath.Dir(queueDir), "post-receive")
	if err := os.WriteFile(hook, []byte(buildHookScript(owner.Username, repo.Name)), 0o700); err != nil {
		t.Fatal(err)
	}

	zero := strings.Repeat("0", 40)
	for _, event := range []string{
		zero + " " + first + " refs/heads/main\n",
		first + " " + second + " refs/heads/main\n",
		second + " " + third + " refs/heads/main\n",
	} {
		cmd := exec.Command(hook)
		cmd.Stdin = strings.NewReader(event)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("offline hook failed: %v\n%s", err, out)
		}
	}

	sameTime := time.Unix(1_700_000_000, 0)
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("event-%020d", i)
		if err := os.Chtimes(filepath.Join(queueDir, name), sameTime, sameTime); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.DrainRepository(ctx, owner, repo); err != nil {
		t.Fatal(err)
	}
	assertQueuedPushStates(t, manager, repo.ID, []string{first, second, third})

	// Reappearance after a database commit but before queue-file removal must
	// resolve to the original trigger key without creating or superseding work.
	writeQueuedEvent(t, queueDir, "event-00000000000000000003", second, third)
	if err := manager.DrainRepository(ctx, owner, repo); err != nil {
		t.Fatal(err)
	}
	assertQueuedPushStates(t, manager, repo.ID, []string{first, second, third})
}

func TestDurableQueueResumesClaimedEventBeforeNewerEvents(t *testing.T) {
	manager, ctx, owner, repo, first, second, _ := setupQueuedPushTest(t, "resume-order")
	queueDir := queueDirForRepo(t, manager, owner, repo)
	writeQueuedEvent(t, queueDir, ".processing-event-00000000000000000001", strings.Repeat("0", 40), first)
	writeQueuedEvent(t, queueDir, "event-00000000000000000002", first, second)

	if err := manager.DrainRepository(ctx, owner, repo); err != nil {
		t.Fatal(err)
	}
	assertQueuedPushStates(t, manager, repo.ID, []string{first, second})
}

func TestDurableQueueRecoversPublicationInterruptedAfterSequenceAssignment(t *testing.T) {
	manager, ctx, owner, repo, first, second, _ := setupQueuedPushTest(t, "publish-order")
	queueDir := queueDirForRepo(t, manager, owner, repo)
	writeQueuedEvent(t, queueDir, ".pending-event-00000000000000000001", strings.Repeat("0", 40), first)
	writeQueuedEvent(t, queueDir, "event-00000000000000000002", first, second)

	if err := manager.DrainRepository(ctx, owner, repo); err != nil {
		t.Fatal(err)
	}
	assertQueuedPushStates(t, manager, repo.ID, []string{first, second})
	sequence, err := readCITriggerSequence(filepath.Join(queueDir, ".sequence"))
	if err != nil || sequence != 2 {
		t.Fatalf("recovered sequence = %d, err=%v; want 2", sequence, err)
	}
}

func TestDurableQueueStopsAtTransientFailureAndResumesInOrder(t *testing.T) {
	manager, ctx, owner, repo, first, second, _ := setupQueuedPushTest(t, "transient-order")
	queueDir := queueDirForRepo(t, manager, owner, repo)
	writeQueuedEvent(t, queueDir, "event-00000000000000000001", strings.Repeat("0", 40), strings.Repeat("a", 40))
	writeQueuedEvent(t, queueDir, "event-00000000000000000002", first, second)

	if err := manager.DrainRepository(ctx, owner, repo); err == nil {
		t.Fatal("expected unresolved oldest event to stop the drain")
	}
	runs, err := manager.DB.GetCIRunsByRepo(ctx, repo.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("newer event overtook transient failure; run count = %d", len(runs))
	}
	writeQueuedEvent(t, queueDir, "event-00000000000000000001", strings.Repeat("0", 40), first)
	if err := manager.DrainRepository(ctx, owner, repo); err != nil {
		t.Fatal(err)
	}
	assertQueuedPushStates(t, manager, repo.ID, []string{first, second})
}

func TestDurableQueueIsolatesMalformedFileWithoutBlockingValidEvent(t *testing.T) {
	manager, ctx, owner, repo, first, _, _ := setupQueuedPushTest(t, "malformed-order")
	queueDir := queueDirForRepo(t, manager, owner, repo)
	if err := os.MkdirAll(queueDir, 0o700); err != nil {
		t.Fatal(err)
	}
	badName := "event-00000000000000000001"
	if err := os.WriteFile(filepath.Join(queueDir, badName), []byte("not an event\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeQueuedEvent(t, queueDir, "event-00000000000000000002", strings.Repeat("0", 40), first)

	if err := manager.DrainRepository(ctx, owner, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(queueDir, ".malformed-"+badName)); err != nil {
		t.Fatalf("malformed event was not quarantined: %v", err)
	}
	assertQueuedPushStates(t, manager, repo.ID, []string{first})
}

func TestQueuedTriggerFileDetectionFailsRepositoryDeletionClosed(t *testing.T) {
	queueDir := t.TempDir()
	for _, name := range []string{
		"event-00000000000000000001",
		".pending-event-00000000000000000001",
		".processing-event-00000000000000000001",
		".event.incomplete",
	} {
		t.Run(name, func(t *testing.T) {
			for _, entry := range []string{
				"event-00000000000000000001",
				".pending-event-00000000000000000001",
				".processing-event-00000000000000000001",
				".event.incomplete",
			} {
				_ = os.Remove(filepath.Join(queueDir, entry))
			}
			if err := os.WriteFile(filepath.Join(queueDir, name), []byte("queued"), 0o600); err != nil {
				t.Fatal(err)
			}
			pending, err := HasQueuedEvents(queueDir)
			if err != nil || !pending {
				t.Fatalf("pending=%t err=%v for %s", pending, err, name)
			}
		})
	}
	if err := os.RemoveAll(queueDir); err != nil {
		t.Fatal(err)
	}
	pending, err := HasQueuedEvents(queueDir)
	if err != nil || pending {
		t.Fatalf("missing queue pending=%t err=%v", pending, err)
	}
}

func TestQueuedAnnotatedTagSurvivesLaterRefDeletion(t *testing.T) {
	manager, ctx, owner := setupTriggerTest(t)
	repoID, err := manager.DB.CreateRepository(ctx, owner.ID, "queued-tag", "", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := manager.DB.GetRepositoryByID(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	mainCommit, _, _ := setupCIRefRepo(t, manager.ReposPath, owner, repo)
	repoPath, err := git.SecureRepoPath(manager.ReposPath, owner.Username, repo.Name)
	if err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repoPath}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("-c", "user.name=Gitman Test", "-c", "user.email=test@example.com", "tag", "-a", "queued-v1", mainCommit, "-m", "queued release")
	tagObject := runGit("rev-parse", "refs/tags/queued-v1")
	if tagObject == mainCommit {
		t.Fatal("expected annotated tag object to differ from its commit")
	}
	runGit("update-ref", "-d", "refs/tags/queued-v1")

	if err := manager.DB.UpsertRepoCIRefRule(ctx, models.RepoCIRefRule{
		RepoID: repo.ID, RefType: "tag", RefName: "queued-v1", AutoRun: true,
	}); err != nil {
		t.Fatal(err)
	}
	queueDir := t.TempDir()
	eventPath := filepath.Join(queueDir, ".processing-event-test")
	event := strings.Repeat("0", 40) + "\n" + tagObject + "\nrefs/tags/queued-v1\n"
	if err := os.WriteFile(eventPath, []byte(event), 0o600); err != nil {
		t.Fatal(err)
	}
	remove, err := manager.processQueued(ctx, owner, repo, "event-test", eventPath)
	if err != nil || !remove {
		t.Fatalf("remove=%v err=%v", remove, err)
	}
	runs, err := manager.DB.GetCIRunsByRepo(ctx, repo.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Tag != "queued-v1" || runs[0].CommitHash != mainCommit {
		t.Fatalf("unexpected queued tag run: %+v", runs)
	}
}

func TestQueuedTriggerRetriesUnresolvedObject(t *testing.T) {
	manager, ctx, owner := setupTriggerTest(t)
	repoID, err := manager.DB.CreateRepository(ctx, owner.ID, "queued-retry", "", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := manager.DB.GetRepositoryByID(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	setupCIRefRepo(t, manager.ReposPath, owner, repo)

	eventPath := filepath.Join(t.TempDir(), ".processing-event-retry")
	event := strings.Repeat("0", 40) + "\n" + strings.Repeat("a", 40) + "\nrefs/heads/main\n"
	if err := os.WriteFile(eventPath, []byte(event), 0o600); err != nil {
		t.Fatal(err)
	}
	remove, err := manager.processQueued(ctx, owner, repo, "event-retry", eventPath)
	if err == nil || remove {
		t.Fatalf("unresolved event should remain queued: remove=%v err=%v", remove, err)
	}
}
