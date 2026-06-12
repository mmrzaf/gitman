package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelp(t *testing.T) {
	var buf bytes.Buffer
	err := help(&buf)
	if err != nil {
		t.Fatal(err)
	}
	output := buf.String()
	if !strings.Contains(output, "gitman <command>") {
		t.Errorf("help output missing expected text: %s", output)
	}
}

func TestVersionStringDefault(t *testing.T) {
	if got := versionString(); got == "" {
		t.Fatal("version string is empty")
	}
}
