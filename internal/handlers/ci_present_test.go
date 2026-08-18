package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cipipeline "github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/models"
)

func TestParseCILogStructuresConfiguredSteps(t *testing.T) {
	started := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	finished := started.Add(8 * time.Second)
	run := &models.CIRun{Status: "failed", StartedAt: &started, CompletedAt: &finished}
	cfg := &cipipeline.Config{Steps: []cipipeline.Step{{Name: "Test"}, {Name: "Build"}, {Name: "Package"}}}
	log := strings.Join([]string{
		"[2026-08-18T00:00:00Z] Repository : me/gitman",
		"2026-08-18T00:00:01Z --- Step: Test ---",
		"ok github.com/me/gitman",
		"2026-08-18T00:00:03Z --- Step: Test: SUCCESS ---",
		"2026-08-18T00:00:04Z --- Step: Build ---",
		"compile error",
		"2026-08-18T00:00:07Z --- Step: Build: FAILED (exit 2) ---",
		"[2026-08-18T00:00:08Z] Exit status: FAILED",
	}, "\n") + "\n"

	view := parseCILog(log, run, cfg)
	if view.Setup.Status != "success" || !strings.Contains(view.Setup.Output, "Repository") {
		t.Fatalf("unexpected setup: %+v", view.Setup)
	}
	if got := view.Steps[0]; got.Status != "success" || got.Duration != "2s" || !strings.Contains(got.Output, "ok github") {
		t.Fatalf("unexpected test step: %+v", got)
	}
	if got := view.Steps[1]; got.Status != "failed" || got.ExitCode != 2 || got.Duration != "3s" || !got.Open {
		t.Fatalf("unexpected build step: %+v", got)
	}
	if got := view.Steps[2]; got.Status != "pending" {
		t.Fatalf("unexpected package step: %+v", got)
	}
	if view.Finalize.Status != "failed" || !strings.Contains(view.Finalize.Output, "Exit status") {
		t.Fatalf("unexpected finalize section: %+v", view.Finalize)
	}
}

func TestParseCILogHandlesStepNamesEndingInStatusWords(t *testing.T) {
	cfg := &cipipeline.Config{Steps: []cipipeline.Step{{Name: "Check: SUCCESS"}}}
	run := &models.CIRun{Status: "success"}
	log := "2026-08-18T00:00:01Z --- Step: Check: SUCCESS ---\noutput\n2026-08-18T00:00:02Z --- Step: Check: SUCCESS: SUCCESS ---\n"
	view := parseCILog(log, run, cfg)
	if len(view.Steps) != 1 || view.Steps[0].Status != "success" || view.Steps[0].Output != "output" {
		t.Fatalf("ambiguous step name parsed incorrectly: %+v", view.Steps)
	}
}

func TestArtifactTreePreservesNestedFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "coverage", "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "coverage", "index.html"), []byte("<h1>report</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "coverage", "assets", "data.json"), []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	files := listArtifactFiles(root)
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2: %+v", len(files), files)
	}
	tree := buildArtifactTree(files)
	if len(tree) != 1 || tree[0].Name != "coverage" || len(tree[0].Children) != 2 {
		t.Fatalf("unexpected tree: %+v", tree)
	}
	if artifactTreeSize(tree) == 0 {
		t.Fatal("artifact tree size was not accumulated")
	}
}
