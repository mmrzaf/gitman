package git

import (
	"context"
	"errors"
	"testing"
)

func TestDeleteRefReturnsWhatItPointedAt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.write(t, "a.txt", "a\n")
	first := f.commit(t, "first")
	f.write(t, "b.txt", "b\n")
	second := f.commit(t, "second")
	gitCmd(t, f.work, "tag", "light", first)
	gitCmd(t, f.work, "tag", "-a", "-m", "annotated", "annotated", second)
	annotated := gitCmd(t, f.work, "rev-parse", "annotated")
	gitCmd(t, f.work, "branch", "release/1.0", first)
	f.push(t, "main", "release/1.0", "light", "annotated")

	for _, tc := range []struct {
		kind Kind
		name string
		// old is what a hook reports as the old value of a pushed delete:
		// the tag object of an annotated tag, not its commit.
		old string
	}{
		{KindBranch, "release/1.0", first},
		{KindTag, "light", first},
		{KindTag, "annotated", annotated},
	} {
		old, err := f.repo.DeleteRef(ctx, tc.kind, tc.name)
		if err != nil || old != tc.old {
			t.Fatalf("DeleteRef(%s %s) = %q, %v, want %q", tc.kind, tc.name, old, err, tc.old)
		}
		if _, err := f.repo.DeleteRef(ctx, tc.kind, tc.name); !errors.Is(err, ErrNotFound) {
			t.Errorf("deleting %s %s again = %v, want ErrNotFound", tc.kind, tc.name, err)
		}
	}
	refs, err := f.repo.Refs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Name != "main" {
		t.Fatalf("refs left = %+v, want only main", refs)
	}
}

func TestDeleteRefLeavesWhatItWasNotAskedToDelete(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.write(t, "a.txt", "a\n")
	first := f.commit(t, "first")
	f.write(t, "b.txt", "b\n")
	second := f.commit(t, "second")
	f.push(t, "main")

	// A name that is a flag, a path out of the ref namespace or a branch
	// that only exists as a tag is not found, and nothing is deleted.
	gitCmd(t, f.work, "tag", "only-a-tag", first)
	f.push(t, "only-a-tag")
	for _, name := range []string{"--all", "../heads/main", "only-a-tag", "HEAD", ""} {
		if _, err := f.repo.DeleteRef(ctx, KindBranch, name); !errors.Is(err, ErrNotFound) {
			t.Errorf("DeleteRef(branch %q) = %v, want ErrNotFound", name, err)
		}
	}
	if refs, err := f.repo.Refs(ctx); err != nil || len(refs) != 2 {
		t.Fatalf("refs = %+v, %v; want main and the tag untouched", refs, err)
	}

	// A ref that moved after it was read is not deleted.
	if err := f.repo.deleteRefIf(ctx, "refs/heads/main", first); !errors.Is(err, ErrRefMoved) {
		t.Fatalf("deleteRefIf(stale) = %v, want ErrRefMoved", err)
	}
	if got := mustResolve(t, f.repo, "refs/heads/main"); got != second {
		t.Fatalf("main = %s after a refused delete, want %s", got, second)
	}
}

func mustResolve(t *testing.T, r *Repo, rev string) string {
	t.Helper()
	hash, err := r.ResolveCommit(context.Background(), rev)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
