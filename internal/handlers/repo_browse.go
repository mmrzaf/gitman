package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

type RepoPageData struct {
	Owner         *models.User
	Repository    *models.Repository
	CurrentRef    string
	CurrentPath   string
	Branches      []string
	Tags          []string
	IsEmpty       bool
	Tree          []git.TreeEntry
	Commits       []git.Commit
	BlobContent   string
	BlobSize      int64
	IsTooBig      bool
	Collaborators []models.Collaborator
}

// RepoAccessMiddleware ensures the repository exists and is accessible.
func (app *App) RepoAccessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username := chi.URLParam(r, "username")
		repoName := chi.URLParam(r, "repo_name")

		owner, err := app.DB.GetUserByUsername(r.Context(), username)
		if err != nil || owner == nil {
			app.renderError(w, r, PageData{User: GetUser(r)}, "Repository not found", http.StatusNotFound)
			return
		}

		repo, err := app.DB.GetRepositoryByOwnerAndName(r.Context(), owner.ID, repoName)
		if err != nil || repo == nil {
			app.renderError(w, r, PageData{User: GetUser(r)}, "Repository not found", http.StatusNotFound)
			return
		}

		currentUser := GetUser(r)

		if repo.IsPrivate {
			hasAccess := currentUser != nil && currentUser.ID == repo.OwnerID
			if currentUser != nil && !hasAccess {
				hasAccess, _ = app.DB.HasRepoAccess(r.Context(), repo.ID, currentUser.ID, "read")
			}

			if !hasAccess {
				app.renderError(w, r, PageData{User: currentUser}, "Repository not found", http.StatusNotFound)
				return
			}
		}

		repoPath, err := git.SecureRepoPath(app.Config.ReposPath, username, repoName)
		if err != nil {
			app.renderError(w, r, PageData{User: currentUser}, "Invalid repository path", http.StatusInternalServerError)
			return
		}

		ctx := context.WithValue(r.Context(), repoContextKey, repo)
		ctx = context.WithValue(ctx, repoPathContextKey, repoPath)
		ctx = context.WithValue(ctx, repoOwnerContextKey, owner)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRepoMember restricts sensitive repository surfaces such as CI logs
// and artifacts to the owner or an explicit collaborator. Public source code
// remains browseable without exposing CI output.
func (app *App) RequireRepoMember(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repo := GetRepo(r)
		user := GetUser(r)
		if repo == nil || user == nil {
			http.NotFound(w, r)
			return
		}
		if user.ID == repo.OwnerID {
			next.ServeHTTP(w, r)
			return
		}
		hasAccess, err := app.DB.HasRepoAccess(r.Context(), repo.ID, user.ID, "read")
		if err != nil || !hasAccess {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loadRefsIntoData populates Branches and Tags on an existing RepoPageData.
func loadRefsIntoData(ctx context.Context, repoPath string, data *RepoPageData) {
	data.Branches, _ = git.GetBranches(ctx, repoPath)
	data.Tags, _ = git.GetTags(ctx, repoPath)
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

// HandleRepoTreeGET renders the repository's file tree view.
func (app *App) HandleRepoTreeGET(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	repoPath := GetRepoPath(r)
	owner := GetRepoOwner(r)
	ctx := r.Context()

	data := RepoPageData{
		Owner:      owner,
		Repository: repo,
	}

	// 1. Handle empty repository case.
	if git.IsEmpty(ctx, repoPath) {
		data.IsEmpty = true
		app.renderPage(w, r, "repo_view.html", PageData{
			Title: repo.Name,
			User:  GetUser(r),
			Data:  data,
		})
		return
	}

	// 2. Resolve the reference (branch, tag, commit hash, or fallback).
	refParam := requestRef(r)
	ref, err := git.ResolveRef(ctx, repoPath, refParam)
	if err != nil {
		slog.Error("Failed to resolve ref for tree", "repoPath", repoPath, "refParam", refParam, "error", err)
		app.renderError(w, r, PageData{User: GetUser(r)}, "Invalid reference", http.StatusBadRequest)
		return
	}

	// 3. Collect basic data.
	data.CurrentRef = ref
	data.CurrentPath = requestRepoPath(r)
	loadRefsIntoData(ctx, repoPath, &data)

	// 4. Fetch tree.
	tree, err := git.GetTree(ctx, repoPath, ref, data.CurrentPath)
	if err != nil {
		if errors.Is(err, git.ErrRepoEmpty) {
			data.IsEmpty = true
			app.renderPage(w, r, "repo_view.html", PageData{
				Title: repo.Name,
				User:  GetUser(r),
				Data:  data,
			})
			return
		}
		app.renderError(w, r, PageData{User: GetUser(r)}, "Failed to read repository tree", http.StatusInternalServerError)
		return
	}
	data.Tree = tree

	app.renderPage(w, r, "repo_view.html", PageData{
		Title: repo.Name,
		User:  GetUser(r),
		Data:  data,
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

	if git.IsEmpty(ctx, repoPath) {
		app.renderError(w, r, PageData{User: GetUser(r)}, "Repository is empty", http.StatusNotFound)
		return
	}

	ref, err := git.ResolveRef(ctx, repoPath, refParam)
	if err != nil {
		if errors.Is(err, git.ErrRepoEmpty) {
			app.renderError(w, r, PageData{User: GetUser(r)}, "Repository is empty", http.StatusNotFound)
			return
		}

		slog.Error("Failed to resolve ref for blob",
			"repoPath", repoPath,
			"refParam", refParam,
			"error", err,
		)
		app.renderError(w, r, PageData{User: GetUser(r)}, "Invalid reference", http.StatusBadRequest)
		return
	}
	data.CurrentRef = ref

	loadRefsIntoData(ctx, repoPath, &data)

	size, err := git.GetBlobSize(ctx, repoPath, ref, path)
	if err != nil {
		slog.Error("Failed to get blob size",
			"repoPath", repoPath,
			"ref", ref,
			"path", path,
			"error", err,
		)
		app.renderError(w, r, PageData{User: GetUser(r)}, "File not found", http.StatusNotFound)
		return
	}
	data.BlobSize = size

	if size > 2*1024*1024 {
		data.IsTooBig = true
	} else {
		content, err := git.GetBlob(ctx, repoPath, ref, path)
		if err != nil {
			if errors.Is(err, git.ErrRepoEmpty) {
				app.renderError(w, r, PageData{User: GetUser(r)}, "Repository is empty", http.StatusNotFound)
				return
			}

			slog.Error("Failed to read blob content",
				"repoPath", repoPath,
				"ref", ref,
				"path", path,
				"error", err,
			)
			app.renderError(w, r, PageData{User: GetUser(r)}, "Failed to read file", http.StatusInternalServerError)
			return
		}
		data.BlobContent = string(content)
	}

	app.renderPage(w, r, "repo_blob.html", PageData{
		Title: repo.Name + " - " + path,
		User:  GetUser(r),
		Data:  data,
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

	if git.IsEmpty(ctx, repoPath) {
		data.IsEmpty = true
		loadRefsIntoData(ctx, repoPath, &data)
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
		if errors.Is(err, git.ErrRepoEmpty) {
			data.IsEmpty = true
			app.renderPage(w, r, "repo_commits.html", PageData{
				Title: repo.Name + " Commits",
				User:  GetUser(r),
				Data:  data,
			})
			return
		}

		slog.Error("Failed to resolve ref for commits",
			"repoPath", repoPath,
			"refParam", refParam,
			"error", err,
		)
		app.renderError(w, r, PageData{User: GetUser(r)}, "Invalid reference", http.StatusBadRequest)
		return
	}
	data.CurrentRef = ref

	loadRefsIntoData(ctx, repoPath, &data)

	commits, err := git.GetCommits(ctx, repoPath, ref, 0, 50)
	if err != nil {
		if errors.Is(err, git.ErrRepoEmpty) {
			data.IsEmpty = true
		} else {
			slog.Error("Failed to fetch commits",
				"repoPath", repoPath,
				"ref", ref,
				"error", err,
			)
			app.renderError(w, r, PageData{User: GetUser(r)}, "Failed to fetch commits", http.StatusInternalServerError)
			return
		}
	} else {
		data.Commits = commits
	}

	app.renderPage(w, r, "repo_commits.html", PageData{
		Title: repo.Name + " Commits",
		User:  GetUser(r),
		Data:  data,
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

	if git.IsEmpty(ctx, repoPath) {
		app.renderError(w, r, PageData{User: GetUser(r)}, "Repository is empty", http.StatusNotFound)
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
			app.renderError(w, r, PageData{User: GetUser(r)}, "Unsupported archive format", http.StatusBadRequest)
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
			app.renderError(w, r, PageData{User: GetUser(r)}, "Unsupported archive format", http.StatusBadRequest)
			return
		}
	}

	if refPart == "" {
		app.renderError(w, r, PageData{User: GetUser(r)}, "Missing ref in archive name", http.StatusBadRequest)
		return
	}

	ref, err := git.ResolveRef(ctx, repoPath, refPart)
	if err != nil {
		slog.Error("Failed to resolve ref for archive",
			"repoPath", repoPath,
			"refPart", refPart,
			"error", err,
		)
		app.renderError(w, r, PageData{User: GetUser(r)}, "Invalid reference", http.StatusBadRequest)
		return
	}

	safeRef := git.SanitizeRefForFilename(ref)
	downloadName := fmt.Sprintf("%s-%s.%s", repo.Name, safeRef, format)

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, downloadName))

	if err := git.StreamArchive(ctx, repoPath, ref, format, w); err != nil {
		// Headers are already sent at this point; log and bail.
		slog.Error("Failed to stream archive",
			"repo", repo.Name,
			"ref", ref,
			"format", format,
			"error", err,
		)
	}
}
