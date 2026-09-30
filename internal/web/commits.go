package web

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/ci"
	"github.com/mmrzaf/gitman/internal/git"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

// commitsPageSize is how many commits one page of a log shows.
const commitsPageSize = 30

// maxCommitsSkip bounds how far back a log pages: Git counts from the
// newest commit for every page.
const maxCommitsSkip = 100000

// maxCompareCommits bounds how many commits a comparison lists; beyond it,
// the page says so rather than growing without limit.
const maxCompareCommits = 250

// commitsURL is the address of the commits of ref, limited to those that
// touched path when it is not empty. Without a ref it is the default
// branch's.
func commitsURL(repoName, ref, path string) string {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	if path != "" {
		q.Set("path", path)
	}
	return commitsAddress(repoName, q)
}

// compareURL is the address of the commits head has that base lacks, and
// the changes they make.
func compareURL(repoName, base, head string) string {
	return commitsAddress(repoName, url.Values{"base": {base}, "ref": {head}})
}

func commitsAddress(repoName string, q url.Values) string {
	u := (&url.URL{Path: "/" + repoName + "/commits"}).EscapedPath()
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// commitRow is one commit of a list: the commit, what ran for it, and the
// branches and tags now at it.
type commitRow struct {
	*git.Commit
	Run  *ci.Summary
	Refs []reposvc.IndexedRef
}

// Merge reports a commit that joins two lines of history.
func (c commitRow) Merge() bool { return len(c.Parents) > 1 }

// side is one end of a comparison, or the ref a log is of.
type side struct {
	// Name is what the person chose: a branch, a tag or a commit.
	Name string
	// Kind is empty for a commit.
	Kind git.Kind
}

// commitsPage is the log of a ref, or, with a base, what the ref has that
// the base lacks.
type commitsPage struct {
	repoFrame
	Head side
	Base side
	Path string
	// Refs are what the pickers offer.
	Refs    []reposvc.IndexedRef
	Commits []commitRow
	// Unpushed reports that no ref was asked for and the default branch
	// has not been pushed yet; Branches are the ones that have been.
	Unpushed bool
	Branches []string
	// Skip and More page a log.
	Skip int
	More bool
	// Comparison is set when a base was chosen; Ahead and Behind are how
	// far Head is from Base, and Capped reports more commits than listed.
	Comparison    *git.Comparison
	Ahead, Behind int
	// Tab is what a comparison shows: its "commits", or the "changes" they
	// make.
	Tab string
}

// TabURL is this comparison's address with a tab open.
func (p commitsPage) TabURL(tab string) string {
	u := compareURL(p.Repo.Name, p.Base.Name, p.Head.Name)
	if tab != "commits" {
		u += "&tab=" + tab
	}
	return u
}

// Comparing reports a page that compares two refs.
func (p commitsPage) Comparing() bool { return p.Comparison != nil }

// NewerSkip and OlderSkip are the ?skip= of the log pages before and after
// this one.
func (p commitsPage) NewerSkip() int { return max(p.Skip-commitsPageSize, 0) }
func (p commitsPage) OlderSkip() int { return p.Skip + commitsPageSize }

// PageURL is this log's address with skip set, for Newer and Older.
func (p commitsPage) PageURL(skip int) string {
	q := url.Values{}
	if p.Head.Name != "" {
		q.Set("ref", p.Head.Name)
	}
	if p.Path != "" {
		q.Set("path", p.Path)
	}
	if skip > 0 {
		q.Set("skip", strconv.Itoa(skip))
	}
	return commitsAddress(p.Repo.Name, q)
}

// SwapURL is the comparison the other way round.
func (p commitsPage) SwapURL() string { return compareURL(p.Repo.Name, p.Head.Name, p.Base.Name) }

// commits serves /{repo}/commits: a branch, tag or commit's log, which
// ?path= narrows to the commits that touched a path, and, with ?base=, the
// commits ?ref= has that the base lacks, with the changes they make.
func (a *App) commits(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoByName(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	gitRepo, err := a.repos.Open(repo)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	page := commitsPage{repoFrame: repoFrame{Repo: repo, Section: "commits"}}
	if page.Refs, err = a.repos.ListRefs(ctx, repo.ID); err != nil {
		return err
	}
	page.Refs = orderedRefs(page.Refs, repo.DefaultBranch)

	// A side is a ref or a commit and nothing more: a path after it is not
	// part of it.
	resolve := func(name string) (side, *resolved, error) {
		res, err := a.resolveRefAndPath(ctx, gitRepo, repo.ID, name)
		if err == nil && res.Path != "" {
			err = notFound("%q is not a branch, tag, or commit of this repository.", name)
		}
		if err != nil {
			return side{}, nil, err
		}
		return side{Name: res.Name, Kind: res.Kind}, res, nil
	}

	headName, baseName := q.Get("ref"), q.Get("base")
	explicit := headName != ""
	if !explicit {
		headName = repo.DefaultBranch
	}
	var head, base *resolved
	if page.Head, head, err = resolve(headName); err != nil {
		// The default branch can be missing only until it is first pushed:
		// a state of the repository to show, not a page that does not exist.
		if !explicit && baseName == "" && apperr.KindOf(err) == apperr.KindNotFound {
			page.Unpushed = true
			page.Head = side{}
			for _, ref := range page.Refs {
				if ref.Kind == git.KindBranch {
					page.Branches = append(page.Branches, ref.Name)
				}
			}
			sort.Strings(page.Branches)
			a.render(w, r, http.StatusOK, "commits", "Commits · "+repo.Name, page)
			return nil
		}
		return err
	}

	var commits []*git.Commit
	title := "Commits · " + repo.Name
	if baseName != "" {
		if page.Base, base, err = resolve(baseName); err != nil {
			return err
		}
		cmp, err := gitRepo.Compare(ctx, base.Commit, head.Commit, maxCompareCommits, git.DefaultDiffLimits)
		if err != nil {
			if errors.Is(err, git.ErrNotFound) {
				return notFound("%s and %s share no history.", page.Base.Name, page.Head.Name)
			}
			return tooLargeToShow(err)
		}
		page.Comparison, commits = cmp, cmp.Commits
		page.Tab = tabFrom(r, "commits", "changes")
		counts, err := gitRepo.Divergences(ctx, base.Commit, []string{head.Commit})
		if err != nil {
			return tooLargeToShow(err)
		}
		page.Ahead, page.Behind = counts[head.Commit].Ahead, counts[head.Commit].Behind
		title = page.Base.Name + "...\u200b" + page.Head.Name + " \u00b7 " + repo.Name
	} else {
		page.Path = strings.Trim(q.Get("path"), "/")
		if err := checkPath(page.Path); err != nil {
			return err
		}
		page.Skip = min(historySkip(r), maxCommitsSkip)
		var more bool
		if commits, more, err = gitRepo.Log(ctx, head.Commit, page.Path, page.Skip, commitsPageSize); err != nil {
			return tooLargeToShow(err)
		}
		page.More = more
	}
	if page.Commits, err = a.commitRows(r, repo, commits, page.Refs); err != nil {
		return err
	}
	a.render(w, r, http.StatusOK, "commits", title, page)
	return nil
}

// checkPath refuses a path that names nothing inside a repository.
func checkPath(path string) error {
	if path == "" {
		return nil
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsRune(part, 0) {
			return apperr.New(apperr.KindInvalid, "That is not a path in this repository.")
		}
	}
	return nil
}

// commitRows adds to commits what a list shows beside each: its latest run
// and the branches and tags now at it.
func (a *App) commitRows(r *http.Request, repo *reposvc.Repo, commits []*git.Commit, refs []reposvc.IndexedRef) ([]commitRow, error) {
	hashes := make([]string, len(commits))
	for i, c := range commits {
		hashes[i] = c.Hash
	}
	runs, err := a.ci.LatestRunPerCommit(r.Context(), repo.ID, hashes)
	if err != nil {
		return nil, err
	}
	at := map[string][]reposvc.IndexedRef{}
	for _, ref := range refs {
		at[ref.Commit] = append(at[ref.Commit], ref)
	}
	for _, here := range at {
		sort.Slice(here, func(i, j int) bool {
			if here[i].Kind != here[j].Kind {
				return here[i].Kind == git.KindBranch
			}
			return here[i].Name < here[j].Name
		})
	}
	rows := make([]commitRow, 0, len(commits))
	for _, c := range commits {
		row := commitRow{Commit: c, Refs: at[c.Hash]}
		if run, ok := runs[c.Hash]; ok {
			row.Run = &run
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// compareSeparator divides the two refs of a comparison's old address.
// "..." can never appear inside a real ref name — ValidateName rejects ".."
// outright — so splitting on it is always unambiguous, even when a ref on
// either side itself contains a slash.
const compareSeparator = "..."

// compareRedirect sends /{repo}/compare/{base}...{head}, where beta 21
// compared two refs, to the same comparison among the commits. It resolves
// the repository first, so to someone who cannot read it, it is not found
// rather than redirected.
func (a *App) compareRedirect(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoByName(r)
	if err != nil {
		return err
	}
	crange := r.PathValue("crange")
	if crange == "" {
		http.Redirect(w, r, commitsAddress(repo.Name, nil), http.StatusMovedPermanently)
		return nil
	}
	sep := strings.Index(crange, compareSeparator)
	if sep < 0 {
		return notFound("A comparison needs two refs, separated by \"...\", e.g. main...develop.")
	}
	base, head := crange[:sep], crange[sep+len(compareSeparator):]
	if base == "" || head == "" {
		return notFound("A comparison needs a ref on each side of \"...\".")
	}
	http.Redirect(w, r, compareURL(repo.Name, base, head), http.StatusMovedPermanently)
	return nil
}

// orderedRefs puts refs in the order a list of them is read in: the default
// branch, then the other branches by name, then tags, newest version first.
func orderedRefs(refs []reposvc.IndexedRef, defaultBranch string) []reposvc.IndexedRef {
	out := slices.Clone(refs)
	slices.SortStableFunc(out, func(a, b reposvc.IndexedRef) int {
		switch {
		case a.Kind != b.Kind:
			if a.Kind == git.KindBranch {
				return -1
			}
			return 1
		case a.Kind == git.KindTag:
			return versionOrder(b.Name, a.Name)
		case a.Name == defaultBranch:
			return -1
		case b.Name == defaultBranch:
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

func versionOrder(a, b string) int {
	switch {
	case git.VersionLess(a, b):
		return -1
	case git.VersionLess(b, a):
		return 1
	}
	return 0
}

// anyRun and anyRefs report whether any commit of a list has a run, or a
// branch or tag at it: a column nothing would fill is left out.
func anyRun(rows []commitRow) bool {
	return slices.ContainsFunc(rows, func(r commitRow) bool { return r.Run != nil })
}

func anyRefs(rows []commitRow) bool {
	return slices.ContainsFunc(rows, func(r commitRow) bool { return len(r.Refs) > 0 })
}
