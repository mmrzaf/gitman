package web

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/push"
)

// refDelete deletes a branch or tag. It checks what a push deleting it
// would be checked against, whatever the page offered: a button is only a
// link to this.
func (a *App) refDelete(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoByName(r)
	if err != nil {
		return err
	}
	if err := parseForm(w, r); err != nil {
		return err
	}
	kind, name, ok := git.SplitFullName(r.PostForm.Get("ref"))
	if !ok {
		return apperr.New(apperr.KindInvalid, "Choose a branch or tag to delete.")
	}
	gitRepo, err := a.repos.Open(repo)
	if err != nil {
		return err
	}
	err = a.refs.Delete(r.Context(), gitRepo, repo.ID, personFrom(r).ID, clientIP(r), kind, name)
	back := "/" + repo.Name
	switch {
	case errors.Is(err, git.ErrNotFound):
		return notFound("%s has no %s named “%s”.", repo.Name, kind, name)
	case errors.Is(err, git.ErrRefMoved):
		return apperr.New(apperr.KindConflict, fmt.Sprintf("%s %s changed while it was being deleted, so it was left alone.", kind, name))
	case errors.Is(err, push.ErrNotRecorded):
		a.log.Error("deleted ref not recorded", "repo", repo.Name, "ref", git.FullName(kind, name), "error", err)
		a.redirect(w, r, back, flashError, fmt.Sprintf("Deleted %s %s, but Gitman could not record it. The next push brings its lists up to date.", kind, name))
		return nil
	case err != nil:
		return err
	}
	a.redirect(w, r, back, flashSuccess, fmt.Sprintf("Deleted %s %s.", kind, name))
	return nil
}
