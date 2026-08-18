package db

import (
	"context"
	"testing"
)

func TestGetCIRunsByRepoFiltered(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()
	user, err := database.CreateUser(ctx, "filterowner", "CiPass1")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := database.CreateRepository(ctx, user.ID, "filter-repo", "", false)
	if err != nil {
		t.Fatal(err)
	}
	mainID, _ := database.CreateCIRun(ctx, repoID, "aaaaaaa", "main", "", "manual")
	_, _ = database.CreateCIRun(ctx, repoID, "bbbbbbb", "develop", "", "manual")
	if _, err := database.ExecContext(ctx, "UPDATE ci_runs SET status = 'failed' WHERE id = ?", mainID); err != nil {
		t.Fatal(err)
	}

	runs, err := database.GetCIRunsByRepoFiltered(ctx, repoID, "failed", "main", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != mainID {
		t.Fatalf("unexpected filtered runs: %+v", runs)
	}
}

func TestGetCIRunRetryChainIncludesSiblingRetries(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()
	user, _ := database.CreateUser(ctx, "retryfamilyowner", "CiPass1")
	repoID, _ := database.CreateRepository(ctx, user.ID, "retry-family", "", false)
	rootID, err := database.CreateCIRun(ctx, repoID, "abcdef0", "main", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE ci_runs SET status = 'failed' WHERE id = ?", rootID); err != nil {
		t.Fatal(err)
	}
	firstRetry, err := database.RetryCIRun(ctx, repoID, rootID)
	if err != nil {
		t.Fatal(err)
	}
	secondRetry, err := database.RetryCIRun(ctx, repoID, rootID)
	if err != nil {
		t.Fatal(err)
	}

	chain, err := database.GetCIRunRetryChain(ctx, repoID, firstRetry)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 3 {
		t.Fatalf("retry family length = %d, want 3: %+v", len(chain), chain)
	}
	seen := map[string]bool{}
	for _, run := range chain {
		seen[run.ID] = true
	}
	for _, id := range []string{rootID, firstRetry, secondRetry} {
		if !seen[id] {
			t.Fatalf("retry family missing %s: %+v", id, chain)
		}
	}
}
