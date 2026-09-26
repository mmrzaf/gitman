package web

import (
	"net/http"

	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

// jumpPage lists everywhere a person can go: Gitman's own pages and every
// repository with its files and settings. It is where the command
// palette gets its destinations — the palette reads this page's links —
// and, without scripting, the palette itself.
type jumpPage struct {
	Repos []*reposvc.Repo
}

func (a *App) jump(w http.ResponseWriter, r *http.Request) error {
	repos, err := a.repos.List(r.Context())
	if err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "jump", "Jump to", jumpPage{Repos: repos})
	return nil
}
