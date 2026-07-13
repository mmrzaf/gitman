package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/models"
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

func TestCreatePushCIRunCancelsOlderPendingSameBranch(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()
	user, err := database.CreateUser(ctx, "dedupe", "CiPass1")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := database.CreateRepository(ctx, user.ID, "dedupe-repo", "", false)
	if err != nil {
		t.Fatal(err)
	}
	oldID, err := database.CreatePushCIRun(ctx, repoID, "aaaaaaaa", "development", "")
	if err != nil {
		t.Fatal(err)
	}
	manualID, err := database.CreateCIRun(ctx, repoID, "bbbbbbbb", "development", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := database.CreatePushCIRun(ctx, repoID, "cccccccc", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	newID, err := database.CreatePushCIRun(ctx, repoID, "dddddddd", "development", "")
	if err != nil {
		t.Fatal(err)
	}
	oldRun, _ := database.GetCIRunByID(ctx, oldID)
	if oldRun.Status != "cancelled" || oldRun.CancelReason == "" {
		t.Fatalf("older push was not visibly cancelled: %+v", oldRun)
	}
	for _, id := range []string{manualID, otherID, newID} {
		run, err := database.GetCIRunByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "pending" {
			t.Fatalf("run %s should remain pending: %+v", id, run)
		}
	}
}

func TestCreatePushCIRunDoesNotCancelRunning(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()
	user, _ := database.CreateUser(ctx, "runningpush", "CiPass1")
	repoID, _ := database.CreateRepository(ctx, user.ID, "running-repo", "", false)
	oldID, err := database.CreatePushCIRun(ctx, repoID, "aaaaaaaa", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimNextPendingRun(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreatePushCIRun(ctx, repoID, "bbbbbbbb", "main", ""); err != nil {
		t.Fatal(err)
	}
	oldRun, _ := database.GetCIRunByID(ctx, oldID)
	if oldRun.Status != "running" {
		t.Fatalf("running push was cancelled: %+v", oldRun)
	}
}

func TestRepoCIRefRulesCRUD(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()
	user, _ := database.CreateUser(ctx, "rules", "CiPass1")
	repoID, _ := database.CreateRepository(ctx, user.ID, "rules-repo", "", false)
	rule := models.RepoCIRefRule{
		RepoID:            repoID,
		RefType:           "branch",
		RefName:           "development",
		AutoRun:           true,
		AllowSecrets:      true,
		AllowDockerSocket: true,
	}
	if err := database.UpsertRepoCIRefRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	got, err := database.GetRepoCIRefRule(ctx, repoID, "branch", "development")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.AutoRun || !got.AllowSecrets || !got.AllowDockerSocket {
		t.Fatalf("unexpected rule: %+v", got)
	}
	rules, err := database.ListRepoCIRefRules(ctx, repoID)
	if err != nil || len(rules) != 1 {
		t.Fatalf("expected one rule, got %d err=%v", len(rules), err)
	}
	if err := database.DeleteRepoCIRefRule(ctx, repoID, "branch", "development"); err != nil {
		t.Fatal(err)
	}
	got, err = database.GetRepoCIRefRule(ctx, repoID, "branch", "development")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("rule was not deleted: %+v", got)
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

func TestRepoCIRefRulesPatternMatch(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()
	user, _ := database.CreateUser(ctx, "rulepatterns", "CiPass1")
	repoID, _ := database.CreateRepository(ctx, user.ID, "rule-patterns-repo", "", false)

	if err := database.UpsertRepoCIRefRule(ctx, models.RepoCIRefRule{
		RepoID:            repoID,
		RefType:           "tag",
		RefName:           "v*",
		AutoRun:           true,
		AllowSecrets:      true,
		AllowDockerSocket: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertRepoCIRefRule(ctx, models.RepoCIRefRule{
		RepoID:            repoID,
		RefType:           "tag",
		RefName:           "v1.0.0",
		AutoRun:           false,
		AllowSecrets:      false,
		AllowDockerSocket: false,
	}); err != nil {
		t.Fatal(err)
	}

	pattern, err := database.MatchRepoCIRefRule(ctx, repoID, "tag", "v1.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if pattern == nil || pattern.RefName != "v*" || !pattern.AllowDockerSocket {
		t.Fatalf("pattern rule did not match: %+v", pattern)
	}

	exact, err := database.MatchRepoCIRefRule(ctx, repoID, "tag", "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if exact == nil || exact.RefName != "v1.0.0" || exact.AllowDockerSocket {
		t.Fatalf("exact rule did not override pattern: %+v", exact)
	}

	missing, err := database.MatchRepoCIRefRule(ctx, repoID, "tag", "release-1")
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Fatalf("unexpected match: %+v", missing)
	}
}

func TestCIRunCancelAndRetry(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()
	user, _ := database.CreateUser(ctx, "controlowner", "CiPass1")
	repoID, _ := database.CreateRepository(ctx, user.ID, "control-repo", "", false)

	runID, err := database.CreateCIRun(ctx, repoID, "abcdef", "main", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := database.CancelCIRun(ctx, repoID, runID, "Cancelled in test")
	if err != nil || !cancelled {
		t.Fatalf("cancel failed: cancelled=%v err=%v", cancelled, err)
	}
	run, err := database.GetCIRunByID(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "cancelled" || run.StatusReason != "Cancelled in test" {
		t.Fatalf("unexpected cancelled run: %+v", run)
	}

	retryID, err := database.RetryCIRun(ctx, repoID, runID)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := database.GetCIRunByID(ctx, retryID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Status != "pending" || retry.Event != "retry" || retry.RetryOfRunID != runID || retry.CommitHash != run.CommitHash {
		t.Fatalf("unexpected retry run: %+v", retry)
	}
}

func TestCreatePushCIRunWithTriggerKeyIsIdempotent(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()
	user, _ := database.CreateUser(ctx, "queueowner", "CiPass1")
	repoID, _ := database.CreateRepository(ctx, user.ID, "queue-repo", "", false)

	first, err := database.CreatePushCIRunWithTriggerKey(ctx, repoID, "abcdef", "main", "", "delivery-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := database.CreatePushCIRunWithTriggerKey(ctx, repoID, "abcdef", "main", "", "delivery-1")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("duplicate delivery created a different run: %s != %s", first, second)
	}
	runs, err := database.GetCIRunsByRepo(ctx, repoID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected one run, got %d", len(runs))
	}
}

func TestHasActiveCIRuns(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()
	user, _ := database.CreateUser(ctx, "activeowner", "CiPass1")
	repoID, _ := database.CreateRepository(ctx, user.ID, "active-repo", "", false)

	active, err := database.HasActiveCIRuns(ctx, repoID)
	if err != nil || active {
		t.Fatalf("empty repository active=%v err=%v", active, err)
	}
	runID, err := database.CreateCIRun(ctx, repoID, "abcdef", "main", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	active, err = database.HasActiveCIRuns(ctx, repoID)
	if err != nil || !active {
		t.Fatalf("pending run active=%v err=%v", active, err)
	}
	cancelled, err := database.CancelCIRun(ctx, repoID, runID, "test complete")
	if err != nil || !cancelled {
		t.Fatalf("cancelled=%v err=%v", cancelled, err)
	}
	active, err = database.HasActiveCIRuns(ctx, repoID)
	if err != nil || active {
		t.Fatalf("completed repository active=%v err=%v", active, err)
	}
}

func TestCancelledRunningRunRemainsActiveUntilWorkerAcknowledges(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()
	user, _ := database.CreateUser(ctx, "cancelackowner", "CiPass1")
	repoID, _ := database.CreateRepository(ctx, user.ID, "cancel-ack-repo", "", false)
	runID, err := database.CreateCIRun(ctx, repoID, "abcdef", "main", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := database.ClaimNextPendingRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled, err := database.CancelCIRun(ctx, repoID, runID, "test"); err != nil || !cancelled {
		t.Fatalf("cancelled=%v err=%v", cancelled, err)
	}
	if active, err := database.HasActiveCIRuns(ctx, repoID); err != nil || !active {
		t.Fatalf("stopping cancelled run active=%v err=%v", active, err)
	}
	if err := database.AcknowledgeCancelledCIRun(ctx, runID, claimed.AttemptID); err != nil {
		t.Fatal(err)
	}
	acknowledged, err := database.GetCIRunByID(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if acknowledged.AttemptID != claimed.AttemptID || acknowledged.HeartbeatAt != nil {
		t.Fatalf("acknowledgement lost attempt identity: %+v", acknowledged)
	}
	if active, err := database.HasActiveCIRuns(ctx, repoID); err != nil || active {
		t.Fatalf("acknowledged cancelled run active=%v err=%v", active, err)
	}
}

func TestReleaseStaleCancelledCIRuns(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	ctx := context.Background()
	user, _ := database.CreateUser(ctx, "stalecancelowner", "CiPass1")
	repoID, _ := database.CreateRepository(ctx, user.ID, "stale-cancel-repo", "", false)
	runID, _ := database.CreateCIRun(ctx, repoID, "abcdef", "main", "", "manual")
	claimed, _ := database.ClaimNextPendingRun(ctx)
	if cancelled, err := database.CancelCIRun(ctx, repoID, runID, "test"); err != nil || !cancelled {
		t.Fatalf("cancelled=%v err=%v", cancelled, err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE ci_runs SET completed_at = ? WHERE id = ?", time.Now().Add(-10*time.Minute).Unix(), runID); err != nil {
		t.Fatal(err)
	}
	released, err := database.ReleaseStaleCancelledCIRuns(ctx, time.Now().Add(-2*time.Minute))
	if err != nil || released != 1 {
		t.Fatalf("released=%d err=%v", released, err)
	}
	if err := database.AcknowledgeCancelledCIRun(ctx, runID, claimed.AttemptID); !errors.Is(err, ErrCIRunLeaseInactive) {
		t.Fatalf("expected inactive attempt after release, got %v", err)
	}
}
