package web

import (
	"net/http"
	"slices"
	"sort"
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
	Comparison commitComparison
	ci.Deployment
	// Behind is how many commits the default branch has that this target
	// lacks, and SinceURL compares them; both are empty when the default
	// branch is at what is live, or is not pushed yet.
	Behind   int
	SinceURL string
}

// runStripLength is how many of the default branch's latest runs the
// Overview's strip shows.
const runStripLength = 12

type repositoryPage struct {
	repoFrame
	CloneURL string
	Targets  []targetView
	Branches []refRow
	Tags     []refRow
	// DefaultExists is false until the default branch is first pushed:
	// the only way it can be missing, since a push may not delete it.
	DefaultExists bool
	Timeline      []activity.RepoEntry
	// Runs are the default branch's latest runs, oldest first, for the strip.
	Runs []ci.Summary
}

// LiveEvents keeps a Repository page's targets, refs and timeline
// current.
func (p repositoryPage) LiveEvents() string { return "/events?repo=" + p.Repo.ID }

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
	latestRun, err := a.ci.LatestHeadRunPerRef(ctx, repo.ID)
	if err != nil {
		return err
	}
	latestDeploy, err := a.ci.LatestDeploymentPerRef(ctx, repo.ID)
	if err != nil {
		return err
	}

	rules, err := a.repos.ListRules(ctx, repo.ID)
	if err != nil {
		return err
	}
	runnable := runnableRefsFor(r, repo, indexed, rules)
	canRun := make(map[string]bool, len(runnable))
	for _, ref := range runnable {
		canRun[ref.FullName] = true
	}
	person := personFrom(r)
	who := reposvc.Who{ID: person.ID, Username: person.Username, IsAdmin: person.IsAdmin}

	page := repositoryPage{
		repoFrame: repoFrame{Repo: repo, Section: "overview"},
		CloneURL:  a.cloneURL(repo),
	}
	for _, ref := range indexed {
		row := refRow{IndexedRef: ref, IsDefault: ref.Kind == git.KindBranch && ref.Name == repo.DefaultBranch}
		page.DefaultExists = page.DefaultExists || row.IsDefault
		row.CanRun = canRun[row.FullName()]
		_, refused := reposvc.CheckDelete(repo, rules, ref.Kind, ref.Name, who)
		row.CanDelete = refused == ""
		if ref.UpdatedBy != nil {
			row.UpdatedByUsername = ref.UpdatedByUsername
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

	// The newest release first, however the tags happened to be pushed; the
	// default branch leads the branches, the rest by how lately they moved.
	sort.SliceStable(page.Tags, func(i, j int) bool { return git.VersionLess(page.Tags[j].Name, page.Tags[i].Name) })
	sort.SliceStable(page.Branches, func(i, j int) bool { return page.Branches[i].IsDefault && !page.Branches[j].IsDefault })
	var defaultHead string
	for _, row := range page.Branches {
		if row.IsDefault {
			defaultHead = row.Commit
		}
	}
	// How far each target is behind needs Git, and is left out, not failed
	// on, when Git cannot give it: the page is worth showing without it.
	var gitRepo *git.Repo
	if defaultHead != "" {
		if gitRepo, err = a.repos.Open(repo); err != nil {
			a.log.Warn("could not open a repository for its overview", "repo", repo.Name, "error", err)
			gitRepo = nil
		}
	}
	var countedHashes, deployedHashes []string
	for _, row := range page.Branches {
		if !row.IsDefault {
			countedHashes = append(countedHashes, row.Commit)
		}
	}
	for _, d := range targets {
		countedHashes = append(countedHashes, d.Commit)
		deployedHashes = append(deployedHashes, d.Commit)
	}
	var counts map[string]git.Divergence
	if gitRepo != nil {
		counts, err = gitRepo.Divergences(ctx, defaultHead, countedHashes)
		if err != nil {
			a.log.Warn("some comparisons are unavailable", "repo", repo.Name, "error", err)
		}
	}
	for i := range page.Branches {
		if d, ok := counts[page.Branches[i].Commit]; ok {
			page.Branches[i].Divergence = &d
		}
	}
	comparisons := comparisonsFromCounts(defaultHead, deployedHashes, counts)
	for _, d := range targets {
		view := targetView{Deployment: d, Comparison: comparisons[d.Commit]}
		if gitRepo != nil && defaultHead != d.Commit {
			// A target whose commit Git can no longer count from is shown
			// without a count; the comparison still says what it can.
			view.Behind = view.Comparison.Ahead
			if view.Behind > 0 {
				view.SinceURL = compareURL(repo.Name, d.Commit, repo.DefaultBranch)
			}
		}
		if view.Comparison.State == "ahead" || view.Comparison.State == "diverged" {
			view.SinceURL = compareURL(repo.Name, d.Commit, repo.DefaultBranch)
		}
		page.Targets = append(page.Targets, view)
	}
	if page.DefaultExists {
		runs, err := a.ci.RunsOfRef(ctx, repo.ID, git.KindBranch, repo.DefaultBranch, runStripLength)
		if err != nil {
			return err
		}
		slices.Reverse(runs)
		page.Runs = runs
	}

	noteDeletes(repo, &page)
	if page.Timeline, _, err = a.activity.ForRepo(ctx, repo.ID, "", overviewActivity); err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "repository", repo.Name, page)
	return nil
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

// hasRun, hasDeployed and hasCounts report whether any ref of a list has a
// run, has shipped something, or has been counted against the default
// branch: a column nothing would fill is left out.
func hasRun(rows []refRow) bool {
	return slices.ContainsFunc(rows, func(r refRow) bool { return r.LatestRun != nil })
}

func hasDeployed(rows []refRow) bool {
	return slices.ContainsFunc(rows, func(r refRow) bool { return r.LatestDeployment != nil })
}

func hasCounts(rows []refRow) bool {
	return slices.ContainsFunc(rows, func(r refRow) bool { return r.Divergence != nil })
}
