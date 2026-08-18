package git

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func prepareInspectionRepo(t *testing.T) (string, string) {
	t.Helper()
	repoPath := setupTestRepo(t)
	work := t.TempDir()
	if out, err := exec.Command("git", "clone", repoPath, work).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	for _, args := range [][]string{{"config", "user.email", "inspect@example.com"}, {"config", "user.name", "Inspect User"}, {"checkout", "-b", "main"}} {
		if out, err := exec.Command("git", append([]string{"-C", work}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "old name.txt"), []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "binary.bin"), []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "odd\tname.txt"), []byte("odd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "unicødé name.txt"), []byte("unicode\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "line\nbreak.txt"), []byte("newline path\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "dir", "nested.txt"), []byte("nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "initial subject", "-m", "initial body"}, {"push", "origin", "main"}} {
		if out, err := exec.Command("git", append([]string{"-C", work}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if out, err := exec.Command("git", "-C", work, "tag", "-a", "v1", "-m", "release v1").CombinedOutput(); err != nil {
		t.Fatalf("annotated tag: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", work, "push", "origin", "v1").CombinedOutput(); err != nil {
		t.Fatalf("push tag: %v\n%s", err, out)
	}

	if out, err := exec.Command("git", "-C", work, "mv", "old name.txt", "new name.txt").CombinedOutput(); err != nil {
		t.Fatalf("rename: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(work, "new name.txt"), []byte("one\ntwo\nTHREE\nfour\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "binary.bin"), []byte{'a', 0, 'c'}, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "rename and binary", "-m", "second body"}, {"push", "origin", "main"}} {
		if out, err := exec.Command("git", append([]string{"-C", work}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return repoPath, work
}

func TestGetCommitDetailAndRefs(t *testing.T) {
	repoPath, _ := prepareInspectionRepo(t)
	commits, err := GetCommits(context.Background(), repoPath, "main", 0, 2)
	if err != nil || len(commits) != 2 {
		t.Fatalf("commits: %v len=%d", err, len(commits))
	}
	detail, err := GetCommitDetail(context.Background(), repoPath, commits[1].Hash)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Subject != "initial subject" || detail.Body != "initial body" {
		t.Fatalf("unexpected message: subject=%q body=%q", detail.Subject, detail.Body)
	}
	if len(detail.Parents) != 0 {
		t.Fatalf("root commit parents: %v", detail.Parents)
	}
	if len(detail.Tags) != 1 || detail.Tags[0] != "v1" {
		t.Fatalf("tags: %v", detail.Tags)
	}
}

func TestGetCommitDiffHandlesRenameAndBinary(t *testing.T) {
	repoPath, _ := prepareInspectionRepo(t)
	commits, err := GetCommits(context.Background(), repoPath, "main", 0, 1)
	if err != nil || len(commits) != 1 {
		t.Fatalf("commits: %v", err)
	}
	detail, err := GetCommitDetail(context.Background(), repoPath, commits[0].Hash)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := GetCommitDiff(context.Background(), repoPath, detail)
	if err != nil {
		t.Fatal(err)
	}
	if diff.TotalFiles != 2 {
		t.Fatalf("files = %d, want 2: %+v", diff.TotalFiles, diff.Files)
	}
	var renamed, binary *FileDiff
	for i := range diff.Files {
		file := &diff.Files[i]
		if file.StatusCode == "R" {
			renamed = file
		}
		if file.Path == "binary.bin" {
			binary = file
		}
	}
	if renamed == nil || renamed.OldPath != "old name.txt" || renamed.Path != "new name.txt" {
		t.Fatalf("rename not preserved: %+v", renamed)
	}
	if renamed.Additions != 1 || renamed.Deletions != 1 || len(renamed.Hunks) == 0 {
		t.Fatalf("rename stats/hunks: %+v", renamed)
	}
	if binary == nil || !binary.Binary {
		t.Fatalf("binary change not detected: %+v", binary)
	}
}

func TestListFilesPreservesOddNames(t *testing.T) {
	repoPath, work := prepareInspectionRepo(t)
	head, err := exec.Command("git", "-C", work, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	gitlink := strings.TrimSpace(string(head))
	if out, err := exec.Command("git", "-C", work, "update-index", "--add", "--cacheinfo", "160000,"+gitlink+",vendor/submodule").CombinedOutput(); err != nil {
		t.Fatalf("add gitlink: %v\n%s", err, out)
	}
	for _, args := range [][]string{{"commit", "-m", "add submodule gitlink"}, {"push", "origin", "main"}} {
		if out, err := exec.Command("git", append([]string{"-C", work}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	files, err := ListFiles(context.Background(), repoPath, "main")
	if err != nil {
		t.Fatal(err)
	}
	foundOdd, foundUnicode, foundNewline, foundGitlink := false, false, false, false
	for _, path := range files {
		switch path {
		case "odd\tname.txt":
			foundOdd = true
		case "unicødé name.txt":
			foundUnicode = true
		case "line\nbreak.txt":
			foundNewline = true
		case "vendor/submodule":
			foundGitlink = true
		}
	}
	if !foundOdd || !foundUnicode || !foundNewline {
		t.Fatalf("odd/unicode paths missing from %q", files)
	}
	if foundGitlink {
		t.Fatalf("gitlink must not be returned as a file: %q", files)
	}
	exists, err := BlobExists(context.Background(), repoPath, "main", "vendor/submodule")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("gitlink reported as a blob")
	}
}

func TestParseUnifiedPatchTracksLineNumbers(t *testing.T) {
	patch := []byte("diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -2,2 +2,3 @@\n keep\n-old\n+new\n+extra\n")
	hunks := parseUnifiedPatch(patch)
	if len(hunks) != 1 || len(hunks[0].Lines) != 4 {
		t.Fatalf("hunks: %+v", hunks)
	}
	if line := hunks[0].Lines[1]; line.Kind != "delete" || line.OldLine != 3 || line.NewLine != 0 {
		t.Fatalf("delete line: %+v", line)
	}
	if line := hunks[0].Lines[2]; line.Kind != "add" || line.NewLine != 3 || line.OldLine != 0 {
		t.Fatalf("add line: %+v", line)
	}
}

func TestStreamBlob(t *testing.T) {
	repoPath, _ := prepareInspectionRepo(t)
	var out bytes.Buffer
	if err := StreamBlob(context.Background(), repoPath, "main", "new name.txt", &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "THREE") {
		t.Fatalf("unexpected blob: %q", got)
	}
}

func TestGetCommitDiffRootCommit(t *testing.T) {
	repoPath, _ := prepareInspectionRepo(t)
	commits, err := GetCommits(context.Background(), repoPath, "main", 0, 2)
	if err != nil || len(commits) != 2 {
		t.Fatalf("commits: %v len=%d", err, len(commits))
	}
	detail, err := GetCommitDetail(context.Background(), repoPath, commits[1].Hash)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := GetCommitDiff(context.Background(), repoPath, detail)
	if err != nil {
		t.Fatal(err)
	}
	if diff.TotalFiles != 6 {
		t.Fatalf("root files = %d, want 6: %+v", diff.TotalFiles, diff.Files)
	}
	for _, file := range diff.Files {
		if file.StatusCode != "A" {
			t.Fatalf("root file %q status = %q, want A", file.Path, file.StatusCode)
		}
	}
}

func TestGetCommitDiffDeletedFile(t *testing.T) {
	repoPath, work := prepareInspectionRepo(t)
	if err := os.Remove(filepath.Join(work, "odd\tname.txt")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "delete odd file"}, {"push", "origin", "main"}} {
		if out, err := exec.Command("git", append([]string{"-C", work}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commits, err := GetCommits(context.Background(), repoPath, "main", 0, 1)
	if err != nil || len(commits) != 1 {
		t.Fatalf("commits: %v", err)
	}
	detail, err := GetCommitDetail(context.Background(), repoPath, commits[0].Hash)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := GetCommitDiff(context.Background(), repoPath, detail)
	if err != nil {
		t.Fatal(err)
	}
	if diff.TotalFiles != 1 || diff.Files[0].StatusCode != "D" || diff.Files[0].OldPath != "odd\tname.txt" {
		t.Fatalf("delete diff: %+v", diff.Files)
	}
}

func TestGetCommitDiffMergeUsesFirstParent(t *testing.T) {
	repoPath := setupTestRepo(t)
	work := t.TempDir()
	if out, err := exec.Command("git", "clone", repoPath, work).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	for _, args := range [][]string{{"config", "user.email", "merge@example.com"}, {"config", "user.name", "Merge User"}, {"checkout", "-b", "main"}} {
		if out, err := exec.Command("git", append([]string{"-C", work}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "base"}, {"checkout", "-b", "feature"}} {
		if out, err := exec.Command("git", append([]string{"-C", work}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "feature"}, {"checkout", "main"}} {
		if out, err := exec.Command("git", append([]string{"-C", work}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "main.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "main"}, {"merge", "--no-ff", "feature", "-m", "merge feature"}, {"push", "origin", "main"}} {
		if out, err := exec.Command("git", append([]string{"-C", work}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commits, err := GetCommits(context.Background(), repoPath, "main", 0, 1)
	if err != nil || len(commits) != 1 {
		t.Fatalf("commits: %v", err)
	}
	detail, err := GetCommitDetail(context.Background(), repoPath, commits[0].Hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Parents) != 2 {
		t.Fatalf("merge parents = %v", detail.Parents)
	}
	diff, err := GetCommitDiff(context.Background(), repoPath, detail)
	if err != nil {
		t.Fatal(err)
	}
	if diff.TotalFiles != 1 || diff.Files[0].Path != "feature.txt" || diff.Files[0].StatusCode != "A" {
		t.Fatalf("first-parent merge diff: %+v", diff.Files)
	}
}

func TestBlobHelpersRejectNonBlobPaths(t *testing.T) {
	repoPath, _ := prepareInspectionRepo(t)
	if _, err := GetBlobSize(context.Background(), repoPath, "main", "dir"); err == nil {
		t.Fatal("GetBlobSize accepted a tree path")
	}
	if _, err := GetBlob(context.Background(), repoPath, "main", "dir"); err == nil {
		t.Fatal("GetBlob accepted a tree path")
	}
	var out bytes.Buffer
	if err := StreamBlob(context.Background(), repoPath, "main", "dir", &out); err == nil {
		t.Fatal("StreamBlob accepted a tree path")
	}
}
