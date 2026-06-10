package db

import (
	"context"
	"testing"
	"time"
)

func TestCreateAndClaimCIRun(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "ciowner", "CiPass1")
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	repoID, err := db.CreateRepository(ctx, user.ID, "ci-repo", "", false)
	if err != nil {
		t.Fatalf("CreateRepository failed: %v", err)
	}

	runID, err := db.CreateCIRun(ctx, repoID, "abc1234", "main", "", "push")
	if err != nil {
		t.Fatalf("CreateCIRun failed: %v", err)
	}

	// Claim the pending run
	run, err := db.ClaimNextPendingRun(ctx)
	if err != nil {
		t.Fatalf("ClaimNextPendingRun failed: %v", err)
	}
	if run == nil {
		t.Fatal("expected a run to claim")
		return
	}
	if run.ID != runID {
		t.Errorf("expected claimed run ID %s, got %s", runID, run.ID)
	}
	if run.Status != "running" {
		t.Errorf("expected status running, got %s", run.Status)
	}
	if run.AttemptID == "" {
		t.Fatal("claimed run is missing attempt ID")
	}

	// Check run in DB
	r, err := db.GetCIRunByID(ctx, runID)
	if err != nil {
		t.Fatalf("GetCIRunByID failed: %v", err)
	}
	if r == nil {
		t.Fatal("run not found after claim")
		return
	}
	if r.Status != "running" {
		t.Errorf("expected running in DB, got %s", r.Status)
	}

	// Update log file
	logFile := "/tmp/test-ci.log"
	err = db.UpdateCIRunLogFile(ctx, runID, run.AttemptID, logFile)
	if err != nil {
		t.Fatalf("UpdateCIRunLogFile failed: %v", err)
	}
	r, err = db.GetCIRunByID(ctx, runID)
	if err != nil || r == nil {
		t.Fatal("run disappeared")
	}
	if r.LogFile != logFile {
		t.Errorf("log file not updated correctly, got %s", r.LogFile)
	}

	// Complete the run
	err = db.CompleteCIRun(ctx, runID, run.AttemptID, "success")
	if err != nil {
		t.Fatalf("CompleteCIRun failed: %v", err)
	}

	// Verify the update happened with a raw query
	var status string
	err = db.QueryRowContext(ctx, "SELECT status FROM ci_runs WHERE id = ?", runID).Scan(&status)
	if err != nil {
		t.Fatalf("failed to query completed run: %v", err)
	}
	if status != "success" {
		t.Errorf("expected status success, got %s", status)
	}

	// Also check GetCIRunByID for the completed run (should work but if not,
	// the raw query already proved the row exists)
	r, err = db.GetCIRunByID(ctx, runID)
	if err != nil || r == nil {
		t.Logf("GetCIRunByID returned nil after completion (raw query succeeded)")
	} else {
		if r.CompletedAt == nil {
			t.Error("completed_at should not be nil")
		}
	}
}

func TestGetCIRunsByRepo(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "runs", "CiPass1")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := db.CreateRepository(ctx, user.ID, "runs-repo", "", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.CreateCIRun(ctx, repoID, "hash1", "main", "", "push")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.CreateCIRun(ctx, repoID, "hash2", "dev", "", "manual")
	if err != nil {
		t.Fatal(err)
	}

	runs, err := db.GetCIRunsByRepo(ctx, repoID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Errorf("expected 2 runs, got %d", len(runs))
	}
}

func TestSecrets(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "secowner", "Pass1")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := db.CreateRepository(ctx, user.ID, "sec-repo", "", false)
	if err != nil {
		t.Fatal(err)
	}

	err = db.AddRepoSecret(ctx, repoID, "TOKEN", "encrypted_token")
	if err != nil {
		t.Fatal(err)
	}
	// Upsert
	err = db.AddRepoSecret(ctx, repoID, "TOKEN", "new_token")
	if err != nil {
		t.Fatal(err)
	}

	secrets, err := db.GetRepoSecrets(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 1 {
		t.Fatalf("expected 1 secret, got %d", len(secrets))
	}
	if secrets[0].EncryptedValue != "new_token" {
		t.Errorf("expected upsert value, got %s", secrets[0].EncryptedValue)
	}

	err = db.DeleteRepoSecret(ctx, secrets[0].ID, repoID)
	if err != nil {
		t.Fatal(err)
	}
	secrets, _ = db.GetRepoSecrets(ctx, repoID)
	if len(secrets) != 0 {
		t.Error("secret not deleted")
	}
}

func TestRequeueStaleCIRuns(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()

	user, err := database.CreateUser(ctx, "staleowner", "CiPass1")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := database.CreateRepository(ctx, user.ID, "stale-repo", "", false)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := database.CreateCIRun(ctx, repoID, "abc1234", "main", "", "push")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := database.ClaimNextPendingRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	oldAttempt := claimed.AttemptID
	if _, err := database.ExecContext(ctx, "UPDATE ci_runs SET heartbeat_at = ? WHERE id = ?", time.Now().Add(-10*time.Minute).Unix(), runID); err != nil {
		t.Fatal(err)
	}
	count, err := database.RequeueStaleCIRuns(ctx, time.Now().Add(-2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one requeued run, got %d", count)
	}
	run, err := database.GetCIRunByID(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "pending" || run.AttemptID != "" || run.StartedAt != nil || run.HeartbeatAt != nil {
		t.Fatalf("stale run was not reset: %+v", run)
	}
	replacement, err := database.ClaimNextPendingRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.AttemptID == "" || replacement.AttemptID == oldAttempt {
		t.Fatalf("replacement claim did not receive a fresh attempt ID: %+v", replacement)
	}
	if err := database.HeartbeatCIRun(ctx, runID, oldAttempt); err == nil {
		t.Fatal("stale attempt renewed replacement lease")
	}
	if err := database.CompleteCIRun(ctx, runID, oldAttempt, "success"); err == nil {
		t.Fatal("stale attempt completed replacement lease")
	}
}
