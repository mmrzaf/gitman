package push

import (
	"context"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/git"
)

func TestPushRecoveryReplaysReceiptExactlyOnce(t *testing.T) {
	f := newReceiveFixture(t)
	ctx := context.Background()
	hash := f.commit(t, "receipt", "refs/heads/main")
	runGit(t, f.bare, "update-ref", "-d", "refs/heads/main")
	updates := []Update{{Old: strings.Repeat("0", 40), New: hash, Ref: "refs/heads/main", Kind: git.KindBranch, Name: "main"}}
	pc, err := f.hook.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	records, err := f.hook.prepareRecords(ctx, pc, updates)
	if err != nil {
		t.Fatal(err)
	}
	op, err := f.hook.Repos.BeginOperation(ctx, pc.repo.ID, "push", "", pc.person.ID, pushIntent{Repository: pc.repo, PersonID: pc.person.ID, Records: records})
	if err != nil {
		t.Fatal(err)
	}
	// Crash boundary: Git committed, PostgreSQL push/run rows have not committed.
	if err := f.hook.Git.ApplyOperation(ctx, op.ID, []git.RefChange{{Ref: updates[0].Ref, Old: updates[0].Old, New: hash}}, []string{hash}, op.Payload); err != nil {
		t.Fatal(err)
	}
	// PostgreSQL JSONB normalizes whitespace/order: compare the stored payload,
	// not the original marshaler representation.
	if err := f.db.Q.QueryRow(ctx, `SELECT payload FROM repository_operations WHERE id=$1`, op.ID).Scan(&op.Payload); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := f.hook.RecoverPush(ctx, *op); err != nil {
			t.Fatal(err)
		}
	}
	var pushes, runs, pending int
	if err := f.db.Q.QueryRow(ctx, `SELECT (SELECT count(*) FROM pushes),(SELECT count(*) FROM runs),(SELECT count(*) FROM repository_operations WHERE completed_at IS NULL)`).Scan(&pushes, &runs, &pending); err != nil {
		t.Fatal(err)
	}
	if pushes != 1 || runs != 1 || pending != 0 {
		t.Fatalf("pushes=%d runs=%d pending=%d", pushes, runs, pending)
	}
	// Pin must keep a detached historical commit fetchable after aggressive GC.
	runGit(t, f.bare, "update-ref", "-d", "refs/heads/main")
	runGit(t, f.bare, "reflog", "expire", "--expire=now", "--all")
	runGit(t, f.bare, "gc", "--prune=now")
	if _, err := f.hook.Git.ResolveCommit(ctx, hash); err != nil {
		t.Fatalf("retained commit lost: %v", err)
	}
}

func TestPushIntentRecoversBeforeGitApply(t *testing.T) {
	f := newReceiveFixture(t)
	ctx := context.Background()
	hash := f.commit(t, "intent", "refs/heads/main")
	runGit(t, f.bare, "update-ref", "-d", "refs/heads/main")
	updates := []Update{{Old: strings.Repeat("0", 40), New: hash, Ref: "refs/heads/main", Kind: git.KindBranch, Name: "main"}}
	pc, _ := f.hook.load(ctx)
	records, err := f.hook.prepareRecords(ctx, pc, updates)
	if err != nil {
		t.Fatal(err)
	}
	op, err := f.hook.Repos.BeginOperation(ctx, pc.repo.ID, "push", "", pc.person.ID, pushIntent{Repository: pc.repo, PersonID: pc.person.ID, Records: records})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.hook.Repos.WithMutation(ctx, pc.repo.ID, func() error { t.Fatal("unfinished operation allowed a mutation"); return nil }); err == nil {
		t.Fatal("expected pending operation refusal")
	}
	if err := f.hook.RecoverPush(ctx, *op); err != nil {
		t.Fatal(err)
	}
}

func TestProcReceivePacketBounds(t *testing.T) {
	for _, input := range []string{"zzzz", "0001", "ffff", "0008x"} {
		if _, err := readPacket(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}
