package repo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
)

func newStore(t *testing.T) *git.Store {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	store := git.NewStore(t.TempDir())
	t.Cleanup(store.Close)
	return store
}

func TestCreateGetDelete(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	store := newStore(t)
	svc := NewService(database, store, testSecretKey)

	repo, err := svc.Create(ctx, "waiotech", "  Plant maintenance  ", "develop", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if repo.Description != "Plant maintenance" || repo.DefaultBranch != "develop" {
		t.Errorf("Create = %+v", repo)
	}
	if _, err := store.Open(repo.ID); err != nil {
		t.Fatalf("repository directory missing: %v", err)
	}
	for _, name := range []string{"waiotech", "WaioTech"} {
		if _, err := svc.Create(ctx, name, "", "", ""); !errors.Is(err, postgres.ErrAlreadyExists) {
			t.Fatalf("duplicate Create(%q) = %v, want ErrAlreadyExists: names differ by more than case", name, err)
		}
	}

	got, err := svc.GetByName(ctx, "waiotech")
	if err != nil || got.ID != repo.ID {
		t.Fatalf("GetByName = %+v, %v", got, err)
	}
	list, err := svc.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %d, %v", len(list), err)
	}

	if err := svc.Delete(ctx, repo.ID, ""); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Open(repo.ID); !errors.Is(err, git.ErrNotFound) {
		t.Fatalf("directory still present after Delete: %v", err)
	}
	if _, err := svc.GetByName(ctx, "waiotech"); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("record still present after Delete: %v", err)
	}
	if err := svc.Delete(ctx, repo.ID, ""); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("second Delete = %v, want ErrNotFound", err)
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	store := newStore(t)
	svc := NewService(database, store, testSecretKey)
	cases := map[string][3]string{
		"reserved name":    {"people", "", "main"},
		"bad name":         {"-bad", "", "main"},
		"long description": {"ok", strings.Repeat("x", MaxDescriptionLen+1), "main"},
		"hash-like branch": {"ok", "", "deadbeef"},
		"invalid branch":   {"ok", "", "a..b"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.Create(ctx, c[0], c[1], c[2], ""); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	entries, _ := os.ReadDir(t.TempDir())
	if len(entries) != 0 {
		t.Fatal("expected no directories to be left behind")
	}
}

func TestRules(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	store := newStore(t)
	svc := NewService(database, store, testSecretKey)
	repo, err := svc.Create(ctx, "demo", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}

	rule := Rule{Kind: git.KindBranch, Pattern: "main", PushPolicy: PushAdmins, RunOnPush: true}
	if err := svc.SaveRule(ctx, repo.ID, rule, ""); err != nil {
		t.Fatalf("SaveRule: %v", err)
	}
	rule.PushPolicy = PushEveryone
	if err := svc.SaveRule(ctx, repo.ID, rule, ""); err != nil {
		t.Fatalf("SaveRule (replace): %v", err)
	}
	rules, err := svc.ListRules(ctx, repo.ID)
	if err != nil || len(rules) != 1 || rules[0].PushPolicy != PushEveryone || !rules[0].RunOnPush {
		t.Fatalf("ListRules = %+v, %v", rules, err)
	}

	invalid := []Rule{
		{Kind: "remote", Pattern: "x", PushPolicy: PushEveryone},
		{Kind: git.KindBranch, Pattern: "a..*", PushPolicy: PushEveryone},
		{Kind: git.KindBranch, Pattern: "x", PushPolicy: PushPeople},
		{Kind: git.KindBranch, Pattern: "x", PushPolicy: PushAdmins, PushPeople: []string{"p1"}},
		{Kind: git.KindBranch, Pattern: "x", PushPolicy: "nobody"},
	}
	for _, r := range invalid {
		if err := svc.SaveRule(ctx, repo.ID, r, ""); err == nil {
			t.Errorf("SaveRule(%+v): expected an error", r)
		}
	}

	if err := svc.DeleteRule(ctx, repo.ID, git.KindBranch, "main", ""); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	if err := svc.DeleteRule(ctx, repo.ID, git.KindBranch, "main", ""); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("second DeleteRule = %v, want ErrNotFound", err)
	}
}

func TestSyncRefs(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	store := newStore(t)
	svc := NewService(database, store, testSecretKey)
	r, err := svc.Create(ctx, "demo", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	bare, err := store.Path(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	sync := func() []IndexedRef {
		t.Helper()
		if _, err := svc.SyncRefs(ctx, r); err != nil {
			t.Fatalf("SyncRefs: %v", err)
		}
		indexed, err := svc.ListRefs(ctx, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		return indexed
	}

	run("clone", "--quiet", bare, ".")
	run("commit", "--quiet", "--allow-empty", "-m", "one")
	run("tag", "v1")
	run("push", "--quiet", "origin", "main", "v1")
	if got := sync(); len(got) != 2 {
		t.Fatalf("after pushing main and v1: %+v", got)
	}
	run("push", "--quiet", "origin", ":refs/tags/v1")
	run("commit", "--quiet", "--allow-empty", "-m", "two")
	run("push", "--quiet", "origin", "main")
	got := sync()
	if len(got) != 1 || got[0].Name != "main" {
		t.Fatalf("after deleting v1 and moving main: %+v", got)
	}
}

const testSecretKey = "a very secret passphrase, at least 32 bytes long"

func TestSecretsNeverExposeValue(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	store := newStore(t)
	svc := NewService(database, store, testSecretKey)
	repo, err := svc.Create(ctx, "demo", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.SetSecret(ctx, repo.ID, "API_KEY", "hunter2", ""); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	secrets, err := svc.ListSecrets(ctx, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 1 || secrets[0].Key != "API_KEY" {
		t.Fatalf("ListSecrets = %+v", secrets)
	}

	var ciphertext []byte
	if err := database.Pool.QueryRow(ctx, `SELECT ciphertext FROM secrets WHERE repo_id = $1 AND key = 'API_KEY'`, repo.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if string(ciphertext) == "hunter2" {
		t.Fatal("the plaintext value was stored")
	}

	if err := svc.SetSecret(ctx, repo.ID, "API_KEY", "rotated", ""); err != nil {
		t.Fatalf("SetSecret (replace): %v", err)
	}
	secrets, err = svc.ListSecrets(ctx, repo.ID)
	if err != nil || len(secrets) != 1 {
		t.Fatalf("after replace: %+v, %v", secrets, err)
	}

	if err := svc.DeleteSecret(ctx, repo.ID, "API_KEY", ""); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	if err := svc.DeleteSecret(ctx, repo.ID, "API_KEY", ""); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("second DeleteSecret = %v, want ErrNotFound", err)
	}
}

func TestValidateSecretKey(t *testing.T) {
	valid := []string{"API_KEY", "A", "TOKEN_2"}
	for _, k := range valid {
		if err := ValidateSecretKey(k); err != nil {
			t.Errorf("ValidateSecretKey(%q): %v", k, err)
		}
	}
	invalid := []string{"", "api_key", "1KEY", "HAS SPACE", "GITMAN_TOKEN", "-x", "PATH", "HOME", "DOCKER_HOST"}
	for _, k := range invalid {
		if err := ValidateSecretKey(k); err == nil {
			t.Errorf("ValidateSecretKey(%q): expected an error", k)
		}
	}
}

func TestSetSecretRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	store := newStore(t)
	svc := NewService(database, store, testSecretKey)
	repo, err := svc.Create(ctx, "demo", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSecret(ctx, repo.ID, "bad key", "v", ""); err == nil {
		t.Error("expected an invalid key to be rejected")
	}
	if err := svc.SetSecret(ctx, repo.ID, "OK", "", ""); err == nil {
		t.Error("expected an empty value to be rejected")
	}
	if err := NewService(database, store, "").SetSecret(ctx, repo.ID, "OK", "v", ""); !errors.Is(err, ErrSecretsUnavailable) {
		t.Errorf("SetSecret without an instance secret key = %v, want ErrSecretsUnavailable", err)
	}
}

// TestInternalErrorsAreNotShownToTheWebLayer covers the two halves of
// what internal/web's handlers now rely on apperr.PublicMessage to get
// right, having replaced their previous "f.Error = err.Error()" (which
// showed a raw error's text unconditionally, including a database
// error's table and constraint names) with apperr.PublicMessage(err)
// everywhere a service call can fail: a raw, unclassified error (a real
// foreign key violation here, not a manufactured one) must come back as
// the safe generic message, and a validation error — reclassified as
// apperr.KindInvalid so this holds — must still come back as its own
// specific, purpose-written text rather than also falling back to the
// generic one.
func TestInternalErrorsAreNotShownToTheWebLayer(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	store := newStore(t)
	svc := NewService(database, store, testSecretKey)

	internal := svc.SetSecret(ctx, "no-such-repo", "OK", "v", "")
	if internal == nil {
		t.Fatal("expected a foreign key violation")
	}
	if !strings.Contains(internal.Error(), "foreign key") {
		t.Fatalf("test did not exercise a real database error: %v", internal)
	}
	if got := apperr.PublicMessage(internal); strings.Contains(got, "foreign key") || strings.Contains(got, "secrets") {
		t.Fatalf("apperr.PublicMessage leaked database detail: %q", got)
	}

	repo, err := svc.Create(ctx, "demo2", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	validation := svc.SetSecret(ctx, repo.ID, "bad key", "v", "")
	if validation == nil {
		t.Fatal("expected an invalid key to be rejected")
	}
	if got := apperr.PublicMessage(validation); !strings.Contains(got, "secret name must be") {
		t.Fatalf("apperr.PublicMessage lost a validation error's own text: %q", got)
	}
}

func TestSetDescription(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	store := newStore(t)
	svc := NewService(database, store, testSecretKey)
	r, err := svc.Create(ctx, "demo", "original", "main", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.SetDescription(ctx, r.ID, "  updated  ", ""); err != nil {
		t.Fatalf("SetDescription: %v", err)
	}
	got, err := svc.GetByID(ctx, r.ID)
	if err != nil || got.Description != "updated" {
		t.Fatalf("GetByID after SetDescription = %+v, %v", got, err)
	}

	if err := svc.SetDescription(ctx, r.ID, strings.Repeat("x", MaxDescriptionLen+1), ""); err == nil {
		t.Error("expected an over-length description to be rejected")
	}
	if err := svc.SetDescription(ctx, "no-such-repo", "x", ""); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("SetDescription(unknown repo) = %v, want ErrNotFound", err)
	}
}

func TestRunSecrets(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	store := newStore(t)
	svc := NewService(database, store, testSecretKey)
	r, err := svc.Create(ctx, "demo", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSecret(ctx, r.ID, "DEPLOY_TOKEN", "hunter2\nsecond line", ""); err != nil {
		t.Fatal(err)
	}
	values, err := svc.RunSecrets(ctx, r.ID)
	if err != nil || values["DEPLOY_TOKEN"] != "hunter2\nsecond line" {
		t.Fatalf("RunSecrets = %v, %v", values, err)
	}
	if _, err := NewService(database, store, "a different key, also at least 32 bytes").RunSecrets(ctx, r.ID); err == nil {
		t.Fatal("expected secrets sealed under another key to fail to decrypt")
	}
}
