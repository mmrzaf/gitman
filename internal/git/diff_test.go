package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffStatuses(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.write(t, "keep.txt", "one\ntwo\nthree\n")
	f.write(t, "gone.txt", "bye\n")
	f.write(t, "move-me.txt", strings.Repeat("stable content line\n", 20))
	base := f.commit(t, "base")

	f.write(t, "keep.txt", "one\nTWO\nthree\nfour\n")
	if err := os.Remove(filepath.Join(f.work, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, f.work, "mv", "move-me.txt", "moved.txt")
	f.write(t, "new.bin", "\x00\x01\x02binary")
	head := f.commit(t, "change everything")
	f.push(t, "main")

	diff, err := f.repo.Diff(ctx, base, head, DefaultDiffLimits)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	byPath := map[string]DiffFile{}
	for _, file := range diff.Files {
		byPath[file.Path()] = file
	}

	keep := byPath["keep.txt"]
	if keep.Status != StatusModified || keep.Additions != 2 || keep.Deletions != 1 {
		t.Errorf("keep.txt = %+v", keep)
	}
	if len(keep.Hunks) != 1 {
		t.Fatalf("keep.txt hunks = %d", len(keep.Hunks))
	}
	var sawAdded bool
	for _, line := range keep.Hunks[0].Lines {
		if line.Kind == LineAdded && line.Text == "TWO" && line.NewNumber == 2 && line.OldNumber == 0 {
			sawAdded = true
		}
	}
	if !sawAdded {
		t.Errorf("keep.txt hunk lines = %+v", keep.Hunks[0].Lines)
	}

	if gone := byPath["gone.txt"]; gone.Status != StatusDeleted || gone.Deletions != 1 {
		t.Errorf("gone.txt = %+v", gone)
	}
	moved := byPath["moved.txt"]
	if moved.Status != StatusRenamed || moved.OldPath != "move-me.txt" || moved.Similarity != 100 {
		t.Errorf("moved.txt = %+v", moved)
	}
	if bin := byPath["new.bin"]; bin.Status != StatusAdded || !bin.Binary || len(bin.Hunks) != 0 {
		t.Errorf("new.bin = %+v", bin)
	}
}

func TestDiffRootCommit(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.write(t, "a.txt", "a\n")
	root := f.commit(t, "root")
	f.push(t, "main")

	diff, err := f.repo.Diff(ctx, "", root, DefaultDiffLimits)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(diff.Files) != 1 || diff.Files[0].Status != StatusAdded || diff.Files[0].Additions != 1 {
		t.Fatalf("root diff = %+v", diff.Files)
	}
}

func TestDiffLimits(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	base := f.commit(t, "empty")
	for i := 0; i < 5; i++ {
		f.write(t, "file"+string(rune('a'+i))+".txt", strings.Repeat("line\n", 10))
	}
	f.write(t, "long.txt", strings.Repeat("x", 5000)+"\n")
	head := f.commit(t, "many files")
	f.push(t, "main")

	diff, err := f.repo.Diff(ctx, base, head, DiffLimits{MaxFiles: 3, MaxLines: 15, MaxLineBytes: 100})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(diff.Files) != 3 || diff.MoreFiles != 3 {
		t.Fatalf("files = %d, more = %d, want 3 and 3", len(diff.Files), diff.MoreFiles)
	}
	lines := 0
	truncated := false
	for _, file := range diff.Files {
		for _, h := range file.Hunks {
			lines += len(h.Lines)
		}
		truncated = truncated || file.PatchTruncated
	}
	if lines > 15 {
		t.Errorf("returned %d patch lines, over the limit of 15", lines)
	}
	if !truncated {
		t.Error("expected at least one file to be marked PatchTruncated")
	}

	longDiff, err := f.repo.Diff(ctx, base, head, DiffLimits{MaxFiles: 10, MaxLines: 1000, MaxLineBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range longDiff.Files {
		if file.NewPath != "long.txt" {
			continue
		}
		line := file.Hunks[0].Lines[0]
		if !line.Truncated || len(line.Text) > 100 {
			t.Errorf("long line: truncated=%v length=%d", line.Truncated, len(line.Text))
		}
	}
}

func TestCompare(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.write(t, "a.txt", "a\n")
	base := f.commit(t, "base")
	f.push(t, "main")
	gitCmd(t, f.work, "checkout", "--quiet", "-b", "develop")
	f.write(t, "b.txt", "b\n")
	f.commit(t, "one")
	f.write(t, "c.txt", "c\n")
	head := f.commit(t, "two")
	f.push(t, "develop")

	cmp, err := f.repo.Compare(ctx, base, head, 10, DefaultDiffLimits)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if cmp.MergeBase != base || len(cmp.Commits) != 2 || cmp.MoreCommits {
		t.Fatalf("Compare = base %s, %d commits, more=%v", cmp.MergeBase, len(cmp.Commits), cmp.MoreCommits)
	}
	if len(cmp.Diff.Files) != 2 {
		t.Fatalf("Compare diff files = %d, want 2", len(cmp.Diff.Files))
	}
}

func TestParseHunkHeader(t *testing.T) {
	cases := map[string][2]int{
		"@@ -1,3 +1,4 @@":             {1, 1},
		"@@ -0,0 +1 @@":               {0, 1},
		"@@ -12 +15,2 @@ func main()": {12, 15},
	}
	for header, want := range cases {
		o, n, ok := parseHunkHeader(header)
		if !ok || o != want[0] || n != want[1] {
			t.Errorf("parseHunkHeader(%q) = %d, %d, %v", header, o, n, ok)
		}
	}
	if _, _, ok := parseHunkHeader("@@ garbage @@"); ok {
		t.Error("expected a malformed header to be rejected")
	}
}
