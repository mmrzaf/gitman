package handlers

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

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
