package web

import (
	"errors"
	"net/http"
	"sort"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/names"
	"github.com/mmrzaf/gitman/internal/postgres"
)

// homeTimelineLimit and homeInProgressLimit bound the two lists below the
// board, so Home stays a quick read rather than a full history.
const (
	homeTimelineLimit   = 30
	homeInProgressLimit = 15
)

// boardRepo is one row of Home's board: a repository and what is live
// on each target, by target name.
type boardRepo struct {
	Name        string
	Description string
	Live        map[string]*ci.Deployment
}

type homePage struct {
	// Targets names every target anything has shipped to, in order: the
	// board's columns.
	Targets    []string
	Board      []boardRepo
	InProgress []ci.Summary
	Timeline   []activity.Entry
	CreateForm *form
	// Dialog is the dialog the page opens with: "new-repo" when asked for
	// by link, or when a creation failed.
	Dialog string
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
	feed, err := a.activity.RecentForRepos(ctx, readableIDs, homeTimelineLimit)
	if err != nil {
		return homePage{}, err
	}

	page := homePage{CreateForm: createForm, InProgress: inProgress, Timeline: feed}
	byRepo := map[string]map[string]*ci.Deployment{}
	targets := map[string]bool{}
	for i, d := range live {
		if byRepo[d.RepoID] == nil {
			byRepo[d.RepoID] = map[string]*ci.Deployment{}
		}
		byRepo[d.RepoID][d.Target] = &live[i]
		if !targets[d.Target] {
			targets[d.Target] = true
			page.Targets = append(page.Targets, d.Target)
		}
	}
	sort.Strings(page.Targets)
	for _, repo := range list {
		page.Board = append(page.Board, boardRepo{Name: repo.Name, Description: repo.Description, Live: byRepo[repo.ID]})
	}
	return page, nil
}

// LiveEvents keeps Home's board, work in progress and timeline current.
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
