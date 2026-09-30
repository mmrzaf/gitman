package web

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

// historyPageSize is how many commits, or activity entries, History shows
// per page.
const historyPageSize = 30

// maxHistorySkip bounds how far back History pages: Git counts from the
// newest commit for every page.
const maxHistorySkip = 100000

// historyURL is the address of History at ref, filtered to path when it
// is not empty. Without a ref it is the default branch's.
func historyURL(repoName, ref, path string) string {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	if path != "" {
		q.Set("path", path)
	}
	u := (&url.URL{Path: "/" + repoName + "/history"}).EscapedPath()
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// historyCommit is one row of History's commit log: the commit, what ran
// for it, and the branches and tags now at it.
type historyCommit struct {
	*git.Commit
	Run  *ci.Summary
	Refs []reposvc.IndexedRef
}

// Merge reports a commit that joins two lines of history.
func (c historyCommit) Merge() bool { return len(c.Parents) > 1 }

type historyPage struct {
	repoFrame
	// Tab is "commits", a ref's log, or "activity", what happened to the
	// repository's refs and settings.
	Tab string
	// Commits
	Ref         string
	RefIsBranch bool
	RefIsTag    bool
	Commit      string
	Path        string
	Refs        []reposvc.IndexedRef
	Commits     []historyCommit
	// Unpushed reports that no ref was asked for and the default branch
	// has not been pushed yet.
	Unpushed bool
	Branches []string
	// Activity
	Entries []activity.HistoryEntry
	// Paging: the ?skip= of the pages before and after this one.
	Skip int
	More bool
}

// LiveEvents keeps History current: a push, a run and a settings change
// all show on it.
func (historyPage) LiveEvents() string { return "/events" }

// NewerSkip and OlderSkip are the ?skip= of the pages before and after
// this one.
func (p historyPage) NewerSkip() int { return max(p.Skip-historyPageSize, 0) }
func (p historyPage) OlderSkip() int { return p.Skip + historyPageSize }

// PageURL is this page's address with skip set, for the Newer and Older
// links.
func (p historyPage) PageURL(skip int) string {
	q := url.Values{}
	if p.Tab == "activity" {
		q.Set("tab", "activity")
	} else {
		if p.Ref != "" {
			q.Set("ref", p.Ref)
		}
		if p.Path != "" {
			q.Set("path", p.Path)
		}
	}
	if skip > 0 {
		q.Set("skip", strconv.Itoa(skip))
	}
	u := (&url.URL{Path: "/" + p.Repo.Name + "/history"}).EscapedPath()
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// history serves /{repo}/history: a ref's commit log, which ?path=
// narrows to the commits that touched a path, and, with ?tab=activity,
// the changes made to the repository's refs and settings.
func (a *App) history(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoByName(r)
	if err != nil {
		return err
	}
	skip := min(historySkip(r), maxHistorySkip)
	page := historyPage{
		repoFrame: repoFrame{Repo: repo, Section: "history"},
		Tab:       tabFrom(r, "commits", "activity"),
		Skip:      skip,
	}
	if page.Tab == "activity" {
		if page.Entries, page.More, err = a.activity.ForRepo(r.Context(), repo.ID, skip, historyPageSize); err != nil {
			return err
		}
	} else if err := a.historyCommits(r, repo, &page); err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "history", "History · "+repo.Name, page)
	return nil
}

// historyCommits fills in a History page's commit log.
func (a *App) historyCommits(r *http.Request, repo *reposvc.Repo, page *historyPage) error {
	ctx := r.Context()
	gitRepo, err := a.repos.Open(repo)
	if err != nil {
		return err
	}
	if page.Refs, err = a.repos.ListRefs(ctx, repo.ID); err != nil {
		return err
	}
	page.Path = strings.Trim(r.URL.Query().Get("path"), "/")
	for _, part := range strings.Split(page.Path, "/") {
		if page.Path != "" && (part == "" || part == "." || part == ".." || strings.ContainsRune(part, 0)) {
			return apperr.New(apperr.KindInvalid, "That is not a path in this repository.")
		}
	}

	ref := r.URL.Query().Get("ref")
	explicit := ref != ""
	if !explicit {
		ref = repo.DefaultBranch
	}
	res, err := a.resolveRefAndPath(ctx, gitRepo, repo.ID, ref)
	if err == nil && res.Path != "" {
		err = notFound("%q is not a branch, tag, or commit of this repository.", ref)
	}
	if err != nil {
		// The default branch can be missing only until it is first pushed:
		// a state of the repository to show, not a page that does not exist.
		if !explicit && apperr.KindOf(err) == apperr.KindNotFound {
			page.Unpushed = true
			for _, ref := range page.Refs {
				if ref.Kind == git.KindBranch {
					page.Branches = append(page.Branches, ref.Name)
				}
			}
			sort.Strings(page.Branches)
			return nil
		}
		return err
	}
	page.Ref, page.Commit = res.Name, res.Commit
	page.RefIsBranch, page.RefIsTag = res.Kind == git.KindBranch, res.Kind == git.KindTag

	log, more, err := gitRepo.Log(ctx, res.Commit, page.Path, page.Skip, historyPageSize)
	if err != nil {
		return tooLargeToShow(err)
	}
	page.More = more
	hashes := make([]string, len(log))
	for i, c := range log {
		hashes[i] = c.Hash
	}
	runs, err := a.ci.LatestRunPerCommit(ctx, repo.ID, hashes)
	if err != nil {
		return err
	}
	at := map[string][]reposvc.IndexedRef{}
	for _, ref := range page.Refs {
		at[ref.Commit] = append(at[ref.Commit], ref)
	}
	for _, refs := range at {
		sort.Slice(refs, func(i, j int) bool {
			if refs[i].Kind != refs[j].Kind {
				return refs[i].Kind == git.KindBranch
			}
			return refs[i].Name < refs[j].Name
		})
	}
	for _, c := range log {
		row := historyCommit{Commit: c, Refs: at[c.Hash]}
		if run, ok := runs[c.Hash]; ok {
			row.Run = &run
		}
		page.Commits = append(page.Commits, row)
	}
	return nil
}
