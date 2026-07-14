package handlers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

func setupQueuedPushTest(t *testing.T, repoName string) (*App, context.Context, *models.User, *models.Repository, string, string, string) {
	t.Helper()
	app := setupTestApp(t)
	ctx := context.Background()
	owner, err := app.DB.GetUserByUsername(ctx, "testuser")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := app.DB.CreateRepository(ctx, owner.ID, repoName, "", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := app.DB.GetRepositoryByID(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	first, second, _ := setupCIRefRepo(t, app.Config.ReposPath, owner, repo)
	repoPath, err := git.SecureRepoPath(app.Config.ReposPath, owner.Username, repo.Name)
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
	if err := app.DB.UpsertRepoCIRefRule(ctx, models.RepoCIRefRule{
		RepoID: repo.ID, RefType: "branch", RefName: "main", AutoRun: true,
	}); err != nil {
		t.Fatal(err)
	}
	return app, ctx, owner, repo, first, second, third
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

func queueDirForRepo(t *testing.T, app *App, owner *models.User, repo *models.Repository) string {
	t.Helper()
	repoPath, err := git.SecureRepoPath(app.Config.ReposPath, owner.Username, repo.Name)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(repoPath, "hooks", ciHookQueueDirName)
}

func assertQueuedPushStates(t *testing.T, app *App, repoID string, commits []string) {
	t.Helper()
	rows, err := app.DB.QueryContext(context.Background(), `
		SELECT commit_hash, status, status_reason FROM ci_runs
		WHERE repo_id = ? ORDER BY rowid ASC
	`, repoID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type state struct{ commit, status, reason string }
	var states []state
	for rows.Next() {
		var got state
		if err := rows.Scan(&got.commit, &got.status, &got.reason); err != nil {
			t.Fatal(err)
		}
		states = append(states, got)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(states) != len(commits) {
		t.Fatalf("run count = %d, want %d: %+v", len(states), len(commits), states)
	}
	for i, commit := range commits {
		if states[i].commit != commit {
			t.Fatalf("run %d commit = %s, want %s; replay order: %+v", i, states[i].commit, commit, states)
		}
		wantStatus := "cancelled"
		if i == len(commits)-1 {
			wantStatus = "pending"
		}
		if states[i].status != wantStatus {
			t.Fatalf("run %d status = %s, want %s: %+v", i, states[i].status, wantStatus, states)
		}
		if i < len(commits)-1 && !strings.Contains(states[i].reason, commits[i+1][:12]) {
			t.Fatalf("run %d reason %q does not name its chronological successor %s", i, states[i].reason, commits[i+1])
		}
	}
}

func TestDurableQueueReplaysOfflinePushesChronologically(t *testing.T) {
	app, ctx, owner, repo, first, second, third := setupQueuedPushTest(t, "offline-order")
	queueDir := queueDirForRepo(t, app, owner, repo)
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
	app.drainRepoCITriggerQueue(ctx, owner, repo)
	assertQueuedPushStates(t, app, repo.ID, []string{first, second, third})

	// Reappearance after a database commit but before queue-file removal must
	// resolve to the original trigger key without creating or superseding work.
	writeQueuedEvent(t, queueDir, "event-00000000000000000003", second, third)
	app.drainRepoCITriggerQueue(ctx, owner, repo)
	assertQueuedPushStates(t, app, repo.ID, []string{first, second, third})
}

func TestDurableQueueResumesClaimedEventBeforeNewerEvents(t *testing.T) {
	app, ctx, owner, repo, first, second, _ := setupQueuedPushTest(t, "resume-order")
	queueDir := queueDirForRepo(t, app, owner, repo)
	writeQueuedEvent(t, queueDir, ".processing-event-00000000000000000001", strings.Repeat("0", 40), first)
	writeQueuedEvent(t, queueDir, "event-00000000000000000002", first, second)

	app.drainRepoCITriggerQueue(ctx, owner, repo)
	assertQueuedPushStates(t, app, repo.ID, []string{first, second})
}

func TestDurableQueueRecoversPublicationInterruptedAfterSequenceAssignment(t *testing.T) {
	app, ctx, owner, repo, first, second, _ := setupQueuedPushTest(t, "publish-order")
	queueDir := queueDirForRepo(t, app, owner, repo)
	writeQueuedEvent(t, queueDir, ".pending-event-00000000000000000001", strings.Repeat("0", 40), first)
	writeQueuedEvent(t, queueDir, "event-00000000000000000002", first, second)

	app.drainRepoCITriggerQueue(ctx, owner, repo)
	assertQueuedPushStates(t, app, repo.ID, []string{first, second})
	sequence, err := readCITriggerSequence(filepath.Join(queueDir, ".sequence"))
	if err != nil || sequence != 2 {
		t.Fatalf("recovered sequence = %d, err=%v; want 2", sequence, err)
	}
}

func TestDurableQueueStopsAtTransientFailureAndResumesInOrder(t *testing.T) {
	app, ctx, owner, repo, first, second, _ := setupQueuedPushTest(t, "transient-order")
	queueDir := queueDirForRepo(t, app, owner, repo)
	writeQueuedEvent(t, queueDir, "event-00000000000000000001", strings.Repeat("0", 40), strings.Repeat("a", 40))
	writeQueuedEvent(t, queueDir, "event-00000000000000000002", first, second)

	app.drainRepoCITriggerQueue(ctx, owner, repo)
	var count int
	if err := app.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM ci_runs WHERE repo_id = ?", repo.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("newer event overtook transient failure; run count = %d", count)
	}
	writeQueuedEvent(t, queueDir, "event-00000000000000000001", strings.Repeat("0", 40), first)
	app.drainRepoCITriggerQueue(ctx, owner, repo)
	assertQueuedPushStates(t, app, repo.ID, []string{first, second})
}

func TestDurableQueueIsolatesMalformedFileWithoutBlockingValidEvent(t *testing.T) {
	app, ctx, owner, repo, first, _, _ := setupQueuedPushTest(t, "malformed-order")
	queueDir := queueDirForRepo(t, app, owner, repo)
	if err := os.MkdirAll(queueDir, 0o700); err != nil {
		t.Fatal(err)
	}
	badName := "event-00000000000000000001"
	if err := os.WriteFile(filepath.Join(queueDir, badName), []byte("not an event\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeQueuedEvent(t, queueDir, "event-00000000000000000002", strings.Repeat("0", 40), first)

	app.drainRepoCITriggerQueue(ctx, owner, repo)
	if _, err := os.Stat(filepath.Join(queueDir, ".malformed-"+badName)); err != nil {
		t.Fatalf("malformed event was not quarantined: %v", err)
	}
	assertQueuedPushStates(t, app, repo.ID, []string{first})
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
			pending, err := hasQueuedCITriggerFiles(queueDir)
			if err != nil || !pending {
				t.Fatalf("pending=%t err=%v for %s", pending, err, name)
			}
		})
	}
	if err := os.RemoveAll(queueDir); err != nil {
		t.Fatal(err)
	}
	pending, err := hasQueuedCITriggerFiles(queueDir)
	if err != nil || pending {
		t.Fatalf("missing queue pending=%t err=%v", pending, err)
	}
}

func TestQueuedAnnotatedTagSurvivesLaterRefDeletion(t *testing.T) {
	app := setupTestApp(t)
	ctx := context.Background()
	owner, err := app.DB.GetUserByUsername(ctx, "testuser")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := app.DB.CreateRepository(ctx, owner.ID, "queued-tag", "", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := app.DB.GetRepositoryByID(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	mainCommit, _, _ := setupCIRefRepo(t, app.Config.ReposPath, owner, repo)
	repoPath, err := git.SecureRepoPath(app.Config.ReposPath, owner.Username, repo.Name)
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

	if err := app.DB.UpsertRepoCIRefRule(ctx, models.RepoCIRefRule{
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
	remove, err := app.processQueuedCITrigger(ctx, owner, repo, "event-test", eventPath)
	if err != nil || !remove {
		t.Fatalf("remove=%v err=%v", remove, err)
	}
	runs, err := app.DB.GetCIRunsByRepo(ctx, repo.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Tag != "queued-v1" || runs[0].CommitHash != mainCommit {
		t.Fatalf("unexpected queued tag run: %+v", runs)
	}
}

func TestQueuedTriggerRetriesUnresolvedObject(t *testing.T) {
	app := setupTestApp(t)
	ctx := context.Background()
	owner, _ := app.DB.GetUserByUsername(ctx, "testuser")
	repoID, _ := app.DB.CreateRepository(ctx, owner.ID, "queued-retry", "", false)
	repo, _ := app.DB.GetRepositoryByID(ctx, repoID)
	setupCIRefRepo(t, app.Config.ReposPath, owner, repo)

	eventPath := filepath.Join(t.TempDir(), ".processing-event-retry")
	event := strings.Repeat("0", 40) + "\n" + strings.Repeat("a", 40) + "\nrefs/heads/main\n"
	if err := os.WriteFile(eventPath, []byte(event), 0o600); err != nil {
		t.Fatal(err)
	}
	remove, err := app.processQueuedCITrigger(ctx, owner, repo, "event-retry", eventPath)
	if err == nil || remove {
		t.Fatalf("unresolved event should remain queued: remove=%v err=%v", remove, err)
	}
}
