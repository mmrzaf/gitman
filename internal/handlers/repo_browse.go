package handlers

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
	readmemarkdown "github.com/mmrzaf/gitman/internal/markdown"
	"github.com/mmrzaf/gitman/internal/models"
	"github.com/mmrzaf/gitman/internal/repository"
)

type RepoPageData struct {
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
	IsEmpty        bool
	Tree           []git.TreeEntry
	Commits        []git.Commit
	CommitViews    []CommitListItem
	BlobContent    string
	BlobLines      []SourceLine
	BlobSize       int64
	BlobLanguage   string
	BlobLanguageID string
	BlobBinary     bool
	IsTooBig       bool
	BlobRenderNote string
	CanViewCI      bool
	CanControlCI   bool
	LatestCommit   *git.Commit
	LatestCI       *models.CIRun
	ReadmePath     string
	ReadmeHTML     template.HTML
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

// loadRefsIntoData populates Branches and Tags without turning a Git failure
// into an apparently empty repository navigation state.
func loadRefsIntoData(ctx context.Context, repoPath string, data *RepoPageData) error {
	branches, err := git.GetBranches(ctx, repoPath)
	if err != nil {
		return err
	}
	tags, err := git.GetTags(ctx, repoPath)
	if err != nil {
		return err
	}
	data.Branches = branches
	data.Tags = tags
	return nil
}

func requestRef(r *http.Request) string {
	if ref := strings.TrimSpace(r.URL.Query().Get("ref")); ref != "" {
		return ref
	}
	return chi.URLParam(r, "ref")
}

func requestRepoPath(r *http.Request) string {
	if path := strings.TrimPrefix(r.URL.Query().Get("path"), "/"); path != "" {
		return path
	}
	return strings.TrimPrefix(chi.URLParam(r, "*"), "/")
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
	data.ReadmeHTML = readmemarkdown.Render(string(content), readmemarkdown.Options{
		Owner:      data.Owner.Username,
		Repository: data.Repository.Name,
		Ref:        data.CurrentRef,
		ReadmePath: readmePath,
	})
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
		app.renderPage(w, r, "repo_view.html", PageData{
			Title: repo.Name,
			User:  GetUser(r),
			Data:  data,
		})
		return
	}

	// 2. Resolve the requested revision exactly; missing/broken HEAD is not substituted.
	refParam := requestRef(r)
	ref, err := git.ResolveRef(ctx, repoPath, refParam)
	if err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "Path not found"))
		return
	}

	// 3. Collect basic data.
	data.CurrentRef = ref
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
	data.CurrentRefKind = classifyRepoRef(data.CurrentRef, data.Branches, data.Tags)

	// 4. Fetch tree.
	tree, err := git.GetTree(ctx, repoPath, ref, data.CurrentPath)
	if err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "Path not found"))
		return
	}
	data.Tree = tree
	if err := app.loadRepoRootOverview(ctx, repoPath, &data); err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
		return
	}

	app.renderPage(w, r, "repo_view.html", PageData{
		Title:   repo.Name,
		User:    GetUser(r),
		Data:    data,
		RepoNav: app.repoNavData(r, data.CurrentRef),
	})
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

	ref, err := git.ResolveRef(ctx, repoPath, refParam)
	if err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "File not found"))
		return
	}
	data.CurrentRef = ref
	data.ResolvedCommit, err = git.ResolveRevisionCommitHash(ctx, repoPath, ref)
	if err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "Revision not found"))
		return
	}
	data.Breadcrumbs = repoBreadcrumbs(path)
	language := detectSourceLanguage(path)
	data.BlobLanguage = language.Label
	data.BlobLanguageID = language.ID

	if err := loadRefsIntoData(ctx, repoPath, &data); err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "Revision not found"))
		return
	}
	data.CurrentRefKind = classifyRepoRef(data.CurrentRef, data.Branches, data.Tags)

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
				data.BlobLines = sourceLines(content, language)
			}
		} else {
			data.BlobBinary = true
		}
	}

	app.renderPage(w, r, "repo_blob.html", PageData{
		Title:   repo.Name + " - " + path,
		User:    GetUser(r),
		Data:    data,
		RepoNav: app.repoNavData(r, data.CurrentRef),
	})
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
		app.renderPage(w, r, "repo_commits.html", PageData{
			Title: repo.Name + " Commits",
			User:  GetUser(r),
			Data:  data,
		})
		return
	}

	refParam := requestRef(r)
	ref, err := git.ResolveRef(ctx, repoPath, refParam)
	if err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "Revision not found"))
		return
	}
	data.CurrentRef = ref
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
	data.CurrentRefKind = classifyRepoRef(data.CurrentRef, data.Branches, data.Tags)

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

	app.renderPage(w, r, "repo_commits.html", PageData{
		Title:   repo.Name + " Commits",
		User:    GetUser(r),
		Data:    data,
		RepoNav: app.repoNavData(r, data.CurrentRef),
	})
}

// HandleRepoArchiveGET streams a zip or tar.gz archive of the repository.
//
// Route: /archive/* — the wildcard captures "<ref>.<format>" including refs
// that contain slashes (e.g. "feature/foo.zip" → ref=feature/foo, format=zip).
func (app *App) HandleRepoArchiveGET(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	repo := GetRepo(r)
	repoPath := GetRepoPath(r)
	ctx := r.Context()

	isEmpty, err := git.IsEmpty(ctx, repoPath)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
		return
	}
	if isEmpty {
		app.respondWebError(w, r, apperr.New(apperr.KindNotFound, "Repository is empty"))
		return
	}

	var refPart, format, contentType string
	format = chi.URLParam(r, "format")
	refPart = strings.TrimSpace(r.URL.Query().Get("ref"))
	if format != "" {
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
	} else {
		archivePath := chi.URLParam(r, "*")
		switch {
		case strings.HasSuffix(archivePath, ".tar.gz"):
			format, contentType = "tar.gz", "application/gzip"
			refPart = strings.TrimSuffix(archivePath, ".tar.gz")
		case strings.HasSuffix(archivePath, ".tar"):
			format, contentType = "tar", "application/x-tar"
			refPart = strings.TrimSuffix(archivePath, ".tar")
		case strings.HasSuffix(archivePath, ".zip"):
			format, contentType = "zip", "application/zip"
			refPart = strings.TrimSuffix(archivePath, ".zip")
		default:
			app.respondWebError(w, r, apperr.New(apperr.KindInvalid, "Unsupported archive format"))
			return
		}
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
