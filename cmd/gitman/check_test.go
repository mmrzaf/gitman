package main

import (
	"os"
	"path/filepath"
	"testing"
)

const validPipeline = `
image: docker:29-cli
steps:
  - name: build
    run: echo hi
`

func TestRunCheckValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitman.yml")
	if err := os.WriteFile(path, []byte(validPipeline), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runCheck([]string{path}); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
}

func TestRunCheckInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitman.yml")
	if err := os.WriteFile(path, []byte("steps: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runCheck([]string{path}); err == nil {
		t.Fatal("expected an error for an invalid pipeline file")
	}
}

func TestRunCheckMissingFile(t *testing.T) {
	if err := runCheck([]string{filepath.Join(t.TempDir(), "does-not-exist.yml")}); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestCheckCommandIsRegistered(t *testing.T) {
	if _, ok := commands["check"]; !ok {
		t.Fatal(`expected a "check" command to be registered`)
	}
}
