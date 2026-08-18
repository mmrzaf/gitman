package repository

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNamespaceLockSerializesSameUsername(t *testing.T) {
	root := t.TempDir()
	first, err := LockNamespace(context.Background(), root, "alice")
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer func() { _ = first.Release() }()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := LockNamespace(ctx, root, "alice"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second lock error = %v, want deadline exceeded", err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("release first lock: %v", err)
	}
	second, err := LockNamespace(context.Background(), root, "alice")
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("release second lock: %v", err)
	}
}

func TestNamespaceLocksDifferentUsersIndependently(t *testing.T) {
	root := t.TempDir()
	alice, err := LockNamespace(context.Background(), root, "alice")
	if err != nil {
		t.Fatalf("alice lock: %v", err)
	}
	defer func() { _ = alice.Release() }()

	bob, err := LockNamespace(context.Background(), root, "bob")
	if err != nil {
		t.Fatalf("bob lock: %v", err)
	}
	if err := bob.Release(); err != nil {
		t.Fatalf("release bob lock: %v", err)
	}
}

func TestNamespaceLockRejectsUnsafeOwner(t *testing.T) {
	if _, err := LockNamespace(context.Background(), t.TempDir(), "../alice"); err == nil {
		t.Fatal("unsafe owner unexpectedly accepted")
	}
}
