package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
)

func requireGitClient(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable is unavailable")
	}
}

func runGitClient(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func createHTTPTestRepo(t *testing.T, app *App, owner, name string, private bool) string {
	t.Helper()
	ctx := context.Background()
	user, err := app.DB.GetUserByUsername(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.CreateRepository(ctx, user.ID, name, "", private); err != nil {
		t.Fatal(err)
	}
	repoPath, err := git.SecureRepoPath(app.Config.ReposPath, owner, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := git.InitBareRepo(ctx, repoPath, 64<<20); err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	if out, err := runGitClient(t, work, "init", "-b", "main"); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "README.md"},
		{"-c", "user.name=Gitman Test", "-c", "user.email=gitman@example.invalid", "commit", "-m", "seed"},
		{"remote", "add", "origin", repoPath},
		{"push", "origin", "main"},
	} {
		if out, err := runGitClient(t, work, args...); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	return repoPath
}

func addHTTPToken(t *testing.T, database *db.DB, username, token string) {
	t.Helper()
	user, err := database.GetUserByUsername(context.Background(), username)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(token))
	if err := database.CreateAccessToken(context.Background(), user.ID, "test", hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
}

func gitURLWithCredentials(t *testing.T, raw, username, token string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(username, token)
	return u.String()
}

func TestGitHTTPRealClientPublicClone(t *testing.T) {
	requireGitClient(t)
	app := setupTestApp(t)
	createHTTPTestRepo(t, app, "testuser", "public-clone", false)
	server := httptest.NewServer(SetupRouter(app))
	defer server.Close()

	cloneDir := filepath.Join(t.TempDir(), "clone")
	out, err := runGitClient(t, "", "clone", server.URL+"/testuser/public-clone.git", cloneDir)
	if err != nil {
		t.Fatalf("public clone failed: %v: %s", err, out)
	}
	if data, err := os.ReadFile(filepath.Join(cloneDir, "README.md")); err != nil || string(data) != "hello\n" {
		t.Fatalf("cloned content = %q, %v", data, err)
	}
}

func TestGitHTTPRealClientPrivateReadAndWriteAuthorization(t *testing.T) {
	requireGitClient(t)
	app := setupTestApp(t)
	createHTTPTestRepo(t, app, "testuser", "private-auth", true)
	ctx := context.Background()
	reader, err := app.DB.CreateUser(ctx, "reader", "ReaderPass1")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := app.DB.GetUserByUsername(ctx, "testuser")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := app.DB.GetRepositoryByOwnerAndName(ctx, owner.ID, "private-auth")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.DB.AddCollaborator(ctx, repo.ID, reader.ID, "read"); err != nil {
		t.Fatal(err)
	}
	addHTTPToken(t, app.DB, "reader", "reader-token")

	server := httptest.NewServer(SetupRouter(app))
	defer server.Close()
	baseURL := server.URL + "/testuser/private-auth.git"

	// Private source is not anonymously readable.
	if out, err := runGitClient(t, "", "clone", baseURL, filepath.Join(t.TempDir(), "anonymous")); err == nil {
		t.Fatalf("anonymous private clone unexpectedly succeeded: %s", out)
	}

	cloneDir := filepath.Join(t.TempDir(), "reader")
	readerURL := gitURLWithCredentials(t, baseURL, "reader", "reader-token")
	if out, err := runGitClient(t, "", "clone", readerURL, cloneDir); err != nil {
		t.Fatalf("reader clone failed: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(cloneDir, "reader.txt"), []byte("read only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "reader.txt"},
		{"-c", "user.name=Reader", "-c", "user.email=reader@example.invalid", "commit", "-m", "reader change"},
	} {
		if out, err := runGitClient(t, cloneDir, args...); err != nil {
			t.Fatalf("prepare reader commit: %v: %s", err, out)
		}
	}
	if out, err := runGitClient(t, cloneDir, "push", "origin", "main"); err == nil {
		t.Fatalf("read-only collaborator push unexpectedly succeeded: %s", out)
	}
}

func TestGitHTTPDatabaseFailureIsUnavailableNotNotFound(t *testing.T) {
	app := setupTestApp(t)
	router := SetupRouter(app)
	if err := app.DB.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/testuser/repo.git/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("database failure status = %d, want 503; body=%q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "temporarily unavailable") {
		t.Fatalf("unexpected body: %q", w.Body.String())
	}
}

func TestGitHTTPPrivateAuthChallengeAndBadToken(t *testing.T) {
	app := setupTestApp(t)
	createHTTPTestRepo(t, app, "testuser", "private-challenge", true)
	router := SetupRouter(app)
	path := "/testuser/private-challenge.git/info/refs?service=git-upload-pack"

	anonymous := httptest.NewRequest(http.MethodGet, path, nil)
	anonymousW := httptest.NewRecorder()
	router.ServeHTTP(anonymousW, anonymous)
	if anonymousW.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous private read status = %d, want 401; body=%q", anonymousW.Code, anonymousW.Body.String())
	}
	if got := anonymousW.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Basic") {
		t.Fatalf("WWW-Authenticate = %q, want Basic challenge", got)
	}

	bad := httptest.NewRequest(http.MethodGet, path, nil)
	bad.SetBasicAuth("testuser", "not-a-token")
	badW := httptest.NewRecorder()
	router.ServeHTTP(badW, bad)
	if badW.Code != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d, want 401; body=%q", badW.Code, badW.Body.String())
	}
}

func TestGitHTTPUnsupportedServiceIsBadRequest(t *testing.T) {
	app := setupTestApp(t)
	createHTTPTestRepo(t, app, "testuser", "bad-service", false)
	router := SetupRouter(app)

	req := httptest.NewRequest(http.MethodGet, "/testuser/bad-service.git/info/refs?service=git-upload-evil", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unsupported service status = %d, want 400; body=%q", w.Code, w.Body.String())
	}
}

func TestGitHTTPMissingRepositoryIsNotFound(t *testing.T) {
	app := setupTestApp(t)
	router := SetupRouter(app)

	req := httptest.NewRequest(http.MethodGet, "/testuser/missing.git/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing repository status = %d, want 404; body=%q", w.Code, w.Body.String())
	}
}

func TestGitHTTPMissingRepositoryStorageIsUnavailable(t *testing.T) {
	app := setupTestApp(t)
	repoPath := createHTTPTestRepo(t, app, "testuser", "missing-storage", false)
	if err := os.RemoveAll(repoPath); err != nil {
		t.Fatal(err)
	}
	router := SetupRouter(app)

	req := httptest.NewRequest(http.MethodGet, "/testuser/missing-storage.git/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing repository storage status = %d, want 503; body=%q", w.Code, w.Body.String())
	}
}

func TestGitHTTPRealClientWriteCollaboratorCanPush(t *testing.T) {
	requireGitClient(t)
	app := setupTestApp(t)
	createHTTPTestRepo(t, app, "testuser", "write-auth", true)
	ctx := context.Background()
	writer, err := app.DB.CreateUser(ctx, "writer", "WriterPass1")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := app.DB.GetUserByUsername(ctx, "testuser")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := app.DB.GetRepositoryByOwnerAndName(ctx, owner.ID, "write-auth")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.DB.AddCollaborator(ctx, repo.ID, writer.ID, "write"); err != nil {
		t.Fatal(err)
	}
	addHTTPToken(t, app.DB, "writer", "writer-token")

	server := httptest.NewServer(SetupRouter(app))
	defer server.Close()
	baseURL := server.URL + "/testuser/write-auth.git"
	writerURL := gitURLWithCredentials(t, baseURL, "writer", "writer-token")
	cloneDir := filepath.Join(t.TempDir(), "writer")
	if out, err := runGitClient(t, "", "clone", writerURL, cloneDir); err != nil {
		t.Fatalf("writer clone failed: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(cloneDir, "writer.txt"), []byte("write allowed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "writer.txt"},
		{"-c", "user.name=Writer", "-c", "user.email=writer@example.invalid", "commit", "-m", "writer change"},
		{"push", "origin", "main"},
	} {
		if out, err := runGitClient(t, cloneDir, args...); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
}
