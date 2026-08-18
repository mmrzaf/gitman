package handlers

import (
	"strings"
	"testing"
)

func TestHighlightSourceLineEscapesHTML(t *testing.T) {
	language := detectSourceLanguage("main.go")
	got := string(highlightSourceLine(`if value == "<script>alert(1)</script>" { // <b>`, language))
	if strings.Contains(got, "<script>") || strings.Contains(got, "<b>") {
		t.Fatalf("highlighted source contains raw HTML: %s", got)
	}
	if !strings.Contains(got, `class="syn-keyword"`) || !strings.Contains(got, "&lt;script&gt;") || !strings.Contains(got, `class="syn-comment"`) {
		t.Fatalf("highlighting missing expected safe spans: %s", got)
	}
}

func TestSourceLinesPreserveUnicodeAndTerminalNewline(t *testing.T) {
	lines := sourceLines([]byte("hello 世界\nsecond\n"), detectSourceLanguage("note.txt"))
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	if got := string(lines[0].Highlighted); got != "hello 世界" {
		t.Fatalf("unicode line = %q", got)
	}
}

func TestRankFileMatchesPrefersBasenamePrefix(t *testing.T) {
	files := []string{
		"internal/worker/worker_test.go",
		"internal/worker/worker.go",
		"docs/worker-notes.md",
		"worker.txt",
	}
	matches := rankFileMatches(files, "worker.g", "me", "repo", "main", 10)
	if len(matches) == 0 || matches[0].Path != "internal/worker/worker.go" {
		t.Fatalf("matches: %+v", matches)
	}
	if !strings.Contains(matches[0].URL, "ref=main") || !strings.Contains(matches[0].URL, "path=internal%2Fworker%2Fworker.go") {
		t.Fatalf("unexpected URL: %s", matches[0].URL)
	}
}

func TestFileSearchSupportsSubsequence(t *testing.T) {
	if score := fileSearchScore("internal/handlers/repo_browse.go", "rbrw"); score < 0 {
		t.Fatalf("expected subsequence match, score=%d", score)
	}
	if score := fileSearchScore("README.md", "zzz"); score >= 0 {
		t.Fatalf("unexpected match, score=%d", score)
	}
}

func TestClassifyRepoRef(t *testing.T) {
	branches := []string{"main", "feature/ci"}
	tags := []string{"v1"}
	if got := classifyRepoRef("main", branches, tags); got != "branch" {
		t.Fatalf("main = %q", got)
	}
	if got := classifyRepoRef("v1", branches, tags); got != "tag" {
		t.Fatalf("v1 = %q", got)
	}
	if got := classifyRepoRef("abc1234", branches, tags); got != "commit" {
		t.Fatalf("hash = %q", got)
	}
}
