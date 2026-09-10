package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
	"github.com/mmrzaf/gitman/internal/repository"
)

type RepoPageData struct {
	PageData
	Owner          *models.User
	Repository     *models.Repository
	CurrentRef     string
	CurrentRefKind string
	ResolvedCommit string
	CurrentPath    string
	Breadcrumbs    []RepoBreadcrumb
	ParentPath     string
	Branches       []string
	Tags           []string
	RefsTruncated  bool
	IsEmpty        bool
	Tree           []git.TreeEntry
	TreeTruncated  bool
	Commits        []git.Commit
	CommitViews    []CommitListItem
	BlobContent    string
	BlobLines      []SourceLine
	BlobSize       int64
	BlobLanguage   string
	BlobBinary     bool
	IsTooBig       bool
	BlobRenderNote string
	CanViewCI      bool
	CanControlCI   bool
	LatestCommit   *git.Commit
	LatestCI       *models.CIRun
	ReadmePath     string
	ReadmeContent  string
	ReadmeTooBig   bool
	Collaborators  []models.Collaborator
}

// RepoAccessMiddleware resolves the repository and establishes source-read and
// member authorization once for the rest of the request. Database failures are
// never translated into a fake 404.
func (app *App) RepoAccessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username := chi.URLParam(r, "username")
		repoName := chi.URLParam(r, "repo_name")

		owner, err := app.DB.GetUserByUsername(r.Context(), username)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				app.respondForSurface(w, r, apperr.New(apperr.KindNotFound, "Repository not found"))
			} else {
				app.respondForSurface(w, r, apperr.Wrap(apperr.KindUnavailable, "Gitman is temporarily unavailable", err))
			}
			return
		}

		repo, err := app.DB.GetRepositoryByOwnerAndName(r.Context(), owner.ID, repoName)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				app.respondForSurface(w, r, apperr.New(apperr.KindNotFound, "Repository not found"))
			} else {
				app.respondForSurface(w, r, apperr.Wrap(apperr.KindUnavailable, "Gitman is temporarily unavailable", err))
			}
			return
		}

		currentUser := GetUser(r)
		access, err := repository.ResolveAccess(r.Context(), app.DB, currentUser, repo)
		if err != nil {
			app.respondForSurface(w, r, apperr.Wrap(apperr.KindUnavailable, "Gitman is temporarily unavailable", err))
			return
		}
		if !access.CanRead {
			// Private repositories deliberately remain indistinguishable from missing ones.
			app.respondForSurface(w, r, apperr.New(apperr.KindNotFound, "Repository not found"))
			return
		}

		repoPath, err := git.SecureRepoPath(app.Config.ReposPath, username, repoName)
		if err != nil {
			app.respondForSurface(w, r, apperr.Wrap(apperr.KindInternal, "Gitman could not resolve this repository", err))
			return
		}

		ctx := context.WithValue(r.Context(), repoContextKey, repo)
		ctx = context.WithValue(ctx, repoPathContextKey, repoPath)
		ctx = context.WithValue(ctx, repoOwnerContextKey, owner)
		ctx = context.WithValue(ctx, repoMemberContextKey, access.IsMember)
		ctx = context.WithValue(ctx, repoWriteContextKey, access.CanWrite)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRepoMember restricts sensitive repository surfaces such as CI logs
// and artifacts to the owner or an explicit collaborator. RepoAccessMiddleware
// already performed the database-backed membership lookup.
func (app *App) RequireRepoMember(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		member, ok := r.Context().Value(repoMemberContextKey).(bool)
		if !ok || !member {
			message := "Repository not found"
			if requestSurface(r) == surfaceAPI {
				message = "repository not found"
			}
			app.respondForSurface(w, r, apperr.New(apperr.KindNotFound, message))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireRepoOwner centralizes the authorization boundary for repository
// settings. Handlers keep their own owner checks as defense in depth, but no
// settings renderer should be reachable before this middleware succeeds.
func (app *App) RequireRepoOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := GetUser(r)
		repo := GetRepo(r)
		if user == nil || repo == nil || user.ID != repo.OwnerID {
			app.respondForSurface(w, r, apperr.New(apperr.KindForbidden, "Forbidden"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loadRefsIntoData populates Branches and Tags without turning a Git failure
// into an apparently empty repository navigation state.
const (
	maxRepoRefDisplayEntries = 2000
	maxRepoTreeEntries       = 10000
	maxRepoTreeBytes         = 8 * 1024 * 1024
)

func loadRefsIntoData(ctx context.Context, repoPath string, data *RepoPageData) error {
	branches, branchesTruncated, err := git.GetBranchesLimited(ctx, repoPath, maxRepoRefDisplayEntries)
	if err != nil {
		return err
	}
	tags, tagsTruncated, err := git.GetTagsLimited(ctx, repoPath, maxRepoRefDisplayEntries)
	if err != nil {
		return err
	}
	data.Branches = branches
	data.Tags = tags
	data.RefsTruncated = branchesTruncated || tagsTruncated
	// A direct URL may select a valid branch/tag beyond the bounded selector
	// window. Keep that active ref visible even when the surrounding list is
	// truncated, otherwise the browser would show a different selected option
	// from the revision actually being rendered.
	switch data.CurrentRefKind {
	case "branch":
		data.Branches = ensureRefVisible(data.Branches, data.CurrentRef)
	case "tag":
		data.Tags = ensureRefVisible(data.Tags, data.CurrentRef)
	}
	return nil
}

func ensureRefVisible(refs []string, current string) []string {
	if current == "" {
		return refs
	}
	for _, ref := range refs {
		if ref == current {
			return refs
		}
	}
	return append(refs, current)
}

func requestRef(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("ref"))
}

func requestRepoPath(r *http.Request) string {
	return strings.TrimPrefix(r.URL.Query().Get("path"), "/")
}

const maxReadmeRenderBytes int64 = 512 * 1024

func readmeCandidate(tree []git.TreeEntry) string {
	preferred := []string{"readme.md", "readme.markdown", "readme", "readme.txt"}
	for _, want := range preferred {
		for _, entry := range tree {
			if entry.Type == "blob" && strings.EqualFold(entry.Name, want) {
				return entry.Name
			}
		}
	}
	return ""
}

func (app *App) loadRepoRootOverview(ctx context.Context, repoPath string, data *RepoPageData) error {
	if data == nil || data.Repository == nil || data.Owner == nil || data.CurrentPath != "" || data.CurrentRef == "" {
		return nil
	}

	commits, err := git.GetCommits(ctx, repoPath, data.CurrentRef, 0, 1)
	if err != nil {
		return fmt.Errorf("load repository overview commit: %w", err)
	}
	if len(commits) == 1 {
		commit := commits[0]
		data.LatestCommit = &commit
		if data.CanViewCI {
			run, runErr := app.DB.GetLatestCIRunForCommit(ctx, data.Repository.ID, commit.Hash)
			switch {
			case runErr == nil:
				data.LatestCI = run
			case errors.Is(runErr, db.ErrNotFound):
				// No CI run for this commit is a normal empty state.
			default:
				return fmt.Errorf("load repository overview CI state: %w", runErr)
			}
		}
	}

	readmePath := readmeCandidate(data.Tree)
	if readmePath == "" {
		return nil
	}
	data.ReadmePath = readmePath
	size, err := git.GetBlobSize(ctx, repoPath, data.CurrentRef, readmePath)
	if err != nil {
		return fmt.Errorf("read README size: %w", err)
	}
	if size > maxReadmeRenderBytes {
		data.ReadmeTooBig = true
		return nil
	}
	content, err := git.GetBlob(ctx, repoPath, data.CurrentRef, readmePath)
	if err != nil {
		return fmt.Errorf("read README: %w", err)
	}
	if !isTextBlob(content) {
		return nil
	}
	data.ReadmeContent = string(content)
	return nil
}

// HandleRepoTreeGET renders the repository's file tree view.
func (app *App) HandleRepoTreeGET(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	repoPath := GetRepoPath(r)
	owner := GetRepoOwner(r)
	ctx := r.Context()

	data := RepoPageData{
		Owner:        owner,
		Repository:   repo,
		CanViewCI:    app.canViewCI(ctx, GetUser(r), repo),
		CanControlCI: app.canControlCI(ctx, GetUser(r), repo),
	}

	// 1. Handle empty repository case.
	isEmpty, err := git.IsEmpty(ctx, repoPath)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
		return
	}
	if isEmpty {
		data.IsEmpty = true
		data.PageData = PageData{Title: repo.Name, User: GetUser(r)}
		app.renderPage(w, r, "repo_view.html", &data)
		return
	}

	// 2. Resolve the requested revision exactly; missing/broken HEAD is not substituted.
	refParam := requestRef(r)
	refInfo, err := git.ResolveRefInfo(ctx, repoPath, refParam)
	if err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "Path not found"))
		return
	}
	ref := refInfo.Name

	// 3. Collect basic data.
	data.CurrentRef = ref
	data.CurrentRefKind = repoRefKindLabel(refInfo.Kind)
	data.CurrentPath = requestRepoPath(r)
	data.Breadcrumbs = repoBreadcrumbs(data.CurrentPath)
	if len(data.Breadcrumbs) > 1 {
		data.ParentPath = data.Breadcrumbs[len(data.Breadcrumbs)-2].Path
	}
	data.ResolvedCommit, err = git.ResolveRevisionCommitHash(ctx, repoPath, ref)
	if err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "Revision not found"))
		return
	}
	if err := loadRefsIntoData(ctx, repoPath, &data); err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "Revision not found"))
		return
	}

	// 4. Fetch tree.
	tree, treeTruncated, err := git.GetTreeLimited(ctx, repoPath, ref, data.CurrentPath, git.TreeListLimits{
		MaxEntries: maxRepoTreeEntries,
		MaxBytes:   maxRepoTreeBytes,
	})
	if err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "Path not found"))
		return
	}
	data.Tree = tree
	data.TreeTruncated = treeTruncated
	if err := app.loadRepoRootOverview(ctx, repoPath, &data); err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
		return
	}

	data.PageData = PageData{Title: repo.Name, User: GetUser(r), RepoNav: app.repoNavData(r, data.CurrentRef)}
	app.renderPage(w, r, "repo_view.html", &data)
}

// HandleRepoBlobGET renders the content of a specific file (blob).
func (app *App) HandleRepoBlobGET(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	repoPath := GetRepoPath(r)
	owner := GetRepoOwner(r)
	ctx := r.Context()

	refParam := requestRef(r)
	path := requestRepoPath(r)

	data := RepoPageData{
		Owner:       owner,
		Repository:  repo,
		CurrentPath: path,
	}

	isEmpty, err := git.IsEmpty(ctx, repoPath)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
		return
	}
	if isEmpty {
		app.respondWebError(w, r, apperr.New(apperr.KindNotFound, "Repository is empty"))
		return
	}

	refInfo, err := git.ResolveRefInfo(ctx, repoPath, refParam)
	if err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "File not found"))
		return
	}
	ref := refInfo.Name
	data.CurrentRef = ref
	data.CurrentRefKind = repoRefKindLabel(refInfo.Kind)
	data.ResolvedCommit, err = git.ResolveRevisionCommitHash(ctx, repoPath, ref)
	if err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "Revision not found"))
		return
	}
	data.Breadcrumbs = repoBreadcrumbs(path)
	data.BlobLanguage = sourceLanguageLabel(path)

	if err := loadRefsIntoData(ctx, repoPath, &data); err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "Revision not found"))
		return
	}

	size, err := git.GetBlobSize(ctx, repoPath, ref, path)
	if err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "File not found"))
		return
	}
	data.BlobSize = size

	if size > maxSourceRenderBytes {
		data.IsTooBig = true
		data.BlobRenderNote = "Files larger than 2 MiB are available through Raw or Download."
	} else {
		content, err := git.GetBlob(ctx, repoPath, ref, path)
		if err != nil {
			app.respondWebError(w, r, repositoryGitError(err, "File not found"))
			return
		}
		data.BlobContent = string(content)
		if isTextBlob(content) {
			if note := sourceRenderLimitNote(content); note != "" {
				data.IsTooBig = true
				data.BlobRenderNote = note
			} else {
				data.BlobLines = sourceLines(content)
			}
		} else {
			data.BlobBinary = true
		}
	}

	data.PageData = PageData{Title: repo.Name + " - " + path, User: GetUser(r), RepoNav: app.repoNavData(r, data.CurrentRef)}
	app.renderPage(w, r, "repo_blob.html", &data)
}

// HandleRepoCommitsGET renders a list of commits for a repository/ref.
func (app *App) HandleRepoCommitsGET(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	repoPath := GetRepoPath(r)
	owner := GetRepoOwner(r)
	ctx := r.Context()

	data := RepoPageData{
		Owner:      owner,
		Repository: repo,
	}

	isEmpty, err := git.IsEmpty(ctx, repoPath)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
		return
	}
	if isEmpty {
		data.IsEmpty = true
		if err := loadRefsIntoData(ctx, repoPath, &data); err != nil {
			app.respondWebError(w, r, repositoryGitError(err, "Revision not found"))
			return
		}
		data.PageData = PageData{Title: repo.Name + " Commits", User: GetUser(r)}
		app.renderPage(w, r, "repo_commits.html", &data)
		return
	}

	refParam := requestRef(r)
	refInfo, err := git.ResolveRefInfo(ctx, repoPath, refParam)
	if err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "Revision not found"))
		return
	}
	ref := refInfo.Name
	data.CurrentRef = ref
	data.CurrentRefKind = repoRefKindLabel(refInfo.Kind)
	data.ResolvedCommit, err = git.ResolveRevisionCommitHash(ctx, repoPath, ref)
	if err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "Revision not found"))
		return
	}
	data.CanViewCI = app.canViewCI(ctx, GetUser(r), repo)
	data.CanControlCI = app.canControlCI(ctx, GetUser(r), repo)

	if err := loadRefsIntoData(ctx, repoPath, &data); err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "Revision not found"))
		return
	}

	commits, err := git.GetCommits(ctx, repoPath, ref, 0, 50)
	if err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "Revision not found"))
		return
	} else {
		data.Commits = commits
		latestRuns := map[string]models.CIRun{}
		if data.CanViewCI && len(commits) > 0 {
			hashes := make([]string, 0, len(commits))
			for _, commit := range commits {
				hashes = append(hashes, commit.Hash)
			}
			runs, runErr := app.DB.GetLatestCIRunsForCommits(ctx, repo.ID, hashes)
			if runErr != nil {
				app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI data is temporarily unavailable", runErr))
				return
			}
			latestRuns = runs
		}
		data.CommitViews = make([]CommitListItem, 0, len(commits))
		for _, commit := range commits {
			item := CommitListItem{Commit: commit}
			if run, ok := latestRuns[commit.Hash]; ok {
				runCopy := run
				item.CI = &runCopy
			}
			data.CommitViews = append(data.CommitViews, item)
		}
	}

	data.PageData = PageData{Title: repo.Name + " Commits", User: GetUser(r), RepoNav: app.repoNavData(r, data.CurrentRef)}
	app.renderPage(w, r, "repo_commits.html", &data)
}

// HandleRepoArchiveGET streams a zip or tar.gz archive of the repository.
//
// Route: /archive/{format}?ref=<revision>.
func (app *App) HandleRepoArchiveGET(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	ctx, cleanup, ok := app.beginRepoStream(w, r)
	if !ok {
		return
	}
	defer cleanup()
	repo := GetRepo(r)
	repoPath := GetRepoPath(r)

	isEmpty, err := git.IsEmpty(ctx, repoPath)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
		return
	}
	if isEmpty {
		app.respondWebError(w, r, apperr.New(apperr.KindNotFound, "Repository is empty"))
		return
	}

	format := chi.URLParam(r, "format")
	refPart := strings.TrimSpace(r.URL.Query().Get("ref"))
	var contentType string
	switch format {
	case "tar.gz":
		contentType = "application/gzip"
	case "tar":
		contentType = "application/x-tar"
	case "zip":
		contentType = "application/zip"
	default:
		app.respondWebError(w, r, apperr.New(apperr.KindInvalid, "Unsupported archive format"))
		return
	}

	if refPart == "" {
		app.respondWebError(w, r, apperr.New(apperr.KindInvalid, "Missing ref in archive name"))
		return
	}

	ref, err := git.ResolveRef(ctx, repoPath, refPart)
	if err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "Revision not found"))
		return
	}

	safeRef := git.SanitizeRefForFilename(ref)
	downloadName := fmt.Sprintf("%s-%s.%s", repo.Name, safeRef, format)

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, downloadName))

	if err := git.StreamArchive(ctx, repoPath, ref, format, w); err != nil {
		// Headers are already sent at this point; log and bail.
		slog.Error("failed to stream repository archive",
			"request_id", RequestID(r),
			"repo", repo.Name,
			"ref", ref,
			"format", format,
			"error", err,
		)
	}
}


