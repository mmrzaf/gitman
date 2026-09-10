package handlers

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
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
	addHTTPTokenWithScope(t, database, username, token, models.AccessTokenScopeRepoWrite)
}

func addHTTPTokenWithScope(t *testing.T, database *db.DB, username, token string, scope models.AccessTokenScope) {
	t.Helper()
	user, err := database.GetUserByUsername(context.Background(), username)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(token))
	expiresAt := time.Now().Add(24 * time.Hour)
	if _, err := database.CreateAccessTokenWithScope(context.Background(), user.ID, "test", hex.EncodeToString(hash[:]), scope, &expiresAt); err != nil {
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

func TestGitHTTPRealClientLargePush(t *testing.T) {
	requireGitClient(t)
	app := setupTestApp(t)
	createHTTPTestRepo(t, app, "testuser", "large-push", false)
	addHTTPToken(t, app.DB, "testuser", "large-push-token")

	server := httptest.NewServer(SetupRouter(app))
	defer server.Close()
	baseURL := gitURLWithCredentials(t, server.URL+"/testuser/large-push.git", "testuser", "large-push-token")
	cloneDir := filepath.Join(t.TempDir(), "large-push")
	if out, err := runGitClient(t, "", "clone", baseURL, cloneDir); err != nil {
		t.Fatalf("clone before large push failed: %v: %s", err, out)
	}

	payload := make([]byte, 3*1024*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloneDir, "large.bin"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "large.bin"},
		{"-c", "user.name=Large Push", "-c", "user.email=large@example.invalid", "commit", "-m", "large push"},
		{"push", "origin", "main"},
	} {
		if out, err := runGitClient(t, cloneDir, args...); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}

	verifyDir := filepath.Join(t.TempDir(), "verify-large-push")
	if out, err := runGitClient(t, "", "clone", baseURL, verifyDir); err != nil {
		t.Fatalf("clone after large push failed: %v: %s", err, out)
	}
	info, err := os.Stat(filepath.Join(verifyDir, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len(payload)) {
		t.Fatalf("large.bin size = %d, want %d", info.Size(), len(payload))
	}
}

func TestGitHTTPRealClientReceiveLimitRejectsOversizedPushWithoutMovingRef(t *testing.T) {
	requireGitClient(t)
	app := setupTestApp(t)
	repoPath := createHTTPTestRepo(t, app, "testuser", "receive-limit", false)
	addHTTPToken(t, app.DB, "testuser", "receive-limit-token")

	if out, err := runGitClient(t, repoPath, "config", "receive.maxInputSize", "1048576"); err != nil {
		t.Fatalf("set receive.maxInputSize: %v: %s", err, out)
	}
	before, err := git.ResolveRevisionCommitHash(context.Background(), repoPath, "main")
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(SetupRouter(app))
	defer server.Close()
	baseURL := gitURLWithCredentials(t, server.URL+"/testuser/receive-limit.git", "testuser", "receive-limit-token")
	cloneDir := filepath.Join(t.TempDir(), "receive-limit")
	if out, err := runGitClient(t, "", "clone", baseURL, cloneDir); err != nil {
		t.Fatalf("clone before oversized push failed: %v: %s", err, out)
	}

	payload := make([]byte, 3*1024*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloneDir, "too-large.bin"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "too-large.bin"},
		{"-c", "user.name=Receive Limit", "-c", "user.email=limit@example.invalid", "commit", "-m", "oversized push"},
	} {
		if out, err := runGitClient(t, cloneDir, args...); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	if out, err := runGitClient(t, cloneDir, "push", "origin", "main"); err == nil {
		t.Fatalf("oversized push unexpectedly succeeded: %s", out)
	}

	after, err := git.ResolveRevisionCommitHash(context.Background(), repoPath, "main")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("main moved after rejected push: before=%s after=%s", before, after)
	}
}

func TestGitHTTPExpiredTokenIsRejected(t *testing.T) {
	app := setupTestApp(t)
	createHTTPTestRepo(t, app, "testuser", "expired-token", true)
	user, err := app.DB.GetUserByUsername(context.Background(), "testuser")
	if err != nil {
		t.Fatal(err)
	}
	const plainToken = "expired-http-token"
	hash := sha256.Sum256([]byte(plainToken))
	expiresAt := time.Now().Add(-time.Minute)
	if _, err := app.DB.CreateAccessToken(context.Background(), user.ID, "expired", hex.EncodeToString(hash[:]), &expiresAt); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/testuser/expired-token.git/info/refs?service=git-upload-pack", nil)
	req.SetBasicAuth("testuser", plainToken)
	w := httptest.NewRecorder()
	SetupRouter(app).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expired token status = %d, want 401; body=%q", w.Code, w.Body.String())
	}
}

func TestGitHTTPLimiterRejectsWhenFull(t *testing.T) {
	app := setupTestApp(t)
	app.Config.GitHTTPMaxConcurrent = 1
	app.Config.GitHTTPMaxConcurrentPerIP = 1
	limiter := app.gitHTTPConcurrencyLimiter()
	release, ok := limiter.tryAcquire("192.0.2.1")
	if !ok {
		t.Fatal("first Git HTTP slot should be available")
	}
	if _, ok := limiter.tryAcquire("198.51.100.1"); ok {
		t.Fatal("second Git HTTP slot should be rejected while the only global slot is occupied")
	}
	release()
	release, ok = limiter.tryAcquire("198.51.100.1")
	if !ok {
		t.Fatal("released Git HTTP slot should become available again")
	}
	release()
}

func TestReadGitHTTPCGIHeaders(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("Status: 201 Created\r\nContent-Type: application/x-git-test\r\nCache-Control: no-cache\r\n\r\nbody"))
	status, headers, err := readGitHTTPCGIHeaders(reader)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	if got := headers.Get("Content-Type"); got != "application/x-git-test" {
		t.Fatalf("Content-Type = %q", got)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "body" {
		t.Fatalf("body = %q", body)
	}
}

func TestServeGitHTTPBackendAcceptsChunkedRequestBody(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is unavailable")
	}
	gitBin := filepath.Join(t.TempDir(), "fake-git")
	script := `#!/bin/sh
body=$(cat)
printf 'Content-Type: text/plain\r\n\r\n'
printf '%s|%s' "${CONTENT_LENGTH-unset}" "$body"
`
	if err := os.WriteFile(gitBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/alice/repo.git/git-upload-pack", strings.NewReader("request"))
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	w := httptest.NewRecorder()
	result, err := serveGitHTTPBackend(context.Background(), w, req, gitBin, t.TempDir(), t.TempDir(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Started {
		t.Fatal("backend did not start a response")
	}
	if got := w.Body.String(); got != "unset|request" {
		t.Fatalf("chunked backend body = %q, want %q", got, "unset|request")
	}
}

func TestServeGitHTTPBackendHonorsDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is unavailable")
	}
	gitBin := filepath.Join(t.TempDir(), "fake-git")
	if err := os.WriteFile(gitBin, []byte("#!/bin/sh\nsleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/alice/repo.git/git-upload-pack", strings.NewReader("request"))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	w := httptest.NewRecorder()
	started := time.Now()
	result, err := serveGitHTTPBackend(ctx, w, req, gitBin, t.TempDir(), t.TempDir(), "alice")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("backend error = %v, want context deadline exceeded", err)
	}
	if result.Started {
		t.Fatal("backend should not have started an HTTP response before timing out")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("deadline did not terminate backend promptly: %s", elapsed)
	}
}

func TestServeGitHTTPBackendDoesNotInheritGitmanSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is unavailable")
	}
	t.Setenv("GITMAN_SECRET_KEY", "must-not-reach-git")
	gitBin := filepath.Join(t.TempDir(), "fake-git")
	script := `#!/bin/sh
printf 'Content-Type: text/plain\r\n\r\n'
printf '%s|%s|%s|%s' "${GITMAN_SECRET_KEY-unset}" "${HTTP_GIT_PROTOCOL-unset}" "${HTTP_AUTHORIZATION-unset}" "${HTTP_COOKIE-unset}"
`
	if err := os.WriteFile(gitBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/alice/repo.git/info/refs?service=git-upload-pack", nil)
	req.Header.Set("Git-Protocol", "version=2")
	req.Header.Set("Authorization", "Basic must-not-reach-git")
	req.Header.Set("Cookie", "session_token=must-not-reach-git")
	w := httptest.NewRecorder()
	result, err := serveGitHTTPBackend(context.Background(), w, req, gitBin, t.TempDir(), t.TempDir(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Started {
		t.Fatal("backend did not produce a response")
	}
	if got := w.Body.String(); got != "unset|version=2|unset|unset" {
		t.Fatalf("backend environment = %q, want secrets/auth absent and Git-Protocol forwarded", got)
	}
}

func TestBoundedBufferCapsGitHTTPStderr(t *testing.T) {
	buffer := newBoundedBuffer(4)
	input := []byte("abcdefgh")
	n, err := buffer.Write(input)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(input) {
		t.Fatalf("Write reported %d bytes, want %d", n, len(input))
	}
	if got := buffer.String(); got != "abcd" {
		t.Fatalf("buffer = %q", got)
	}
	if !buffer.truncated {
		t.Fatal("expected truncation flag")
	}
}

func TestHandleGitHTTPBusyReturnsRetryableServiceUnavailable(t *testing.T) {
	app := setupTestApp(t)
	app.Config.GitHTTPMaxConcurrent = 1
	app.Config.GitHTTPMaxConcurrentPerIP = 1
	req := httptest.NewRequest(http.MethodGet, "/testuser/repo.git/info/refs?service=git-upload-pack", nil)
	release, ok := app.gitHTTPConcurrencyLimiter().tryAcquire(app.clientIP(req))
	if !ok {
		t.Fatal("failed to reserve Git HTTP slot")
	}
	defer release()

	w := httptest.NewRecorder()
	app.HandleGitHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2", got)
	}
}

func TestGitHTTPReadOnlyTokenCannotPushForWriteCollaborator(t *testing.T) {
	requireGitClient(t)
	app := setupTestApp(t)
	createHTTPTestRepo(t, app, "testuser", "scope-auth", true)
	ctx := context.Background()
	writer, err := app.DB.CreateUser(ctx, "scoped-writer", "WriterPass1")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := app.DB.GetUserByUsername(ctx, "testuser")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := app.DB.GetRepositoryByOwnerAndName(ctx, owner.ID, "scope-auth")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.DB.AddCollaborator(ctx, repo.ID, writer.ID, "write"); err != nil {
		t.Fatal(err)
	}
	addHTTPTokenWithScope(t, app.DB, writer.Username, "read-only-token", models.AccessTokenScopeRepoRead)

	server := httptest.NewServer(SetupRouter(app))
	defer server.Close()
	baseURL := server.URL + "/testuser/scope-auth.git"
	readURL := gitURLWithCredentials(t, baseURL, writer.Username, "read-only-token")
	cloneDir := filepath.Join(t.TempDir(), "scoped-writer")
	if out, err := runGitClient(t, "", "clone", readURL, cloneDir); err != nil {
		t.Fatalf("read-only token clone failed: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(cloneDir, "scope.txt"), []byte("scope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "scope.txt"},
		{"-c", "user.name=Scoped Writer", "-c", "user.email=scoped@example.invalid", "commit", "-m", "scope test"},
	} {
		if out, err := runGitClient(t, cloneDir, args...); err != nil {
			t.Fatalf("prepare scoped commit: %v: %s", err, out)
		}
	}
	if out, err := runGitClient(t, cloneDir, "push", "origin", "main"); err == nil {
		t.Fatalf("read-only token push unexpectedly succeeded: %s", out)
	}
}
