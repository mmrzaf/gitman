package handlers

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

func setupTestApp(t *testing.T) *App {
	t.Helper()
	database, err := db.InitDB("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	_, err = database.CreateUser(ctx, "testuser", "TestPass123")
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	cfg := &config.Config{
		Port:              "8080",
		ReposPath:         t.TempDir(),
		SSHUser:           "git",
		ServerHost:        "localhost",
		ArtifactsPath:     t.TempDir(),
		SecretKey:         "testsecretkey",
		WorkerConcurrency: 1,
	}

	// Minimal templates that properly render errors and content.
	tmpl := map[string]*template.Template{
		"home.html":     template.Must(template.New("").Parse(`{{define "base.html"}}home{{end}}`)),
		"login.html":    template.Must(template.New("").Parse(`{{define "base.html"}}{{if .Error}}<div class="error">{{.Error}}</div>{{else}}login page{{end}}{{end}}`)),
		"register.html": template.Must(template.New("").Parse(`{{define "base.html"}}{{if .Error}}<div class="error">{{.Error}}</div>{{else}}register page{{end}}{{end}}`)),
		"repos.html": template.Must(template.New("").Parse(`
			{{define "base.html"}}
			{{range .Data.Repos}}<span>{{.Name}}</span>{{end}}
			{{end}}`)),
		"keys.html": template.Must(template.New("").Parse(`
			{{define "base.html"}}
			{{range .Data.Keys}}<span>{{.Name}}</span>{{end}}
			{{end}}`)),
		"tokens.html": template.Must(template.New("").Parse(`
			{{define "base.html"}}
			{{range .Data.Tokens}}<span>{{.Name}}</span>{{end}}
			{{end}}`)),
	}

	return &App{
		Config:    cfg,
		DB:        database,
		Templates: tmpl,
	}
}

func loginUser(t *testing.T, app *App) *http.Cookie {
	t.Helper()
	form := url.Values{
		"username": {"testuser"},
		"password": {"TestPass123"},
	}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.HandleLoginPOST(w, req)
	resp := w.Result()
	for _, c := range resp.Cookies() {
		if c.Name == "session_token" {
			return c
		}
	}
	t.Fatal("login failed, no session token cookie")
	return nil
}

func TestHandleLoginGET(t *testing.T) {
	app := setupTestApp(t)
	req := httptest.NewRequest("GET", "/login", nil)
	w := httptest.NewRecorder()
	app.HandleLoginGET(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHandleLoginPOSTSuccess(t *testing.T) {
	app := setupTestApp(t)
	cookie := loginUser(t, app)
	if cookie.Value == "" {
		t.Error("missing session token")
	}
}

func TestHandleLoginPOSTInvalid(t *testing.T) {
	app := setupTestApp(t)
	form := url.Values{"username": {"testuser"}, "password": {"wrong"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.HandleLoginPOST(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Invalid username or password") {
		t.Errorf("expected error message, got %s", body)
	}
}

func TestHandleRegisterGET(t *testing.T) {
	app := setupTestApp(t)
	app.Config.AllowRegister = true
	req := httptest.NewRequest("GET", "/register", nil)
	w := httptest.NewRecorder()
	app.HandleRegisterGET(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200")
	}
}

func TestHandleRegisterPOSTSuccess(t *testing.T) {
	app := setupTestApp(t)
	app.Config.AllowRegister = true
	form := url.Values{
		"username": {"newuser"},
		"password": {"NewPass123"},
	}
	req := httptest.NewRequest("POST", "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.HandleRegisterPOST(w, req)
	resp := w.Result()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected redirect, got %d", resp.StatusCode)
	}
	u, err := app.DB.GetUserByUsername(context.Background(), "newuser")
	if err != nil || u == nil {
		t.Fatal("user not created")
	}
}

func TestHandleLogout(t *testing.T) {
	app := setupTestApp(t)
	cookie := loginUser(t, app)
	req := httptest.NewRequest("GET", "/logout", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	app.HandleLogout(w, req)
	resp := w.Result()
	cleared := false
	for _, c := range resp.Cookies() {
		if c.Name == "session_token" && c.MaxAge == -1 {
			cleared = true
			break
		}
	}
	if !cleared {
		t.Error("logout did not clear session cookie")
	}
}

func TestHandleReposGET(t *testing.T) {
	app := setupTestApp(t)
	user, _ := app.DB.GetUserByUsername(context.Background(), "testuser")
	req := httptest.NewRequest("GET", "/repos", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	w := httptest.NewRecorder()
	app.HandleReposGET(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHealthRoutesBypassAuthAndCSRF(t *testing.T) {
	app := setupTestApp(t)
	router := SetupRouter(app)
	for _, path := range []string{"/health", "/healthz", "/readyz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", path, w.Code, w.Body.String())
		}
		if cookies := w.Result().Cookies(); len(cookies) != 0 {
			t.Fatalf("%s: health probe unexpectedly set cookies: %v", path, cookies)
		}
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s: expected no-store, got %q", path, got)
		}
	}
}

func TestReadinessDoesNotExposeStorageErrors(t *testing.T) {
	app := setupTestApp(t)
	app.Config.ReposPath = filepath.Join(t.TempDir(), "missing")
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	app.HandleReadiness(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"component":"repositories"`) || strings.Contains(body, app.Config.ReposPath) {
		t.Fatalf("unexpected readiness response: %s", body)
	}
}

func TestArtifactAPIUnauthenticatedResponseIsJSON(t *testing.T) {
	app := setupTestApp(t)
	router := SetupRouter(app)
	req := httptest.NewRequest(http.MethodGet, "/api/repos/testuser/repo/artifacts/latest/branch/main/report.txt", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected JSON response, got %q", got)
	}
	if got := w.Header().Get("Location"); got != "" {
		t.Fatalf("API response redirected to %q", got)
	}
}

func TestAuthMiddlewareRefreshesExpiringSessionCookie(t *testing.T) {
	app := setupTestApp(t)
	user, _ := app.DB.GetUserByUsername(context.Background(), "testuser")
	token, err := app.DB.CreateSession(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.ExecContext(context.Background(), "UPDATE sessions SET expires_at = ?", time.Now().Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	handler := app.AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if GetUser(r) == nil {
			t.Error("session user was not resolved")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session_token", Value: token})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	var refreshed *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "session_token" {
			refreshed = cookie
		}
	}
	if refreshed == nil || refreshed.Value != token || refreshed.MaxAge < 23*60*60 {
		t.Fatalf("session cookie was not refreshed: %+v", refreshed)
	}
}

func TestHandleKeysGET(t *testing.T) {
	app := setupTestApp(t)
	user, _ := app.DB.GetUserByUsername(context.Background(), "testuser")
	req := httptest.NewRequest("GET", "/keys", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	w := httptest.NewRecorder()
	app.HandleKeysGET(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200")
	}
}

func TestHandleRegisterDisabled(t *testing.T) {
	app := setupTestApp(t)
	app.Config.AllowRegister = false

	req := httptest.NewRequest(http.MethodGet, "/register", nil)
	w := httptest.NewRecorder()
	app.HandleRegisterGET(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}

	r := SetupRouter(app)
	req = httptest.NewRequest(http.MethodGet, "/register", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("router should not expose /register when disabled, got %d", w.Code)
	}
}

func TestWriteCILogFragmentServesPlainText(t *testing.T) {
	w := httptest.NewRecorder()
	payload := `<img src=x onerror="alert(1)">`
	writeCILogFragment(w, payload)
	body := w.Body.String()
	if body != payload {
		t.Fatalf("expected raw log text, got %q", body)
	}
	if strings.Contains(body, "&lt;img") {
		t.Fatalf("CI log text endpoint unexpectedly HTML-escaped output: %s", body)
	}
	if got := w.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("expected text/plain content type, got %q", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("expected no-store, got %q", got)
	}
}

func TestListArtifactsIncludesNestedFilesAndSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "reports", "coverage.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(filepath.Join(root, "reports", "coverage.txt"), filepath.Join(root, "link.txt"))
	artifacts := listArtifacts(root)
	if len(artifacts) != 1 || artifacts[0] != "reports/coverage.txt" {
		t.Fatalf("unexpected artifacts: %v", artifacts)
	}
}

func TestBuildHookScriptUsesDurableLocalQueue(t *testing.T) {
	script := buildHookScript("owner", "repo")
	for _, expected := range []string{
		gitmanHookMarker,
		ciHookQueueDirName,
		`mktemp "$QUEUE_DIR/.event.XXXXXXXXXXXX"`,
		`flock -x 9`,
		`event-%020d`,
		`printf '%s\n%s\n%s\n' "$old" "$new" "$ref"`,
		`logger -t gitman-ci-hook`,
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("hook script missing %q:\n%s", expected, script)
		}
	}
	if strings.Contains(script, "curl ") {
		t.Fatalf("durable hook must not depend on synchronous HTTP delivery:\n%s", script)
	}
	if strings.Contains(script, "secret") || strings.Contains(script, "token") {
		t.Fatalf("durable hook must not contain credentials:\n%s", script)
	}
}

func TestSupportedSSHKeyTypesRejectDSAAndCertificates(t *testing.T) {
	for _, keyType := range []string{
		"ssh-rsa",
		"ecdsa-sha2-nistp256",
		"ecdsa-sha2-nistp384",
		"ecdsa-sha2-nistp521",
		"ssh-ed25519",
		"sk-ecdsa-sha2-nistp256@openssh.com",
		"sk-ssh-ed25519@openssh.com",
	} {
		if !supportedSSHKeyType(keyType) {
			t.Errorf("supported key type %q was rejected", keyType)
		}
	}
	for _, keyType := range []string{"ssh-dss", "ssh-ed25519-cert-v01@openssh.com", "unknown"} {
		if supportedSSHKeyType(keyType) {
			t.Errorf("unsupported key type %q was accepted", keyType)
		}
	}
}

func TestBuildHookScriptQueuesOnlyUpdatedBranchesAndTags(t *testing.T) {
	hooksDir := t.TempDir()
	hookPath := filepath.Join(hooksDir, "post-receive")
	if err := os.WriteFile(hookPath, []byte(buildHookScript("owner", "repo")), 0o700); err != nil {
		t.Fatal(err)
	}
	oldCommit := strings.Repeat("a", 40)
	branchCommit := strings.Repeat("b", 40)
	tagCommit := strings.Repeat("c", 40)
	input := strings.Join([]string{
		oldCommit + " " + branchCommit + " refs/heads/main",
		oldCommit + " " + tagCommit + " refs/tags/v1.0.0",
		oldCommit + " " + strings.Repeat("0", 40) + " refs/heads/deleted",
		oldCommit + " " + branchCommit + " refs/notes/test",
	}, "\n") + "\n"
	cmd := exec.Command(hookPath)
	cmd.Stdin = strings.NewReader(input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hook failed: %v\n%s", err, out)
	}

	entries, err := os.ReadDir(filepath.Join(hooksDir, ciHookQueueDirName))
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "event-") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(hooksDir, ciHookQueueDirName, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, string(data))
	}
	if len(events) != 2 {
		t.Fatalf("queued event count = %d, want 2", len(events))
	}
	joined := strings.Join(events, "\n")
	for _, expected := range []string{branchCommit + "\nrefs/heads/main", tagCommit + "\nrefs/tags/v1.0.0"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("queued events missing %q: %q", expected, joined)
		}
	}
}

func TestDetectHookState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "post-receive")
	if got := detectHookState(path); got != hookAbsent {
		t.Fatalf("expected absent, got %s", got)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho custom\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := detectHookState(path); got != hookUnmanaged {
		t.Fatalf("expected unmanaged, got %s", got)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+gitmanHookMarker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := detectHookState(path); got != hookManaged {
		t.Fatalf("expected managed, got %s", got)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n# Managed by Gitman CI/CD. Schema: 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := detectHookState(path); got != hookOutdated {
		t.Fatalf("expected outdated, got %s", got)
	}
}

func TestLoginLimiterUsernameAndSuccessReset(t *testing.T) {
	now := time.Unix(1000, 0)
	limiter := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < loginUsernameLimit; i++ {
		if ok, _ := limiter.allow("Alice", "192.0.2.1"); !ok {
			t.Fatalf("attempt %d blocked too early", i)
		}
		limiter.recordFailure("Alice", "192.0.2.1")
	}
	if ok, retry := limiter.allow(" alice ", "192.0.2.2"); ok || retry <= 0 {
		t.Fatalf("expected username block, ok=%v retry=%s", ok, retry)
	}
	limiter.recordSuccess("ALICE")
	if ok, _ := limiter.allow("alice", "192.0.2.2"); !ok {
		t.Fatal("success did not reset username limiter")
	}
}

func TestClientIPTrustsProxyOnlyWhenConfigured(t *testing.T) {
	app := &App{Config: &config.Config{}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "198.51.100.10:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	if got := app.clientIP(req); got != "198.51.100.10" {
		t.Fatalf("proxy header was trusted while disabled: %s", got)
	}
	app.Config.TrustProxyHeaders = true
	if got := app.clientIP(req); got != "203.0.113.5" {
		t.Fatalf("trusted proxy header was not used: %s", got)
	}
}

func setupCIRefRepo(t *testing.T, reposPath string, owner *models.User, repo *models.Repository) (mainCommit, devCommit, tagCommit string) {
	t.Helper()
	repoPath, err := git.SecureRepoPath(reposPath, owner.Username, repo.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := git.InitBareRepo(context.Background(), repoPath, 512*1024*1024); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", work}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	clone := exec.Command("git", "clone", repoPath, work)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone failed: %v\n%s", err, out)
	}
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(work, ".gitman-ci.yml"), []byte("image: alpine\nsteps:\n- name: main\n  run: echo main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("checkout", "-b", "main")
	runGit("add", ".gitman-ci.yml")
	runGit("commit", "-m", "main")
	mainCommit = runGit("rev-parse", "HEAD")
	runGit("tag", "v1.0.0")
	tagCommit = mainCommit
	runGit("checkout", "-b", "development")
	if err := os.WriteFile(filepath.Join(work, ".gitman-ci.yml"), []byte("image: alpine\nsteps:\n- name: dev\n  run: echo dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".gitman-ci.yml")
	runGit("commit", "-m", "development")
	devCommit = runGit("rev-parse", "HEAD")
	runGit("push", "origin", "main", "development", "v1.0.0")
	return mainCommit, devCommit, tagCommit
}

func TestNormalizeCITriggerManualRefs(t *testing.T) {
	app := setupTestApp(t)
	owner, _ := app.DB.GetUserByUsername(context.Background(), "testuser")
	repoID, err := app.DB.CreateRepository(context.Background(), owner.ID, "refs", "", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := app.DB.GetRepositoryByID(context.Background(), repoID)
	_, devCommit, tagCommit := setupCIRefRepo(t, app.Config.ReposPath, owner, repo)
	got, err := normalizeCITrigger(context.Background(), app.Config.ReposPath, owner, repo, triggerRequest{Branch: "development"}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitHash != devCommit {
		t.Fatalf("development resolved to %s, want %s", got.CommitHash, devCommit)
	}
	got, err = normalizeCITrigger(context.Background(), app.Config.ReposPath, owner, repo, triggerRequest{Tag: "v1.0.0"}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitHash != tagCommit {
		t.Fatalf("tag resolved to %s, want %s", got.CommitHash, tagCommit)
	}
	if _, err := normalizeCITrigger(context.Background(), app.Config.ReposPath, owner, repo, triggerRequest{Branch: "main", CommitHash: devCommit}, "manual"); err == nil {
		t.Fatal("unreachable branch commit was accepted")
	}
}

func TestServeArtifactNestedAndRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	artifactDir := filepath.Join(root, "files", "owner", "repo", "run", "attempt", "reports")
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, "coverage.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/artifact", nil)
	serveArtifact(w, r, root, "owner", "repo", "run", "attempt", "reports/coverage.txt")
	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Fatalf("nested artifact response: status=%d body=%q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("expected no-store, got %q", got)
	}

	w = httptest.NewRecorder()
	serveArtifact(w, r, root, "owner", "repo", "run", "attempt", "../outside")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected traversal rejection, got %d", w.Code)
	}
}
func TestRenderPageBuffersTemplateErrors(t *testing.T) {
	bad := template.Must(template.New("base.html").Parse(`{{define "base.html"}}prefix{{.Data.Missing}}{{end}}`))
	app := &App{Config: &config.Config{}, Templates: map[string]*template.Template{"bad.html": bad}}
	req := httptest.NewRequest(http.MethodGet, "/bad", nil)
	w := httptest.NewRecorder()
	app.renderPage(w, req, "bad.html", PageData{Data: struct{}{}})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
	if body := w.Body.String(); body != "Internal Server Error\n" {
		t.Fatalf("template output leaked before error response: %q", body)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("expected no-store, got %q", got)
	}
}

func TestRepoNavRendersCIPageWithoutCurrentRefField(t *testing.T) {
	app := setupTestApp(t)
	owner, err := app.DB.GetUserByUsername(context.Background(), "testuser")
	if err != nil || owner == nil {
		t.Fatal("owner not found")
	}
	repoID, err := app.DB.CreateRepository(context.Background(), owner.ID, "repo", "", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := app.DB.GetRepositoryByID(context.Background(), repoID)
	if err != nil {
		t.Fatal(err)
	}
	partial, err := os.ReadFile(filepath.Join("..", "..", "templates", "partials", "repo_nav.html"))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := template.Must(template.New("base.html").Funcs(templateFuncs).Parse(`{{define "base.html"}}{{template "repo_nav" .}}{{end}}` + string(partial)))
	app.Templates["repo_ci.html"] = tmpl

	req := httptest.NewRequest(http.MethodGet, "/testuser/repo/ci?ref=feature/a", nil)
	ctx := context.WithValue(req.Context(), userContextKey, owner)
	ctx = context.WithValue(ctx, repoContextKey, repo)
	ctx = context.WithValue(ctx, repoOwnerContextKey, owner)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	app.renderPage(w, req, "repo_ci.html", PageData{User: owner, Data: CIPageData{Owner: owner, Repository: repo}})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"/testuser/repo/tree?ref=feature%2fa", "/testuser/repo/commits?ref=feature%2fa", "CI/CD", "Secrets"} {
		if !strings.Contains(body, want) {
			t.Fatalf("navigation missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "%252f") {
		t.Fatalf("navigation ref was double escaped: %s", body)
	}
}

func TestRepoNavHidesCIFromNonMember(t *testing.T) {
	app := setupTestApp(t)
	owner, _ := app.DB.GetUserByUsername(context.Background(), "testuser")
	viewer, err := app.DB.CreateUser(context.Background(), "viewer", "ViewerPass1")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := app.DB.CreateRepository(context.Background(), owner.ID, "public", "", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := app.DB.GetRepositoryByID(context.Background(), repoID)
	req := httptest.NewRequest(http.MethodGet, "/testuser/public/tree", nil)
	ctx := context.WithValue(req.Context(), userContextKey, viewer)
	ctx = context.WithValue(ctx, repoContextKey, repo)
	ctx = context.WithValue(ctx, repoOwnerContextKey, owner)
	nav := app.repoNavData(req.WithContext(ctx), "main")
	if nav == nil || nav.CanViewCI || nav.IsOwner {
		t.Fatalf("non-member unexpectedly received CI navigation: %+v", nav)
	}
}

func TestWebhookAuthRejectsMismatchedRoute(t *testing.T) {
	app := setupTestApp(t)
	owner, _ := app.DB.GetUserByUsername(context.Background(), "testuser")
	repoID, err := app.DB.CreateRepository(context.Background(), owner.ID, "repo", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.DB.SetWebhookSecret(context.Background(), repoID, "hook-secret"); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Post("/repos/{username}/{repo_name}/ci/webhook", app.WebhookAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP)

	for _, tc := range []struct {
		path string
		want int
	}{
		{path: "/repos/testuser/repo/ci/webhook", want: http.StatusNoContent},
		{path: "/repos/testuser/other/ci/webhook", want: http.StatusUnauthorized},
		{path: "/repos/other/repo/ci/webhook", want: http.StatusUnauthorized},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, nil)
		req.Header.Set("X-Gitman-Webhook-Secret", "hook-secret")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("%s: expected %d, got %d", tc.path, tc.want, w.Code)
		}
	}
}

func TestCISecretPreservesWhitespace(t *testing.T) {
	app := setupTestApp(t)
	owner, _ := app.DB.GetUserByUsername(context.Background(), "testuser")
	repoID, err := app.DB.CreateRepository(context.Background(), owner.ID, "repo", "", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := app.DB.GetRepositoryByID(context.Background(), repoID)
	app.Templates["repo_ci_secrets.html"] = template.Must(template.New("ci_secrets_panel").Parse(`{{define "ci_secrets_panel"}}ok{{end}}`))
	form := url.Values{"key": {"TOKEN"}, "value": {"  keep spaces  "}}
	req := httptest.NewRequest(http.MethodPost, "/testuser/repo/ci/secrets", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := context.WithValue(req.Context(), userContextKey, owner)
	ctx = context.WithValue(ctx, repoContextKey, repo)
	ctx = context.WithValue(ctx, repoOwnerContextKey, owner)
	w := httptest.NewRecorder()
	app.HandleCISecretsAddPOST(w, req.WithContext(ctx))
	secrets, err := app.DB.GetRepoSecrets(context.Background(), repoID)
	if err != nil || len(secrets) != 1 {
		t.Fatalf("secret was not stored: %v %v", secrets, err)
	}
	value, err := db.DecryptSecret(app.Config.SecretKey, secrets[0].EncryptedValue)
	if err != nil {
		t.Fatal(err)
	}
	if value != "  keep spaces  " {
		t.Fatalf("secret whitespace changed: %q", value)
	}
}

func TestLimitRequestBodyRejectsOversizedUIRequest(t *testing.T) {
	handler := limitRequestBody(4)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("12345"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", w.Code)
	}
}
