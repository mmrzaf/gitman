package repo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/auth"
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

func TestVisibilityAndReaders(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	store := newStore(t)
	svc := NewService(database, store, testSecretKey)
	people := auth.NewService(database)
	person, err := people.Create(ctx, "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := svc.Create(ctx, "demo", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Visibility != VisibilityEveryone {
		t.Fatalf("Create: Visibility = %q, want %q", r.Visibility, VisibilityEveryone)
	}
	if ok, err := svc.CanRead(ctx, r, person.ID, false); err != nil || !ok {
		t.Fatalf("CanRead on a new repository = %v, %v, want true", ok, err)
	}

	if err := svc.SetVisibility(ctx, r.ID, VisibilityRestricted, ""); err != nil {
		t.Fatalf("SetVisibility: %v", err)
	}
	r, err = svc.GetByID(ctx, r.ID)
	if err != nil || r.Visibility != VisibilityRestricted {
		t.Fatalf("GetByID after SetVisibility = %+v, %v", r, err)
	}
	if ok, err := svc.CanRead(ctx, r, person.ID, false); err != nil || ok {
		t.Fatalf("CanRead(non-reader) = %v, %v, want false", ok, err)
	}
	if ok, err := svc.CanRead(ctx, r, person.ID, true); err != nil || !ok {
		t.Fatalf("CanRead(admin) = %v, %v, want true", ok, err)
	}

	if err := svc.AddReader(ctx, r.ID, person.ID, person.Username, ""); err != nil {
		t.Fatalf("AddReader: %v", err)
	}
	if err := svc.AddReader(ctx, r.ID, person.ID, person.Username, ""); err != nil {
		t.Fatalf("AddReader (again): %v", err)
	}
	// The activity log names the reader by username, the same way every
	// other action naming a person does — not by their opaque ID, which
	// would be meaningless on the timeline.
	entries, err := activity.NewService(database).Recent(ctx, &r.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == activity.RepoReaderAdded {
			found = true
			if e.Detail != person.Username {
				t.Fatalf("RepoReaderAdded detail = %q, want the username %q", e.Detail, person.Username)
			}
		}
	}
	if !found {
		t.Fatal("no RepoReaderAdded activity entry was recorded")
	}
	if ok, err := svc.CanRead(ctx, r, person.ID, false); err != nil || !ok {
		t.Fatalf("CanRead(reader) = %v, %v, want true", ok, err)
	}
	readers, err := svc.ListReaders(ctx, r.ID)
	if err != nil || len(readers) != 1 || readers[0] != person.ID {
		t.Fatalf("ListReaders = %v, %v", readers, err)
	}

	if err := svc.RemoveReader(ctx, r.ID, person.ID, person.Username, ""); err != nil {
		t.Fatalf("RemoveReader: %v", err)
	}
	if ok, err := svc.CanRead(ctx, r, person.ID, false); err != nil || ok {
		t.Fatalf("CanRead after RemoveReader = %v, %v, want false", ok, err)
	}
	if err := svc.RemoveReader(ctx, r.ID, person.ID, person.Username, ""); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("second RemoveReader = %v, want ErrNotFound", err)
	}

	if err := svc.SetVisibility(ctx, r.ID, "unknown", ""); err == nil {
		t.Error("expected an invalid visibility to be rejected")
	}
	if err := svc.SetVisibility(ctx, "no-such-repo", VisibilityEveryone, ""); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("SetVisibility(unknown repo) = %v, want ErrNotFound", err)
	}
}

func TestListReadable(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	store := newStore(t)
	svc := NewService(database, store, testSecretKey)
	people := auth.NewService(database)
	reader, err := people.Create(ctx, "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := people.Create(ctx, "mmrzaf", "correct-horse-battery", true, "")
	if err != nil {
		t.Fatal(err)
	}

	open, err := svc.Create(ctx, "open", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	restricted, err := svc.Create(ctx, "restricted", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetVisibility(ctx, restricted.ID, VisibilityRestricted, ""); err != nil {
		t.Fatal(err)
	}

	nonReader, err := svc.ListReadable(ctx, reader.ID, false)
	if err != nil || len(nonReader) != 1 || nonReader[0].ID != open.ID {
		t.Fatalf("ListReadable(non-reader) = %+v, %v, want only %q", nonReader, err, open.Name)
	}

	if err := svc.AddReader(ctx, restricted.ID, reader.ID, reader.Username, ""); err != nil {
		t.Fatal(err)
	}
	readable, err := svc.ListReadable(ctx, reader.ID, false)
	if err != nil || len(readable) != 2 {
		t.Fatalf("ListReadable(reader) = %+v, %v, want both repositories", readable, err)
	}

	admin, err := svc.ListReadable(ctx, other.ID, true)
	if err != nil || len(admin) != 2 {
		t.Fatalf("ListReadable(admin) = %+v, %v, want both repositories", admin, err)
	}
}

func TestSetDefaultPush(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	store := newStore(t)
	svc := NewService(database, store, testSecretKey)
	r, err := svc.Create(ctx, "demo", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.DefaultPushPolicy != PushEveryone {
		t.Fatalf("Create: DefaultPushPolicy = %q, want %q", r.DefaultPushPolicy, PushEveryone)
	}

	if err := svc.SetDefaultPush(ctx, r.ID, PushPeople, []string{"person-1"}, ""); err != nil {
		t.Fatalf("SetDefaultPush: %v", err)
	}
	r, err = svc.GetByID(ctx, r.ID)
	if err != nil || r.DefaultPushPolicy != PushPeople || len(r.DefaultPushPeople) != 1 || r.DefaultPushPeople[0] != "person-1" {
		t.Fatalf("GetByID after SetDefaultPush = %+v, %v", r, err)
	}

	if err := svc.SetDefaultPush(ctx, r.ID, PushPeople, nil, ""); err == nil {
		t.Error("expected a people policy naming nobody to be rejected")
	}
	if err := svc.SetDefaultPush(ctx, "no-such-repo", PushEveryone, nil, ""); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("SetDefaultPush(unknown repo) = %v, want ErrNotFound", err)
	}
}

// TestSetDefaultBranch is the repository created with main as its
// default whose first push was develop: the default can then be moved to
// develop, and only to a branch that exists.
func TestSetDefaultBranch(t *testing.T) {
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
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@x")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	work := t.TempDir()
	git(work, "clone", "--quiet", bare, ".")
	git(work, "commit", "--quiet", "--allow-empty", "-m", "one")
	git(work, "push", "--quiet", "origin", "HEAD:refs/heads/develop", "HEAD:refs/tags/v1")

	for name, branch := range map[string]string{
		"a branch never pushed":   "feature",
		"a tag, not a branch":     "v1",
		"an invalid branch name":  "a..b",
		"a hash-like branch name": "deadbeef",
	} {
		if err := svc.SetDefaultBranch(ctx, r, branch, ""); apperr.KindOf(err) != apperr.KindInvalid {
			t.Errorf("SetDefaultBranch(%s) = %v, want an invalid-input error", name, err)
		}
	}

	if err := svc.SetDefaultBranch(ctx, r, "develop", ""); err != nil {
		t.Fatalf("SetDefaultBranch: %v", err)
	}
	got, err := svc.GetByID(ctx, r.ID)
	if err != nil || got.DefaultBranch != "develop" {
		t.Fatalf("GetByID after SetDefaultBranch = %+v, %v", got, err)
	}
	if head := git(bare, "symbolic-ref", "HEAD"); head != "refs/heads/develop" {
		t.Errorf("HEAD = %q, want refs/heads/develop: a clone must check out the new default", head)
	}
	recorded := func() []string {
		t.Helper()
		entries, err := activity.NewService(database).Recent(ctx, &r.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		var details []string
		for _, e := range entries {
			if e.Action == activity.RepoDefaultBranchChanged {
				details = append(details, e.Detail)
			}
		}
		return details
	}
	if got := recorded(); len(got) != 1 || got[0] != "develop" {
		t.Fatalf("recorded default branch changes = %q, want [develop]", got)
	}

	// Choosing the branch that is already the default records nothing.
	if err := svc.SetDefaultBranch(ctx, got, "develop", ""); err != nil {
		t.Fatalf("SetDefaultBranch (unchanged): %v", err)
	}
	if got := recorded(); len(got) != 1 {
		t.Errorf("recorded default branch changes = %q; an unchanged default was recorded", got)
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
