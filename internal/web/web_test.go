package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
	"github.com/mmrzaf/gitman/internal/push"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

const testSecretKey = "a very secret passphrase, at least 32 bytes long"

type browser struct {
	t       *testing.T
	server  *httptest.Server
	cookies map[string]*http.Cookie
}

func newBrowser(t *testing.T, server *httptest.Server) *browser {
	return &browser{t: t, server: server, cookies: map[string]*http.Cookie{}}
}

// do sends a request as a same-origin browser would, keeps cookies, and
// never follows redirects, so each test sees exactly what the server
// answered.
func (b *browser) do(method, path string, form url.Values, headers map[string]string) (*http.Response, string) {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, b.server.URL+path, body)
	if err != nil {
		b.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, c := range b.cookies {
		req.AddCookie(c)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	for _, c := range resp.Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
	}
	return resp, string(data)
}

func setup(t *testing.T) (*postgres.DB, *browser) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	database := pgtest.Open(t)
	store := git.NewStore(t.TempDir())
	t.Cleanup(store.Close)
	cfg := &config.Config{DataDir: t.TempDir(), PublicURL: "http://gitman.test", Port: 8080, SecretKey: testSecretKey}
	app, err := New(cfg, testServices(database, store, cfg.SecretKey), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.handler)
	t.Cleanup(server.Close)
	return database, newBrowser(t, server)
}

func expect(t *testing.T, resp *http.Response, body string, status int, contains ...string) {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("status = %d, want %d\n%s", resp.StatusCode, status, body)
	}
	for _, c := range contains {
		if !strings.Contains(body, c) {
			t.Fatalf("body does not contain %q:\n%s", c, body)
		}
	}
}

func login(b *browser, username, password, next string) (*http.Response, string) {
	return b.do(http.MethodPost, "/login", url.Values{"username": {username}, "password": {password}, "next": {next}}, nil)
}

func TestSignInFlow(t *testing.T) {
	database, b := setup(t)
	if _, err := auth.NewService(database).Create(context.Background(), "darius", "correct-horse-battery", true, ""); err != nil {
		t.Fatal(err)
	}

	resp, _ := b.do(http.MethodGet, "/", nil, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login?next=%2F" {
		t.Fatalf("anonymous Home: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp, body := b.do(http.MethodGet, "/login", nil, nil)
	expect(t, resp, body, http.StatusOK, `name="username"`, "gitman.test")
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Error("missing Content-Security-Policy")
	}
	if resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("HTML Cache-Control = %q", resp.Header.Get("Cache-Control"))
	}

	resp, body = login(b, "darius", "wrong-password", "/")
	expect(t, resp, body, http.StatusUnauthorized, "Wrong username or password.", `value="darius"`)
	if strings.Contains(body, "wrong-password") {
		t.Fatal("the password was echoed back into the page")
	}

	resp, body = login(b, "darius", "correct-horse-battery", "//evil.example/")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("login redirect: %d %q\n%s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	session := b.cookies["gitman_session"]
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie = %+v", session)
	}

	resp, body = b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, "darius", "No repositories yet")

	resp, _ = b.do(http.MethodGet, "/login?next=/somewhere", nil, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/somewhere" {
		t.Fatalf("signed-in /login: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp, body = b.do(http.MethodGet, "/no/such/page", nil, nil)
	expect(t, resp, body, http.StatusNotFound, "Not found", "Sign out")

	// A forged cross-site submission is refused and changes nothing.
	resp, body = b.do(http.MethodPost, "/logout", url.Values{}, map[string]string{"Sec-Fetch-Site": "cross-site"})
	expect(t, resp, body, http.StatusForbidden, "another site")
	resp, _ = b.do(http.MethodGet, "/", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal("a refused cross-site logout signed the person out")
	}

	stale := *session
	resp, _ = b.do(http.MethodPost, "/logout", url.Values{}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("logout: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, body = b.do(http.MethodGet, "/login", nil, nil)
	expect(t, resp, body, http.StatusOK, "Signed out.")
	_, body = b.do(http.MethodGet, "/login", nil, nil)
	if strings.Contains(body, "Signed out.") {
		t.Fatal("the flash was shown twice")
	}

	// The old session is gone, not merely forgotten by this browser.
	b.cookies[stale.Name] = &stale
	resp, _ = b.do(http.MethodGet, "/", nil, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a signed-out session still works: %d", resp.StatusCode)
	}
	if _, kept := b.cookies[stale.Name]; kept {
		t.Error("expected the dead session cookie to be cleared")
	}
}

func TestDisabledPersonCannotSignIn(t *testing.T) {
	ctx := context.Background()
	database, b := setup(t)
	if _, err := auth.NewService(database).Create(ctx, "lead", "correct-horse-battery", true, ""); err != nil {
		t.Fatal(err)
	}
	p, err := auth.NewService(database).Create(ctx, "sara", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.NewService(database).Disable(ctx, p.ID, ""); err != nil {
		t.Fatal(err)
	}
	resp, body := login(b, "sara", "correct-horse-battery", "/")
	expect(t, resp, body, http.StatusUnauthorized, "Wrong username or password.")
}

func TestLoginIsRateLimited(t *testing.T) {
	_, b := setup(t)
	for i := 0; i < loginUsernameLimit; i++ {
		login(b, "darius", "nope", "/")
	}
	resp, body := login(b, "darius", "nope", "/")
	expect(t, resp, body, http.StatusTooManyRequests, "Too many failed attempts. Try again in 15 minutes.")
}

func TestMissingFieldsAreReportedInline(t *testing.T) {
	_, b := setup(t)
	resp, body := login(b, "", "", "/")
	expect(t, resp, body, http.StatusUnprocessableEntity, "Enter your username.", "Enter your password.", `aria-invalid="true"`)
}

func TestSignedOutActionGoesToLogin(t *testing.T) {
	_, b := setup(t)
	resp, _ := b.do(http.MethodPost, "/repos", url.Values{"name": {"demo"}}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("signed-out POST: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, body := b.do(http.MethodGet, "/login", nil, nil)
	expect(t, resp, body, http.StatusOK, "Sign in to continue.")
}

// TestSignOutWithoutASession is a browser whose session already ended —
// expired, or signed out in another tab: it is simply signed out, not
// asked to sign in so that it can sign out.
func TestSignOutWithoutASession(t *testing.T) {
	_, b := setup(t)
	resp, _ := b.do(http.MethodPost, "/logout", url.Values{}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("POST /logout: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, body := b.do(http.MethodGet, "/login", nil, nil)
	expect(t, resp, body, http.StatusOK, "Signed out.")
}

func TestAssetsAreServedForever(t *testing.T) {
	_, b := setup(t)
	_, body := b.do(http.MethodGet, "/login", nil, nil)
	start := strings.Index(body, "/assets/static/gitman.")
	if start < 0 {
		t.Fatal("login page does not link the stylesheet")
	}
	end := strings.Index(body[start:], `"`)
	resp, css := b.do(http.MethodGet, body[start:start+end], nil, nil)
	expect(t, resp, css, http.StatusOK, "--accent")
	if resp.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/css") {
		t.Errorf("asset headers: %v", resp.Header)
	}
	resp, _ = b.do(http.MethodGet, "/assets/static/gitman.css", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unfingerprinted asset name returned %d", resp.StatusCode)
	}
}

func TestHomeBoard(t *testing.T) {
	ctx := context.Background()
	database, b := setup(t)
	p, err := auth.NewService(database).Create(ctx, "darius", "correct-horse-battery", true, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO repos (id, name, description) VALUES ('r1', 'waiotech', ''), ('r2', 'cerv', '')`,
		`INSERT INTO runs (id, repo_id, number, commit_hash, trigger, status, finished_at, ref_kind, ref_name) VALUES ('run1', 'r1', 7, 'aaaaaaaaaaaa', 'push', 'passed', now(), 'branch', 'main')`,
		`INSERT INTO deployments (id, repo_id, target, version, commit_hash, run_id, person_id, created_at)
		 VALUES ('d1', 'r1', 'staging', '3f2a91cb1de0', 'aaaaaaaaaaaa', 'run1', '` + p.ID + `', now() - interval '2 hours'),
		        ('d0', 'r1', 'staging', 'old', 'bbbbbbbbbbbb', NULL, NULL, now() - interval '3 days')`,
	} {
		if _, err := database.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	login(b, "darius", "correct-horse-battery", "/")
	resp, body := b.do(http.MethodGet, "/", nil, nil)
	// One column per target anything has shipped to; a repository that
	// has shipped nothing there says so.
	expect(t, resp, body, http.StatusOK, "waiotech", "3f2a91cb1de0", ">#7</a>", "2 h ago", "cerv", `<th scope="col">staging</th>`,
		`<span class="board-none">—<span class="visually-hidden">nothing shipped</span></span>`)
	if !strings.Contains(body, `class="brand" href="/" aria-label="Gitman home"`) ||
		!strings.Contains(body, `class="menu-item" href="/people"`) || strings.Contains(body, `class="topbar-link"`) {
		t.Error("the logo must link home and People must be in the admin account menu")
	}

	// The board (what's live now) must not show a superseded deployment,
	// even though the timeline below it legitimately does — that older
	// deployment really happened.
	boardStart := strings.Index(body, `id="board-title"`)
	boardEnd := strings.Index(body, `id="timeline-title"`)
	if boardStart < 0 || boardEnd < 0 || boardEnd < boardStart {
		t.Fatal("could not locate the board section in the page")
	}
	if strings.Contains(body[boardStart:boardEnd], ">old<") {
		t.Error("the board shows a superseded deployment")
	}
}

// setupWithStore is setup, but also returns the git store — for tests
// that need to write real commits directly into a repository's bare
// directory, bypassing the HTTP Git transport (which git_http_test.go
// already covers end to end).
func setupWithStore(t *testing.T) (*postgres.DB, *git.Store, *browser) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	database := pgtest.Open(t)
	store := git.NewStore(t.TempDir())
	t.Cleanup(store.Close)
	cfg := &config.Config{DataDir: t.TempDir(), PublicURL: "http://gitman.test", Port: 8080, SecretKey: testSecretKey}
	app, err := New(cfg, testServices(database, store, cfg.SecretKey), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.handler)
	t.Cleanup(server.Close)
	return database, store, newBrowser(t, server)
}

// testServices builds the services an App works with from a database and
// a repository store, the same way the gitman binary does.
func testServices(database *postgres.DB, store *git.Store, secretKey string) Services {
	people, repos, runs := auth.NewService(database), reposvc.NewService(database, store, secretKey), ci.NewService(database)
	return Services{
		People:   people,
		Repos:    repos,
		CI:       runs,
		Refs:     push.NewRefs(database, people, repos, runs),
		Activity: activity.NewService(database),
		Ping:     database.Ping,
		Listen:   database.Listen,
	}
}
