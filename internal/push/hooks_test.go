package push

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseUpdates(t *testing.T) {
	zero := strings.Repeat("0", 40)
	a := strings.Repeat("a", 40)
	input := zero + " " + a + " refs/heads/main\n" + a + " " + zero + " refs/tags/v1\n" + zero + " " + a + " refs/notes/x\n"
	updates, err := ParseUpdates(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 3 {
		t.Fatalf("got %d updates", len(updates))
	}
	if !updates[0].IsCreate() || updates[0].Kind != "branch" || updates[0].Name != "main" {
		t.Errorf("update 0 = %+v", updates[0])
	}
	if !updates[1].IsDelete() || updates[1].Kind != "tag" {
		t.Errorf("update 1 = %+v", updates[1])
	}
	if updates[2].Kind != "" {
		t.Errorf("a ref outside heads and tags must have no kind, got %+v", updates[2])
	}

	for _, bad := range []string{"nonsense\n", zero + " " + a + "\n", "xyz " + a + " refs/heads/main\n"} {
		if _, err := ParseUpdates(strings.NewReader(bad)); err == nil {
			t.Errorf("ParseUpdates(%q): expected an error", bad)
		}
	}
}

func TestInstallWritesExecutableScripts(t *testing.T) {
	dir := t.TempDir()
	// A path with a quote exercises the shell quoting.
	exe := filepath.Join(t.TempDir(), "it's gitman")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Install(dir, exe); err != nil {
		t.Fatal(err)
	}
	if err := Install(dir, exe); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	for _, name := range []string{PreReceive, PostReceive} {
		out, err := exec.Command(filepath.Join(dir, name)).Output()
		if err != nil {
			t.Fatalf("run %s: %v", name, err)
		}
		if got := strings.TrimSpace(string(out)); got != "hook "+name {
			t.Errorf("%s script ran the binary with %q", name, got)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("expected only the two scripts, found %d entries", len(entries))
	}
	if err := Install(dir, "relative/gitman"); err == nil {
		t.Error("expected a relative executable path to be rejected")
	}
}

func TestContextFromEnvRequiresPushContext(t *testing.T) {
	t.Setenv(EnvRepoID, "")
	t.Setenv(EnvPersonID, "")
	if _, err := ContextFromEnv(); err == nil {
		t.Fatal("expected an error without a push context")
	}
	t.Setenv(EnvRepoID, "r1")
	t.Setenv(EnvPersonID, "p1")
	t.Setenv("GIT_QUARANTINE_PATH", "/tmp/q")
	c, err := ContextFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.GitEnv) != 1 || c.GitEnv[0] != "GIT_QUARANTINE_PATH=/tmp/q" {
		t.Errorf("GitEnv = %v", c.GitEnv)
	}
}
