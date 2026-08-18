package db

import (
	"context"
	"testing"
)

func TestGetLatestCIRunsForCommits(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()
	user, err := database.CreateUser(ctx, "commitstatusowner", "CiPass2")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := database.CreateRepository(ctx, user.ID, "commit-status", "", false)
	if err != nil {
		t.Fatal(err)
	}
	commitA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	commitB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	oldID, err := database.CreateCIRun(ctx, repoID, commitA, "main", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	newID, err := database.CreateCIRun(ctx, repoID, commitA, "main", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := database.CreateCIRun(ctx, repoID, commitB, "main", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE ci_runs SET status='failed', created_at=100 WHERE id=?", oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE ci_runs SET status='success', created_at=200 WHERE id=?", newID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE ci_runs SET status='running', created_at=150 WHERE id=?", otherID); err != nil {
		t.Fatal(err)
	}

	latest, err := database.GetLatestCIRunForCommit(ctx, repoID, commitA)
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.ID != newID || latest.Status != "success" {
		t.Fatalf("latest commit run: %+v", latest)
	}

	runs, err := database.GetLatestCIRunsForCommits(ctx, repoID, []string{commitA, commitB, commitA, ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[commitA].ID != newID || runs[commitB].ID != otherID {
		t.Fatalf("batch latest runs: %+v", runs)
	}
}
