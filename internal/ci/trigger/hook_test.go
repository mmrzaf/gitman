package trigger

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/git"
)

func TestBuildHookScriptUsesDurableLocalQueue(t *testing.T) {
	script := buildHookScript("owner", "repo")
	for _, expected := range []string{
		managedHookMarker,
		QueueDirName,
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
	if strings.Contains(script, "curl ") || strings.Contains(script, "secret") || strings.Contains(script, "token") {
		t.Fatalf("managed hook must use credential-free local delivery:\n%s", script)
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

	entries, err := os.ReadDir(filepath.Join(hooksDir, QueueDirName))
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "event-") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(hooksDir, QueueDirName, entry.Name()))
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
	path := filepath.Join(t.TempDir(), "post-receive")
	state, err := detectHookState(path)
	if err != nil || state != hookAbsent {
		t.Fatalf("absent: state=%v err=%v", state, err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho custom\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	state, err = detectHookState(path)
	if err != nil || state != hookUnmanaged {
		t.Fatalf("unmanaged: state=%v err=%v", state, err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+managedHookMarker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	state, err = detectHookState(path)
	if err != nil || state != hookManaged {
		t.Fatalf("managed: state=%v err=%v", state, err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n# Managed by Gitman CI/CD. Schema: 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	state, err = detectHookState(path)
	if err != nil || state != hookOutdated {
		t.Fatalf("outdated: state=%v err=%v", state, err)
	}
}

func TestEnsureHookInstallsAndDoesNotOverwriteUnmanagedHook(t *testing.T) {
	ctx := context.Background()
	manager := &Manager{ReposPath: t.TempDir()}
	const owner = "owner"
	const repo = "hooks"
	repoPath, err := git.SecureRepoPath(manager.ReposPath, owner, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := git.InitBareRepo(ctx, repoPath, 512*1024*1024); err != nil {
		t.Fatal(err)
	}
	if err := manager.EnsureHook(ctx, owner, repo); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(repoPath, "hooks", "post-receive")
	state, err := detectHookState(hook)
	if err != nil || state != hookManaged {
		t.Fatalf("installed state=%v err=%v", state, err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho custom\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := manager.EnsureHook(ctx, owner, repo); !errors.Is(err, ErrUnmanagedHook) {
		t.Fatalf("EnsureHook error=%v, want ErrUnmanagedHook", err)
	}
	data, err := os.ReadFile(hook)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "echo custom") {
		t.Fatal("unmanaged hook was overwritten")
	}
}

func TestEnsureHookRefusesMissingRepositoryStorage(t *testing.T) {
	ctx := context.Background()
	manager := &Manager{ReposPath: t.TempDir()}
	const owner = "owner"
	const repo = "missing-storage"
	repoPath, err := git.SecureRepoPath(manager.ReposPath, owner, repo)
	if err != nil {
		t.Fatal(err)
	}

	if err := manager.EnsureHook(ctx, owner, repo); err == nil {
		t.Fatal("EnsureHook succeeded without bare repository storage")
	}
	if _, err := os.Stat(repoPath); !os.IsNotExist(err) {
		t.Fatalf("EnsureHook created missing repository path: %v", err)
	}
}

func TestReconcileAllReportsMissingStorageWithoutCreatingIt(t *testing.T) {
	manager, ctx, owner := setupTriggerTest(t)
	if _, err := manager.DB.CreateRepository(ctx, owner.ID, "missing-reconcile", "", false); err != nil {
		t.Fatal(err)
	}
	repoPath, err := git.SecureRepoPath(manager.ReposPath, owner.Username, "missing-reconcile")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ReconcileAll(ctx); err == nil {
		t.Fatal("ReconcileAll succeeded with missing repository storage")
	}
	if _, err := os.Stat(repoPath); !os.IsNotExist(err) {
		t.Fatalf("ReconcileAll created missing repository path: %v", err)
	}
}
