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
)

// homeTimelineLimit and homeInProgressLimit bound the feed and the runs
// shown on their repositories' cards, so Home stays a quick read rather
// than a full history.
const (
	homeTimelineLimit   = 30
	homeInProgressLimit = 50
	// attentionLimit bounds the rows of "Needs attention"; the rest are
	// counted. refusedWindow is how far back a refused push still does.
	attentionLimit = 6
	refusedWindow  = 7 * 24 * time.Hour
)

// homeRepo is a repository in Home's list: what landed last on its default
// branch and how that ran.
type homeRepo struct {
	Name          string
	Description   string
	DefaultBranch string
	// Head is the latest commit of the default branch; nil until it is
	// pushed.
	Head *git.Commit
	// Run is the default branch's latest run, and Running every run in
	// progress, of any ref.
	Run     *ci.Summary
	Running []ci.Summary
}

// homeDeployment is what is live on one target of one repository, and how
// far the default branch has gone since.
type homeDeployment struct {
	Comparison commitComparison
	Repo       string
	Target     string
	Live       ci.Deployment
	// Behind is how many commits the default branch has that the target
	// lacks, and SinceURL compares them; both are empty when it is up to
	// date.
	Behind   int
	SinceURL string
}

// attention is one thing on Home that wants a look: what, and where to go.
type attention struct {
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
	// Attention are the things that want a look; AttentionMore counts those
	// the list leaves out.
	Attention     []attention
	AttentionMore int
	Timeline      []activity.Entry
	CreateForm    *form
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
	list, err := a.repos.ListReadable(ctx, person.ID, person.IsAdmin)
	if err != nil {
		return homePage{}, err
	}
	readableIDs := make([]string, len(list))
	for i, repo := range list {
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
	latestRuns, err := a.ci.LatestDefaultHeadRuns(ctx, readableIDs)
	if err != nil {
		return homePage{}, err
	}
	heads, err := a.repos.DefaultHeads(ctx, readableIDs)
	if err != nil {
		return homePage{}, err
	}
	feed, err := a.activity.RecentForRepos(ctx, readableIDs, homeTimelineLimit)
	if err != nil {
		return homePage{}, err
	}
	refused, err := a.activity.RefusedSince(ctx, readableIDs, a.now().Add(-refusedWindow), attentionLimit)
	if err != nil {
		return homePage{}, err
	}

	page := homePage{CreateForm: createForm, Timeline: feed, After: after, Next: next}
	running := map[string][]ci.Summary{}
	for _, run := range inProgress {
		running[run.RepoName] = append(running[run.RepoName], run)
	}
	var items []attention
	if slices.ContainsFunc(inProgress, func(run ci.Summary) bool { return run.Status == ci.StatusQueued }) {
		online, err := a.ci.AnyWorkerReady(ctx)
		if err != nil {
			return homePage{}, err
		}
		if !online {
			item := attention{Icon: "queued", Text: "Runs are waiting for a ready worker."}
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

	var failed, behind []attention
	for _, repo := range all {
		row := homeRepo{Name: repo.Name, Description: repo.Description, DefaultBranch: repo.DefaultBranch, Running: running[repo.Name]}
		if run, ok := latestRuns[repo.ID]; ok {
			row.Run = &run
			if run.Status == ci.StatusFailed {
				failed = append(failed, attention{Icon: "failed", URL: runPath(repo.Name, run.Number),
					Text: fmt.Sprintf("%s: the latest run of %s failed.", repo.Name, repo.DefaultBranch)})
			}
		}
		head, pushed := heads[repo.ID]
		var gitRepo *git.Repo
		if pushed {
			if len(byRepo[repo.ID]) > 0 {
				if gitRepo, err = a.repos.Open(repo); err != nil {
					// A repository that cannot be read is not worth losing Home for.
					a.log.Warn("could not open a repository for Home", "repo", repo.Name, "error", err)
					gitRepo = nil
				}
			}
			row.Head = head.Head
			if row.Head == nil {
				row.Head = &git.Commit{Hash: head.Commit, Subject: "Commit metadata unavailable"}
			}
		}
		if listed[repo.ID] {
			page.Repos = append(page.Repos, row)
		}

		var deployedHashes []string
		for _, d := range byRepo[repo.ID] {
			deployedHashes = append(deployedHashes, d.Commit)
		}
		comparisons := compareDeployments(ctx, gitRepo, head.Commit, deployedHashes)
		for _, d := range byRepo[repo.ID] {
			dep := homeDeployment{Repo: repo.Name, Target: d.Target, Live: d, Comparison: comparisons[d.Commit]}
			if pushed && gitRepo != nil && d.Commit != head.Commit {
				dep.Behind = dep.Comparison.Ahead
				if dep.Behind > 0 {
					dep.SinceURL = compareURL(repo.Name, d.Commit, repo.DefaultBranch)
					behind = append(behind, attention{Icon: "compare", URL: dep.SinceURL,
						Text: fmt.Sprintf("%s: %s is %d commit%s behind %s.", repo.Name, d.Target, dep.Behind, plural(dep.Behind), repo.DefaultBranch)})
				}
			}
			if dep.Comparison.State == "ahead" || dep.Comparison.State == "diverged" {
				dep.SinceURL = compareURL(repo.Name, d.Commit, repo.DefaultBranch)
			}
			page.Deployments = append(page.Deployments, dep)
		}
	}
	items = append(items, failed...)
	items = append(items, behind...)
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
		items = append(items, attention{Icon: "warning", URL: "/" + push.RepoName + "/activity", Text: text})
	}
	page.Attention = items
	if len(items) > attentionLimit {
		page.Attention, page.AttentionMore = items[:attentionLimit], len(items)-attentionLimit
	}
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
