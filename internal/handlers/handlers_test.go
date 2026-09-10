package handlers

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	crypto_ssh "golang.org/x/crypto/ssh"
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
		Port:               "8080",
		ReposPath:          t.TempDir(),
		SSHUser:            "git",
		ServerHost:         "localhost",
		ArtifactsPath:      t.TempDir(),
		SecretKey:          "testsecretkey",
		AuthKeysPath:       filepath.Join(t.TempDir(), "authorized_keys"),
		BinaryPath:         "/usr/local/bin/gitman",
		WorkerConcurrency:  1,
		GitReceiveMaxBytes: 512 * 1024 * 1024,
	}

	// Minimal templates that properly render errors and content.
	tmpl := map[string]*template.Template{
		"home.html":     template.Must(template.New("").Parse(`{{define "base.html"}}home{{end}}`)),
		"login.html":    template.Must(template.New("").Parse(`{{define "base.html"}}{{if .Error}}<div class="error">{{.Error}}</div>{{else}}login page{{end}}{{end}}`)),
		"register.html": template.Must(template.New("").Parse(`{{define "base.html"}}{{if .Error}}<div class="error">{{.Error}}</div>{{else}}register page{{end}}{{end}}`)),
		"error.html":    template.Must(template.New("").Parse(`{{define "base.html"}}ERROR:{{.Error}}{{end}}`)),
		"repos.html": template.Must(template.New("").Parse(`
			{{define "base.html"}}
			{{range .Repos}}<span>{{.Name}}</span>{{end}}
			{{end}}`)),
		"keys.html": template.Must(template.New("").Parse(`
			{{define "base.html"}}
			{{range .Keys}}<span>{{.Name}}</span>{{end}}
			{{end}}`)),
		"tokens.html": template.Must(template.New("").Parse(`
			{{define "base.html"}}
			{{range .Tokens}}<span>{{.Name}}</span>{{end}}
			{{end}}`)),
		"security.html": template.Must(template.New("").Parse(`
			{{define "base.html"}}
			{{range .Events}}<span>{{.Summary}} {{.Detail}}</span>{{end}}
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

func TestRegistrationWritesAuditEventWithoutPassword(t *testing.T) {
	app := setupTestApp(t)
	app.Config.AllowRegister = true
	const password = "RegisteredPass123"
	form := url.Values{
		"username": {"registered-audit-user"},
		"password": {password},
	}
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "192.0.2.81:9000"
	w := httptest.NewRecorder()
	app.HandleRegisterPOST(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("registration status = %d, want 303; body=%q", w.Code, w.Body.String())
	}
	user, err := app.DB.GetUserByUsername(context.Background(), "registered-audit-user")
	if err != nil {
		t.Fatal(err)
	}
	events, err := app.DB.ListAuditEvents(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != models.AuditActionUserRegistered || events[0].TargetID != user.ID {
		t.Fatalf("unexpected registration audit events: %+v", events)
	}
	if events[0].ActorUserID != user.ID || events[0].ActorUsername != user.Username || events[0].SourceIP != "192.0.2.81" {
		t.Fatalf("unexpected registration audit identity/context: %+v", events[0])
	}
	for key, value := range events[0].Metadata {
		if strings.Contains(strings.ToLower(key), "password") || value == password {
			t.Fatalf("password leaked into registration audit metadata: %+v", events[0].Metadata)
		}
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
	wants := map[string]int{
		"/health":     http.StatusOK,
		"/healthz":    http.StatusOK,
		"/readyz":     http.StatusOK,
		"/ci-healthz": http.StatusServiceUnavailable, // no worker is registered in this test
	}
	for path, wantStatus := range wants {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != wantStatus {
			t.Fatalf("%s: expected %d, got %d: %s", path, wantStatus, w.Code, w.Body.String())
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
	req := httptest.NewRequest(http.MethodGet, "/api/repos/testuser/repo/artifacts/latest/branch/report.txt?ref=main", nil)
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
	extended, err := app.DB.ExtendSessionIfExpiring(context.Background(), token, time.Minute, 25*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !extended {
		t.Fatal("expected session expiry to be shortened for refresh test")
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
	if err := os.Symlink(filepath.Join(root, "reports", "coverage.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	files, err := listArtifactFiles(root)
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}
	if len(files) != 1 || files[0].Path != "reports/coverage.txt" {
		t.Fatalf("unexpected artifacts: %+v", files)
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

func TestLoginLimiterScopesUsernameFailuresToSourceIP(t *testing.T) {
	now := time.Unix(1000, 0)
	limiter := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < loginUsernameIPLimit; i++ {
		if ok, _ := limiter.allow("Alice", "192.0.2.1"); !ok {
			t.Fatalf("attempt %d blocked too early", i)
		}
		limiter.recordFailure("Alice", "192.0.2.1")
	}
	if ok, retry := limiter.allow(" alice ", "192.0.2.1"); ok || retry <= 0 {
		t.Fatalf("expected same-source username block, ok=%v retry=%s", ok, retry)
	}
	if ok, _ := limiter.allow("alice", "192.0.2.2"); !ok {
		t.Fatal("failures from one IP locked the same username from another IP")
	}
	limiter.recordSuccess("ALICE", "192.0.2.1")
	if ok, _ := limiter.allow("alice", "192.0.2.1"); !ok {
		t.Fatal("success did not reset the username/source limiter")
	}
}

func TestLoginFailuresFromOneIPDoNotLockValidLoginFromAnotherIP(t *testing.T) {
	app := setupTestApp(t)
	for i := 0; i < loginUsernameIPLimit; i++ {
		form := url.Values{"username": {"testuser"}, "password": {"WrongPass123"}}
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "192.0.2.60:9000"
		w := httptest.NewRecorder()
		app.HandleLoginPOST(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("attacker attempt %d status = %d, want 200", i, w.Code)
		}
	}

	blockedReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{
		"username": {"testuser"}, "password": {"WrongPass123"},
	}.Encode()))
	blockedReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	blockedReq.RemoteAddr = "192.0.2.60:9000"
	blockedW := httptest.NewRecorder()
	app.HandleLoginPOST(blockedW, blockedReq)
	if blockedW.Code != http.StatusTooManyRequests {
		t.Fatalf("attacker source status = %d, want 429", blockedW.Code)
	}

	validForm := url.Values{"username": {"testuser"}, "password": {"TestPass123"}}
	validReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(validForm.Encode()))
	validReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	validReq.RemoteAddr = "198.51.100.70:9000"
	validW := httptest.NewRecorder()
	app.HandleLoginPOST(validW, validReq)
	if validW.Code != http.StatusFound {
		t.Fatalf("valid login from another IP status = %d, want 302; body=%q", validW.Code, validW.Body.String())
	}
	foundSession := false
	for _, cookie := range validW.Result().Cookies() {
		if cookie.Name == "session_token" && cookie.Value != "" {
			foundSession = true
			break
		}
	}
	if !foundSession {
		t.Fatal("valid login from another IP did not create a session")
	}
}

func TestLoginLimiterStillBlocksAbusiveSourceIP(t *testing.T) {
	now := time.Unix(1000, 0)
	limiter := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < loginIPLimit; i++ {
		username := fmt.Sprintf("user%d", i)
		if ok, _ := limiter.allow(username, "192.0.2.9"); !ok {
			t.Fatalf("attempt %d blocked too early", i)
		}
		limiter.recordFailure(username, "192.0.2.9")
	}
	if ok, retry := limiter.allow("another-user", "192.0.2.9"); ok || retry <= 0 {
		t.Fatalf("expected source IP block, ok=%v retry=%s", ok, retry)
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

	app := &App{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/artifact", nil)
	app.serveArtifact(w, r, root, "owner", "repo", "run", "attempt", "reports/coverage.txt")
	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Fatalf("nested artifact response: status=%d body=%q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("expected no-store, got %q", got)
	}

	w = httptest.NewRecorder()
	app.serveArtifact(w, r, root, "owner", "repo", "run", "attempt", "../outside")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected traversal rejection, got %d", w.Code)
	}
}
func TestRenderPageBuffersTemplateErrors(t *testing.T) {
	bad := template.Must(template.New("base.html").Parse(`{{define "base.html"}}prefix{{.Missing}}{{end}}`))
	app := &App{Config: &config.Config{}, Templates: map[string]*template.Template{"bad.html": bad}}
	req := httptest.NewRequest(http.MethodGet, "/bad", nil)
	w := httptest.NewRecorder()
	app.renderPage(w, req, "bad.html", &PageData{})
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
	app.renderPage(w, req, "repo_ci.html", &CIPageData{PageData: PageData{User: owner}, Owner: owner, Repository: repo})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	escapedRef := url.QueryEscape("feature/a")
	for _, want := range []string{"/testuser/repo/tree?ref=" + escapedRef, "/testuser/repo/commits?ref=" + escapedRef, ">CI<", ">Settings<"} {
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

func TestCISecretPreservesWhitespace(t *testing.T) {
	app := setupTestApp(t)
	owner, _ := app.DB.GetUserByUsername(context.Background(), "testuser")
	repoID, err := app.DB.CreateRepository(context.Background(), owner.ID, "repo", "", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := app.DB.GetRepositoryByID(context.Background(), repoID)
	app.Templates["repo_ci_settings.html"] = template.Must(template.New("ci_secrets_panel").Parse(`{{define "ci_secrets_panel"}}ok{{end}}`))
	form := url.Values{"key": {"TOKEN"}, "value": {"  keep spaces  "}}
	req := httptest.NewRequest(http.MethodPost, "/testuser/repo/settings/ci/secrets", strings.NewReader(form.Encode()))
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
	events, err := app.DB.ListAuditEvents(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != models.AuditActionCISecretUpserted || events[0].Metadata["key"] != "TOKEN" {
		t.Fatalf("missing CI secret audit event: %+v", events)
	}
	for _, metadataValue := range events[0].Metadata {
		if strings.Contains(metadataValue, "keep spaces") {
			t.Fatalf("CI secret value leaked into audit metadata: %+v", events[0].Metadata)
		}
	}
}

func TestLimitRequestBodyRejectsOversizedUIRequest(t *testing.T) {
	app := setupTestApp(t)
	handler := app.limitRequestBody(4)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("12345"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", w.Code)
	}
}

func TestCSRFMiddlewarePreservesValidatedTokenInPOSTContext(t *testing.T) {
	app := setupTestApp(t)
	const token = "csrf-test-token"
	handler := app.CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := r.Context().Value(csrfTokenKey).(string)
		if got != token {
			t.Fatalf("csrf token context = %q, want %q", got, token)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("csrf_token="+token))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: token})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body=%q", w.Code, w.Body.String())
	}
}

func TestSecurityHeadersDoNotRequireInlineScriptOrStyle(t *testing.T) {
	app := setupTestApp(t)
	handler := app.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	csp := w.Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "'unsafe-inline'") {
		t.Fatalf("CSP still permits inline script/style: %q", csp)
	}
	for _, want := range []string{"script-src 'self'", "style-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP missing %q: %q", want, csp)
		}
	}
}

func TestBearerTokenSchemeIsCaseInsensitive(t *testing.T) {
	for _, header := range []string{"Bearer abc123", "bearer abc123", "BEARER abc123"} {
		token, ok := bearerToken(header)
		if !ok || token != "abc123" {
			t.Fatalf("bearerToken(%q) = %q, %v", header, token, ok)
		}
	}
	for _, header := range []string{"", "Basic abc123", "Bearer", "Bearer ", "Bearer a b", "Bearer a\tb"} {
		if token, ok := bearerToken(header); ok {
			t.Fatalf("bearerToken(%q) unexpectedly accepted %q", header, token)
		}
	}
}

func TestLogoutDoesNotHideSessionStoreFailure(t *testing.T) {
	app := setupTestApp(t)
	cookie := loginUser(t, app)
	if err := app.DB.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	app.HandleLogout(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("logout status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	cleared := false
	for _, c := range w.Result().Cookies() {
		if c.Name == "session_token" && c.MaxAge == -1 {
			cleared = true
			break
		}
	}
	if !cleared {
		t.Fatal("logout did not clear browser session cookie after revocation failure")
	}
}

func TestRepositoryWebDBFailureIsUnavailableNotNotFound(t *testing.T) {
	app := setupTestApp(t)
	if err := app.DB.Close(); err != nil {
		t.Fatal(err)
	}
	router := SetupRouter(app)
	req := httptest.NewRequest(http.MethodGet, "/testuser/missing", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
}

func TestTrustedForwardedHeadersUseExactParameters(t *testing.T) {
	app := &App{Config: &config.Config{TrustProxyHeaders: true}}
	req := httptest.NewRequest(http.MethodGet, "http://gitman.test/", nil)
	req.Header.Set("Forwarded", `for="203.0.113.8:4321";proto=https, for=198.51.100.9;proto=http`)
	if got := app.clientIP(req); got != "203.0.113.8" {
		t.Fatalf("forwarded client IP = %q, want 203.0.113.8", got)
	}
	if !app.requestIsHTTPS(req) {
		t.Fatal("exact Forwarded proto=https was not recognized")
	}

	req.Header.Set("Forwarded", `for=203.0.113.8;notproto=https`)
	if app.requestIsHTTPS(req) {
		t.Fatal("substring notproto=https was incorrectly trusted as proto=https")
	}
}

func withURLParam(req *http.Request, key, value string) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

func auditActionCount(t *testing.T, app *App, action string) int {
	t.Helper()
	events, err := app.DB.ListAuditEvents(context.Background(), 500)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Action == action {
			count++
		}
	}
	return count
}

func TestOwnerOnlySettingsPOSTRejectBeforeProtectedDataRender(t *testing.T) {
	app := setupTestApp(t)
	ctx := context.Background()
	owner, err := app.DB.GetUserByUsername(ctx, "testuser")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := app.DB.CreateUser(ctx, "viewer", "ViewerPass1")
	if err != nil {
		t.Fatal(err)
	}
	secretUser, err := app.DB.CreateUser(ctx, "sensitive-collaborator", "SensitivePass1")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := app.DB.CreateRepository(ctx, owner.ID, "protected-settings", "", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := app.DB.GetRepositoryByID(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.DB.AddCollaborator(ctx, repo.ID, viewer.ID, models.AccessRead); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.AddCollaborator(ctx, repo.ID, secretUser.ID, models.AccessWrite); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.AddRepoSecret(ctx, repo.ID, "SENSITIVE_SECRET_NAME", "ciphertext"); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.UpsertRepoCIRefRule(ctx, models.RepoCIRefRule{
		RepoID: repo.ID, RefType: models.CIRefBranch, RefName: "private-release/*", AllowSecrets: true,
	}); err != nil {
		t.Fatal(err)
	}
	app.Templates["repo_collaborators.html"] = template.Must(template.New("").Parse(`{{define "base.html"}}{{range .Collaborators}}{{.User.Username}}{{end}}{{end}}`))
	app.Templates["repo_ci_settings.html"] = template.Must(template.New("").Parse(`{{define "base.html"}}{{range .Secrets}}{{.Key}}{{end}}{{range .RefRules}}{{.RefName}}{{end}}{{end}}`))

	baseContext := func(req *http.Request) *http.Request {
		rctx := context.WithValue(req.Context(), userContextKey, viewer)
		rctx = context.WithValue(rctx, repoContextKey, repo)
		rctx = context.WithValue(rctx, repoOwnerContextKey, owner)
		return req.WithContext(rctx)
	}
	tests := []struct {
		name    string
		path    string
		handler http.HandlerFunc
	}{
		{"collaborator add", "/testuser/protected-settings/settings/access/add", app.HandleRepoCollaboratorsAddPOST},
		{"collaborator remove", "/testuser/protected-settings/settings/access/remove?user_id=" + secretUser.ID, app.HandleRepoCollaboratorsRemovePOST},
		{"ci secret add", "/testuser/protected-settings/settings/ci/secrets", app.HandleCISecretsAddPOST},
		{"ci secret delete", "/testuser/protected-settings/settings/ci/secrets/missing/delete", app.HandleCISecretsDeletePOST},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := baseContext(httptest.NewRequest(http.MethodPost, tc.path, nil))
			w := httptest.NewRecorder()
			tc.handler(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%q", w.Code, w.Body.String())
			}
			body := w.Body.String()
			if strings.Contains(body, secretUser.Username) || strings.Contains(body, "SENSITIVE_SECRET_NAME") || strings.Contains(body, "private-release/") {
				t.Fatalf("protected settings data leaked in denial response: %q", body)
			}
		})
	}
}

func TestOwnerOnlySettingsRoutesRejectNonOwnerEndToEnd(t *testing.T) {
	app := setupTestApp(t)
	ctx := context.Background()
	owner, err := app.DB.GetUserByUsername(ctx, "testuser")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := app.DB.CreateUser(ctx, "route-settings-viewer", "ViewerPass1")
	if err != nil {
		t.Fatal(err)
	}
	secretUser, err := app.DB.CreateUser(ctx, "route-sensitive-user", "SensitivePass1")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := app.DB.CreateRepository(ctx, owner.ID, "route-protected-settings", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.DB.AddCollaborator(ctx, repoID, secretUser.ID, models.AccessWrite); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.AddRepoSecret(ctx, repoID, "ROUTE_SECRET_NAME", "ciphertext"); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.UpsertRepoCIRefRule(ctx, models.RepoCIRefRule{
		RepoID: repoID, RefType: models.CIRefBranch, RefName: "route-private/*", AllowSecrets: true,
	}); err != nil {
		t.Fatal(err)
	}

	// If owner authorization regresses and a settings renderer becomes reachable,
	// these templates make the protected values visible to the assertion below.
	app.Templates["repo_collaborators.html"] = template.Must(template.New("").Parse(`{{define "base.html"}}{{range .Collaborators}}{{.User.Username}}{{end}}{{end}}`))
	app.Templates["repo_ci_settings.html"] = template.Must(template.New("").Parse(`{{define "base.html"}}{{range .Secrets}}{{.Key}}{{end}}{{range .RefRules}}{{.RefName}}{{end}}{{end}}`))

	session, err := app.DB.CreateSession(ctx, viewer.ID)
	if err != nil {
		t.Fatal(err)
	}
	router := SetupRouter(app)
	const csrf = "phase1-owner-boundary-csrf"
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"collaborators get", http.MethodGet, "/testuser/route-protected-settings/settings/access"},
		{"ci settings get", http.MethodGet, "/testuser/route-protected-settings/settings/ci"},
		{"collaborator add", http.MethodPost, "/testuser/route-protected-settings/settings/access/add"},
		{"collaborator remove", http.MethodPost, "/testuser/route-protected-settings/settings/access/remove"},
		{"ci secret add", http.MethodPost, "/testuser/route-protected-settings/settings/ci/secrets"},
		{"ci secret delete", http.MethodPost, "/testuser/route-protected-settings/settings/ci/secrets/missing/delete"},
		{"ci rule upsert", http.MethodPost, "/testuser/route-protected-settings/settings/ci/rules"},
		{"ci rule delete", http.MethodPost, "/testuser/route-protected-settings/settings/ci/rules/delete"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body *strings.Reader
			if tc.method == http.MethodPost {
				body = strings.NewReader("csrf_token=" + url.QueryEscape(csrf))
			} else {
				body = strings.NewReader("")
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			if tc.method == http.MethodPost {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			req.AddCookie(&http.Cookie{Name: "session_token", Value: session})
			req.AddCookie(&http.Cookie{Name: "csrf_token", Value: csrf})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%q", w.Code, w.Body.String())
			}
			responseBody := w.Body.String()
			for _, protected := range []string{secretUser.Username, "ROUTE_SECRET_NAME", "route-private/"} {
				if strings.Contains(responseBody, protected) {
					t.Fatalf("protected value %q leaked in denial response: %q", protected, responseBody)
				}
			}
		})
	}
}

func TestTokenCreationUsesFiniteExpiryAndWritesSafeAuditEvent(t *testing.T) {
	app := setupTestApp(t)
	user, err := app.DB.GetUserByUsername(context.Background(), "testuser")
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"name": {"release laptop"}, "expires_in_days": {"30"}}
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "192.0.2.25:1234"
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	before := time.Now()
	w := httptest.NewRecorder()
	app.HandleTokensPOST(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%q", w.Code, w.Body.String())
	}
	tokens, err := app.DB.GetUserAccessTokens(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].ExpiresAt == nil {
		t.Fatalf("new token does not have an expiration: %+v", tokens)
	}
	if tokens[0].Scope != models.AccessTokenScopeRepoRead {
		t.Fatalf("new token default scope = %q, want repo:read", tokens[0].Scope)
	}
	// Token expiry is persisted with one-second granularity (stored as a Unix
	// timestamp), so allow the stored value to sit up to a second below the
	// pre-call lower bound.
	minExpiry, maxExpiry := before.Add(-time.Second).AddDate(0, 0, 30), time.Now().AddDate(0, 0, 30).Add(time.Second)
	if tokens[0].ExpiresAt.Before(minExpiry) || tokens[0].ExpiresAt.After(maxExpiry) {
		t.Fatalf("token expiry %v outside expected range [%v, %v]", tokens[0].ExpiresAt, minExpiry, maxExpiry)
	}

	events, err := app.DB.ListAuditEvents(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != models.AuditActionTokenCreated {
		t.Fatalf("missing token-created audit event: %+v", events)
	}
	if events[0].ActorUserID != user.ID || events[0].SourceIP != "192.0.2.25" || events[0].TargetID != tokens[0].ID {
		t.Fatalf("unexpected audit event identity/context: %+v", events[0])
	}
	if events[0].Metadata["scope"] != string(models.AccessTokenScopeRepoRead) {
		t.Fatalf("token audit scope = %q", events[0].Metadata["scope"])
	}
	for key, value := range events[0].Metadata {
		if strings.Contains(key, "token") || strings.HasPrefix(value, "gm_") {
			t.Fatalf("audit metadata contains token material: %q=%q", key, value)
		}
	}

	revokeReq := httptest.NewRequest(http.MethodPost, "/tokens/"+tokens[0].ID+"/delete", nil)
	revokeReq = withURLParam(revokeReq, "id", tokens[0].ID)
	revokeReq = revokeReq.WithContext(context.WithValue(revokeReq.Context(), userContextKey, user))
	revokeW := httptest.NewRecorder()
	app.HandleTokenDeletePOST(revokeW, revokeReq)
	if revokeW.Code != http.StatusOK {
		t.Fatalf("revoke status = %d, body=%q", revokeW.Code, revokeW.Body.String())
	}
	if got := auditActionCount(t, app, models.AuditActionTokenRevoked); got != 1 {
		t.Fatalf("token revoke audit count = %d", got)
	}
}

func TestTokenExpirationValidation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, days := range []string{"30", "90", "180", "365", ""} {
		if _, err := tokenExpiration(days, now); err != nil {
			t.Fatalf("expiration %q rejected: %v", days, err)
		}
	}
	for _, days := range []string{"0", "1", "366", "forever"} {
		if _, err := tokenExpiration(days, now); err == nil {
			t.Fatalf("expiration %q unexpectedly accepted", days)
		}
	}
}

func TestBearerAuthRejectsExpiredToken(t *testing.T) {
	app := setupTestApp(t)
	user, err := app.DB.GetUserByUsername(context.Background(), "testuser")
	if err != nil {
		t.Fatal(err)
	}
	const plainToken = "gm_expired_bearer"
	hash := sha256.Sum256([]byte(plainToken))
	expiresAt := time.Now().Add(-time.Minute)
	if _, err := app.DB.CreateAccessToken(context.Background(), user.ID, "expired bearer", hex.EncodeToString(hash[:]), &expiresAt); err != nil {
		t.Fatal(err)
	}

	calledWithUser := false
	handler := app.AuthMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		calledWithUser = GetUser(r) != nil
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Authorization", "Bearer "+plainToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if calledWithUser {
		t.Fatal("expired bearer token authenticated a request")
	}
}

func TestAuditWriteSurvivesRequestCancellationAfterMutation(t *testing.T) {
	app := setupTestApp(t)
	user, err := app.DB.GetUserByUsername(context.Background(), "testuser")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/tokens", nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)

	app.recordAuditEvent(req, user, models.AuditActionTokenCreated, "access_token", "cancelled-request-token", nil)

	events, err := app.DB.ListAuditEvents(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != models.AuditActionTokenCreated || events[0].TargetID != "cancelled-request-token" {
		t.Fatalf("audit event was lost after request cancellation: %+v", events)
	}
}

func TestSuccessfulLoginWritesAuditEvent(t *testing.T) {
	app := setupTestApp(t)
	_ = loginUser(t, app)
	if got := auditActionCount(t, app, models.AuditActionLoginSucceeded); got != 1 {
		t.Fatalf("login audit count = %d, want 1", got)
	}
}

func TestFailedLoginWritesSafeAuditEvent(t *testing.T) {
	app := setupTestApp(t)
	const password = "DefinitelyWrong123"
	for _, username := range []string{"testuser", "missing-user"} {
		form := url.Values{"username": {username}, "password": {password}}
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "192.0.2.44:9000"
		w := httptest.NewRecorder()
		app.HandleLoginPOST(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("failed login for %q status = %d", username, w.Code)
		}
	}

	events, err := app.DB.ListAuditEvents(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	failed := make([]models.AuditEvent, 0, 2)
	for _, event := range events {
		if event.Action == models.AuditActionLoginFailed {
			failed = append(failed, event)
		}
	}
	if len(failed) != 2 {
		t.Fatalf("failed-login audit events = %d, want 2: %+v", len(failed), events)
	}
	for _, event := range failed {
		if event.ActorUserID != "" || event.ActorUsername != "" {
			t.Fatalf("failed login was attributed to an authenticated actor: %+v", event)
		}
		if event.SourceIP != "192.0.2.44" {
			t.Fatalf("failed login source IP = %q", event.SourceIP)
		}
		for key, value := range event.Metadata {
			if strings.Contains(strings.ToLower(key), "password") || value == password {
				t.Fatalf("password leaked into failed-login audit metadata: %+v", event.Metadata)
			}
		}
	}
}

func TestRateLimitedLoginDoesNotKeepAppendingFailureAuditEvents(t *testing.T) {
	app := setupTestApp(t)
	for i := 0; i < loginUsernameIPLimit; i++ {
		form := url.Values{"username": {"testuser"}, "password": {"WrongPass123"}}
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "192.0.2.55:9000"
		w := httptest.NewRecorder()
		app.HandleLoginPOST(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("attempt %d status = %d", i, w.Code)
		}
	}
	if got := auditActionCount(t, app, models.AuditActionLoginFailed); got != loginUsernameIPLimit {
		t.Fatalf("failure audit count before throttle = %d", got)
	}

	form := url.Values{"username": {"testuser"}, "password": {"WrongPass123"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "192.0.2.55:9000"
	w := httptest.NewRecorder()
	app.HandleLoginPOST(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited status = %d, want 429", w.Code)
	}
	if got := auditActionCount(t, app, models.AuditActionLoginFailed); got != loginUsernameIPLimit {
		t.Fatalf("rate-limited attempt appended audit row; count = %d", got)
	}
}

func TestCollaboratorChangesWriteAuditEvents(t *testing.T) {
	app := setupTestApp(t)
	ctx := context.Background()
	owner, err := app.DB.GetUserByUsername(ctx, "testuser")
	if err != nil {
		t.Fatal(err)
	}
	collaborator, err := app.DB.CreateUser(ctx, "audit-collab", "CollabPass1")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := app.DB.CreateRepository(ctx, owner.ID, "audit-collab-repo", "", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := app.DB.GetRepositoryByID(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	app.Templates["repo_collaborators.html"] = template.Must(template.New("").Parse(`{{define "base.html"}}ok{{end}}`))

	form := url.Values{"username": {collaborator.Username}, "access_level": {string(models.AccessWrite)}}
	req := httptest.NewRequest(http.MethodPost, "/testuser/audit-collab-repo/settings/access/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rctx := context.WithValue(req.Context(), userContextKey, owner)
	rctx = context.WithValue(rctx, repoContextKey, repo)
	rctx = context.WithValue(rctx, repoOwnerContextKey, owner)
	w := httptest.NewRecorder()
	app.HandleRepoCollaboratorsAddPOST(w, req.WithContext(rctx))
	if w.Code != http.StatusOK {
		t.Fatalf("add status = %d, body=%q", w.Code, w.Body.String())
	}
	if got := auditActionCount(t, app, models.AuditActionCollaboratorUpsert); got != 1 {
		t.Fatalf("collaborator upsert audit count = %d", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/testuser/audit-collab-repo/settings/access/remove?user_id="+collaborator.ID, nil)
	rctx = context.WithValue(req.Context(), userContextKey, owner)
	rctx = context.WithValue(rctx, repoContextKey, repo)
	rctx = context.WithValue(rctx, repoOwnerContextKey, owner)
	w = httptest.NewRecorder()
	app.HandleRepoCollaboratorsRemovePOST(w, req.WithContext(rctx))
	if w.Code != http.StatusOK {
		t.Fatalf("remove status = %d, body=%q", w.Code, w.Body.String())
	}
	if got := auditActionCount(t, app, models.AuditActionCollaboratorRemoved); got != 1 {
		t.Fatalf("collaborator remove audit count = %d", got)
	}
}

func TestCISecretDeleteWritesAuditEvent(t *testing.T) {
	app := setupTestApp(t)
	ctx := context.Background()
	owner, err := app.DB.GetUserByUsername(ctx, "testuser")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := app.DB.CreateRepository(ctx, owner.ID, "audit-secret-delete", "", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := app.DB.GetRepositoryByID(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.DB.AddRepoSecret(ctx, repo.ID, "DELETE_ME", "encrypted-value"); err != nil {
		t.Fatal(err)
	}
	secrets, err := app.DB.GetRepoSecrets(ctx, repo.ID)
	if err != nil || len(secrets) != 1 {
		t.Fatalf("prepare secret: %v %+v", err, secrets)
	}
	app.Templates["repo_ci_settings.html"] = template.Must(template.New("").Parse(`{{define "base.html"}}ok{{end}}`))
	req := httptest.NewRequest(http.MethodPost, "/testuser/audit-secret-delete/settings/ci/secrets/"+secrets[0].ID+"/delete", nil)
	req = withURLParam(req, "id", secrets[0].ID)
	rctx := context.WithValue(req.Context(), userContextKey, owner)
	rctx = context.WithValue(rctx, repoContextKey, repo)
	rctx = context.WithValue(rctx, repoOwnerContextKey, owner)
	w := httptest.NewRecorder()
	app.HandleCISecretsDeletePOST(w, req.WithContext(rctx))
	if w.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body=%q", w.Code, w.Body.String())
	}
	if got := auditActionCount(t, app, models.AuditActionCISecretDeleted); got != 1 {
		t.Fatalf("CI secret delete audit count = %d", got)
	}
}

func TestRepositoryLifecycleWritesAuditEvents(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable is unavailable")
	}
	app := setupTestApp(t)
	user, err := app.DB.GetUserByUsername(context.Background(), "testuser")
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"name": {"lifecycle-audit"}, "description": {"audit test"}, "is_private": {"on"}}
	req := httptest.NewRequest(http.MethodPost, "/repos", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	w := httptest.NewRecorder()
	app.HandleReposPOST(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%q", w.Code, w.Body.String())
	}
	if got := auditActionCount(t, app, models.AuditActionRepositoryCreated); got != 1 {
		t.Fatalf("repository create audit count = %d", got)
	}
	repo, err := app.DB.GetRepositoryByOwnerAndName(context.Background(), user.ID, "lifecycle-audit")
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/repos/"+repo.ID+"/delete", nil)
	req = withURLParam(req, "id", repo.ID)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	w = httptest.NewRecorder()
	app.HandleRepoDeletePOST(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body=%q", w.Code, w.Body.String())
	}
	if got := auditActionCount(t, app, models.AuditActionRepositoryDeleted); got != 1 {
		t.Fatalf("repository delete audit count = %d", got)
	}
}

func TestRepositorySettingsAndCITrustRulesWriteAuditEvents(t *testing.T) {
	app := setupTestApp(t)
	ctx := context.Background()
	owner, err := app.DB.GetUserByUsername(ctx, "testuser")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := app.DB.CreateRepository(ctx, owner.ID, "settings-audit", "", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := app.DB.GetRepositoryByID(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	app.Templates["repo_settings.html"] = template.Must(template.New("").Parse(`{{define "base.html"}}ok{{end}}`))

	form := url.Values{"description": {"updated"}, "is_private": {"on"}}
	req := httptest.NewRequest(http.MethodPost, "/testuser/settings-audit/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rctx := context.WithValue(req.Context(), userContextKey, owner)
	rctx = context.WithValue(rctx, repoContextKey, repo)
	rctx = context.WithValue(rctx, repoOwnerContextKey, owner)
	w := httptest.NewRecorder()
	app.HandleRepoSettingsPOST(w, req.WithContext(rctx))
	if w.Code != http.StatusOK {
		t.Fatalf("settings status = %d, body=%q", w.Code, w.Body.String())
	}
	if got := auditActionCount(t, app, models.AuditActionRepositoryUpdated); got != 1 {
		t.Fatalf("repository settings audit count = %d", got)
	}

	form = url.Values{
		"ref_type":            {string(models.CIRefBranch)},
		"ref_name":            {"release/*"},
		"auto_run":            {"on"},
		"allow_secrets":       {"on"},
		"allow_docker_socket": {"on"},
	}
	req = httptest.NewRequest(http.MethodPost, "/testuser/settings-audit/settings/ci/rules", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rctx = context.WithValue(req.Context(), userContextKey, owner)
	rctx = context.WithValue(rctx, repoContextKey, repo)
	rctx = context.WithValue(rctx, repoOwnerContextKey, owner)
	w = httptest.NewRecorder()
	app.HandleCISettingsRulePOST(w, req.WithContext(rctx))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("CI rule upsert status = %d, body=%q", w.Code, w.Body.String())
	}
	if got := auditActionCount(t, app, models.AuditActionCIRefRuleUpserted); got != 1 {
		t.Fatalf("CI ref rule upsert audit count = %d", got)
	}

	form = url.Values{"ref_type": {string(models.CIRefBranch)}, "ref_name": {"release/*"}}
	req = httptest.NewRequest(http.MethodPost, "/testuser/settings-audit/settings/ci/rules/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rctx = context.WithValue(req.Context(), userContextKey, owner)
	rctx = context.WithValue(rctx, repoContextKey, repo)
	rctx = context.WithValue(rctx, repoOwnerContextKey, owner)
	w = httptest.NewRecorder()
	app.HandleCISettingsRuleDeletePOST(w, req.WithContext(rctx))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("CI rule delete status = %d, body=%q", w.Code, w.Body.String())
	}
	if got := auditActionCount(t, app, models.AuditActionCIRefRuleDeleted); got != 1 {
		t.Fatalf("CI ref rule delete audit count = %d", got)
	}
}

func TestSSHKeyChangesWriteAuditEvents(t *testing.T) {
	app := setupTestApp(t)
	user, err := app.DB.GetUserByUsername(context.Background(), "testuser")
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshKey, err := crypto_ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := strings.TrimSpace(string(crypto_ssh.MarshalAuthorizedKey(sshKey)))
	form := url.Values{"name": {"audit-key"}, "public_key": {publicKey}}
	req := httptest.NewRequest(http.MethodPost, "/keys", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	w := httptest.NewRecorder()
	app.HandleKeysPOST(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("key add status = %d, body=%q", w.Code, w.Body.String())
	}
	if got := auditActionCount(t, app, models.AuditActionSSHKeyCreated); got != 1 {
		t.Fatalf("SSH key create audit count = %d", got)
	}
	keys, err := app.DB.GetUserSSHKeys(context.Background(), user.ID)
	if err != nil || len(keys) != 1 {
		t.Fatalf("load SSH keys: %v %+v", err, keys)
	}

	req = httptest.NewRequest(http.MethodPost, "/keys/"+keys[0].ID+"/delete", nil)
	req = withURLParam(req, "id", keys[0].ID)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	w = httptest.NewRecorder()
	app.HandleKeyDeletePOST(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("key delete status = %d, body=%q", w.Code, w.Body.String())
	}
	if got := auditActionCount(t, app, models.AuditActionSSHKeyDeleted); got != 1 {
		t.Fatalf("SSH key delete audit count = %d", got)
	}
}

func TestRequireRepoOwnerBlocksNonOwnerBeforeHandler(t *testing.T) {
	app := setupTestApp(t)
	owner, err := app.DB.GetUserByUsername(context.Background(), "testuser")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := app.DB.CreateUser(context.Background(), "settings-viewer", "ViewerPass1")
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := app.DB.CreateRepository(context.Background(), owner.ID, "owner-boundary", "", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := app.DB.GetRepositoryByID(context.Background(), repoID)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	handler := app.RequireRepoOwner(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/testuser/owner-boundary/settings", nil)
	ctx := context.WithValue(req.Context(), userContextKey, viewer)
	ctx = context.WithValue(ctx, repoContextKey, repo)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req.WithContext(ctx))
	if w.Code != http.StatusForbidden || called {
		t.Fatalf("non-owner status=%d called=%v", w.Code, called)
	}

	req = httptest.NewRequest(http.MethodGet, "/testuser/owner-boundary/settings", nil)
	ctx = context.WithValue(req.Context(), userContextKey, owner)
	ctx = context.WithValue(ctx, repoContextKey, repo)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req.WithContext(ctx))
	if w.Code != http.StatusNoContent || !called {
		t.Fatalf("owner status=%d called=%v", w.Code, called)
	}
}

func TestCIHealthReportsNoWorkerWithoutExposingDetails(t *testing.T) {
	app := setupTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/ci-healthz", nil)
	w := httptest.NewRecorder()
	app.HandleCIHealth(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"component":"ci_worker"`) {
		t.Fatalf("unexpected CI health response: %s", w.Body.String())
	}
}

func TestCIHealthReportsHealthyFleetWithoutHostnames(t *testing.T) {
	app := setupTestApp(t)
	now := time.Now()
	err := app.DB.RegisterCIWorker(context.Background(), models.CIWorker{
		ID: "worker-health-test", Hostname: "sensitive.internal.example", PID: 42,
		Concurrency: 2, Healthy: true, StatusMessage: "ready", ActiveJobs: 1,
		StartedAt: now, HeartbeatAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/ci-healthz", nil)
	w := httptest.NewRecorder()
	app.HandleCIHealth(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`"healthy_workers":1`, `"active_jobs":1`, `"workers":1`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, "sensitive.internal.example") || strings.Contains(body, "ready") {
		t.Fatalf("CI health leaked worker details: %s", body)
	}
}

func TestCIHealthIgnoresStaleHeartbeat(t *testing.T) {
	app := setupTestApp(t)
	stale := time.Now().Add(-time.Minute)
	err := app.DB.RegisterCIWorker(context.Background(), models.CIWorker{
		ID: "worker-stale-test", Hostname: "runner", PID: 7, Concurrency: 1,
		Healthy: true, StatusMessage: "ready", StartedAt: stale, HeartbeatAt: stale,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/ci-healthz", nil)
	w := httptest.NewRecorder()
	app.HandleCIHealth(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected stale worker to produce 503, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"healthy_workers":1`) {
		t.Fatalf("stale worker counted healthy: %s", w.Body.String())
	}
}

func TestSecurityActivityRouteRequiresAuthentication(t *testing.T) {
	app := setupTestApp(t)
	router := SetupRouter(app)
	req := httptest.NewRequest(http.MethodGet, "/security", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
		t.Fatalf("security route without login = %d location=%q", w.Code, w.Header().Get("Location"))
	}
}

func TestSecurityActivityShowsOnlyRelevantEventsAndDoesNotRenderRawMetadata(t *testing.T) {
	app := setupTestApp(t)
	ctx := context.Background()
	user, err := app.DB.GetUserByUsername(ctx, "testuser")
	if err != nil {
		t.Fatal(err)
	}
	other, err := app.DB.CreateUser(ctx, "security-other", "OtherPass123")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.DB.RecordAuditEvent(ctx, models.AuditEvent{
		ActorUserID: user.ID, ActorUsername: user.Username,
		Action: models.AuditActionTokenCreated, TargetType: "access_token", TargetID: "own-token",
		Metadata: map[string]string{"name": "Work laptop", "raw_secret": "SHOULD_NOT_RENDER"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.RecordAuditEvent(ctx, models.AuditEvent{
		ActorUsername: "admin-cli", Action: models.AuditActionPasswordReset,
		TargetType: "user", TargetID: user.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.RecordAuditEvent(ctx, models.AuditEvent{
		ActorUserID: other.ID, ActorUsername: other.Username,
		Action: models.AuditActionTokenCreated, TargetType: "access_token", TargetID: "other-token",
		Metadata: map[string]string{"name": "Other user's token"},
	}); err != nil {
		t.Fatal(err)
	}

	cookie := loginUser(t, app)
	router := SetupRouter(app)
	req := httptest.NewRequest(http.MethodGet, "/security", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("security page status = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"Access token created", "Work laptop", "Password reset by administrator"} {
		if !strings.Contains(body, want) {
			t.Fatalf("security page missing %q: %s", want, body)
		}
	}
	for _, forbidden := range []string{"Other user's token", "SHOULD_NOT_RENDER"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("security page leaked %q: %s", forbidden, body)
		}
	}
}

func TestAccessTokenViewStates(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	activeExpiry := now.Add(30 * 24 * time.Hour)
	soonExpiry := now.Add(3 * 24 * time.Hour)
	expiredAt := now.Add(-time.Minute)

	tests := []struct {
		name  string
		token models.AccessToken
		state string
		class string
	}{
		{name: "active", token: models.AccessToken{ExpiresAt: &activeExpiry}, state: "Active", class: "success"},
		{name: "soon", token: models.AccessToken{ExpiresAt: &soonExpiry}, state: "Expires soon", class: "pending"},
		{name: "expired", token: models.AccessToken{ExpiresAt: &expiredAt}, state: "Expired", class: "failed"},
		{name: "missing expiry", token: models.AccessToken{}, state: "Expiry unavailable", class: "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := accessTokenView(tt.token, now)
			if view.State != tt.state || view.StateClass != tt.class {
				t.Fatalf("state=%q class=%q, want %q/%q", view.State, view.StateClass, tt.state, tt.class)
			}
		})
	}
}

func TestAccessTokenScopeValidation(t *testing.T) {
	for _, raw := range []string{"", "repo:read", "repo:write"} {
		scope, err := accessTokenScope(raw)
		if err != nil {
			t.Fatalf("scope %q rejected: %v", raw, err)
		}
		if raw == "" && scope != models.AccessTokenScopeRepoRead {
			t.Fatalf("default scope = %q, want repo:read", scope)
		}
	}
	for _, raw := range []string{"admin", "repo:*", "write", "repo:read repo:write"} {
		if _, err := accessTokenScope(raw); err == nil {
			t.Fatalf("invalid scope %q accepted", raw)
		}
	}
}

func TestMachineAuthAcceptsReadOnlyBearerToken(t *testing.T) {
	app := setupTestApp(t)
	user, err := app.DB.GetUserByUsername(context.Background(), "testuser")
	if err != nil {
		t.Fatal(err)
	}
	const plainToken = "gm_machine_read_scope"
	hash := sha256.Sum256([]byte(plainToken))
	expiresAt := time.Now().Add(time.Hour)
	if _, err := app.DB.CreateAccessTokenWithScope(context.Background(), user.ID, "machine read", hex.EncodeToString(hash[:]), models.AccessTokenScopeRepoRead, &expiresAt); err != nil {
		t.Fatal(err)
	}
	handler := app.AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := GetUser(r); got == nil || got.ID != user.ID {
			t.Fatalf("read-only bearer token did not resolve user: %+v", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/example", nil)
	req.Header.Set("Authorization", "Bearer "+plainToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("machine bearer status = %d", w.Code)
	}
}

func TestBrowserRoutesDoNotAcceptBearerPAT(t *testing.T) {
	app := setupTestApp(t)
	user, err := app.DB.GetUserByUsername(context.Background(), "testuser")
	if err != nil {
		t.Fatal(err)
	}
	const plainToken = "gm_browser_scope_boundary"
	hash := sha256.Sum256([]byte(plainToken))
	expiresAt := time.Now().Add(time.Hour)
	if _, err := app.DB.CreateAccessTokenWithScope(context.Background(), user.ID, "browser boundary", hex.EncodeToString(hash[:]), models.AccessTokenScopeRepoWrite, &expiresAt); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/tokens", nil)
	req.Header.Set("Authorization", "Bearer "+plainToken)
	w := httptest.NewRecorder()
	SetupRouter(app).ServeHTTP(w, req)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
		t.Fatalf("browser route accepted bearer PAT: status=%d location=%q body=%q", w.Code, w.Header().Get("Location"), w.Body.String())
	}
}
