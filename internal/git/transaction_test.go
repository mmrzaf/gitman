package git

import (
	"context"
	"strings"
	"testing"
)

func TestRefTransactionRejectsContradictoryInput(t *testing.T) {
	zero := strings.Repeat("0", 40)
	one := strings.Repeat("a", 40)
	cases := []struct {
		name    string
		changes []RefChange
	}{
		{"identical object", []RefChange{{Ref: "refs/heads/main", Old: one, New: one}}},
		{"both zero", []RefChange{{Ref: "refs/heads/main", Old: zero, New: zero}}},
		{"duplicate ref", []RefChange{{Ref: "refs/heads/main", Old: zero, New: one}, {Ref: "refs/heads/main", Old: one, New: zero}}},
		{"internal ref", []RefChange{{Ref: "refs/gitman/pins/secret", Old: zero, New: one}}},
		{"invalid name", []RefChange{{Ref: "refs/heads/a..b", Old: zero, New: one}}},
		{"invalid hash", []RefChange{{Ref: "refs/heads/main", Old: "nope", New: one}}},
		{"mixed hash formats", []RefChange{{Ref: "refs/heads/main", Old: zero, New: strings.Repeat("a", 64)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateRefChanges(tc.changes); err == nil {
				t.Fatal("invalid changes accepted")
			}
		})
	}
	if err := validateRefChanges([]RefChange{{Ref: "refs/heads/main", Old: zero, New: one}, {Ref: "refs/tags/v1", Old: zero, New: one}}); err != nil {
		t.Fatalf("valid changes rejected: %v", err)
	}
}

func TestAtomicRefTransactionRejectsParentChildConflict(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hash := f.commit(t, "one")
	f.push(t, "HEAD:refs/heads/topic")
	changes := []RefChange{
		{Ref: "refs/heads/topic", Old: hash, New: strings.Repeat("0", 40)},
		{Ref: "refs/heads/topic/child", Old: strings.Repeat("0", 40), New: hash},
	}
	payload := []byte(`{"example":"namespace-conflict"}`)
	if err := f.repo.ApplyOperation(ctx, "bad-namespace", changes, []string{hash}, payload); err == nil {
		t.Fatal("Git unexpectedly accepted an atomic parent/child ref swap")
	}
	old, err := f.repo.RefOID(ctx, KindBranch, "topic")
	if err != nil || old != hash {
		t.Fatalf("rejected transaction changed original ref: %q %v", old, err)
	}
	if _, err := f.repo.RefOID(ctx, KindBranch, "topic/child"); err != ErrNotFound {
		t.Fatalf("rejected transaction created nested ref: %v", err)
	}
	applied, err := f.repo.OperationApplied(ctx, "bad-namespace", payload)
	if err != nil || applied {
		t.Fatalf("rejected transaction has receipt: %v %v", applied, err)
	}
}

func TestAtomicRefTransactionReceiptAndIdempotentCheck(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hash := f.commit(t, "release")
	f.push(t, "HEAD:refs/heads/staging")
	gitCmd(t, f.repo.path, "update-ref", "-d", "refs/heads/staging")
	zero := strings.Repeat("0", 40)
	changes := []RefChange{
		{Ref: "refs/heads/main", Old: zero, New: hash},
		{Ref: "refs/tags/v1", Old: zero, New: hash},
	}
	payload := []byte(`{"example":"atomic-push"}`)
	if err := f.repo.CheckRefChanges(ctx, changes); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.ApplyOperation(ctx, "valid-operation", changes, []string{hash}, payload); err != nil {
		t.Fatal(err)
	}
	applied, err := f.repo.OperationApplied(ctx, "valid-operation", payload)
	if err != nil || !applied {
		t.Fatalf("missing receipt: %v %v", applied, err)
	}
	if _, err := f.repo.OperationApplied(ctx, "valid-operation", []byte("tampered")); err == nil {
		t.Fatal("tampered operation payload accepted by receipt")
	}
	if _, err := f.repo.RefOID(ctx, KindBranch, "main"); err != nil {
		t.Fatalf("branch lost: %v", err)
	}
	if _, err := f.repo.RefOID(ctx, KindTag, "v1"); err != nil {
		t.Fatalf("tag lost: %v", err)
	}
	if err := f.repo.CheckRefChanges(ctx, changes); err != ErrRefMoved && !strings.Contains(err.Error(), ErrRefMoved.Error()) {
		t.Fatalf("stale operation was not rejected: %v", err)
	}
}
