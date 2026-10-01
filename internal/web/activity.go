package web

import (
	"net/http"
	"net/url"

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
	Before  string
	Next    string
	More    bool
}

// LiveEvents keeps the feed current: a push, a run and a settings change
// all belong to it.
func (p activityPage) LiveEvents() string { return "/events?repo=" + p.Repo.ID }

func (p activityPage) PageURL(cursor string) string {
	u := &url.URL{Path: "/" + p.Repo.Name + "/activity"}
	if cursor != "" {
		q := url.Values{}
		q.Set("before", cursor)
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// activityView serves /{repo}/activity: everything that happened to the
// repository, newest first — refs changing, pushes Gitman refused and why,
// runs, what was shipped, and changes to its settings.
func (a *App) activityView(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoByName(r)
	if err != nil {
		return err
	}
	page := activityPage{repoFrame: repoFrame{Repo: repo, Section: "activity"}, Before: r.URL.Query().Get("before")}
	if page.Entries, page.More, err = a.activity.ForRepo(r.Context(), repo.ID, page.Before, activityPageSize); err != nil {
		return err
	}
	if page.More && len(page.Entries) > 0 {
		page.Next = page.Entries[len(page.Entries)-1].Cursor()
	}
	a.render(w, r, http.StatusOK, "activity", "Activity · "+repo.Name, page)
	return nil
}
