package web

import (
	"net/http"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

// refRow is one row of the Repository page's refs list: the indexed ref,
// enriched with what a person actually wants to see next to it.
type refRow struct {
	reposvc.IndexedRef
	UpdatedByUsername string
	IsDefault         bool
	LatestRun         *ci.Summary
	LatestDeployment  *ci.Deployment
}

type repositoryPage struct {
	repoFrame
	CloneURL string
	Targets  []ci.Deployment
	Branches []refRow
	Tags     []refRow
	// DefaultExists is false until the default branch is first pushed:
	// the only way it can be missing, since a push may not delete it.
	DefaultExists bool
	Timeline      []activity.Entry
	// Tab is the refs list shown: "branches" or "tags".
	Tab string
}

// LiveEvents keeps a Repository page's targets, refs and timeline
// current.
func (repositoryPage) LiveEvents() string { return "/events" }

// cloneURL is the address Git clones a repository from.
func (a *App) cloneURL(repo *reposvc.Repo) string {
	return a.cfg.PublicURL + "/" + repo.Name + ".git"
}

// repositoryPageLimit bounds the timeline shown on the Repository page.
const repositoryPageLimit = 20

func (a *App) repository(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoByName(r)
	if err != nil {
		return err
	}
	ctx := r.Context()

	targets, err := a.ci.LiveForRepo(ctx, repo.ID)
	if err != nil {
		return err
	}
	indexed, err := a.repos.ListRefs(ctx, repo.ID)
	if err != nil {
		return err
	}
	everyone, err := a.people.List(ctx)
	if err != nil {
		return err
	}
	usernames := make(map[string]string, len(everyone))
	for _, p := range everyone {
		usernames[p.ID] = p.Username
	}
	latestRun, err := a.ci.LatestRunPerRef(ctx, repo.ID)
	if err != nil {
		return err
	}
	latestDeploy, err := a.ci.LatestDeploymentPerRef(ctx, repo.ID)
	if err != nil {
		return err
	}

	page := repositoryPage{
		repoFrame: repoFrame{Repo: repo, Section: "overview"},
		CloneURL:  a.cloneURL(repo), Targets: targets,
		Tab: tabFrom(r, "branches", "tags"),
	}
	for _, ref := range indexed {
		row := refRow{IndexedRef: ref, IsDefault: ref.Kind == git.KindBranch && ref.Name == repo.DefaultBranch}
		page.DefaultExists = page.DefaultExists || row.IsDefault
		if ref.UpdatedBy != nil {
			row.UpdatedByUsername = usernames[*ref.UpdatedBy]
		}
		key := string(ref.Kind) + "/" + ref.Name
		if run, ok := latestRun[key]; ok {
			row.LatestRun = &run
		}
		if dep, ok := latestDeploy[key]; ok {
			row.LatestDeployment = &dep
		}
		if ref.Kind == git.KindTag {
			page.Tags = append(page.Tags, row)
		} else {
			page.Branches = append(page.Branches, row)
		}
	}

	if page.Timeline, err = a.activity.Recent(ctx, &repo.ID, repositoryPageLimit); err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "repository", repo.Name, page)
	return nil
}
