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
	if _, err := app.DB.ExecContext(ctx, "UPDATE ci_runs SET status = 'running', log_file = ? WHERE id = ?", logPath, runID); err != nil {
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
