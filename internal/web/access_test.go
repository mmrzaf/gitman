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

	"github.com/mmrzaf/gitman/internal/auth"
	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

func signIn(t *testing.T, database *postgres.DB, b *browser, username string, admin bool) {
	t.Helper()
	if _, err := auth.NewService(database).Create(context.Background(), username, "correct-horse-battery", admin, ""); err != nil {
		t.Fatal(err)
	}
	resp, body := login(b, username, "correct-horse-battery", "/")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign in as %s: %d\n%s", username, resp.StatusCode, body)
	}
}

func TestMeChangePassword(t *testing.T) {
	database, b := setup(t)
	signIn(t, database, b, "darius", false)

	resp, body := b.do(http.MethodPost, "/me/password", url.Values{
		"current_password": {"wrong"}, "new_password": {"a-new-password"}, "confirm_password": {"a-new-password"},
	}, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity, "not your current password")

	resp, body = b.do(http.MethodPost, "/me/password", url.Values{
		"current_password": {"correct-horse-battery"}, "new_password": {"short"}, "confirm_password": {"short"},
	}, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity, "at least 8 characters")

	resp, body = b.do(http.MethodPost, "/me/password", url.Values{
		"current_password": {"correct-horse-battery"}, "new_password": {"a-new-password"}, "confirm_password": {"different"},
	}, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity, "Does not match")

	resp, _ = b.do(http.MethodPost, "/me/password", url.Values{
		"current_password": {"correct-horse-battery"}, "new_password": {"a-new-password"}, "confirm_password": {"a-new-password"},
	}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("valid password change: %d", resp.StatusCode)
	}
	resp, body = b.do(http.MethodGet, "/me", nil, nil)
	expect(t, resp, body, http.StatusOK, "Password changed")

	// The new password now works from a fresh browser; the old one no
	// longer does.
	fresh := newBrowser(t, b.server)
	resp, body = login(fresh, "darius", "correct-horse-battery", "/")
	expect(t, resp, body, http.StatusUnauthorized, "Wrong username or password.")
	resp, _ = login(fresh, "darius", "a-new-password", "/")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign in with the new password: %d", resp.StatusCode)
	}
}

func TestMeTokens(t *testing.T) {
	database, b := setup(t)
	signIn(t, database, b, "darius", false)

	resp, body := b.do(http.MethodPost, "/me/tokens", url.Values{"name": {"laptop"}, "scope": {"write"}, "expires": {"never"}}, nil)
	expect(t, resp, body, http.StatusOK, "Your new token", "laptop", "write")
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("the page showing a new token has Cache-Control %q, want no-store", cc)
	}
	start := strings.Index(body, `data-copy="`) + len(`data-copy="`)
	end := strings.Index(body[start:], `"`)
	plain := body[start : start+end]
	if plain == "" {
		t.Fatal("no token value found in the reveal")
	}

	resp, body = b.do(http.MethodPost, "/me/tokens", url.Values{"name": {""}, "scope": {"read"}}, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity, "Name the token")

	tokens, err := auth.NewService(database).ListTokens(context.Background(), mustPerson(t, database, "darius").ID)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("ListTokens = %v, %v", tokens, err)
	}
	resp, body = b.do(http.MethodPost, "/me/tokens/"+tokens[0].ID+"/delete", url.Values{}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete own token: %d\n%s", resp.StatusCode, body)
	}
	resp, body = b.do(http.MethodGet, "/me", nil, nil)
	expect(t, resp, body, http.StatusOK, "Revoked", "No tokens yet")

	// Deleting a token that isn't yours, or doesn't exist, is refused.
	other, err := auth.NewService(database).Create(context.Background(), "sara", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	_, otherToken, err := auth.NewService(database).CreateToken(context.Background(), other.ID, "x", auth.ScopeRead, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, body = b.do(http.MethodPost, "/me/tokens/"+otherToken.ID+"/delete", url.Values{}, nil)
	expect(t, resp, body, http.StatusForbidden, "belong to you")
	resp, body = b.do(http.MethodPost, "/me/tokens/nonexistent/delete", url.Values{}, nil)
	expect(t, resp, body, http.StatusNotFound, "no longer exists")
}

func mustPerson(t *testing.T, database *postgres.DB, username string) *auth.Person {
	t.Helper()
	p, err := auth.NewService(database).GetByUsername(context.Background(), username)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPeopleRequiresAdmin(t *testing.T) {
	database, b := setup(t)
	signIn(t, database, b, "darius", false)
	resp, body := b.do(http.MethodGet, "/people", nil, nil)
	expect(t, resp, body, http.StatusForbidden, "Only admins")
}

func TestPeopleManagement(t *testing.T) {
	database, b := setup(t)
	signIn(t, database, b, "lead", true)

	// A generated password is shown once, in the response to the action
	// that made it, and kept out of every cache.
	resp, body := b.do(http.MethodPost, "/people", url.Values{"username": {"sara"}, "role": {"member"}}, nil)
	expect(t, resp, body, http.StatusOK, "Password for sara")
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("a page showing a password has Cache-Control %q, want no-store", cc)
	}
	resp, body = b.do(http.MethodGet, "/people", nil, nil)
	expect(t, resp, body, http.StatusOK, "sara")
	if strings.Contains(body, "Password for sara") {
		t.Error("the password was shown again on the next page")
	}

	resp, body = b.do(http.MethodPost, "/people", url.Values{"username": {"sara"}, "role": {"member"}}, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity, "already has that username")

	resp, _ = b.do(http.MethodPost, "/people/sara/role", url.Values{"role": {"admin"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("promote: %d", resp.StatusCode)
	}
	resp, body = b.do(http.MethodGet, "/people", nil, nil)
	expect(t, resp, body, http.StatusOK, "is now an admin")

	resp, _ = b.do(http.MethodPost, "/people/sara/disable", url.Values{}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("disable: %d", resp.StatusCode)
	}
	resp, body = b.do(http.MethodGet, "/people", nil, nil)
	expect(t, resp, body, http.StatusOK, "disabled")

	resp, _ = b.do(http.MethodPost, "/people/sara/enable", url.Values{}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("enable: %d", resp.StatusCode)
	}

	resp, body = b.do(http.MethodPost, "/people/sara/reset-password", url.Values{}, nil)
	expect(t, resp, body, http.StatusOK, "Password for sara")

	// Resetting one's own password here would sign this session out
	// before the new password could be shown.
	resp, _ = b.do(http.MethodPost, "/people/lead/reset-password", url.Values{}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("own reset: %d", resp.StatusCode)
	}
	resp, body = b.do(http.MethodGet, "/people", nil, nil)
	expect(t, resp, body, http.StatusOK, "To change your own password, use your account page.")
	if n := strings.Count(body, "To change your own password"); n != 1 {
		t.Errorf("the outcome is shown %d times, want once", n)
	}

	// The only enabled admin can't disable themself or step down. sara
	// was promoted earlier in this test; demote her back first so lead
	// really is the only one.
	resp, _ = b.do(http.MethodPost, "/people/sara/role", url.Values{"role": {"member"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("demote: %d", resp.StatusCode)
	}
	resp, _ = b.do(http.MethodPost, "/people/lead/disable", url.Values{}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("refused disable still redirects: %d", resp.StatusCode)
	}
	resp, body = b.do(http.MethodGet, "/people", nil, nil)
	expect(t, resp, body, http.StatusOK, "only enabled admin")

	resp, body = b.do(http.MethodPost, "/people/nonexistent/disable", url.Values{}, nil)
	expect(t, resp, body, http.StatusNotFound, "There is no person named \u201cnonexistent\u201d.")
}

func TestRepoCreateAndSettings(t *testing.T) {
	database, b := setup(t)
	signIn(t, database, b, "lead", true)

	resp, body := b.do(http.MethodPost, "/repos", url.Values{"name": {"waiotech"}, "description": {"Plant maintenance"}}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/waiotech/settings" {
		t.Fatalf("create: %d %q\n%s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	resp, body = b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, "waiotech", "Plant maintenance")

	resp, body = b.do(http.MethodPost, "/repos", url.Values{"name": {"waiotech"}}, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity, "already exists")

	resp, body = b.do(http.MethodPost, "/repos", url.Values{"name": {"people"}}, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity, "reserved")

	resp, body = b.do(http.MethodGet, "/waiotech/settings", nil, nil)
	expect(t, resp, body, http.StatusOK, "waiotech", "No rules yet", "No secrets yet")

	resp, _ = b.do(http.MethodPost, "/waiotech/settings/description", url.Values{"description": {"Updated"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("description: %d", resp.StatusCode)
	}
	resp, body = b.do(http.MethodGet, "/waiotech/settings", nil, nil)
	expect(t, resp, body, http.StatusOK, "Updated")

	resp, _ = b.do(http.MethodPost, "/waiotech/settings/rules", url.Values{
		"kind": {"branch"}, "pattern": {"main"}, "push_policy": {"everyone"}, "run_on_push": {"on"}, "allow_ship": {"on"},
	}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save rule: %d", resp.StatusCode)
	}
	resp, body = b.do(http.MethodGet, "/waiotech/settings", nil, nil)
	expect(t, resp, body, http.StatusOK, "main", "everyone", "run", "ship")

	resp, body = b.do(http.MethodPost, "/waiotech/settings/rules", url.Values{
		"kind": {"branch"}, "pattern": {"has space"}, "push_policy": {"everyone"},
	}, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity)

	// A rule that fails is shown again with everything that was chosen,
	// not only the pattern.
	lead := mustPerson(t, database, "lead")
	resp, body = b.do(http.MethodPost, "/waiotech/settings/rules", url.Values{
		"kind": {"tag"}, "pattern": {"v 1"}, "push_policy": {"people"}, "push_people": {lead.ID},
		"allow_force": {"on"}, "allow_ship": {"on"},
	}, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity,
		`name="kind" value="tag" checked>`, `name="push_policy" value="people" checked>`,
		`value="`+lead.ID+`" checked>`, `name="allow_force" checked>`, `name="allow_ship" checked>`,
		// It opens where it was: the rules tab, in the new-rule dialog.
		`data-tab="rules" aria-current="page"`, `id="rule-new" aria-labelledby="rule-new-title" data-dialog open>`)
	if strings.Contains(body, `name="allow_delete" checked`) {
		t.Error("a box that was not ticked came back ticked")
	}

	// A link opens a saved rule for editing, filled in from the rule, with
	// its branch or tag fixed: a rule is known by it.
	resp, body = b.do(http.MethodGet, "/waiotech/settings?tab=rules&dialog=rule-edit&kind=branch&pattern=main", nil, nil)
	expect(t, resp, body, http.StatusOK, `id="rule-edit" aria-labelledby="rule-edit-title" data-dialog data-dialog-params="kind pattern" open>`,
		`<input type="hidden" name="mode" value="edit">`, `<input type="hidden" name="pattern" value="main">`,
		`name="run_on_push" checked>`, `name="allow_ship" checked>`, `name="push_policy" value="everyone" checked>`)
	if strings.Contains(body, `name="allow_docker" checked`) {
		t.Error("the edited rule came back allowing Docker, which it does not")
	}
	// A rule that does not exist opens nothing.
	resp, body = b.do(http.MethodGet, "/waiotech/settings?tab=rules&dialog=rule-edit&kind=branch&pattern=nope", nil, nil)
	expect(t, resp, body, http.StatusOK)
	if strings.Contains(body, `id="rule-edit"`) {
		t.Error("an edit dialog opened for a rule that does not exist")
	}
	// An edit that fails comes back in the edit dialog.
	resp, body = b.do(http.MethodPost, "/waiotech/settings/rules", url.Values{
		"mode": {"edit"}, "kind": {"branch"}, "pattern": {"main"}, "push_policy": {"people"},
	}, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity, `id="rule-edit" aria-labelledby="rule-edit-title" data-dialog data-dialog-params="kind pattern" open>`)

	resp, _ = b.do(http.MethodPost, "/waiotech/settings/rules/delete", url.Values{"kind": {"branch"}, "pattern": {"main"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete rule: %d", resp.StatusCode)
	}
	resp, body = b.do(http.MethodGet, "/waiotech/settings", nil, nil)
	expect(t, resp, body, http.StatusOK, "No rules yet")

	resp, _ = b.do(http.MethodPost, "/waiotech/settings/secrets", url.Values{"key": {"DEPLOY_TOKEN"}, "value": {"hunter2"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save secret: %d", resp.StatusCode)
	}
	resp, body = b.do(http.MethodGet, "/waiotech/settings", nil, nil)
	expect(t, resp, body, http.StatusOK, "DEPLOY_TOKEN")
	if strings.Contains(body, "hunter2") {
		t.Fatal("the secret's plaintext value appeared in the page")
	}
	resp, body = b.do(http.MethodGet, "/waiotech/settings?tab=secrets&dialog=secret-replace&key=DEPLOY_TOKEN", nil, nil)
	expect(t, resp, body, http.StatusOK, `id="secret-replace" aria-labelledby="secret-replace-title" data-dialog data-dialog-params="key" open>`,
		`<input type="hidden" name="key" value="DEPLOY_TOKEN">`)
	if strings.Contains(body, "hunter2") {
		t.Fatal("the secret's plaintext value appeared in the replace dialog")
	}

	resp, body = b.do(http.MethodPost, "/waiotech/settings/secrets", url.Values{"key": {"bad key"}, "value": {"x"}}, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity)

	resp, _ = b.do(http.MethodPost, "/waiotech/settings/secrets/delete", url.Values{"key": {"DEPLOY_TOKEN"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete secret: %d", resp.StatusCode)
	}
	resp, body = b.do(http.MethodGet, "/waiotech/settings", nil, nil)
	expect(t, resp, body, http.StatusOK, "No secrets yet")

	resp, body = b.do(http.MethodPost, "/waiotech/settings/delete", url.Values{"confirm_name": {"wrong-name"}}, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity, "Type")

	resp, _ = b.do(http.MethodPost, "/waiotech/settings/delete", url.Values{"confirm_name": {"waiotech"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	resp, body = b.do(http.MethodGet, "/", nil, nil)
	expect(t, resp, body, http.StatusOK, "Deleted waiotech, with its history, runs and deployments.")
	if strings.Contains(body, "waiotech.git") {
		t.Fatal("the deleted repository is still listed")
	}
	if _, err := reposvc.NewService(database, nil, "").GetByName(context.Background(), "waiotech"); err == nil {
		t.Fatal("the repository record still exists")
	}
}

func TestRepoSettingsRequiresAdmin(t *testing.T) {
	database, b := setup(t)
	signIn(t, database, b, "lead", true)
	resp, _ := b.do(http.MethodPost, "/repos", url.Values{"name": {"waiotech"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", resp.StatusCode)
	}

	member := newBrowser(t, b.server)
	signIn(t, database, member, "darius", false)
	resp, body := member.do(http.MethodGet, "/waiotech/settings", nil, nil)
	expect(t, resp, body, http.StatusForbidden, "Only admins")
	resp, body = member.do(http.MethodPost, "/waiotech/settings/delete", url.Values{"confirm_name": {"waiotech"}}, nil)
	expect(t, resp, body, http.StatusForbidden, "Only admins")
}

func TestSecretsUnavailableWithoutInstanceKey(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	database, store := pgtest.Open(t), newWebStore(t)
	cfg := &config.Config{DataDir: t.TempDir(), PublicURL: "http://gitman.test", Port: 8080}
	app, err := New(cfg, testServices(database, store, cfg.SecretKey), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.handler)
	t.Cleanup(server.Close)
	b := newBrowser(t, server)
	signIn(t, database, b, "lead", true)

	if _, err := reposvc.NewService(database, store, "").Create(context.Background(), "waiotech", "", "main", ""); err != nil {
		t.Fatal(err)
	}
	resp, body := b.do(http.MethodGet, "/waiotech/settings", nil, nil)
	expect(t, resp, body, http.StatusOK, "GITMAN_SECRET_KEY is not configured")

	resp, body = b.do(http.MethodPost, "/waiotech/settings/secrets", url.Values{"key": {"X"}, "value": {"y"}}, nil)
	expect(t, resp, body, http.StatusUnprocessableEntity, "GITMAN_SECRET_KEY is not configured")
}

func newWebStore(t *testing.T) *git.Store {
	t.Helper()
	store := git.NewStore(t.TempDir())
	t.Cleanup(store.Close)
	return store
}

func TestRepositoryPage(t *testing.T) {
	database, b := setup(t)
	signIn(t, database, b, "darius", false)

	// A member, who cannot open the settings, lands on the repository.
	resp, _ := b.do(http.MethodPost, "/repos", url.Values{"name": {"waiotech"}, "description": {"Plant maintenance"}}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/waiotech" {
		t.Fatalf("create: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp, body := b.do(http.MethodGet, "/waiotech", nil, nil)
	expect(t, resp, body, http.StatusOK, "Created waiotech.")
	expect(t, resp, body, http.StatusOK, "waiotech", "Plant maintenance", "http://gitman.test/waiotech.git", "No pushes yet",
		// Nothing has shipped, but the targets region is there for the
		// first deployment to appear in on a live page.
		`<div data-live-region="targets">`, "Nothing shipped yet")
	if strings.Contains(body, "Settings") {
		t.Error("a non-admin should not see a Settings link")
	}

	repo, err := reposvc.NewService(database, nil, "").GetByName(context.Background(), "waiotech")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO refs (repo_id, kind, name, commit_hash) VALUES ($1, 'branch', 'main', repeat('a',40))`, repo.ID)
	exec(`INSERT INTO runs (id, repo_id, number, commit_hash, ref_kind, ref_name, trigger, status, finished_at)
	      VALUES ('run1', $1, 1, repeat('a',40), 'branch', 'main', 'push', 'passed', now())`, repo.ID)
	exec(`INSERT INTO deployments (id, repo_id, target, version, commit_hash, run_id, created_at)
	      VALUES ('dep1', $1, 'staging', 'v1', repeat('a',40), 'run1', now())`, repo.ID)
	exec(`INSERT INTO pushes (id, repo_id, created_at) VALUES ('push1', $1, now())`, repo.ID)
	exec(`INSERT INTO push_updates (id, push_id, kind, name, old_commit, new_commit)
	      VALUES ('pu1', 'push1', 'branch', 'main', repeat('0',40), repeat('a',40))`)

	resp, body = b.do(http.MethodGet, "/waiotech", nil, nil)
	expect(t, resp, body, http.StatusOK, "main", "default", "staging", "v1", "passed", "#1")

	admin := newBrowser(t, b.server)
	signIn(t, database, admin, "lead", true)
	resp, body = admin.do(http.MethodGet, "/waiotech", nil, nil)
	expect(t, resp, body, http.StatusOK, "Settings")

	resp, body = admin.do(http.MethodGet, "/no-such-repo", nil, nil)
	expect(t, resp, body, http.StatusNotFound, "no-such-repo")
}

// TestJumpPage is where the palette gets its destinations, and the
// palette itself without scripting: every repository with its files, and
// its settings for an admin only.
func TestJumpPage(t *testing.T) {
	database, b := setup(t)
	signIn(t, database, b, "lead", true)
	if resp, _ := b.do(http.MethodPost, "/repos", url.Values{"name": {"waiotech"}, "description": {"Plant maintenance"}}, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	resp, body := b.do(http.MethodGet, "/jump", nil, nil)
	expect(t, resp, body, http.StatusOK, `data-jump`, `href="/waiotech" data-group="Repositories"`, `data-hint="Plant maintenance"`,
		`href="/waiotech@main" data-group="Files"`, `href="/waiotech/settings" data-group="Settings"`, `href="/people" data-group="Go to"`)

	member := newBrowser(t, b.server)
	signIn(t, database, member, "darius", false)
	resp, body = member.do(http.MethodGet, "/jump", nil, nil)
	expect(t, resp, body, http.StatusOK, `href="/waiotech" data-group="Repositories"`)
	if strings.Contains(body, `/waiotech/settings`) || strings.Contains(body, `href="/people"`) || strings.Contains(body, `class="topbar-link"`) {
		t.Error("a member is offered pages only admins may open")
	}
}
