package web

import (
	"context"
	"fmt"

	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

// commitComparison describes a source ref relative to a recorded deployment. Unknown never masquerades as equality.
type commitComparison struct {
	State         string
	Ahead, Behind int
}

func (c commitComparison) Label() string {
	switch c.State {
	case "equal":
		return "Up to date"
	case "behind":
		return fmt.Sprintf("%d commit%s not deployed", c.Ahead, plural(c.Ahead))
	case "ahead":
		return fmt.Sprintf("Deployment is %d commit%s ahead", c.Behind, plural(c.Behind))
	case "diverged":
		return fmt.Sprintf("Diverged: %d not deployed, %d only in deployment", c.Ahead, c.Behind)
	default:
		return "Comparison unavailable"
	}
}

// comparisonsFromCounts uses counts relative to the current head. Missing pairs
// remain unknown; one missing deployment must not hide other valid comparisons.
func comparisonsFromCounts(head string, deployed []string, counts map[string]git.Divergence) map[string]commitComparison {
	result := map[string]commitComparison{}
	for _, hash := range deployed {
		c := commitComparison{State: "unknown"}
		if head != "" && hash != "" && hash == head {
			c.State = "equal"
		} else if d, ok := counts[hash]; ok {
			c.Ahead, c.Behind = d.Behind, d.Ahead
			switch {
			case c.Ahead > 0 && c.Behind > 0:
				c.State = "diverged"
			case c.Ahead > 0:
				c.State = "behind"
			case c.Behind > 0:
				c.State = "ahead"
			default:
				c.State = "equal"
			}
		}
		result[hash] = c
	}
	return result
}
func compareDeployments(ctx context.Context, repository *git.Repo, head string, deployed []string) map[string]commitComparison {
	var counts map[string]git.Divergence
	if repository != nil && head != "" {
		counts, _ = repository.Divergences(ctx, head, deployed)
	}
	return comparisonsFromCounts(head, deployed, counts)
}

// compareDeploymentTargets compares each deployed commit to the current head
// of the ref that actually produced it. Grouping by source ref avoids a
// separate Git history walk for each target. Deleted refs stay unknown.
func compareDeploymentTargets(ctx context.Context, repository *git.Repo, refs []reposvc.IndexedRef, deployments []ci.Deployment) map[string]commitComparison {
	result := make(map[string]commitComparison, len(deployments))
	heads := make(map[string]string, len(refs))
	for _, ref := range refs {
		heads[string(ref.Kind)+"/"+ref.Name] = ref.Commit
	}
	groups := map[string][]ci.Deployment{}
	for _, deployment := range deployments {
		result[deployment.Target] = commitComparison{State: "unknown"}
		if deployment.SourceName == "" {
			continue
		}
		key := string(deployment.SourceKind) + "/" + deployment.SourceName
		if heads[key] != "" {
			groups[key] = append(groups[key], deployment)
		}
	}

	for key, group := range groups {
		hashes := make([]string, 0, len(group))
		for _, d := range group {
			hashes = append(hashes, d.Commit)
		}
		compared := compareDeployments(ctx, repository, heads[key], hashes)
		for _, d := range group {
			result[d.Target] = compared[d.Commit]
		}
	}
	return result
}
