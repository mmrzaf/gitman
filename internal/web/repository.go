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
	CanDelete  bool
	DeleteNote string
	// Divergence is how far a branch is from the default branch; it is
	// nil for the default branch itself, for a tag, and when it could not
	// be counted.
	Divergence       *git.Divergence
	LatestRun        *ci.Summary
	LatestDeployment *ci.Deployment
}

// FullName is the row's full ref name, what the "Run" form posts.
func (r refRow) FullName() string { return git.FullName(r.Kind, r.Name) }

// targetView is what is live on a target, and where to see what the default
// branch has gained since.
type targetView struct {
	ci.Deployment
	// SinceURL is empty when the default branch is at what is live, or is
	// not pushed yet.
	SinceURL string
}

// overviewTags is how many tags the Overview lists before "all tags".
const overviewTags = 10

type repositoryPage struct {
	repoFrame
	CloneURL string
	Targets  []targetView
	Branches []refRow
	// Tags are the most recently moved, unless all are asked for;
	// TagsTotal counts every one.
	Tags      []refRow
	TagsTotal int
	AllTags   bool
	// DefaultExists is false until the default branch is first pushed:
	// the only way it can be missing, since a push may not delete it.
	DefaultExists bool
	Timeline      []activity.RepoEntry
}

// MoreTags reports tags the page leaves out.
func (p repositoryPage) MoreTags() bool { return p.TagsTotal > len(p.Tags) }

// LiveEvents keeps a Repository page's targets, refs and timeline
// current.
func (repositoryPage) LiveEvents() string { return "/events" }

// cloneURL is the address Git clones a repository from.
func (a *App) cloneURL(repo *reposvc.Repo) string {
	return a.cfg.PublicURL + "/" + repo.Name + ".git"
}

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
		CloneURL:  a.cloneURL(repo),
		// ?tab=tags is where the tags were when they were a tab.
		AllTags: r.URL.Query().Get("tags") == "all" || r.URL.Query().Get("tab") == "tags",
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

	page.TagsTotal = len(page.Tags)
	if !page.AllTags && len(page.Tags) > overviewTags {
		page.Tags = page.Tags[:overviewTags]
	}
	var defaultHead string
	for _, row := range page.Branches {
		if row.IsDefault {
			defaultHead = row.Commit
		}
	}
	for _, d := range targets {
		view := targetView{Deployment: d}
		if defaultHead != "" && defaultHead != d.Commit {
			view.SinceURL = compareURL(repo.Name, d.Commit, repo.DefaultBranch)
		}
		page.Targets = append(page.Targets, view)
	}

	a.countDivergence(r, repo, &page)
	noteDeletes(repo, &page)
	if page.Timeline, _, err = a.activity.ForRepo(ctx, repo.ID, 0, overviewActivity); err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "repository", repo.Name, page)
	return nil
}

// countDivergence sets how far each branch is from the default branch,
// counted by Git. A failure to count leaves the counts out, since the page
// is worth showing without them.
func (a *App) countDivergence(r *http.Request, repo *reposvc.Repo, page *repositoryPage) {
	var base string
	for _, row := range page.Branches {
		if row.IsDefault {
			base = row.Commit
		}
	}
	if base == "" || len(page.Branches) < 2 {
		return
	}
	gitRepo, err := a.repos.Open(repo)
	if err != nil {
		a.log.Warn("could not open a repository to count its branches", "repo", repo.Name, "error", err)
		return
	}
	heads := make([]string, 0, len(page.Branches))
	for _, row := range page.Branches {
		if !row.IsDefault {
			heads = append(heads, row.Commit)
		}
	}
	counts, err := gitRepo.Divergences(r.Context(), base, heads)
	if err != nil {
		a.log.Warn("could not count how far branches are from the default", "repo", repo.Name, "error", err)
		return
	}
	for i := range page.Branches {
		if row := &page.Branches[i]; !row.IsDefault {
			d := counts[row.Commit]
			row.Divergence = &d
		}
	}
}

// noteDeletes words what each Delete button's confirmation says. A branch
// says whether it is merged into the default branch, which is when it is
// no commits ahead of it.
func noteDeletes(repo *reposvc.Repo, page *repositoryPage) {
	const cannotRestore = "Gitman can\u2019t restore it."
	for i := range page.Tags {
		if page.Tags[i].CanDelete {
			page.Tags[i].DeleteNote = "The commit stays, and the tag can be pushed again. " + cannotRestore
		}
	}
	for i := range page.Branches {
		row := &page.Branches[i]
		switch {
		case !row.CanDelete:
		case row.Divergence == nil:
			row.DeleteNote = cannotRestore
		case row.Divergence.Ahead == 0:
			row.DeleteNote = "It is merged into " + repo.DefaultBranch + ", so nothing is lost. " + cannotRestore
		default:
			row.DeleteNote = "It is not merged into " + repo.DefaultBranch + ". Commits only it has are left without a branch. " + cannotRestore
		}
	}
}
