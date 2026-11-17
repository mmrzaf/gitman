package push

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/git"
)

func TestPushRejectsNestedRefWithoutLeavingPendingIntent(t *testing.T) {
	f := newReceiveFixture(t)
	ctx := context.Background()
	hash := f.commit(t, "one", "refs/heads/topic")
	update := Update{Old: zeroHash, New: hash, Ref: "refs/heads/topic/child", Kind: git.KindBranch, Name: "topic/child"}
	if err := f.hook.ApplyPush(ctx, []Update{update}); !errors.Is(err, ErrRejected) {
		t.Fatalf("nested push = %v, want rejection", err)
	}
	if !strings.Contains(f.out.String(), "conflicts with refs/heads/topic") {
		t.Fatalf("missing actionable namespace error: %q", f.out.String())
	}
	if _, err := f.hook.Git.RefOID(ctx, git.KindBranch, "topic/child"); !errors.Is(err, git.ErrNotFound) {
		t.Fatalf("conflicting ref was created: %v", err)
	}
	ops, err := f.hook.Repos.PendingOperations(ctx)
	if err != nil || len(ops) != 0 {
		t.Fatalf("rejected push left pending intents: %v %v", ops, err)
	}
	// A valid ref must still be pushable without operator intervention.
	valid := Update{Old: zeroHash, New: hash, Ref: "refs/heads/other", Kind: git.KindBranch, Name: "other"}
	if err := f.hook.ApplyPush(ctx, []Update{valid}); err != nil {
		t.Fatalf("valid push after rejection: %v", err)
	}
}

func TestPushRejectsPrefixConflictsWithinOneBatch(t *testing.T) {
	f := newReceiveFixture(t)
	hash := f.commit(t, "one", "refs/heads/main")
	changes := []Update{
		{Old: zeroHash, New: hash, Ref: "refs/heads/new", Kind: git.KindBranch, Name: "new"},
		{Old: zeroHash, New: hash, Ref: "refs/heads/new/child", Kind: git.KindBranch, Name: "new/child"},
	}
	if err := f.hook.ApplyPush(context.Background(), changes); !errors.Is(err, ErrRejected) {
		t.Fatalf("conflicting batch = %v", err)
	}
	ops, err := f.hook.Repos.PendingOperations(context.Background())
	if err != nil || len(ops) != 0 {
		t.Fatalf("conflicting batch left pending intent: %v %v", ops, err)
	}
}

func TestPushRejectsParentReplacementInOneBatch(t *testing.T) {
	f := newReceiveFixture(t)
	ctx := context.Background()
	hash := f.commit(t, "one", "refs/heads/topic")
	changes := []Update{
		{Old: hash, New: zeroHash, Ref: "refs/heads/topic", Kind: git.KindBranch, Name: "topic"},
		{Old: zeroHash, New: hash, Ref: "refs/heads/topic/child", Kind: git.KindBranch, Name: "topic/child"},
	}
	if err := f.hook.ApplyPush(ctx, changes); !errors.Is(err, ErrRejected) {
		t.Fatalf("atomic parent replacement = %v", err)
	}
	if old, err := f.hook.Git.RefOID(ctx, git.KindBranch, "topic"); err != nil || old != hash {
		t.Fatalf("parent ref changed: %s %v", old, err)
	}
}

func TestPushRejectsInvalidAndDuplicateUpdates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		make    func(hash string) []Update
		message string
	}{
		{"duplicate", func(hash string) []Update {
			u := Update{Old: zeroHash, New: hash, Ref: "refs/heads/new", Kind: git.KindBranch, Name: "new"}
			return []Update{u, u}
		}, "more than once"},
		{"no-op", func(hash string) []Update {
			return []Update{{Old: hash, New: hash, Ref: "refs/heads/main", Kind: git.KindBranch, Name: "main"}}
		}, "identical"},
		{"both zero", func(string) []Update {
			return []Update{{Old: zeroHash, New: zeroHash, Ref: "refs/heads/new", Kind: git.KindBranch, Name: "new"}}
		}, "identical"},
		{"mixed hash formats", func(hash string) []Update {
			return []Update{{Old: strings.Repeat("0", 64), New: hash, Ref: "refs/heads/new", Kind: git.KindBranch, Name: "new"}}
		}, "malformed"},
		{"mismatched ref", func(hash string) []Update {
			return []Update{{Old: zeroHash, New: hash, Ref: "refs/heads/new", Kind: git.KindTag, Name: "new"}}
		}, "malformed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReceiveFixture(t)
			hash := f.commit(t, "source", "refs/heads/main")
			if err := f.hook.ApplyPush(context.Background(), tc.make(hash)); !errors.Is(err, ErrRejected) {
				t.Fatalf("invalid push = %v", err)
			}
			if !strings.Contains(f.out.String(), tc.message) {
				t.Fatalf("missing validation reason %q in %q", tc.message, f.out.String())
			}
			ops, err := f.hook.Repos.PendingOperations(context.Background())
			if err != nil || len(ops) != 0 {
				t.Fatalf("invalid push left pending intent: %v %v", ops, err)
			}
		})
	}
}

func TestPushRecoversAfterDatabaseRecordingFailure(t *testing.T) {
	f := newReceiveFixture(t)
	ctx := context.Background()
	hash := f.commit(t, "durable", "refs/heads/main")
	runGit(t, f.bare, "update-ref", "-d", "refs/heads/main")
	pc, err := f.hook.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	records, err := f.hook.prepareRecords(ctx, pc, []Update{{Old: zeroHash, New: hash, Ref: "refs/heads/main", Kind: git.KindBranch, Name: "main"}})
	if err != nil {
		t.Fatal(err)
	}
	op, err := f.hook.Repos.BeginOperation(ctx, pc.repo.ID, "push", "", pc.person.ID, pushIntent{Repository: pc.repo, PersonID: pc.person.ID, Records: records})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.hook.Git.ApplyOperation(ctx, op.ID, []git.RefChange{{Ref: "refs/heads/main", Old: zeroHash, New: hash}}, []string{hash}, op.Payload); err != nil {
		t.Fatal(err)
	}
	// A real database constraint forces the entire recording transaction to
	// roll back after Git has committed its atomic ref transaction.
	if _, err := f.db.Q.Exec(ctx, `ALTER TABLE pushes ADD CONSTRAINT test_reject_new_pushes CHECK (false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if err := f.hook.RecoverPush(ctx, *op); err == nil {
		t.Fatal("expected database recording failure")
	}
	var recorded, pending int
	if err := f.db.Q.QueryRow(ctx, `SELECT (SELECT count(*) FROM pushes),(SELECT count(*) FROM repository_operations WHERE completed_at IS NULL)`).Scan(&recorded, &pending); err != nil {
		t.Fatal(err)
	}
	if recorded != 0 || pending != 1 {
		t.Fatalf("failed DB commit recorded=%d pending=%d", recorded, pending)
	}
	if _, err := f.db.Q.Exec(ctx, `ALTER TABLE pushes DROP CONSTRAINT test_reject_new_pushes`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := f.hook.Repos.RecoverOperations(ctx, f.hook.RecoverPush); err != nil {
			t.Fatal(err)
		}
	}
	var pushes, runs int
	if err := f.db.Q.QueryRow(ctx, `SELECT (SELECT count(*) FROM pushes),(SELECT count(*) FROM runs)`).Scan(&pushes, &runs); err != nil {
		t.Fatal(err)
	}
	if pushes != 1 || runs != 1 {
		t.Fatalf("recovery duplicated or lost DB rows: pushes=%d runs=%d", pushes, runs)
	}
}
