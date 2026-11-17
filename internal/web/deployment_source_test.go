package web

import (
	"context"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

func TestDeploymentComparisonUsesActualSourceNotDefault(t *testing.T) {
	commit := strings.Repeat("a", 40)
	// Default branch has a different head but staging tracks develop.
	refs := []reposvc.IndexedRef{
		{Kind: git.KindBranch, Name: "main", Commit: strings.Repeat("b", 40)},
		{Kind: git.KindBranch, Name: "develop", Commit: commit},
	}
	deployments := []ci.Deployment{{Target: "staging", Commit: commit, SourceKind: git.KindBranch, SourceName: "develop"}, {Target: "production", Commit: commit, SourceKind: git.KindTag, SourceName: "release/missing"}}
	got := compareDeploymentTargets(context.Background(), nil, refs, deployments)
	if got["staging"].State != "equal" {
		t.Fatalf("staging comparison = %+v", got["staging"])
	}
	if got["production"].State != "unknown" {
		t.Fatalf("deleted release tag comparison = %+v", got["production"])
	}
}
