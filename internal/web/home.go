package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/names"
	"github.com/mmrzaf/gitman/internal/postgres"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

// homeTimelineLimit and homeInProgressLimit bound the feed and the runs
// shown on their repositories' cards, so Home stays a quick read rather
// than a full history.
const (
	homeTimelineLimit   = 30
	homeInProgressLimit = 50
	// A short refusal window keeps old denied pushes from lingering in Home.
	attentionRefusalLimit = 6
	refusedWindow         = 7 * 24 * time.Hour
)

// homeRepo is a repository in Home's list, with its default-branch
// commit and its most recent run on any branch or tag.
type homeRepo struct {
	Name          string
	Description   string
	DefaultBranch string
	// Head is the latest commit of the default branch; nil until it is
	// pushed.
	Head *git.Commit
	// Run is the most recent run on any ref; Running includes currently
	// active runs on other refs.
	Run     *ci.Summary
	Running []ci.Summary
}

// homeDeployment is what is live on one target of one repository, compared
// with the ref that produced the deployment.
type homeDeployment struct {
	Comparison commitComparison
	Repo       string
	Target     string
	Live       ci.Deployment
	// Behind is how many commits the deployment's source ref has that this
	// target lacks. A removed source ref has no comparison.
	Behind   int
	SinceURL string
}

// attention is one thing on Home that wants a look: what, and where to go.
type attention struct {
	Key string // Identifies the current condition for a browser-local dismissal.
	// Icon names the icon shown with it.
	Icon string
	Text string
	// URL is where to look; empty for something with no page of its own.
	URL string
}

type homePage struct {
	After, Next string
	Repos       []homeRepo
	Deployments []homeDeployment
	Attention   []attention
	Timeline    []activity.Entry
	CreateForm  *form
	// Dialog is the dialog the page opens with: "new-repo" when asked for
	// by link, or when a creation failed.
	Dialog string
}

// HasRun reports whether any listed repository has run its pipeline: a
// column nothing would fill is left out.
func (p homePage) HasRun() bool {
	return slices.ContainsFunc(p.Repos, func(r homeRepo) bool { return r.Run != nil || len(r.Running) > 0 })
}

// buildHomeData loads everything Home shows. createForm carries a failed
// repository-creation submission back onto the page it failed on.
func (a *App) buildHomeData(r *http.Request, createForm *form) (homePage, error) {
	ctx := r.Context()
	person := personFrom(r)
	after := r.URL.Query().Get("after")
	all, err := a.repos.ListReadable(ctx, person.ID, person.IsAdmin)
	if err != nil {
		return homePage{}, err
	}
	readableIDs := make([]string, len(all))
	for i, repo := range all {
		readableIDs[i] = repo.ID
	}
	live, err := a.ci.LiveForRepos(ctx, readableIDs)
	if err != nil {
		return homePage{}, err
	}
	inProgress, err := a.ci.InProgressForRepos(ctx, readableIDs, homeInProgressLimit)
	if err != nil {
		return homePage{}, err
	}
	latestRuns, err := a.ci.LatestRunsForRepos(ctx, readableIDs)
	if err != nil {
		return homePage{}, err
	}
	heads, err := a.repos.DefaultHeads(ctx, readableIDs)
	if err != nil {
		return homePage{}, err
	}
	feed, err := a.activity.RecentForRepos(ctx, readableIDs, person.IsAdmin, homeTimelineLimit)
	if err != nil {
		return homePage{}, err
	}
	refused, err := a.activity.RefusedSince(ctx, readableIDs, a.now().Add(-refusedWindow), attentionRefusalLimit)
	if err != nil {
		return homePage{}, err
	}

	// Sort by meaningful repository activity: pushes, runs and deployments.
	// Names only break ties. "after" anchors pagination to the last shown repo.
	activityAt, err := a.repos.LastActivityForRepos(ctx, readableIDs)
	if err != nil {
		return homePage{}, err
	}
	slices.SortFunc(all, func(a, b *reposvc.Repo) int {
		if atA, atB := activityAt[a.ID], activityAt[b.ID]; !atA.Equal(atB) {
			if atA.After(atB) {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	start := 0
	if after != "" {
		found := false
		for i, repo := range all {
			if repo.Name == after {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			after = ""
		}
	}
	end := min(start+25, len(all))
	listed := make(map[string]bool, end-start)
	for _, repo := range all[start:end] {
		listed[repo.ID] = true
	}
	next := ""
	if end < len(all) {
		next = all[end-1].Name
	}
	page := homePage{CreateForm: createForm, Timeline: feed, After: after, Next: next}
	running := map[string][]ci.Summary{}
	for _, run := range inProgress {
		running[run.RepoName] = append(running[run.RepoName], run)
	}
	var items []attention
	if slices.ContainsFunc(inProgress, func(run ci.Summary) bool { return run.Status == ci.StatusQueued }) {
		blocked, err := a.ci.QueuedBlockCounts(ctx)
		if err != nil {
			return homePage{}, err
		}
		total := 0
		for _, n := range blocked {
			total += n
		}
		if total > 0 {
			text := fmt.Sprintf("%d queued run%s cannot start: no ready worker is available.", total, plural(total))
			if n := blocked[ci.BlockMissingImages]; n > 0 {
				text = fmt.Sprintf("%d queued run%s cannot start: no ready worker has the required images.", n, plural(n))
			}
			// A new queued run changes the dismissal key, so a new blocker
			// can draw attention even after an earlier one was ignored.
			queuedIDs := make([]string, 0, len(inProgress))
			for _, run := range inProgress {
				if run.Status == ci.StatusQueued {
					queuedIDs = append(queuedIDs, run.ID)
				}
			}
			slices.Sort(queuedIDs)
			item := attention{Key: "queued:" + strings.Join(queuedIDs, ","), Icon: "queued", Text: text}
			if person.IsAdmin {
				item.URL = "/workers"
			}
			items = append(items, item)
		}
	}

	byRepo := map[string][]ci.Deployment{}
	for _, d := range live {
		byRepo[d.RepoID] = append(byRepo[d.RepoID], d)
	}

	// Deployment comparisons need the actual source branch or tag, not a
	// repository's default branch. Fetch those heads in one database query.
	var sourceRefs map[string][]reposvc.IndexedRef
	if len(live) > 0 {
		sourceRefs, err = a.repos.RefsForRepos(ctx, readableIDs)
		if err != nil {
			return homePage{}, err
		}
	}
	var failed, behind []attention
	for _, repo := range all {
		row := homeRepo{Name: repo.Name, Description: repo.Description, DefaultBranch: repo.DefaultBranch, Running: running[repo.Name]}
		if run, ok := latestRuns[repo.ID]; ok {
			row.Run = &run
			if run.Status == ci.StatusFailed {
				failed = append(failed, attention{Key: "run:" + run.ID, Icon: "failed", URL: runPath(repo.Name, run.Number),
					Text: fmt.Sprintf("%s: run #%d of %s %s failed.", repo.Name, run.Number, run.RefKind, run.RefName)})
			}
		}
		head, pushed := heads[repo.ID]
		var gitRepo *git.Repo
		if len(byRepo[repo.ID]) > 0 {
			if gitRepo, err = a.repos.Open(repo); err != nil {
				// A repository that cannot be read is not worth losing Home for.
				a.log.Warn("could not open a repository for Home", "repo", repo.Name, "error", err)
				gitRepo = nil
			}
		}
		if pushed {
			row.Head = head.Head
			if row.Head == nil {
				row.Head = &git.Commit{Hash: head.Commit, Subject: "Commit metadata unavailable"}
			}
		}
		if listed[repo.ID] {
			page.Repos = append(page.Repos, row)
		}

		if len(byRepo[repo.ID]) == 0 {
			continue
		}
		comparisons := compareDeploymentTargets(ctx, gitRepo, sourceRefs[repo.ID], byRepo[repo.ID])
		for _, d := range byRepo[repo.ID] {
			dep := homeDeployment{Repo: repo.Name, Target: d.Target, Live: d, Comparison: comparisons[d.Target]}
			if d.SourceName != "" && dep.Comparison.State != "unknown" && dep.Comparison.State != "equal" {
				dep.SinceURL = compareURL(repo.Name, d.Commit, d.SourceName)
			}
			dep.Behind = dep.Comparison.Ahead
			if dep.Behind > 0 {
				behind = append(behind, attention{Key: fmt.Sprintf("deploy:%s:%s:%s", repo.ID, d.Target, d.Commit), Icon: "compare", URL: dep.SinceURL,
					Text: fmt.Sprintf("%s: %s is %d commit%s behind %s %s.", repo.Name, d.Target, dep.Behind, plural(dep.Behind), d.SourceKind, d.SourceName)})
			}
			page.Deployments = append(page.Deployments, dep)
		}
	}
	items = append(items, failed...)
	// Recent rejected pushes require action ahead of routine deployment drift.
	for _, push := range refused {
		who := push.Actor
		if who == "" {
			who = "Someone"
		}
		text := fmt.Sprintf("%s: %s's push was refused", push.RepoName, who)
		if push.Reason != "" {
			text += ": " + push.Reason
		}
		if !strings.HasSuffix(text, ".") {
			text += "."
		}
		items = append(items, attention{Key: fmt.Sprintf("refusal:%s:%d", push.RepoName, push.At.UnixNano()), Icon: "warning", URL: "/" + push.RepoName + "/activity", Text: text})
	}
	items = append(items, behind...)
	page.Attention = items
	return page, nil
}

// LiveEvents keeps Home's cards, what needs attention and its timeline
// current.
func (homePage) LiveEvents() string { return "/events" }

func (a *App) home(w http.ResponseWriter, r *http.Request) error {
	page, err := a.buildHomeData(r, newForm(nil))
	if err != nil {
		return err
	}
	page.Dialog = dialogFrom(r, "new-repo")
	a.render(w, r, http.StatusOK, "home", "Home", page)
	return nil
}

func (a *App) repoCreate(w http.ResponseWriter, r *http.Request) error {
	if err := parseForm(w, r); err != nil {
		return err
	}
	f := newForm(r.PostForm)
	name := f.Get("name")
	description := f.Get("description")
	branch := f.Get("default_branch")
	if branch == "" {
		branch = "main"
	}
	if name == "" {
		f.Fail("name", "Enter a name.")
	} else if err := names.ValidateRepository(name); err != nil {
		failForm(f, "name", err)
	}
	if len(f.Errors) > 0 {
		return a.reRenderHomeCreate(w, r, f, http.StatusUnprocessableEntity)
	}

	person := personFrom(r)
	repo, err := a.repos.Create(r.Context(), name, description, branch, person.ID)
	switch {
	case errors.Is(err, postgres.ErrAlreadyExists):
		f.Fail("name", "A repository with that name already exists.")
		return a.reRenderHomeCreate(w, r, f, http.StatusUnprocessableEntity)
	case failForm(f, "", err):
		return a.reRenderHomeCreate(w, r, f, http.StatusUnprocessableEntity)
	case err != nil:
		return err
	}
	// An admin goes on to the settings, where rules are added; anyone
	// else to the repository, which shows where to push.
	target := "/" + repo.Name
	if person.IsAdmin {
		target += "/settings"
	}
	a.redirect(w, r, target, flashSuccess, "Created "+repo.Name+".")
	return nil
}

func (a *App) reRenderHomeCreate(w http.ResponseWriter, r *http.Request, f *form, status int) error {
	page, err := a.buildHomeData(r, f)
	if err != nil {
		return err
	}
	page.Dialog = "new-repo"
	a.render(w, r, status, "home", "Home", page)
	return nil
}

func (p homePage) PageURL(after string) string {
	u := &url.URL{Path: "/"}
	if after != "" {
		q := url.Values{}
		q.Set("after", after)
		u.RawQuery = q.Encode()
	}
	return u.String()
}
