package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/git"
	reposvc "github.com/mmrzaf/gitman/internal/repo"
)

// tooLargeToShow turns a git operation that ran out of time into a
// message the person can act on: the repository state itself is too
// large to compute in time, not a broken request or a failing server.
func tooLargeToShow(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return apperr.New(apperr.KindTooLarge, "This is too large for Gitman to show.")
	}
	return err
}

// splitRepoRef splits a route's combined "{repo}" path value into the
// repository name and, if present, what follows an "@": a ref, or a ref
// with a file path glued to it. Gitman's file-browsing URLs put the ref
// directly against the repository name — /waiotech@develop — since a
// slash there would be indistinguishable from a repository path.
//
// A ref name may itself contain "@" (Git allows it, just not "@{"), so
// this splits on the first "@" rather than trying to be clever about
// which one is Gitman's separator; a ref containing "@" is outside what
// this URL scheme can address, the same tradeoff most Git hosts make
// with similarly compact ref-in-path URLs.
func splitRepoRef(seg string) (name, refAndPath string, hasRef bool) {
	name, refAndPath, hasRef = strings.Cut(seg, "@")
	return name, refAndPath, hasRef
}

// resolved is a ref or a bare commit, resolved to an actual commit hash,
// with whatever file path followed it in the URL.
type resolved struct {
	// Kind is empty when the URL named a commit directly rather than a
	// branch or tag.
	Kind   git.Kind
	Name   string
	Commit string
	Path   string
}

// commitPrefixPattern matches a plausible abbreviated or full commit
// hash: hex digits, at least 7 of them (Git's shortest useful
// abbreviation), at most 64 (a full SHA-256 hash).
var commitPrefixPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// notFound is a not-found error with a message meant for the page.
func notFound(format string, args ...any) error {
	return apperr.New(apperr.KindNotFound, fmt.Sprintf(format, args...))
}

// resolveRefAndPath splits refAndPath into a ref (or commit) and a file
// path, preferring the longest known branch or tag name that is a
// prefix of it, so a branch named "release" and a tag named
// "release/1.2" are never confused. Failing that, it tries the leading
// path segment as a commit hash.
func (a *App) resolveRefAndPath(ctx context.Context, gitRepo *git.Repo, repoID, refAndPath string) (*resolved, error) {
	if refAndPath == "" {
		return nil, notFound("A ref is required after \u201c@\u201d.")
	}
	indexed, err := a.repos.ListRefs(ctx, repoID)
	if err != nil {
		return nil, err
	}

	var best *reposvc.IndexedRef
	for i, ref := range indexed {
		if refAndPath != ref.Name && !strings.HasPrefix(refAndPath, ref.Name+"/") {
			continue
		}
		if best == nil || len(ref.Name) > len(best.Name) {
			best = &indexed[i]
		}
	}
	if best != nil {
		path := strings.TrimPrefix(strings.TrimPrefix(refAndPath, best.Name), "/")
		return &resolved{Kind: best.Kind, Name: best.Name, Commit: best.Commit, Path: path}, nil
	}

	candidate, path, _ := strings.Cut(refAndPath, "/")
	if !commitPrefixPattern.MatchString(candidate) {
		return nil, notFound("%q is not a branch, tag, or commit of this repository.", candidate)
	}
	commit, err := gitRepo.ResolveCommit(ctx, candidate)
	if err != nil {
		if errors.Is(err, git.ErrNotFound) {
			return nil, notFound("%q is not a branch, tag, or commit of this repository.", candidate)
		}
		return nil, err
	}
	return &resolved{Name: candidate, Commit: commit, Path: path}, nil
}

// refPath builds the URL for ref (or a bare commit hash) at path within
// repoName — the inverse of resolveRefAndPath.
func refPath(repoName, ref, path string) string {
	u := "/" + repoName + "@" + ref
	if path != "" {
		u += "/" + path
	}
	return u
}

// refURL is refPath escaped for use in a link: a file or ref name can
// hold "?" or "#", which would otherwise end the path.
func refURL(repoName, ref, path string) string {
	return (&url.URL{Path: refPath(repoName, ref, path)}).EscapedPath()
}

// maxFileDisplayBytes bounds how much of a file Gitman will read to show
// it, either rendered or raw. Files are shown exactly as they are, with
// no rendering or highlighting, so there is no reason to read more of a
// file than a person could reasonably look at.
const maxFileDisplayBytes = 1 << 20

// breadcrumbPart is one segment of the path breadcrumb.
type breadcrumbPart struct {
	Name string
	Path string
}

// fileEntry is one row of a directory listing.
type fileEntry struct {
	git.TreeEntry
	Path string
}

// Icon is the icon a listing shows the entry with.
func (e fileEntry) Icon() string {
	switch e.Kind {
	case git.EntryDir:
		return "folder"
	case git.EntrySymlink:
		return "symlink"
	case git.EntrySubmodule:
		return "module"
	}
	return "file"
}

type filesPage struct {
	repoFrame
	Ref         string
	RefIsBranch bool
	RefIsTag    bool
	Commit      string
	Path        string
	Breadcrumb  []breadcrumbPart
	Refs        []reposvc.IndexedRef
	IsDir       bool
	Entries     []fileEntry
	IsBinary    bool
	TooLarge    bool
	// Lines is a text file's content, a line at a time.
	Lines        []string
	LastChanged  *git.Commit
	RawURL       string
	PermalinkURL string
	// ArchiveRef is what an archive of this page's ref is asked for by: its
	// name, or for a commit its full hash.
	ArchiveRef string
}

// breadcrumb splits a path into its parts, each carrying the path up to
// and including itself.
func breadcrumb(p string) []breadcrumbPart {
	if p == "" {
		return nil
	}
	parts := strings.Split(p, "/")
	out := make([]breadcrumbPart, len(parts))
	for i, name := range parts {
		out[i] = breadcrumbPart{Name: name, Path: strings.Join(parts[:i+1], "/")}
	}
	return out
}

// looksBinary applies Git's own heuristic: a NUL byte anywhere in the
// content marks it binary. It is checked on exactly what was read, which
// is capped, so this never scans an unbounded file.
func looksBinary(data []byte) bool {
	return bytes.IndexByte(data, 0) >= 0
}

// files serves /{repo}@{ref}, a ref's root directory, and
// /{repo}@{ref}/{path}, one directory or file in it. routeFilesAtRef
// hands such requests here before the router sees them.
func (a *App) files(w http.ResponseWriter, r *http.Request, name, refAndPath string) error {
	repo, err := a.repoNamed(r, name)
	if err != nil {
		return err
	}
	ctx := r.Context()
	gitRepo, err := a.repos.Open(repo)
	if err != nil {
		return err
	}
	res, err := a.resolveRefAndPath(ctx, gitRepo, repo.ID, refAndPath)
	if err != nil {
		// The default branch is what every Files link opens, and it can be
		// missing only until it is first pushed: that is a state of the
		// repository to show, not a page that does not exist.
		if refAndPath == repo.DefaultBranch && apperr.KindOf(err) == apperr.KindNotFound {
			return a.filesUnpushed(w, r, repo)
		}
		return err
	}

	entry, err := gitRepo.Entry(ctx, res.Commit, res.Path)
	if err != nil {
		if errors.Is(err, git.ErrNotFound) {
			return notFound("There is no %s at %s in %s.", pathOrRoot(res.Path), res.Name, repo.Name)
		}
		return err
	}

	if r.URL.Query().Has("raw") {
		return a.fileRaw(w, r, gitRepo, entry, res.Path)
	}
	// The ref picker submits ?ref=: the same path at the chosen ref.
	if ref := r.URL.Query().Get("ref"); ref != "" {
		target := url.URL{Path: refPath(repo.Name, ref, res.Path)}
		http.Redirect(w, r, target.EscapedPath(), http.StatusSeeOther)
		return nil
	}

	// A file's history lives in History, filtered to its path; this is
	// where the old History tab's addresses lead.
	if r.URL.Query().Get("tab") == "history" {
		target := commitsURL(repo.Name, res.Name, res.Path)
		if skip := historySkip(r); skip > 0 {
			target += "&skip=" + strconv.Itoa(skip)
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return nil
	}

	page := filesPage{
		repoFrame: repoFrame{Repo: repo, Section: "files"},
		Ref:       res.Name, Commit: res.Commit, Path: res.Path,
		Breadcrumb:   breadcrumb(res.Path),
		RawURL:       refURL(repo.Name, res.Name, res.Path) + "?raw",
		PermalinkURL: refURL(repo.Name, res.Commit, res.Path),
		RefIsBranch:  res.Kind == git.KindBranch,
		RefIsTag:     res.Kind == git.KindTag,
		ArchiveRef:   res.Name,
	}
	if res.Kind == "" {
		page.ArchiveRef = res.Commit
	}

	if page.Refs, err = a.repos.ListRefs(ctx, repo.ID); err != nil {
		return err
	}

	switch entry.Kind {
	case git.EntryDir:
		page.IsDir = true
		children, err := gitRepo.Tree(ctx, entry.Hash)
		if err != nil {
			return tooLargeToShow(err)
		}
		page.Entries = make([]fileEntry, len(children))
		for i, c := range children {
			childPath := c.Name
			if res.Path != "" {
				childPath = res.Path + "/" + c.Name
			}
			page.Entries[i] = fileEntry{TreeEntry: c, Path: childPath}
		}
		sort.Slice(page.Entries, func(i, j int) bool {
			x, y := page.Entries[i], page.Entries[j]
			if (x.Kind == git.EntryDir) != (y.Kind == git.EntryDir) {
				return x.Kind == git.EntryDir
			}
			return x.Name < y.Name
		})

	case git.EntryFile:
		data, err := gitRepo.Blob(ctx, entry.Hash, maxFileDisplayBytes)
		if err != nil {
			var tooLarge *git.TooLargeError
			if errors.As(err, &tooLarge) {
				page.TooLarge = true
				break
			}
			return err
		}
		if looksBinary(data) {
			page.IsBinary = true
		} else if len(data) > 0 {
			page.Lines = splitLines(string(data))
		}

	case git.EntrySymlink:
		if target, err := gitRepo.Blob(ctx, entry.Hash, 4096); err == nil && len(target) > 0 {
			page.Lines = splitLines(string(target))
		}
	}

	// The commit that last changed this file or directory.
	last, _, err := gitRepo.Log(ctx, res.Commit, res.Path, 0, 1)
	if err != nil {
		return tooLargeToShow(err)
	}
	if len(last) > 0 {
		page.LastChanged = last[0]
	}

	title := repo.Name + "@" + res.Name
	if res.Path != "" {
		title = path.Base(res.Path) + " \u00b7 " + title
	}
	a.render(w, r, http.StatusOK, "files", title, page)
	return nil
}

type filesUnpushedPage struct {
	repoFrame
	CloneURL string
	// Branches are the branches the repository does have, if any.
	Branches []string
}

// filesUnpushed serves the Files page of a repository whose default
// branch has not been pushed yet, pointing to the branches it has.
func (a *App) filesUnpushed(w http.ResponseWriter, r *http.Request, repo *reposvc.Repo) error {
	refs, err := a.repos.ListRefs(r.Context(), repo.ID)
	if err != nil {
		return err
	}
	page := filesUnpushedPage{repoFrame: repoFrame{Repo: repo, Section: "files"}, CloneURL: a.cloneURL(repo)}
	for _, ref := range refs {
		if ref.Kind == git.KindBranch {
			page.Branches = append(page.Branches, ref.Name)
		}
	}
	sort.Strings(page.Branches)
	a.render(w, r, http.StatusOK, "files_unpushed", repo.Name+"@"+repo.DefaultBranch, page)
	return nil
}

func pathOrRoot(p string) string {
	if p == "" {
		return "root"
	}
	return "\u201c" + p + "\u201d"
}

// historySkip is how many of the newest entries a History page skips:
// ?skip=, for "Older".
func historySkip(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("skip"))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// fileRaw serves a file's exact bytes, with no page around them. What a
// repository holds is written by any member, so it is never served as
// something a browser runs: see rawContentType and setRawHeaders.
func (a *App) fileRaw(w http.ResponseWriter, r *http.Request, gitRepo *git.Repo, entry git.TreeEntry, entryPath string) error {
	if entry.Kind != git.EntryFile {
		return notFound("Only files have a raw view.")
	}
	data, err := gitRepo.Blob(r.Context(), entry.Hash, maxFileDisplayBytes)
	if err != nil {
		var tooLarge *git.TooLargeError
		if errors.As(err, &tooLarge) {
			return apperr.New(apperr.KindTooLarge, "This file is too large to view raw.")
		}
		return err
	}
	setRawHeaders(w.Header(), rawContentType(entryPath, data), path.Base(entryPath))
	_, _ = w.Write(data)
	return nil
}

// maxTreePaths bounds how many paths the file finder loads for one ref,
// and maxTreeEntries how many tree entries, directories included, it
// looks at to find them. Counting files alone would not bound the walk:
// a pushed tree can hold directories that share one another's subtrees,
// so a handful of objects can describe millions of directories and no
// files at all.
const (
	maxTreePaths   = 20000
	maxTreeEntries = 100000
)

// treePaths serves the file finder's data: every file path in a ref's
// tree, for the client to filter as the person types.
func (a *App) treePaths(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoByName(r)
	if err != nil {
		return err
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		return apperr.New(apperr.KindInvalid, "A ref is required.")
	}
	ctx := r.Context()
	gitRepo, err := a.repos.Open(repo)
	if err != nil {
		return err
	}
	res, err := a.resolveRefAndPath(ctx, gitRepo, repo.ID, ref)
	if err != nil {
		return err
	}
	commit, err := gitRepo.Commit(ctx, res.Commit)
	if err != nil {
		return err
	}
	walk := treeWalk{git: gitRepo}
	if err := walk.dir(ctx, commit.Tree, ""); err != nil {
		return err
	}
	return writeJSON(w, walk.paths)
}

func writeJSON(w http.ResponseWriter, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_, err = w.Write(data)
	return err
}

// treeWalk collects the file paths of a tree, stopping at maxTreePaths
// paths or maxTreeEntries entries, whichever comes first.
type treeWalk struct {
	git     *git.Repo
	paths   []string
	entries int
}

func (w *treeWalk) full() bool {
	return len(w.paths) >= maxTreePaths || w.entries >= maxTreeEntries
}

func (w *treeWalk) dir(ctx context.Context, treeHash, prefix string) error {
	if w.full() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := w.git.Tree(ctx, treeHash)
	if err != nil {
		return tooLargeToShow(err)
	}
	for _, e := range entries {
		if w.full() {
			return nil
		}
		w.entries++
		p := e.Name
		if prefix != "" {
			p = prefix + "/" + e.Name
		}
		switch e.Kind {
		case git.EntryDir:
			if err := w.dir(ctx, e.Hash, p); err != nil {
				return err
			}
		case git.EntryFile, git.EntrySymlink:
			w.paths = append(w.paths, p)
		}
	}
	return nil
}

type commitPage struct {
	repoFrame
	Commit *git.Commit
	Diff   *git.Diff
}

func (a *App) commitView(w http.ResponseWriter, r *http.Request) error {
	repo, err := a.repoByName(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	gitRepo, err := a.repos.Open(repo)
	if err != nil {
		return err
	}

	hash, err := a.commitNamed(r, gitRepo, repo)
	if err != nil {
		return err
	}
	commit, err := gitRepo.Commit(ctx, hash)
	if err != nil {
		return err
	}

	var parent string
	if len(commit.Parents) > 0 {
		parent = commit.Parents[0]
	}
	diff, err := gitRepo.Diff(ctx, parent, hash, git.DefaultDiffLimits)
	if err != nil {
		return tooLargeToShow(err)
	}

	a.render(w, r, http.StatusOK, "commit", commit.ShortHash()+" \u00b7 "+repo.Name,
		commitPage{repoFrame: repoFrame{Repo: repo}, Commit: commit, Diff: diff})
	return nil
}

// commitNamed resolves the {sha} path value: a full or abbreviated commit
// hash, and nothing else — Git's other revision syntax, such as ":/text",
// which searches every commit message, is not part of a commit's address.
func (a *App) commitNamed(r *http.Request, gitRepo *git.Repo, repo *reposvc.Repo) (string, error) {
	sha := r.PathValue("sha")
	if !commitPrefixPattern.MatchString(sha) {
		return "", notFound("%q is not a commit of %s.", sha, repo.Name)
	}
	hash, err := gitRepo.ResolveCommit(r.Context(), sha)
	if errors.Is(err, git.ErrNotFound) {
		return "", notFound("%q is not a commit of %s.", sha, repo.Name)
	}
	return hash, err
}

// rawImageTypes are the file types a raw view serves as themselves:
// raster images, which a browser displays but cannot execute. SVG is
// not among them — it can carry script — and is served as text.
var rawImageTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".ico":  "image/x-icon",
}

// rawContentType decides how a raw file is served: a raster image as
// itself, any other binary as a download, and all text — HTML,
// JavaScript and SVG included — as plain text, so a file pushed to a
// repository can never run as a page of Gitman's own origin.
func rawContentType(name string, data []byte) string {
	if t, ok := rawImageTypes[strings.ToLower(path.Ext(name))]; ok && looksBinary(data) {
		return t
	}
	if looksBinary(data) {
		return "application/octet-stream"
	}
	return "text/plain; charset=utf-8"
}

// setRawHeaders sets the headers of any response that carries repository
// or run content verbatim. The sandbox policy is the second line of
// defense: whatever a browser makes of the bytes runs in an isolated
// origin with nothing allowed.
func setRawHeaders(h http.Header, contentType, filename string) {
	disposition := "inline"
	if contentType == "application/octet-stream" {
		disposition = "attachment"
	}
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": filename}))
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-cache")
}
