package handlers

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/config"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

func TestEmbeddedTemplatesExecuteWithRepresentativeTypedPages(t *testing.T) {
	templates, err := LoadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{AllowRegister: true, SSHUser: "git", ServerHost: "git.example", PublicURL: "https://git.example"}
	owner := &models.User{ID: "u1", Username: "alice", CreatedAt: now}
	repo := &models.Repository{ID: "r1", OwnerID: owner.ID, Name: "project", Description: "Small repository", IsPrivate: true, CreatedAt: now}
	nav := func(active, section, ref string) *RepoNavData {
		return &RepoNavData{Owner: owner, Repository: repo, CurrentRef: ref, Active: active, SettingsSection: section, IsOwner: true, CanViewCI: true}
	}
	base := func(title string, repoNav *RepoNavData) PageData {
		return PageData{Title: title, User: owner, Config: cfg, CSRFToken: "csrf", RepoNav: repoNav, RequestID: "req-1"}
	}
	commit := git.Commit{Hash: "0123456789abcdef0123456789abcdef01234567", Author: "Alice", Email: "alice@example.com", Date: now, Message: "Tighten the UI"}
	started := now.Add(time.Minute)
	completed := started.Add(10 * time.Second)
	run := models.CIRun{ID: "run-1", RepoID: repo.ID, CommitHash: commit.Hash, Branch: "main", Event: models.CIEventManual, Status: models.CIStatusSuccess, CreatedAt: now, StartedAt: &started, CompletedAt: &completed}
	collaborator := models.Collaborator{User: models.User{ID: "u2", Username: "bob"}, AccessLevel: models.AccessRead, CreatedAt: now}
	commitDetail := git.CommitDetail{Hash: commit.Hash, Parents: []string{"fedcba9876543210fedcba9876543210fedcba98"}, Author: commit.Author, Email: commit.Email, Date: now, Subject: commit.Message, Body: "A deliberately small change.", Branches: []string{"main"}}
	diff := git.CommitDiff{TotalFiles: 1, Additions: 1, Deletions: 1, Files: []git.FileDiff{{ChangedFile: git.ChangedFile{Status: "modified", StatusCode: "M", Path: "main.go", Additions: 1, Deletions: 1}, Hunks: []git.DiffHunk{{Header: "@@ -1 +1 @@", Lines: []git.DiffLine{{Kind: "delete", OldLine: 1, Text: "old"}, {Kind: "add", NewLine: 1, Text: "new"}}}}}}}
	ciView := newCIRunView(run, &commit)
	ciView.AttemptNumber = 1

	cases := map[string]pageModel{
		"home.html":               &PageData{Title: "Home", Config: cfg},
		"error.html":              &PageData{Title: "Not found", User: owner, Config: cfg, StatusCode: 404, ErrorTitle: "Not found", Error: "Repository not found", ErrorHint: "Check the address.", RequestID: "req-1"},
		"login.html":              &AuthPageData{PageData: PageData{Title: "Login", Config: cfg, CSRFToken: "csrf"}, Username: "alice"},
		"register.html":           &AuthPageData{PageData: PageData{Title: "Register", Config: cfg, CSRFToken: "csrf"}, Username: "alice"},
		"repos.html":              &ReposPageData{PageData: base("Repositories", nil), Repos: []models.Repository{*repo}},
		"keys.html":               &KeysPageData{PageData: base("SSH keys", nil), Keys: []models.SSHKey{{ID: "k1", Name: "Laptop", Fingerprint: "SHA256:test", CreatedAt: now}}},
		"tokens.html":             &TokensPageData{PageData: base("Tokens", nil), Tokens: []models.AccessToken{{ID: "t1", Name: "Laptop", CreatedAt: now}}, NewToken: "gm_example"},
		"repo_view.html":          &RepoPageData{PageData: base("Files", nav("files", "", "main")), Owner: owner, Repository: repo, CurrentRef: "main", CurrentRefKind: "branch", ResolvedCommit: commit.Hash, Branches: []string{"main", "develop"}, Tags: []string{"v1.0.0"}, Tree: []git.TreeEntry{{Type: "tree", Name: "internal"}, {Type: "blob", Name: "README.md", Size: 1234}}, LatestCommit: &commit, LatestCI: &run, ReadmePath: "README.md", ReadmeContent: "# Project\n\nSmall and boring."},
		"repo_blob.html":          &RepoPageData{PageData: base("main.go", nav("files", "", "main")), Owner: owner, Repository: repo, CurrentRef: "main", CurrentRefKind: "branch", ResolvedCommit: commit.Hash, CurrentPath: "internal/main.go", Breadcrumbs: []RepoBreadcrumb{{Name: "internal", Path: "internal"}, {Name: "main.go", Path: "internal/main.go"}}, BlobContent: "package main\n", BlobLines: []SourceLine{{Number: 1, Text: "package main"}}, BlobSize: 13, BlobLanguage: "Go"},
		"repo_commits.html":       &RepoPageData{PageData: base("Commits", nav("commits", "", "main")), Owner: owner, Repository: repo, CurrentRef: "main", CurrentRefKind: "branch", ResolvedCommit: commit.Hash, Branches: []string{"main"}, Tags: []string{"v1.0.0"}, CommitViews: []CommitListItem{{Commit: commit, CI: &run}}},
		"repo_commit.html":        &CommitPageData{PageData: base(commit.Message, nav("commits", "", "main")), Owner: owner, Repository: repo, CurrentRef: "main", CurrentRefKind: "branch", CIBranch: "main", Commit: commitDetail, Diff: diff, LatestCI: &run, CanViewCI: true, CanControlCI: true},
		"repo_ci.html":            &CIPageData{PageData: base("CI", nav("ci", "", "main")), Owner: owner, Repository: repo, Runs: []CIRunView{ciView}, Branches: []string{"main", "develop"}, Tags: []string{"v1.0.0"}, DefaultBranch: "main", CanControl: true},
		"repo_ci_run.html":        &CIRunPageData{PageData: base("CI run", nav("ci", "", commit.Hash)), Owner: owner, Repository: repo, Run: &run, Commit: &commit, LogContent: "ok\n", LogOffset: 3, Log: CILogView{Setup: CILogSection{Name: "Setup", Kind: "setup", Status: models.CIStatusSuccess, Output: "ready", Duration: "1s", StartedAt: &started}, Steps: []CILogSection{{Name: "Test", Kind: "step", Status: models.CIStatusSuccess, Output: "ok", Duration: "2s", StartedAt: &started}}, Finalize: CILogSection{Name: "Finalize", Kind: "finalize", Status: models.CIStatusSuccess, Output: "done", Duration: "1s", StartedAt: &started}}, Pipeline: CIConfigView{Found: true, Valid: true, Image: "golang:1.26", EnvCount: 1, SecretCount: 1}, Attempts: []CIRunView{ciView}, CanControl: true},
		"repo_collaborators.html": &RepoPageData{PageData: base("Access", nav("settings", "access", "")), Owner: owner, Repository: repo, Collaborators: []models.Collaborator{collaborator}},
		"repo_ci_settings.html":   &RepoCISettingsPageData{PageData: base("CI settings", nav("settings", "ci", "")), Owner: owner, Repository: repo, Secrets: []models.RepoSecret{{ID: "s1", Key: "TOKEN", CreatedAt: now}}, RefRules: []models.RepoCIRefRule{{RepoID: repo.ID, RefType: models.CIRefBranch, RefName: "main", AutoRun: true, AllowSecrets: true, CreatedAt: now}}},
		"repo_settings.html":      &RepoSettingsPageData{PageData: base("Settings", nav("settings", "general", "")), Owner: owner, Repository: repo},
	}

	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			tmpl := templates[name]
			if tmpl == nil {
				t.Fatalf("template %s not loaded", name)
			}
			var out bytes.Buffer
			if err := tmpl.ExecuteTemplate(&out, "base.html", data); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if out.Len() == 0 {
				t.Fatal("rendered page is empty")
			}
		})
	}
}

func TestPurifiedTemplatesKeepRoutineAndDestructiveActionsSeparate(t *testing.T) {
	templates, err := LoadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{SSHUser: "git", ServerHost: "git.example", PublicURL: "https://git.example"}
	owner := &models.User{ID: "u1", Username: "alice", CreatedAt: now}
	repo := &models.Repository{ID: "r1", OwnerID: owner.ID, Name: "project", IsPrivate: true, CreatedAt: now}
	base := PageData{User: owner, Config: cfg, CSRFToken: "csrf"}

	var reposOut bytes.Buffer
	if err := templates["repos.html"].ExecuteTemplate(&reposOut, "base.html", &ReposPageData{PageData: base, Repos: []models.Repository{*repo}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reposOut.String(), "/repos/r1/delete") || strings.Contains(reposOut.String(), ">Delete<") {
		t.Fatalf("repository index exposed destructive action:\n%s", reposOut.String())
	}

	base.RepoNav = &RepoNavData{Owner: owner, Repository: repo, Active: "settings", SettingsSection: "general", IsOwner: true, CanViewCI: true}
	var settingsOut bytes.Buffer
	if err := templates["repo_settings.html"].ExecuteTemplate(&settingsOut, "base.html", &RepoSettingsPageData{PageData: base, Owner: owner, Repository: repo}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(settingsOut.String(), "readonly") {
		t.Fatalf("immutable repository name still rendered as readonly form control:\n%s", settingsOut.String())
	}
	if !strings.Contains(settingsOut.String(), "/repos/r1/delete") {
		t.Fatal("settings danger zone lost repository deletion")
	}
}

func TestTerminalCIRunOmitsLiveOnlyControls(t *testing.T) {
	templates, err := LoadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	owner := &models.User{ID: "u1", Username: "alice"}
	repo := &models.Repository{ID: "r1", OwnerID: owner.ID, Name: "project"}
	run := &models.CIRun{ID: "run-1", RepoID: repo.ID, CommitHash: "0123456789abcdef0123456789abcdef01234567", Branch: "main", Status: models.CIStatusSuccess, Event: models.CIEventManual, CreatedAt: now}
	data := &CIRunPageData{
		PageData: PageData{User: owner, Config: &config.Config{}, CSRFToken: "csrf", RepoNav: &RepoNavData{Owner: owner, Repository: repo, Active: "ci", CanViewCI: true}},
		Owner:    owner, Repository: repo, Run: run,
		Log: CILogView{Setup: CILogSection{Name: "Setup", Kind: "setup", Status: models.CIStatusSuccess}, Finalize: CILogSection{Name: "Finalize", Kind: "finalize", Status: models.CIStatusSuccess}},
	}
	var out bytes.Buffer
	if err := templates["repo_ci_run.html"].ExecuteTemplate(&out, "base.html", data); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	if strings.Contains(html, "data-log-follow") || strings.Contains(html, "data-log-new-output") || strings.Contains(html, "data-log-live-note") {
		t.Fatalf("terminal run rendered live-only controls:\n%s", html)
	}
	if !strings.Contains(html, "data-log-wrap") || !strings.Contains(html, "logs/download") {
		t.Fatal("terminal run lost persistent log controls")
	}
}
