package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/db"
	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/models"
)

type CommitPageData struct {
	PageData
	Owner          *models.User
	Repository     *models.Repository
	CurrentRef     string
	CurrentRefKind string
	CIBranch       string
	CITag          string
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

	isEmpty, err := git.IsEmpty(ctx, repoPath)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
		return
	}
	if isEmpty {
		app.respondWebError(w, r, apperr.New(apperr.KindNotFound, "Repository is empty"))
		return
	}

	hash := strings.TrimSpace(chi.URLParam(r, "commit_hash"))
	detail, err := git.GetCommitDetail(ctx, repoPath, hash)
	if err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "Commit not found"))
		return
	}

	diff, err := git.GetCommitDiff(ctx, repoPath, detail)
	if err != nil {
		app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
		return
	}

	currentRef := strings.TrimSpace(r.URL.Query().Get("ref"))
	if currentRef != "" {
		if _, refErr := git.ResolveRef(ctx, repoPath, currentRef); refErr != nil {
			if errors.Is(refErr, git.ErrInvalidRef) || errors.Is(refErr, git.ErrRefNotFound) {
				// A stale/invalid presentation context should not hide an otherwise
				// valid immutable commit.
				currentRef = ""
			} else {
				app.respondWebError(w, r, repositoryGitError(refErr, "Revision not found"))
				return
			}
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

	currentRefInfo, err := git.ResolveRefInfo(ctx, repoPath, currentRef)
	if err != nil {
		app.respondWebError(w, r, repositoryGitError(err, "Revision not found"))
		return
	}
	currentRefKind := repoRefKindLabel(currentRefInfo.Kind)
	if currentRefKind == "branch" {
		reachable, reachErr := git.IsCommitReachableFromBranch(ctx, repoPath, detail.Hash, currentRef)
		if reachErr != nil {
			app.respondWebError(w, r, repositoryGitError(reachErr, "Revision not found"))
			return
		}
		if !reachable {
			currentRef = detail.Hash
			currentRefKind = "commit"
		}
	}

	var ciBranch, ciTag string
	switch currentRefKind {
	case "branch":
		ciBranch = currentRef
	case "tag":
		tagHash, tagErr := git.ResolveTagCommitHash(ctx, repoPath, currentRef)
		if tagErr != nil {
			app.respondWebError(w, r, repositoryGitError(tagErr, "Revision not found"))
			return
		}
		if tagHash == detail.Hash {
			ciTag = currentRef
		}
	}

	data := CommitPageData{
		Owner:          owner,
		Repository:     repo,
		CurrentRef:     currentRef,
		CurrentRefKind: currentRefKind,
		CIBranch:       ciBranch,
		CITag:          ciTag,
		Commit:         detail,
		Diff:           diff,
		CanViewCI:      app.canViewCI(ctx, GetUser(r), repo),
		CanControlCI:   app.canControlCI(ctx, GetUser(r), repo),
	}
	if data.CanViewCI {
		run, runErr := app.DB.GetLatestCIRunForCommit(ctx, repo.ID, detail.Hash)
		switch {
		case runErr == nil:
			data.LatestCI = run
		case errors.Is(runErr, db.ErrNotFound):
			// No run for this commit is a normal empty state.
		default:
			app.respondWebError(w, r, apperr.Wrap(apperr.KindUnavailable, "CI data is temporarily unavailable", runErr))
			return
		}
	}

	data.PageData = PageData{
		Title: repo.Name + " - " + shortString(detail.Hash, 12),
		User:  GetUser(r),
		RepoNav: &RepoNavData{
			Owner: owner, Repository: repo, CurrentRef: currentRef, Active: "commits",
			IsOwner: GetUser(r) != nil && GetUser(r).ID == repo.OwnerID, CanViewCI: data.CanViewCI,
		},
	}
	app.renderPage(w, r, "repo_commit.html", &data)
}

func (app *App) HandleRepoBlobRawGET(w http.ResponseWriter, r *http.Request) {
	app.serveRepoBlob(w, r, false)
}

func (app *App) HandleRepoBlobDownloadGET(w http.ResponseWriter, r *http.Request) {
	app.serveRepoBlob(w, r, true)
}

func (app *App) serveRepoBlob(w http.ResponseWriter, r *http.Request, download bool) {
	noStore(w)
	ctx, cleanup, ok := app.beginRepoStream(w, r)
	if !ok {
		return
	}
	defer cleanup()
	repoPath := GetRepoPath(r)
	ref := requestRef(r)
	path := requestRepoPath(r)
	if strings.TrimSpace(path) == "" {
		app.respondPlainError(w, r, apperr.New(apperr.KindNotFound, "file not found"))
		return
	}
	resolvedRef, err := git.ResolveRef(ctx, repoPath, ref)
	if err != nil {
		app.respondPlainError(w, r, repositoryGitError(err, "file not found"))
		return
	}
	size, err := git.GetBlobSize(ctx, repoPath, resolvedRef, path)
	if err != nil {
		app.respondPlainError(w, r, repositoryGitError(err, "file not found"))
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
		slog.Warn("blob stream ended with error", "request_id", RequestID(r), "path", path, "ref", resolvedRef, "error", err)
	}
}

type fileSearchResponse struct {
	Ref       string            `json:"ref"`
	Results   []fileSearchMatch `json:"results"`
	Truncated bool              `json:"truncated,omitempty"`
}

const (
	defaultFileSearchMaxConcurrent      = 8
	defaultFileSearchMaxConcurrentPerIP = 2
	defaultFileSearchMaxFiles           = 100000
	defaultFileSearchMaxBytes           = int64(32 * 1024 * 1024)
	defaultFileSearchTimeout            = 5 * time.Second
)

func (app *App) fileSearchConcurrencyLimiter() *requestConcurrencyLimiter {
	app.fileSearchOnce.Do(func() {
		total := app.Config.FileSearchMaxConcurrent
		if total <= 0 {
			total = defaultFileSearchMaxConcurrent
		}
		perIP := app.Config.FileSearchMaxConcurrentPerIP
		if perIP <= 0 {
			perIP = defaultFileSearchMaxConcurrentPerIP
		}
		app.fileSearchLimiter = newRequestConcurrencyLimiter(total, perIP)
	})
	return app.fileSearchLimiter
}

func (app *App) HandleRepoFileSearchGET(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	releaseSlot, ok := app.fileSearchConcurrencyLimiter().tryAcquire(app.clientIP(r))
	if !ok {
		w.Header().Set("Retry-After", "1")
		app.respondAPIError(w, r, apperr.New(apperr.KindUnavailable, "File search is busy; retry shortly"))
		return
	}
	defer releaseSlot()
	repoPath := GetRepoPath(r)
	owner := GetRepoOwner(r)
	repo := GetRepo(r)
	timeout := app.Config.FileSearchTimeout
	if timeout <= 0 {
		timeout = defaultFileSearchTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	isEmpty, err := git.IsEmpty(ctx, repoPath)
	if err != nil {
		app.respondAPIError(w, r, apperr.Wrap(apperr.KindUnavailable, "Repository data is temporarily unavailable", err))
		return
	}
	if isEmpty {
		writeFileSearchJSON(w, fileSearchResponse{Results: []fileSearchMatch{}})
		return
	}
	ref, err := git.ResolveRef(ctx, repoPath, requestRef(r))
	if err != nil {
		app.respondAPIError(w, r, repositoryGitError(err, "revision not found"))
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if runes := []rune(query); len(runes) > 200 {
		query = string(runes[:200])
	}
	maxFiles := app.Config.FileSearchMaxFiles
	if maxFiles <= 0 {
		maxFiles = defaultFileSearchMaxFiles
	}
	maxBytes := app.Config.FileSearchMaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultFileSearchMaxBytes
	}
	ownerPath := url.PathEscape(owner.Username)
	repoPathPart := url.PathEscape(repo.Name)
	matches := make([]fileSearchMatch, 0, 40)
	truncated, err := git.WalkFiles(ctx, repoPath, ref, git.FileWalkLimits{
		MaxFiles: maxFiles,
		MaxBytes: maxBytes,
	}, func(path string) error {
		match, ok := fileSearchMatchForPath(path, query)
		if ok {
			matches = addFileSearchMatch(matches, match, 40)
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			truncated = true
		case errors.Is(err, context.Canceled) && r.Context().Err() != nil:
			return
		default:
			app.respondAPIError(w, r, repositoryGitError(err, "revision not found"))
			return
		}
	}
	finalizeFileSearchMatches(matches, ownerPath, repoPathPart, ref)
	writeFileSearchJSON(w, fileSearchResponse{
		Ref:       ref,
		Results:   matches,
		Truncated: truncated,
	})
}

func writeFileSearchJSON(w http.ResponseWriter, response fileSearchResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}
