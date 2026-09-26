package git

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestReadsCommitsTreesAndBlobs(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.write(t, "README.md", "# Hello\n")
	f.write(t, "src/main.go", "package main\n")
	first := f.commit(t, "Initial commit\n\nWith a body.")
	f.push(t, "main")

	head, err := f.repo.ResolveCommit(ctx, "refs/heads/main")
	if err != nil {
		t.Fatalf("ResolveCommit: %v", err)
	}
	if head != first {
		t.Fatalf("ResolveCommit = %s, want %s", head, first)
	}
	short, err := f.repo.ResolveCommit(ctx, first[:7])
	if err != nil || short != first {
		t.Fatalf("ResolveCommit(short) = %s, %v", short, err)
	}

	commit, err := f.repo.Commit(ctx, head)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if commit.Subject != "Initial commit" || commit.Body != "With a body." {
		t.Errorf("Subject/Body = %q / %q", commit.Subject, commit.Body)
	}
	if commit.Author.Name != "Test Author" || commit.Author.Email != "author@example.com" {
		t.Errorf("Author = %+v", commit.Author)
	}
	if _, offset := commit.Author.When.Zone(); offset != 3*3600+30*60 {
		t.Errorf("author timezone offset = %d, want +03:30", offset)
	}
	if len(commit.Parents) != 0 {
		t.Errorf("Parents = %v, want none", commit.Parents)
	}

	root, err := f.repo.Tree(ctx, commit.Tree)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if len(root) != 2 || root[0].Name != "README.md" || root[1].Name != "src" || root[1].Kind != EntryDir {
		t.Fatalf("root tree = %+v", root)
	}

	entry, err := f.repo.Entry(ctx, head, "src/main.go")
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if entry.Kind != EntryFile {
		t.Errorf("Entry kind = %s", entry.Kind)
	}
	data, err := f.repo.Blob(ctx, entry.Hash, 1024)
	if err != nil || string(data) != "package main\n" {
		t.Fatalf("Blob = %q, %v", data, err)
	}
	if _, err := f.repo.Blob(ctx, entry.Hash, 4); err == nil {
		t.Fatal("expected a TooLargeError for a blob over the limit")
	} else {
		var tl *TooLargeError
		if !errors.As(err, &tl) || tl.Size != int64(len("package main\n")) {
			t.Fatalf("Blob over limit = %v", err)
		}
	}

	content, err := f.repo.FileAt(ctx, head, "README.md", 1024)
	if err != nil || string(content) != "# Hello\n" {
		t.Fatalf("FileAt = %q, %v", content, err)
	}
	if _, err := f.repo.FileAt(ctx, head, "src", 1024); !errors.Is(err, ErrNotFound) {
		t.Errorf("FileAt(dir) = %v, want ErrNotFound", err)
	}
	if _, err := f.repo.FileAt(ctx, head, "missing.txt", 1024); !errors.Is(err, ErrNotFound) {
		t.Errorf("FileAt(missing) = %v, want ErrNotFound", err)
	}
	if _, err := f.repo.Entry(ctx, head, "src/../README.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Entry with '..' = %v, want ErrNotFound", err)
	}
}

func TestResolveCommitMissing(t *testing.T) {
	f := newFixture(t)
	for _, rev := range []string{"refs/heads/nope", strings.Repeat("0", 40), "-flag", "", "bad\nname"} {
		if _, err := f.repo.ResolveCommit(context.Background(), rev); err == nil {
			t.Errorf("ResolveCommit(%q): expected an error", rev)
		}
	}
}

func TestRefsAndAncestry(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.write(t, "a.txt", "a\n")
	first := f.commit(t, "first")
	f.write(t, "b.txt", "b\n")
	second := f.commit(t, "second")
	gitCmd(t, f.work, "tag", "-a", "v1.0.0", "-m", "release", first)
	gitCmd(t, f.work, "tag", "light", second)
	f.push(t, "main", "v1.0.0", "light")

	refs, err := f.repo.Refs(ctx)
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	main, ok := findRef(refs, KindBranch, "main")
	if !ok || main.Commit != second {
		t.Fatalf("main = %+v", main)
	}
	annotated, ok := findRef(refs, KindTag, "v1.0.0")
	if !ok || annotated.Commit != first || annotated.Target == first {
		t.Fatalf("annotated tag = %+v, want commit peeled from a distinct tag object", annotated)
	}
	light, ok := findRef(refs, KindTag, "light")
	if !ok || light.Commit != second || light.Target != second {
		t.Fatalf("lightweight tag = %+v", light)
	}

	if yes, err := f.repo.IsAncestor(ctx, first, second); err != nil || !yes {
		t.Errorf("IsAncestor(first, second) = %v, %v", yes, err)
	}
	if yes, err := f.repo.IsAncestor(ctx, second, first); err != nil || yes {
		t.Errorf("IsAncestor(second, first) = %v, %v", yes, err)
	}
	if base, err := f.repo.MergeBase(ctx, first, second); err != nil || base != first {
		t.Errorf("MergeBase = %s, %v", base, err)
	}

	n, capped, err := f.repo.CountCommits(ctx, second, []string{first}, 100)
	if err != nil || n != 1 || capped {
		t.Errorf("CountCommits = %d, %v, %v", n, capped, err)
	}
	n, capped, err = f.repo.CountCommits(ctx, second, nil, 1)
	if err != nil || n != 1 || !capped {
		t.Errorf("capped CountCommits = %d, %v, %v", n, capped, err)
	}

	peeled, err := f.repo.ResolveCommit(ctx, "refs/tags/v1.0.0")
	if err != nil || peeled != first {
		t.Errorf("ResolveCommit(annotated tag) = %s, %v", peeled, err)
	}
}

func TestCountNewCommits(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.commit(t, "base")
	f.push(t, "main")
	gitCmd(t, f.work, "checkout", "--quiet", "-b", "feature")
	f.commit(t, "feature one")
	tip := f.commit(t, "feature two")
	f.push(t, "feature")

	n, _, err := f.repo.CountNewCommits(ctx, tip, KindBranch, "feature", 100)
	if err != nil || n != 2 {
		t.Fatalf("CountNewCommits = %d, %v, want 2", n, err)
	}

	gitCmd(t, f.work, "tag", "t1", tip)
	f.push(t, "t1")
	n, _, err = f.repo.CountNewCommits(ctx, tip, KindTag, "t1", 100)
	if err != nil || n != 0 {
		t.Fatalf("CountNewCommits(tag on an existing branch) = %d, %v, want 0", n, err)
	}
}

func TestLogPagesAndPathFilter(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	var hashes []string
	for i := 0; i < 5; i++ {
		if i%2 == 0 {
			f.write(t, "even.txt", strings.Repeat("x", i+1))
		} else {
			f.write(t, "odd.txt", strings.Repeat("y", i+1))
		}
		hashes = append(hashes, f.commit(t, "commit"))
	}
	f.push(t, "main")
	head := hashes[len(hashes)-1]

	page, more, err := f.repo.Log(ctx, head, "", 0, 2)
	if err != nil || len(page) != 2 || !more || page[0].Hash != hashes[4] || page[1].Hash != hashes[3] {
		t.Fatalf("first page = %d commits, more=%v, err=%v", len(page), more, err)
	}
	page, more, err = f.repo.Log(ctx, head, "", 4, 2)
	if err != nil || len(page) != 1 || more || page[0].Hash != hashes[0] {
		t.Fatalf("last page = %d commits, more=%v, err=%v", len(page), more, err)
	}
	evens, _, err := f.repo.Log(ctx, head, "even.txt", 0, 10)
	if err != nil || len(evens) != 3 {
		t.Fatalf("path-filtered log = %d commits, %v, want 3", len(evens), err)
	}
}

func TestConcurrentReadsShareOneReader(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.write(t, "a.txt", "a\n")
	head := f.commit(t, "a")
	f.push(t, "main")

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.repo.FileAt(ctx, head, "a.txt", 1024); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent FileAt: %v", err)
	}
}

func TestReaderRecoversAfterCancellation(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "a\n")
	head := f.commit(t, "a")
	f.push(t, "main")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = f.repo.ResolveCommit(cancelled, head)

	if _, err := f.repo.ResolveCommit(context.Background(), head); err != nil {
		t.Fatalf("ResolveCommit after a cancelled request: %v", err)
	}
}
