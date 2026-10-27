package web

import "net/http"

// releaseView is one release as the page shows it.
type releaseView struct {
	releaseNote
	// Badge labels the running version's own card; empty for the rest.
	Badge string
}

type changelogPage struct {
	// Version is the running build's version, as it was built.
	Version string
	// Exact says Current is the running version's own notes; otherwise it is
	// the newest release, shown because the running build has none.
	Exact   bool
	Current releaseView
	Earlier []releaseView
}

func (a *App) changelogView(w http.ResponseWriter, r *http.Request) error {
	current, exact, earlier := releaseNotesFor(a.releases, a.version)
	page := changelogPage{Version: a.version, Exact: exact, Current: releaseView{releaseNote: current, Badge: "Latest release"}}
	if exact {
		page.Current.Badge = "Current version"
	}
	for _, note := range earlier {
		page.Earlier = append(page.Earlier, releaseView{releaseNote: note})
	}
	a.render(w, r, http.StatusOK, "changelog", "What's new", page)
	return nil
}
