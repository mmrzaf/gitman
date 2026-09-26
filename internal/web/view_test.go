package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mmrzaf/gitman/internal/git"
)

// TestTabAndDialogComeFromTheAddress covers the views a page's address
// selects: a known tab or dialog is used, anything else falls back to the
// page's default — never an error for a stale or mistyped link.
func TestTabAndDialogComeFromTheAddress(t *testing.T) {
	get := func(query string) *http.Request { return httptest.NewRequest(http.MethodGet, "/x"+query, nil) }
	for query, want := range map[string]string{"": "general", "?tab=rules": "rules", "?tab=nope": "general", "?tab=": "general"} {
		if got := tabFrom(get(query), settingsTabs...); got != want {
			t.Errorf("tabFrom(%q) = %q, want %q", query, got, want)
		}
	}
	for query, want := range map[string]string{"": "", "?dialog=new-repo": "new-repo", "?dialog=rule-edit": ""} {
		if got := dialogFrom(get(query), "new-repo"); got != want {
			t.Errorf("dialogFrom(%q) = %q, want %q", query, got, want)
		}
	}
}

func TestDiffTotalsCountEveryChangedFile(t *testing.T) {
	d := &git.Diff{Files: []git.DiffFile{{Additions: 3, Deletions: 1}, {Additions: 2}}, MoreFiles: 4}
	if got := diffTotals(d); got != (diffStat{Files: 6, Additions: 5, Deletions: 1}) {
		t.Fatalf("diffTotals = %+v", got)
	}
}

func TestCompareURLEscapesRefs(t *testing.T) {
	if got := compareURL("w", "main", "feat#1"); got != "/w/compare/main...feat%231" {
		t.Fatalf("compareURL = %q", got)
	}
}

func TestShortDurationsRead(t *testing.T) {
	if got := formatDuration(300 * 1e6); got != "<1s" {
		t.Fatalf("formatDuration(300ms) = %q", got)
	}
}
