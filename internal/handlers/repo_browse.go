package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

type RepoPageData struct {
	Owner       *models.User
	Repository  *models.Repository
	CurrentRef  string
	CurrentPath string
	Branches    []string
	IsEmpty     bool
	Tree        []git.TreeEntry
	Commits     []git.Commit
	BlobContent string
	BlobSize    int64
	IsTooBig    bool
}

// RepoAccessMiddleware ensures the repository exists and is accessible.
func (app *App) RepoAccessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username := chi.URLParam(r, "username")
		repoName := chi.URLParam(r, "repo_name")

		owner, err := app.DB.GetUserByUsername(r.Context(), username)
		if err != nil || owner == nil {
			app.renderError(w, PageData{User: GetUser(r)}, "Repository not found", http.StatusNotFound)
			return
		}

		repo, err := app.DB.GetRepositoryByOwnerAndName(r.Context(), owner.ID, repoName)
		if err != nil || repo == nil {
			app.renderError(w, PageData{User: GetUser(r)}, "Repository not found", http.StatusNotFound)
			return
		}

		currentUser := GetUser(r)

		if repo.IsPrivate {
			if currentUser == nil || currentUser.ID != repo.OwnerID {
				app.renderError(w, PageData{User: currentUser}, "Repository not found", http.StatusNotFound)
				return
			}
		}

		repoPath, err := git.SecureRepoPath(app.Config.ReposPath, username, repoName)
		if err != nil {
			app.renderError(w, PageData{User: currentUser}, "Invalid repository path", http.StatusInternalServerError)
			return
		}

		ctx := context.WithValue(r.Context(), repoContextKey, repo)
		ctx = context.WithValue(ctx, repoPathContextKey, repoPath)
		ctx = context.WithValue(ctx, repoOwnerContextKey, owner)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
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
		app.renderPage(w, "repo_view.html", PageData{
			Title: repo.Name,
			User:  GetUser(r),
			Data:  data,
		})
		return
	}

	// 2. Resolve the reference (branch or fallback).
	refParam := chi.URLParam(r, "ref")
	ref, err := git.ResolveRef(ctx, repoPath, refParam)
	if err != nil {
		slog.Error("Failed to resolve ref for tree",
			"repoPath", repoPath,
			"refParam", refParam,
			"error", err,
		)
		app.renderError(w, PageData{User: GetUser(r)}, "Failed to determine branch", http.StatusInternalServerError)
		return
	}

	// 3. Collect basic data.
	data.CurrentRef = ref
	data.CurrentPath = chi.URLParam(r, "*")
	branches, _ := git.GetBranches(ctx, repoPath) // ignore error for UI; empty slice is fine
	data.Branches = branches

	// 4. Fetch tree.
	tree, err := git.GetTree(ctx, repoPath, ref, data.CurrentPath)
	if err != nil {
		// If Git itself reports empty, show empty state (defensive, though IsEmpty handled earlier).
		if errors.Is(err, git.ErrRepoEmpty) {
			data.IsEmpty = true
			app.renderPage(w, "repo_view.html", PageData{
				Title: repo.Name,
				User:  GetUser(r),
				Data:  data,
			})
			return
		}

		slog.Error("Failed to read repository tree",
			"repoPath", repoPath,
			"ref", ref,
			"path", data.CurrentPath,
			"error", err,
		)
		app.renderError(w, PageData{User: GetUser(r)}, "Failed to read repository tree", http.StatusInternalServerError)
		return
	}
	data.Tree = tree

	app.renderPage(w, "repo_view.html", PageData{
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

	refParam := chi.URLParam(r, "ref")
	path := chi.URLParam(r, "*")

	data := RepoPageData{
		Owner:       owner,
		Repository:  repo,
		CurrentPath: path,
	}

	// 1. Empty repository -> 404.
	if git.IsEmpty(ctx, repoPath) {
		app.renderError(w, PageData{User: GetUser(r)}, "Repository is empty", http.StatusNotFound)
		return
	}

	// 2. Resolve reference through git.ResolveRef.
	ref, err := git.ResolveRef(ctx, repoPath, refParam)
	if err != nil {
		if errors.Is(err, git.ErrRepoEmpty) {
			app.renderError(w, PageData{User: GetUser(r)}, "Repository is empty", http.StatusNotFound)
			return
		}

		slog.Error("Failed to resolve ref for blob",
			"repoPath", repoPath,
			"refParam", refParam,
			"error", err,
		)
		app.renderError(w, PageData{User: GetUser(r)}, "Invalid reference", http.StatusBadRequest)
		return
	}
	data.CurrentRef = ref

	// 3. Branch list (for header / UI).
	branches, _ := git.GetBranches(ctx, repoPath)
	data.Branches = branches

	// 4. Blob size first.
	size, err := git.GetBlobSize(ctx, repoPath, ref, path)
	if err != nil {
		slog.Error("Failed to get blob size",
			"repoPath", repoPath,
			"ref", ref,
			"path", path,
			"error", err,
		)
		app.renderError(w, PageData{User: GetUser(r)}, "File not found", http.StatusNotFound)
		return
	}
	data.BlobSize = size

	// 5. Big file: do not load content.
	if size > 2*1024*1024 { // 2MB
		data.IsTooBig = true
	} else {
		content, err := git.GetBlob(ctx, repoPath, ref, path)
		if err != nil {
			// again, check for empty defensively
			if errors.Is(err, git.ErrRepoEmpty) {
				app.renderError(w, PageData{User: GetUser(r)}, "Repository is empty", http.StatusNotFound)
				return
			}

			slog.Error("Failed to read blob content",
				"repoPath", repoPath,
				"ref", ref,
				"path", path,
				"error", err,
			)
			app.renderError(w, PageData{User: GetUser(r)}, "Failed to read file", http.StatusInternalServerError)
			return
		}
		data.BlobContent = string(content)
	}

	app.renderPage(w, "repo_blob.html", PageData{
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

	// 1. If repo is empty, show empty state + branches (if any).
	if git.IsEmpty(ctx, repoPath) {
		data.IsEmpty = true
		data.Branches, _ = git.GetBranches(ctx, repoPath)
		app.renderPage(w, "repo_commits.html", PageData{
			Title: repo.Name + " Commits",
			User:  GetUser(r),
			Data:  data,
		})
		return
	}

	// 2. Resolve ref using git.ResolveRef.
	refParam := chi.URLParam(r, "ref")
	ref, err := git.ResolveRef(ctx, repoPath, refParam)
	if err != nil {
		if errors.Is(err, git.ErrRepoEmpty) {
			data.IsEmpty = true
			app.renderPage(w, "repo_commits.html", PageData{
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
		app.renderError(w, PageData{User: GetUser(r)}, "Failed to determine branch", http.StatusInternalServerError)
		return
	}
	data.CurrentRef = ref

	// 3. Branch list.
	branches, _ := git.GetBranches(ctx, repoPath)
	data.Branches = branches

	// 4. Fetch commits.
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
			app.renderError(w, PageData{User: GetUser(r)}, "Failed to fetch commits", http.StatusInternalServerError)
			return
		}
	} else {
		data.Commits = commits
	}

	app.renderPage(w, "repo_commits.html", PageData{
		Title: repo.Name + " Commits",
		User:  GetUser(r),
		Data:  data,
	})
}

// HandleRepoArchiveGET streams a zip or tar.gz archive of the repository.
func (app *App) HandleRepoArchiveGET(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	repoPath := GetRepoPath(r)
	ctx := r.Context()

	if git.IsEmpty(ctx, repoPath) {
		app.renderError(w, PageData{User: GetUser(r)}, "Repository is empty", http.StatusNotFound)
		return
	}

	refParam := chi.URLParam(r, "ref")
	format := chi.URLParam(r, "format")

	if format != "zip" && format != "tar.gz" && format != "tar" {
		app.renderError(w, PageData{User: GetUser(r)}, "Unsupported archive format", http.StatusBadRequest)
		return
	}

	ref, err := git.ResolveRef(ctx, repoPath, refParam)
	if err != nil {
		if errors.Is(err, git.ErrRepoEmpty) {
			app.renderError(w, PageData{User: GetUser(r)}, "Repository is empty", http.StatusNotFound)
			return
		}
		slog.Error("Failed to resolve ref for archive", "error", err)
		app.renderError(w, PageData{User: GetUser(r)}, "Invalid reference", http.StatusBadRequest)
		return
	}

	contentType := "application/zip"
	ext := ".zip"
	if format == "tar.gz" {
		contentType = "application/gzip"
		ext = ".tar.gz"
	} else if format == "tar" {
		contentType = "application/x-tar"
		ext = ".tar"
	}

	filename := fmt.Sprintf("%s-%s%s", repo.Name, ref, ext)

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	// If streaming fails midway, we can't send an HTTP error page anymore because headers
	// and partial content are already sent, so we log it.
	if err := git.StreamArchive(ctx, repoPath, ref, format, w); err != nil {
		slog.Error("Failed to stream archive",
			"repo", repo.Name,
			"ref", ref,
			"format", format,
			"error", err,
		)
	}
}
