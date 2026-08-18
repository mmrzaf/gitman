package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/models"
)

func TestHandleCIRunLogGETSupportsIncrementalOffsets(t *testing.T) {
	app := setupTestApp(t)
	ctx := context.Background()
	owner, err := app.DB.GetUserByUsername(ctx, "testuser")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := app.DB.CreateRepository(ctx, owner.ID, "log-offset", "", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := app.DB.GetRepositoryByID(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := app.DB.CreateCIRun(ctx, repoID, "abcdef0", "main", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "run.log")
	content := "first\nsecond\n"
	if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	claimed, err := app.DB.ClaimNextPendingRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != runID {
		t.Fatalf("claimed run = %+v, want %s", claimed, runID)
	}
	if err := app.DB.UpdateCIRunLogFile(ctx, runID, claimed.AttemptID, logPath); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/testuser/log-offset/ci/"+runID+"/log?offset=6", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("run_id", runID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	req = req.WithContext(context.WithValue(req.Context(), repoContextKey, repo))
	w := httptest.NewRecorder()
	app.HandleCIRunLogGET(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%q", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "second\n" {
		t.Fatalf("incremental body = %q, want %q", got, "second\\n")
	}
	if got := w.Header().Get("X-Gitman-Log-Offset"); got != strconv.Itoa(len(content)) {
		t.Fatalf("offset = %q, want %d", got, len(content))
	}
	if got := w.Header().Get("X-Gitman-CI-Status"); got != "running" {
		t.Fatalf("status header = %q", got)
	}
}

func TestCompleteUTF8PrefixLen(t *testing.T) {
	full := []byte("hello 世界")
	if got := completeUTF8PrefixLen(full); got != len(full) {
		t.Fatalf("full UTF-8 prefix = %d, want %d", got, len(full))
	}
	partial := append([]byte("hello "), []byte{0xe4, 0xb8}...)
	if got := completeUTF8PrefixLen(partial); got != len("hello ") {
		t.Fatalf("partial UTF-8 prefix = %d, want %d", got, len("hello "))
	}
	invalid := append([]byte("hello "), 0xff)
	if got := completeUTF8PrefixLen(invalid); got != len(invalid) {
		t.Fatalf("invalid byte should be consumed: got %d want %d", got, len(invalid))
	}
}

func TestCILogPrefixAndANSIStripping(t *testing.T) {
	full := []byte("before \x1b[31mred\x1b[0m after")
	if got := stripANSI(full); got != "before red after" {
		t.Fatalf("stripANSI = %q", got)
	}
	partial := []byte("before \x1b[31")
	if got := completeCILogPrefixLen(partial); got != len("before ") {
		t.Fatalf("partial CSI prefix = %d, want %d", got, len("before "))
	}
	osc := []byte("x\x1b]8;;https://example.com\x07label\x1b]8;;\x07y")
	if got := stripANSI(osc); got != "xlabely" {
		t.Fatalf("OSC strip = %q", got)
	}
}
func TestCIRunNavigationRefPrefersImmutableCommit(t *testing.T) {
	run := &models.CIRun{CommitHash: "deadbeef", Branch: "main", Tag: "v1.0.0"}
	if got := ciRunNavigationRef(run); got != "deadbeef" {
		t.Fatalf("navigation ref = %q, want immutable commit", got)
	}
	if got := ciRunNavigationRef(&models.CIRun{Branch: "main"}); got != "main" {
		t.Fatalf("branch fallback = %q", got)
	}
	if got := ciRunNavigationRef(&models.CIRun{Tag: "v1.0.0"}); got != "v1.0.0" {
		t.Fatalf("tag fallback = %q", got)
	}
}

func TestCILogUnavailableTextDistinguishesPreparingFromTerminalRun(t *testing.T) {
	if got := ciLogUnavailableText(&models.CIRun{Status: models.CIStatusRunning}); got != "log not yet available — worker is preparing the workspace" {
		t.Fatalf("running message = %q", got)
	}
	if got := ciLogUnavailableText(&models.CIRun{Status: models.CIStatusFailed}); got != "no build log is available for this run" {
		t.Fatalf("terminal message = %q", got)
	}
}
