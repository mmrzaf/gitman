package handlers

import (
	"strings"
	"testing"
)

func TestSourceLinesPreserveExactTextAndUnicode(t *testing.T) {
	lines := sourceLines([]byte("<script>alert(1)</script> 世界\nsecond\n"))
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	if lines[0].Text != "<script>alert(1)</script> 世界" {
		t.Fatalf("first line = %q", lines[0].Text)
	}
	if lines[1].Text != "second" {
		t.Fatalf("second line = %q", lines[1].Text)
	}
}

func TestSourceLanguageLabelIsPresentationOnly(t *testing.T) {
	if got := sourceLanguageLabel("main.go"); got != "Go" {
		t.Fatalf("main.go label = %q", got)
	}
	if got := sourceLanguageLabel("README"); got != "Text" {
		t.Fatalf("README label = %q", got)
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

func TestSourceRenderLimitProtectsPathologicalFiles(t *testing.T) {
	if got := sourceRenderLimitNote([]byte(strings.Repeat("x\n", maxSourceRenderLines))); got != "" {
		t.Fatalf("exact line limit unexpectedly rejected: %q", got)
	}
	if got := sourceRenderLimitNote([]byte(strings.Repeat("x\n", maxSourceRenderLines) + "x")); got == "" {
		t.Fatal("expected line-count render limit")
	}
	if got := sourceRenderLimitNote([]byte(strings.Repeat("x", maxSourceLineBytes+1))); got == "" {
		t.Fatal("expected long-line render limit")
	}
	if got := sourceRenderLimitNote([]byte("small\nfile\n")); got != "" {
		t.Fatalf("small file unexpectedly limited: %q", got)
	}
}

func TestRepoNavActiveUsesRouteSegmentsNotRepositoryName(t *testing.T) {
	tests := map[string]string{
		"/alice/ci":                   "files",
		"/alice/settings/tree":        "files",
		"/alice/commits/blob":         "files",
		"/alice/demo/ci":              "ci",
		"/alice/demo/settings/ci":     "settings",
		"/alice/demo/commit/deadbeef": "commits",
		"/alice/demo/commits":         "commits",
		"/alice/demo/settings":        "settings",
		"/alice/demo/settings/access": "settings",
	}
	for path, want := range tests {
		if got := repoNavActive(path); got != want {
			t.Errorf("repoNavActive(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestRepoSettingsSectionUsesCanonicalSettingsRoutes(t *testing.T) {
	tests := map[string]string{
		"/alice/demo/settings":        "general",
		"/alice/demo/settings/access": "access",
		"/alice/demo/settings/ci":     "ci",
		"/alice/demo/ci":              "",
	}
	for path, want := range tests {
		if got := repoSettingsSection(path); got != want {
			t.Errorf("repoSettingsSection(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestRankFileMatchesKeepsOnlyBestLimit(t *testing.T) {
	files := []string{
		"zzz/deep/worker.go",
		"docs/worker-notes.md",
		"worker.txt",
		"internal/worker.go",
		"worker.go",
	}
	matches := rankFileMatches(files, "worker", "me", "repo", "main", 2)
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2: %+v", len(matches), matches)
	}
	if matches[0].Path != "worker.go" || matches[1].Path != "internal/worker.go" {
		t.Fatalf("unexpected top matches: %+v", matches)
	}
}

func TestEnsureRefVisible(t *testing.T) {
	original := []string{"branch-0001", "branch-0002"}
	got := ensureRefVisible(append([]string(nil), original...), "branch-9000")
	if len(got) != 3 || got[2] != "branch-9000" {
		t.Fatalf("ensureRefVisible() = %#v", got)
	}

	got = ensureRefVisible(append([]string(nil), original...), "branch-0002")
	if len(got) != len(original) {
		t.Fatalf("existing ref duplicated: %#v", got)
	}
}
