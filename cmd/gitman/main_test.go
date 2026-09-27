package main

import "testing"

func TestVersionCommandIsRegistered(t *testing.T) {
	if _, ok := commands["version"]; !ok {
		t.Fatal("expected a \"version\" command to be registered")
	}
}

func TestRunVersionSucceeds(t *testing.T) {
	if err := runVersion(nil); err != nil {
		t.Fatalf("runVersion: %v", err)
	}
}
