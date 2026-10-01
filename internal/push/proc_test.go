package push

import (
	"context"
	"errors"
	"os/exec"
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

func TestRecoveryRejectsUnappliedStaleIntent(t *testing.T) {
	f := newReceiveFixture(t)
	ctx := t.Context()
	older := f.commit(t, "older", "refs/heads/main")
	newer := f.commit(t, "newer", "refs/heads/main")
	pc, err := f.hook.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	updates := []Update{{Old: zeroHash, New: older, Ref: "refs/heads/main", Kind: git.KindBranch, Name: "main"}}
	records, err := f.hook.prepareRecords(ctx, pc, updates)
	if err != nil {
		t.Fatal(err)
	}
	op, err := f.hook.Repos.BeginOperation(ctx, pc.repo.ID, "push", "", pc.person.ID, pushIntent{Repository: pc.repo, PersonID: pc.person.ID, Records: records})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.hook.Repos.RecoverOperations(ctx, f.hook.RecoverPush); err != nil {
		t.Fatal(err)
	}
	applied, err := f.hook.Git.OperationApplied(ctx, op.ID, op.Payload)
	if err != nil || applied {
		t.Fatalf("stale intent applied=%v err=%v", applied, err)
	}
	var reason string
	if err := f.db.Q.QueryRow(ctx, `SELECT error FROM repository_operations WHERE id=$1 AND completed_at IS NOT NULL`, op.ID).Scan(&reason); err != nil || reason == "" {
		t.Fatalf("missing rejection receipt: %q %v", reason, err)
	}
	if err := f.hook.ApplyPush(ctx, []Update{{Old: zeroHash, New: newer, Ref: "refs/heads/recovered", Kind: git.KindBranch, Name: "recovered"}}); err != nil {
		t.Fatalf("valid push after recovery: %v", err)
	}
}

func TestPushMetadataHandlesNULAndNestedTags(t *testing.T) {
	for _, kind := range []string{"nul", "oversized", "nested-tag"} {
		t.Run(kind, func(t *testing.T) {
			f := newReceiveFixture(t)
			ctx := t.Context()
			commit := f.commit(t, "source", "refs/heads/main")
			var target string
			if kind == "nul" || kind == "oversized" {
				tree := runGit(t, f.work, "rev-parse", "HEAD^{tree}")
				message := "subject\x00suffix\n"
				if kind == "oversized" {
					message = strings.Repeat("x", (1<<20)+1)
				}
				raw := "tree " + tree + "\nauthor Author <author@example.invalid> 1700000000 +0000\ncommitter Author <author@example.invalid> 1700000000 +0000\n\n" + message
				cmd := exec.Command("git", "hash-object", "--literally", "-t", "commit", "-w", "--stdin")
				cmd.Dir = f.bare
				cmd.Stdin = strings.NewReader(raw)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("write commit: %v %s", err, out)
				}
				target = strings.TrimSpace(string(out))
			} else {
				runGit(t, f.work, "tag", "-a", "inner", "-m", "inner", commit)
				runGit(t, f.work, "tag", "-a", "outer", "-m", "outer", "inner")
				runGit(t, f.work, "push", "--quiet", f.bare, "refs/tags/outer:refs/tags/staged")
				target = runGit(t, f.bare, "rev-parse", "refs/tags/staged")
				runGit(t, f.bare, "update-ref", "-d", "refs/tags/staged")
			}
			update := Update{Old: zeroHash, New: target, Ref: "refs/tags/release", Kind: git.KindTag, Name: "release"}
			if kind == "oversized" {
				if err := f.hook.ApplyPush(ctx, []Update{update}); err == nil {
					t.Fatal("accepted unreadable commit metadata")
				}
				pending, err := f.hook.Repos.PendingOperations(ctx)
				if err != nil || len(pending) != 0 {
					t.Fatalf("rejected commit left pending operations: %v %v", pending, err)
				}
				if _, err := f.hook.Git.RefOID(ctx, git.KindTag, "release"); !errors.Is(err, git.ErrNotFound) {
					t.Fatalf("rejected commit moved ref: %v", err)
				}
				update.New = commit
			}
			if err := f.hook.ApplyPush(ctx, []Update{update}); err != nil {
				t.Fatal(err)
			}
			if err := f.hook.Repos.RecoverOperations(ctx, f.hook.RecoverPush); err != nil {
				t.Fatal(err)
			}
			var subject, indexed string
			if err := f.db.Q.QueryRow(ctx, `SELECT m.subject,r.commit_hash FROM refs r JOIN commit_metadata m ON m.repo_id=r.repo_id AND m.hash=r.commit_hash WHERE r.repo_id=$1 AND r.kind='tag' AND r.name='release'`, f.hook.Ctx.RepoID).Scan(&subject, &indexed); err != nil {
				t.Fatal(err)
			}
			if kind == "nul" && subject != "subject�suffix" {
				t.Fatalf("subject=%q", subject)
			}
			if kind == "nested-tag" && indexed != commit {
				t.Fatalf("indexed=%s want %s", indexed, commit)
			}
			pending, err := f.hook.Repos.PendingOperations(ctx)
			if err != nil || len(pending) != 0 {
				t.Fatalf("pending=%v err=%v", pending, err)
			}
		})
	}
}
