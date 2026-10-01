package web

import (
	"github.com/mmrzaf/gitman/internal/git"
	"testing"
)

func TestComparisonKeepsMissingPairsUnknown(t *testing.T) {
	got := comparisonsFromCounts("head", []string{"head", "behind", "ahead", "diverged", "missing"}, map[string]git.Divergence{
		"behind": {Behind: 2}, "ahead": {Ahead: 3}, "diverged": {Ahead: 1, Behind: 4},
	})
	for hash, state := range map[string]string{"head": "equal", "behind": "behind", "ahead": "ahead", "diverged": "diverged", "missing": "unknown"} {
		if got[hash].State != state {
			t.Errorf("%s = %+v, want %s", hash, got[hash], state)
		}
	}
	if got["behind"].Ahead != 2 || got["ahead"].Behind != 3 {
		t.Fatal("comparison direction is reversed")
	}
}
