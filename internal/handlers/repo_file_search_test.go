package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRepoFileSearchReturnsBoundedPartialResults(t *testing.T) {
	requireGitClient(t)
	app := setupTestApp(t)
	repoPath := createHTTPTestRepo(t, app, "testuser", "search-limit", false)

	cloneDir := filepath.Join(t.TempDir(), "clone")
	if out, err := runGitClient(t, "", "clone", repoPath, cloneDir); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}
	for _, name := range []string{"alpha.txt", "beta.txt", "gamma.txt"} {
		if err := os.WriteFile(filepath.Join(cloneDir, name), []byte(name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"add", "."},
		{"-c", "user.name=Search Test", "-c", "user.email=search@example.invalid", "commit", "-m", "add searchable files"},
		{"push", "origin", "main"},
	} {
		if out, err := runGitClient(t, cloneDir, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	app.Config.FileSearchMaxFiles = 1
	app.Config.FileSearchMaxBytes = 1 << 20
	app.Config.FileSearchTimeout = time.Second

	req := httptest.NewRequest(http.MethodGet, "/testuser/search-limit/files/search?q=txt", nil)
	w := httptest.NewRecorder()
	SetupRouter(app).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", w.Code, w.Body.String())
	}
	var response fileSearchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Truncated {
		t.Fatalf("response should report truncation: %+v", response)
	}
	if len(response.Results) > 40 {
		t.Fatalf("returned %d results, want at most 40", len(response.Results))
	}
}

func TestRepoFileSearchBusyReturnsRetryableServiceUnavailable(t *testing.T) {
	app := setupTestApp(t)
	app.Config.FileSearchMaxConcurrent = 1
	app.Config.FileSearchMaxConcurrentPerIP = 1
	req := httptest.NewRequest(http.MethodGet, "/testuser/repo/files/search?q=main", nil)
	release, ok := app.fileSearchConcurrencyLimiter().tryAcquire(app.clientIP(req))
	if !ok {
		t.Fatal("failed to reserve file-search slot")
	}
	defer release()

	w := httptest.NewRecorder()
	app.HandleRepoFileSearchGET(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
}
