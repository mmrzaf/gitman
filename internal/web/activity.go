package web

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/mmrzaf/gitman/internal/activity"
)

// activityPageSize is how many entries one page of a repository's activity
// shows. The Overview shows the newest overviewActivity of the same feed.
const (
	activityPageSize = 30
	overviewActivity = 20
)

type activityPage struct {
	repoFrame
	Entries []activity.RepoEntry
	Skip    int
	More    bool
}

// LiveEvents keeps the feed current: a push, a run and a settings change
// all belong to it.
func (activityPage) LiveEvents() string { return "/events" }

// NewerSkip and OlderSkip are the ?skip= of the pages before and after
// this one.
func (p activityPage) NewerSkip() int { return max(p.Skip-activityPageSize, 0) }
func (p activityPage) OlderSkip() int { return p.Skip + activityPageSize }

// PageURL is this page's address with skip set.
func (p activityPage) PageURL(skip int) string {
	u := (&url.URL{Path: "/" + p.Repo.Name + "/activity"}).EscapedPath()
	if skip > 0 {
		u += "?skip=" + strconv.Itoa(skip)
	}
	return u
}

// activityView serves /{repo}/activity: everything that happened to the
// repository, newest first — refs changing, pushes Gitman refused and why,
// runs, what was shipped, and changes to its settings.
func (a *App) activityView(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoByName(r)
	if err != nil {
		return err
	}
	page := activityPage{repoFrame: repoFrame{Repo: repo, Section: "overview"}, Skip: min(historySkip(r), maxCommitsSkip)}
	if page.Entries, page.More, err = a.activity.ForRepo(r.Context(), repo.ID, page.Skip, activityPageSize); err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "activity", "Activity · "+repo.Name, page)
	return nil
}
