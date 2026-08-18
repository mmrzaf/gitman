package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

type CommitPageData struct {
	Owner          *models.User
	Repository     *models.Repository
	CurrentRef     string
	CurrentRefKind string
	Commit         git.CommitDetail
	Diff           git.CommitDiff
	LatestCI       *models.CIRun
	CanViewCI      bool
	CanControlCI   bool
}

func (app *App) HandleRepoCommitGET(w http.ResponseWriter, r *http.Request) {
	repo := GetRepo(r)
	repoPath := GetRepoPath(r)
	owner := GetRepoOwner(r)
	ctx := r.Context()

	if git.IsEmpty(ctx, repoPath) {
		app.renderError(w, r, PageData{User: GetUser(r)}, "Repository is empty", http.StatusNotFound)
		return
	}

	hash := strings.TrimSpace(chi.URLParam(r, "commit_hash"))
	detail, err := git.GetCommitDetail(ctx, repoPath, hash)
	if err != nil {
		if errors.Is(err, git.ErrRefNotFound) {
			app.renderError(w, r, PageData{User: GetUser(r)}, "Commit not found", http.StatusNotFound)
			return
		}
		slog.Warn("failed to load commit detail", "repo", repo.ID, "commit", hash, "error", err)
		app.renderError(w, r, PageData{User: GetUser(r)}, "Commit not found", http.StatusNotFound)
		return
	}

	diff, err := git.GetCommitDiff(ctx, repoPath, detail)
	if err != nil {
		slog.Error("failed to load commit diff", "repo", repo.ID, "commit", detail.Hash, "error", err)
		app.renderError(w, r, PageData{User: GetUser(r)}, "Failed to read commit diff", http.StatusInternalServerError)
		return
	}

	currentRef := strings.TrimSpace(r.URL.Query().Get("ref"))
	if currentRef != "" {
		if _, err := git.ResolveRef(ctx, repoPath, currentRef); err != nil {
			currentRef = ""
		}
	}
	if currentRef == "" {
		if len(detail.Branches) > 0 {
			currentRef = detail.Branches[0]
		} else if len(detail.Tags) > 0 {
			currentRef = detail.Tags[0]
		} else {
			currentRef = detail.Hash
		}
	}

	branches, _ := git.GetBranches(ctx, repoPath)
	tags, _ := git.GetTags(ctx, repoPath)
	data := CommitPageData{
		Owner:          owner,
		Repository:     repo,
		CurrentRef:     currentRef,
		CurrentRefKind: classifyRepoRef(currentRef, branches, tags),
		Commit:         detail,
		Diff:           diff,
		CanViewCI:      app.canViewCI(ctx, GetUser(r), repo),
		CanControlCI:   app.canControlCI(ctx, GetUser(r), repo),
	}
	if data.CanViewCI {
		if run, runErr := app.DB.GetLatestCIRunForCommit(ctx, repo.ID, detail.Hash); runErr == nil {
			data.LatestCI = run
		}
	}

	app.renderPage(w, r, "repo_commit.html", PageData{
		Title: repo.Name + " - " + shortString(detail.Hash, 12),
		User:  GetUser(r),
		Data:  data,
		RepoNav: &RepoNavData{
			Owner:      owner,
			Repository: repo,
			CurrentRef: currentRef,
			Active:     "commits",
			IsOwner:    GetUser(r) != nil && GetUser(r).ID == repo.OwnerID,
			CanViewCI:  data.CanViewCI,
		},
	})
}

func (app *App) HandleRepoBlobRawGET(w http.ResponseWriter, r *http.Request) {
	app.serveRepoBlob(w, r, false)
}

func (app *App) HandleRepoBlobDownloadGET(w http.ResponseWriter, r *http.Request) {
	app.serveRepoBlob(w, r, true)
}

func (app *App) serveRepoBlob(w http.ResponseWriter, r *http.Request, download bool) {
	noStore(w)
	repoPath := GetRepoPath(r)
	ctx := r.Context()
	ref := requestRef(r)
	path := requestRepoPath(r)
	if strings.TrimSpace(path) == "" {
		http.NotFound(w, r)
		return
	}
	resolvedRef, err := git.ResolveRef(ctx, repoPath, ref)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	size, err := git.GetBlobSize(ctx, repoPath, resolvedRef, path)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Repository blobs are untrusted, same-origin content. Raw mode deliberately
	// renders as text instead of honoring HTML/SVG MIME types; unlike GitHub,
	// Gitman does not have a separate raw-content origin to isolate active files.
	if download {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(path)}))
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	if err := git.StreamBlob(ctx, repoPath, resolvedRef, path, w); err != nil {
		slog.Warn("blob stream ended with error", "path", path, "ref", resolvedRef, "error", err)
	}
}

type fileSearchResponse struct {
	Ref     string            `json:"ref"`
	Results []fileSearchMatch `json:"results"`
}

func (app *App) HandleRepoFileSearchGET(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	repoPath := GetRepoPath(r)
	owner := GetRepoOwner(r)
	repo := GetRepo(r)
	ctx := r.Context()
	if git.IsEmpty(ctx, repoPath) {
		writeFileSearchJSON(w, fileSearchResponse{Results: []fileSearchMatch{}})
		return
	}
	ref, err := git.ResolveRef(ctx, repoPath, requestRef(r))
	if err != nil {
		http.Error(w, "Invalid reference", http.StatusBadRequest)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 200 {
		query = query[:200]
	}
	files, err := git.ListFiles(ctx, repoPath, ref)
	if err != nil {
		slog.Warn("failed to list files for finder", "repo", repo.ID, "ref", ref, "error", err)
		http.Error(w, "Failed to list files", http.StatusInternalServerError)
		return
	}
	writeFileSearchJSON(w, fileSearchResponse{
		Ref:     ref,
		Results: rankFileMatches(files, query, url.PathEscape(owner.Username), url.PathEscape(repo.Name), ref, 40),
	})
}

func writeFileSearchJSON(w http.ResponseWriter, response fileSearchResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}
