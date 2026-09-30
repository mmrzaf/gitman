package web

import (
	"net/http"
	"strings"

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
	// CanRun is whether the signed-in person may start a run of it.
	CanRun bool
	// CanDelete is whether a push by the signed-in person deleting it would
	// be accepted, and DeleteNote what the confirmation says about it.
	CanDelete        bool
	DeleteNote       string
	LatestRun        *ci.Summary
	LatestDeployment *ci.Deployment
}

// FullName is the row's full ref name, what the "Run" form posts.
func (r refRow) FullName() string { return git.FullName(r.Kind, r.Name) }

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
	// The clone URL opened in a browser lands on the repository. A name
	// never contains ".", so this can't shadow one.
	if name, ok := strings.CutSuffix(r.PathValue("repo"), ".git"); ok {
		http.Redirect(w, r, "/"+name, http.StatusMovedPermanently)
		return nil
	}
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

	runnable, err := a.runnableRefs(r, repo)
	if err != nil {
		return err
	}
	canRun := make(map[string]bool, len(runnable))
	for _, ref := range runnable {
		canRun[ref.FullName] = true
	}

	rules, err := a.repos.ListRules(ctx, repo.ID)
	if err != nil {
		return err
	}
	person := personFrom(r)
	who := reposvc.Who{ID: person.ID, Username: person.Username, IsAdmin: person.IsAdmin}

	page := repositoryPage{
		repoFrame: repoFrame{Repo: repo, Section: "overview"},
		CloneURL:  a.cloneURL(repo), Targets: targets,
		Tab: tabFrom(r, "branches", "tags"),
	}
	for _, ref := range indexed {
		row := refRow{IndexedRef: ref, IsDefault: ref.Kind == git.KindBranch && ref.Name == repo.DefaultBranch}
		page.DefaultExists = page.DefaultExists || row.IsDefault
		row.CanRun = canRun[row.FullName()]
		_, refused := reposvc.CheckDelete(repo, rules, ref.Kind, ref.Name, who)
		row.CanDelete = refused == ""
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

	if err := a.noteDeletes(r, repo, &page); err != nil {
		return err
	}
	if page.Timeline, err = a.activity.Recent(ctx, &repo.ID, repositoryPageLimit); err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "repository", repo.Name, page)
	return nil
}

// noteDeletes words what each Delete button's confirmation says. A branch
// says whether it is merged into the default branch, which needs the
// default branch to be there.
func (a *App) noteDeletes(r *http.Request, repo *reposvc.Repo, page *repositoryPage) error {
	const cannotRestore = "Gitman can\u2019t restore it."
	for i := range page.Tags {
		if page.Tags[i].CanDelete {
			page.Tags[i].DeleteNote = "The commit stays, and the tag can be pushed again. " + cannotRestore
		}
	}
	var defaultCommit string
	for _, row := range page.Branches {
		if row.IsDefault {
			defaultCommit = row.Commit
		}
	}
	var gitRepo *git.Repo
	for i := range page.Branches {
		row := &page.Branches[i]
		if !row.CanDelete {
			continue
		}
		row.DeleteNote = cannotRestore
		if defaultCommit == "" {
			continue
		}
		if gitRepo == nil {
			var err error
			if gitRepo, err = a.repos.Open(repo); err != nil {
				return err
			}
		}
		merged, err := gitRepo.IsAncestor(r.Context(), row.Commit, defaultCommit)
		if err != nil {
			continue
		}
		if merged {
			row.DeleteNote = "It is merged into " + repo.DefaultBranch + ", so nothing is lost. " + cannotRestore
		} else {
			row.DeleteNote = "It is not merged into " + repo.DefaultBranch + ". Commits only it has are left without a branch. " + cannotRestore
		}
	}
	return nil
}
