package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedLocksCoexistAndBlockExclusive(t *testing.T) {
	dbPath := t.TempDir() + "/db/gitman.sqlite"
	first, err := AcquireShared(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := AcquireShared(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	if _, err := AcquireExclusive(dbPath); err == nil || !strings.Contains(err.Error(), "state is in use") {
		t.Fatalf("expected exclusive lock refusal, got %v", err)
	}
}

func TestExclusiveBlocksShared(t *testing.T) {
	dbPath := t.TempDir() + "/db/gitman.sqlite"
	exclusive, err := AcquireExclusive(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer exclusive.Release()
	if _, err := AcquireShared(dbPath); err == nil || !strings.Contains(err.Error(), "locked for backup") {
		t.Fatalf("expected shared lock refusal, got %v", err)
	}
}

func TestDatabaseSymlinkAndTargetShareStateLock(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.sqlite")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.sqlite")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	shared, err := AcquireShared(alias)
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Release()
	if _, err := AcquireExclusive(target); err == nil {
		t.Fatal("database symlink bypassed the state lock")
	}
}
